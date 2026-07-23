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

	"github.com/akins/jarvis/internal/sentinel"
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
	PID        int       `json:"pid"`
	Started    time.Time `json:"started"`
	Watchers   []string  `json:"watchers"`
	Chatty     string    `json:"chattiness"`
	Notified   int       `json:"notified"`
	Suppressed int       `json:"suppressed"`
}

// Daemon runs the watchers and delivers what they find.
type Daemon struct {
	DataDir  string
	Sentinel *sentinel.Sentinel
	// Speak, when set, reads critical notifications aloud.
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

	mu         sync.Mutex
	started    time.Time
	notified   int
	suppressed int
	listener   net.Listener
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

// deliver sends an observation to the desktop, and aloud when it is urgent.
func (d *Daemon) deliver(o sentinel.Observation) {
	d.mu.Lock()
	d.notified++
	d.mu.Unlock()

	if !d.Quiet {
		notify(o)
	}
	// Only genuinely urgent things interrupt audibly. A spoken sentence is far
	// more intrusive than a notification that waits to be noticed.
	if d.Speak != nil && o.Urgency >= sentinel.UrgencyCritical {
		d.Speak(o.Summary)
	}
}

// notify posts a desktop notification.
func notify(o sentinel.Observation) {
	if _, err := exec.LookPath("notify-send"); err != nil {
		return
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
	_ = cmd.Run()
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
			Notified: d.notified, Suppressed: d.suppressed,
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
func (s *Status) Describe() string {
	return fmt.Sprintf("pid %d, up %s, %d watchers (%s), chattiness %s, %d notifications sent",
		s.PID, time.Since(s.Started).Round(time.Second),
		len(s.Watchers), strings.Join(s.Watchers, ", "), s.Chatty, s.Notified)
}
