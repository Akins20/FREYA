// Package daemon keeps Freya running when nobody is talking to her.
//
// # Why this exists
//
// Everything proactive was, until now, a lie of omission. Watchers only watched
// while a terminal happened to be open; a reminder set for tomorrow would fire
// only if you were already mid-conversation when it came due. An assistant that
// exists solely while you are looking at it is a program, not an assistant.
//
// # Who owns what
//
// Two processes touching the same memory files would corrupt them: the archive
// is append-only and the JSON stores rewrite whole files. So ownership is split
// rather than shared.
//
//	daemon   owns the watchers and notifications; reads memory, never writes it
//	session  owns the conversation and every write to memory
//
// A session that finds a daemon running defers to it for observations instead
// of starting a second set of watchers, so the same disk warning cannot arrive
// twice from two directions.
package daemon

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Akins20/FREYA/internal/sentinel"
)

// SocketName is the control socket's base name.
const SocketName = "freya.sock"

// maxSocketPath is the length a Unix socket path may not exceed.
//
// The kernel's sockaddr_un.sun_path is 108 bytes on Linux, and exceeding it
// fails with a bare "invalid argument" that says nothing about length. A data
// directory nested a few levels deep is enough to hit it.
const maxSocketPath = 100

// SocketPath returns where the control socket lives for a data directory.
//
// The runtime directory is preferred, and not only for length: a socket is
// runtime state, not user data, and belongs somewhere that is cleared when the
// session ends rather than alongside the memory archive.
func SocketPath(dataDir string) string {
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		// Keyed by the data directory so two instances with separate memory do
		// not fight over one socket.
		name := fmt.Sprintf("freya-%s.sock", shortKey(dataDir))
		if p := filepath.Join(runtimeDir, name); len(p) <= maxSocketPath {
			return p
		}
	}

	if p := filepath.Join(dataDir, SocketName); len(p) <= maxSocketPath {
		return p
	}
	// Last resort: a short, stable name under the system temp directory.
	return filepath.Join(os.TempDir(), fmt.Sprintf("freya-%s.sock", shortKey(dataDir)))
}

// shortKey derives a stable short identifier from a path.
func shortKey(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:4])
}

// Request is a command sent to the daemon.
type Request struct {
	Command string `json:"command"`
}

// Reply is the daemon's answer.
type Reply struct {
	OK           bool                   `json:"ok"`
	Message      string                 `json:"message,omitempty"`
	Observations []sentinel.Observation `json:"observations,omitempty"`
	Status       *Status                `json:"status,omitempty"`
}

// Status describes a running daemon.
type Status struct {
	PID         int       `json:"pid"`
	Started     time.Time `json:"started"`
	Watchers    []string  `json:"watchers"`
	Chatty      string    `json:"chattiness"`
	Notified    int       `json:"notified"`
	Undelivered int       `json:"undelivered,omitempty"`
	Suppressed  int       `json:"suppressed"`
}

// Daemon runs the watchers and delivers what they find.
type Daemon struct {
	DataDir  string
	Sentinel *sentinel.Sentinel
	// Speak, when set, reads critical observations aloud.
	Speak func(string)
	// Quiet suppresses desktop notifications, for debugging.
	Quiet bool

	// Yield is called when an interactive session takes over. The daemon must
	// stop listening and release the memory store: exactly one process writes
	// at a time, and the one with a human in front of it wins.
	Yield func()
	// Resume is called when that session ends and the daemon takes back over.
	Resume func()
	// Talk starts one push-to-talk exchange, triggered through the socket by a
	// desktop keybinding. Nil when voice is unavailable.
	Talk func()

	// yielded tracks whether a session currently holds the store, so that two
	// sessions opening in succession do not resume the daemon between them.
	yielded int

	mu       sync.Mutex
	started  time.Time
	notified int
	// undelivered counts observations that reached no channel at all: no desktop
	// notifier, nothing to speak with. It is reported rather than hidden, because
	// a silent proactivity engine looks exactly like a calm one.
	undelivered int
	suppressed  int
	listener    net.Listener
}

// New builds a daemon around an existing sentinel.
func New(dataDir string, s *sentinel.Sentinel) *Daemon {
	return &Daemon{DataDir: dataDir, Sentinel: s, started: time.Now()}
}

// Run starts the watchers and serves the control socket until the context ends.
func (d *Daemon) Run(ctx context.Context) error {
	if err := os.MkdirAll(d.DataDir, 0o755); err != nil {
		return fmt.Errorf("daemon: data dir: %w", err)
	}

	path := SocketPath(d.DataDir)
	// A socket left behind by a crash would block binding. Removing it is only
	// safe once we know nobody is listening on it.
	if Running(d.DataDir) {
		return fmt.Errorf("daemon: already running (socket %s is live)", path)
	}
	_ = os.Remove(path)

	listener, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("daemon: listen: %w", err)
	}
	// Owner-only: the socket can trigger speech and read observations about
	// the machine.
	_ = os.Chmod(path, 0o600)

	d.mu.Lock()
	d.listener = listener
	d.started = time.Now()
	d.mu.Unlock()

	d.Sentinel.Notify = d.deliver
	d.Sentinel.Start(ctx)

	go func() {
		<-ctx.Done()
		listener.Close()
		_ = os.Remove(path)
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				d.Sentinel.Stop()
				return nil
			default:
				continue
			}
		}
		go d.serve(conn)
	}
}

// deliver puts an observation in front of the user, by whatever channels exist,
// and records what actually happened to it.
//
// # The counter used to lie
//
// It incremented here, before anything was attempted, and notify() returns
// silently when notify-send is not installed. Measured on a box without
// libnotify: three observations raised, none delivered anywhere, and the status
// line said "3 notifications sent". A raised observation left no trace in the
// journal either, so there was nothing to check the count against.
//
// That is the same shape as a tool reporting success for work it did not do:
// every layer above reads green. So the count is of deliveries that happened,
// the journal records the observation either way, and a delivery that reached
// nobody says so.
func (d *Daemon) deliver(o sentinel.Observation) {
	desktop := false
	if !d.Quiet {
		desktop = notify(o)
	}

	// Aloud only when it is worth interrupting for. A spoken sentence is far more
	// intrusive than a notification that waits to be noticed, and the deliberate
	// design is that observations arrive as toasts. Her talking unprompted is a
	// separate path (Agent.Followup), which is about the conversation rather than
	// about the machine.
	spoken := false
	if d.Speak != nil && o.Urgency >= sentinel.UrgencyCritical {
		d.Speak(o.Summary)
		spoken = true
	}

	d.mu.Lock()
	if desktop || spoken {
		d.notified++
	} else {
		d.undelivered++
	}
	d.mu.Unlock()

	d.journal(o, desktop, spoken)
}

// journal writes the observation and its fate to the daemon's stdout.
//
// Scheduled self-tasks already print when they fire and when they finish, and
// watchers printed nothing at all — which is backwards, because the watcher path
// is the one nobody is watching. A missed toast now leaves a line behind.
func (d *Daemon) journal(o sentinel.Observation, desktop, spoken bool) {
	var how string
	switch {
	case desktop && spoken:
		how = "notified, spoken"
	case desktop:
		how = "notified"
	case spoken:
		how = "spoken"
	default:
		how = "NOT DELIVERED: no desktop notifier and nothing to speak with"
	}
	fmt.Printf("  👁 [%s] %s (%s)\n", o.Urgency, o.Summary, how)
}

// notify posts a desktop notification, and reports whether one was actually
// posted. A missing notify-send is not an error worth stopping for, but it must
// not be mistaken for a delivery.
func notify(o sentinel.Observation) bool {
	if _, err := exec.LookPath("notify-send"); err != nil {
		return false
	}

	urgency := "normal"
	switch o.Urgency {
	case sentinel.UrgencyCritical:
		urgency = "critical"
	case sentinel.UrgencyAmbient:
		urgency = "low"
	}

	body := o.Summary
	if o.Detail != "" {
		body += "\n" + o.Detail
	}
	cmd := exec.Command("notify-send",
		"--app-name=Freya", "--urgency="+urgency, "--icon=dialog-information",
		"Freya", body)
	return cmd.Run() == nil
}

// serve handles one control connection.
func (d *Daemon) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return
	}

	var req Request
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &req); err != nil {
		writeReply(conn, Reply{OK: false, Message: "bad request"})
		return
	}

	switch req.Command {
	case "ping":
		writeReply(conn, Reply{OK: true, Message: "alive"})

	case "status":
		d.mu.Lock()
		status := &Status{
			PID: os.Getpid(), Started: d.started,
			Watchers: d.Sentinel.Watchers(),
			Chatty:   d.Sentinel.Chattiness.String(),
			Notified: d.notified, Undelivered: d.undelivered, Suppressed: d.suppressed,
		}
		d.mu.Unlock()
		writeReply(conn, Reply{OK: true, Status: status})

	case "pending":
		writeReply(conn, Reply{OK: true, Observations: d.Sentinel.Peek()})

	case "drain":
		writeReply(conn, Reply{OK: true, Observations: d.Sentinel.Pending()})

	case "yield":
		// A session is starting. Stand down: stop listening for the wake word
		// and let go of the store.
		d.mu.Lock()
		d.yielded++
		first := d.yielded == 1
		d.mu.Unlock()
		if first && d.Yield != nil {
			d.Yield()
		}
		writeReply(conn, Reply{OK: true, Message: "yielded"})

	case "resume":
		// That session has ended. Take over again, but only once the last one
		// has gone — two overlapping sessions would otherwise hand the store
		// back while one of them is still writing to it.
		d.mu.Lock()
		if d.yielded > 0 {
			d.yielded--
		}
		last := d.yielded == 0
		d.mu.Unlock()
		if last && d.Resume != nil {
			d.Resume()
		}
		writeReply(conn, Reply{OK: true, Message: "resumed"})

	case "talk":
		// Push-to-talk, triggered from outside. The X server on this hardware
		// would not deliver a physical global-hotkey grab to us, but the desktop
		// environment captures the key reliably and can run a command — so the
		// key binding poked this socket instead. Acknowledge at once and run the
		// exchange in the background: the caller is a one-shot process that
		// should not block for the length of a spoken conversation.
		if d.Talk != nil {
			go d.Talk()
			writeReply(conn, Reply{OK: true, Message: "listening"})
		} else {
			writeReply(conn, Reply{OK: false, Message: "voice is not available"})
		}

	case "stop":
		writeReply(conn, Reply{OK: true, Message: "stopping"})
		go func() {
			time.Sleep(200 * time.Millisecond)
			os.Exit(0)
		}()

	default:
		writeReply(conn, Reply{OK: false, Message: "unknown command " + req.Command})
	}
}

func writeReply(conn net.Conn, r Reply) {
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(b, '\n'))
}

// --- client side ------------------------------------------------------------

// Running reports whether a daemon is listening for this data directory.
//
// Tested by connecting rather than by checking for the socket file, because a
// crash leaves the file behind and a stale socket looks identical to a live one
// from the filesystem.
func Running(dataDir string) bool {
	conn, err := net.DialTimeout("unix", SocketPath(dataDir), time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Ask sends a command to a running daemon.
func Ask(dataDir, command string) (*Reply, error) {
	conn, err := net.DialTimeout("unix", SocketPath(dataDir), 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("daemon: not running")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	req, err := json.Marshal(Request{Command: command})
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return nil, err
	}

	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("daemon: no reply: %w", err)
	}
	var reply Reply
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &reply); err != nil {
		return nil, err
	}
	return &reply, nil
}

// Describe renders a status for display.
//
// The undelivered count is only shown when it is non-zero, and it is worth
// showing loudly when it is: it means she noticed things and had no way to tell
// anyone, which from the outside is indistinguishable from noticing nothing.
func (s *Status) Describe() string {
	out := fmt.Sprintf("pid %d, up %s, %d watchers (%s), chattiness %s, %d delivered",
		s.PID, time.Since(s.Started).Round(time.Second),
		len(s.Watchers), strings.Join(s.Watchers, ", "), s.Chatty, s.Notified)
	if s.Undelivered > 0 {
		out += fmt.Sprintf(", %d NOT DELIVERED (no desktop notifier and no voice — "+
			"install notify-send, or she notices things and cannot tell you)", s.Undelivered)
	}
	return out
}
