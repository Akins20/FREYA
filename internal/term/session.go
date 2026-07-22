package term

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Defaults chosen for reading rather than for a human at a keyboard.
const (
	defaultRows = 40
	defaultCols = 140

	// bufferLimit caps retained output per session. A build log can run to
	// megabytes; keeping the tail is what matters, and holding all of it would
	// leak memory across a long-lived session.
	bufferLimit = 256 << 10

	// idleSettle is how long output must pause before a command is treated as
	// finished. Long enough to survive a slow line, short enough not to add
	// noticeable latency to every call.
	idleSettle = 400 * time.Millisecond
)

// Session is a live shell on a pseudo-terminal.
type Session struct {
	Name    string
	Started time.Time

	mu      sync.Mutex
	cmd     *exec.Cmd
	master  *os.File
	buf     bytes.Buffer
	closed  bool
	exited  bool
	lastOut time.Time
}

// Manager owns the running sessions.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

// NewManager creates an empty manager.
func NewManager() *Manager {
	return &Manager{sessions: map[string]*Session{}}
}

// Start opens a new session running the given program, or a login shell.
func (m *Manager) Start(name, program, dir string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if name == "" {
		name = fmt.Sprintf("session-%d", len(m.sessions)+1)
	}
	if existing, ok := m.sessions[name]; ok && !existing.Done() {
		return nil, fmt.Errorf("session %q is already running", name)
	}

	if program == "" {
		program = os.Getenv("SHELL")
		if program == "" {
			program = "/bin/bash"
		}
	}

	master, slaveName, err := openPTY()
	if err != nil {
		return nil, err
	}

	slave, err := os.OpenFile(slaveName, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, fmt.Errorf("term: open slave: %w", err)
	}
	defer slave.Close()

	cmd := exec.Command(program)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	if dir != "" {
		cmd.Dir = dir
	}
	// A new session with the pty as controlling terminal, so job control and
	// signal handling behave as they would for a person at a keyboard.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}

	// TERM matters: without it programs assume a dumb terminal and some refuse
	// to run at all. dumb is deliberate for the opposite reason — it suppresses
	// the cursor-movement escapes that make captured output unreadable.
	cmd.Env = append(os.Environ(),
		"TERM=dumb",
		"PAGER=cat", // stop `git log` and friends waiting on a pager forever
		"GIT_PAGER=cat",
		"PYTHONUNBUFFERED=1",
	)

	if err := cmd.Start(); err != nil {
		master.Close()
		return nil, fmt.Errorf("term: start %s: %w", program, err)
	}
	_ = setWindowSize(master, defaultRows, defaultCols)
	// Without this the pty echoes every command back into its own output, so
	// captured results contain the commands that produced them.
	_ = disableEcho(master)

	s := &Session{
		Name:    name,
		Started: time.Now(),
		cmd:     cmd,
		master:  master,
		lastOut: time.Now(),
	}
	go s.pump()
	go func() {
		_ = cmd.Wait()
		s.mu.Lock()
		s.exited = true
		s.mu.Unlock()
	}()

	m.sessions[name] = s
	return s, nil
}

// pump copies terminal output into the session buffer until the pty closes.
func (s *Session) pump() {
	chunk := make([]byte, 8192)
	for {
		n, err := s.master.Read(chunk)
		if n > 0 {
			s.mu.Lock()
			s.buf.Write(chunk[:n])
			// Keep the tail rather than the head: the end of a build log is
			// where the error is.
			if s.buf.Len() > bufferLimit {
				trimmed := s.buf.Bytes()[s.buf.Len()-bufferLimit:]
				s.buf.Reset()
				s.buf.Write(trimmed)
			}
			s.lastOut = time.Now()
			s.mu.Unlock()
		}
		if err != nil {
			s.mu.Lock()
			s.closed = true
			s.mu.Unlock()
			return
		}
	}
}

// Send writes input to the session without waiting for a reply.
func (s *Session) Send(input string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.exited {
		return fmt.Errorf("session %q has ended", s.Name)
	}
	if !strings.HasSuffix(input, "\n") {
		input += "\n"
	}
	_, err := s.master.WriteString(input)
	return err
}

// SendRaw writes bytes with no newline appended, for control characters.
func (s *Session) SendRaw(data string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.exited {
		return fmt.Errorf("session %q has ended", s.Name)
	}
	_, err := s.master.WriteString(data)
	return err
}

// Run sends a command and waits for the output to settle.
//
// "Settle" rather than "finish": there is no reliable way to know a shell
// command has completed when watching a terminal, because the prompt is just
// more output. Waiting for a pause is what a person does, and it is honest
// about being a heuristic — a command that pauses mid-run may return early,
// which is why Read exists to collect the rest.
func (s *Session) Run(command string, timeout time.Duration) (string, error) {
	s.Drain() // discard anything left from before, so the result is this command's

	if err := s.Send(command); err != nil {
		return "", err
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(60 * time.Millisecond)

		s.mu.Lock()
		quiet := time.Since(s.lastOut)
		hasOutput := s.buf.Len() > 0
		done := s.exited || s.closed
		s.mu.Unlock()

		if done {
			break
		}
		if hasOutput && quiet > idleSettle {
			break
		}
	}
	return s.Drain(), nil
}

// Read returns buffered output without clearing it.
func (s *Session) Read() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// Drain returns buffered output and clears the buffer.
func (s *Session) Drain() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.buf.String()
	s.buf.Reset()
	return out
}

// Done reports whether the session has ended.
func (s *Session) Done() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exited || s.closed
}

// Idle reports how long the session has been quiet.
func (s *Session) Idle() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.lastOut)
}

// Close ends the session.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true

	if s.cmd != nil && s.cmd.Process != nil {
		// Signal the whole process group: killing only the shell would orphan
		// whatever it launched.
		if pgid, err := syscall.Getpgid(s.cmd.Process.Pid); err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
		} else {
			_ = s.cmd.Process.Kill()
		}
	}
	return s.master.Close()
}

// Get returns a session by name.
func (m *Manager) Get(name string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[name]
	return s, ok
}

// List returns every session, oldest first.
func (m *Manager) List() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// Close ends one session and forgets it.
func (m *Manager) Close(name string) error {
	m.mu.Lock()
	s, ok := m.sessions[name]
	if ok {
		delete(m.sessions, name)
	}
	m.mu.Unlock()

	if !ok {
		return fmt.Errorf("no session named %q", name)
	}
	return s.Close()
}

// CloseAll ends every session, for shutdown.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = map[string]*Session{}
	m.mu.Unlock()

	for _, s := range sessions {
		_ = s.Close()
	}
}

// ansiEscape matches the control sequences a terminal emits for cursor
// movement, colour and screen clearing.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b[()][B0]|\r`)

// Clean strips terminal control sequences from captured output.
//
// Even with TERM=dumb some programs emit them, and a model reading
// "\x1b[32mOK\x1b[0m" sees noise where a person sees green text.
func Clean(s string) string {
	s = ansiEscape.ReplaceAllString(s, "")
	// Collapse the blank-line runs that clearing sequences leave behind.
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if strings.TrimSpace(l) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// Describe summarises a session for display.
func (s *Session) Describe() string {
	state := "running"
	if s.Done() {
		state = "ended"
	}
	return fmt.Sprintf("%s — %s, started %s, idle %s",
		s.Name, state, s.Started.Format("15:04:05"), s.Idle().Round(time.Second))
}
