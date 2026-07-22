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
	"sync/atomic"
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
	// isShell records whether completion can be detected exactly. A shell can
	// be asked to echo a marker after a command; a REPL cannot.
	isShell bool

	mu      sync.Mutex
	cmd     *exec.Cmd
	master  *os.File
	buf     bytes.Buffer
	screen  *Screen
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

	// TERM=dumb seemed safer — fewer escape sequences to strip — but it makes
	// programs announce their own degradation ("Starship disabled due to
	// TERM=dumb") and some refuse features entirely. Clean() removes escapes
	// anyway, so a real terminal type costs nothing and buys compatibility.
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
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
		isShell: looksLikeShell(program),
		cmd:     cmd,
		master:  master,
		screen:  NewScreen(defaultRows, defaultCols),
		lastOut: time.Now(),
	}
	// Automated sessions get a silent prompt. A themed prompt like Starship
	// emits forty escape sequences per line, hooks PROMPT_COMMAND, and turns on
	// bracketed paste — all of which is noise around the output that matters.
	// The user's rc file still loads, so aliases and environment survive.
	if s.isShell {
		go func() {
			time.Sleep(250 * time.Millisecond)
			_ = s.SendRaw("PROMPT_COMMAND=''; PS1=''; PS2=''; PS0=''\n" +
				"unset STARSHIP_SHELL STARSHIP_SESSION_KEY 2>/dev/null\n" +
				"bind 'set enable-bracketed-paste off' 2>/dev/null\n")
		}()
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

// looksLikeShell reports whether a program accepts shell syntax, and so can be
// asked to signal its own completion.
func looksLikeShell(program string) bool {
	base := program
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	switch base {
	case "", "sh", "bash", "zsh", "dash", "ksh", "fish":
		return true
	}
	return false
}

// markerCounter makes each completion marker unique within a session.
var markerCounter atomic.Uint64

// Run sends a command and returns its output.
//
// In a shell, completion is *known* rather than guessed: the command is
// followed by an echo of a unique marker, and output is read until that marker
// appears. Waiting for output to go quiet cannot work — a shell prompt is just
// more output, and a 35-character Starship prompt looks exactly as substantive
// as a real answer. That heuristic returned the prompt and left the actual
// reply to be collected by the *next* command, so every answer arrived a turn
// late.
//
// Outside a shell — a REPL, an ssh session — there is nothing to echo a marker,
// so the settle heuristic remains, with its limitations.
func (s *Session) Run(command string, timeout time.Duration) (string, error) {
	s.Drain() // discard anything left from before, so the result is this command's

	if s.isShell {
		return s.runWithMarker(command, timeout)
	}

	if err := s.Send(command); err != nil {
		return "", err
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(60 * time.Millisecond)

		s.mu.Lock()
		quiet := time.Since(s.lastOut)
		current := s.buf.String()
		done := s.exited || s.closed
		s.mu.Unlock()

		if done {
			break
		}
		if quiet <= idleSettle {
			continue
		}
		// Settling is not the same as finishing. A slow command often emits a
		// prompt or a warning first, then pauses while it works — returning at
		// that point hands back the noise and discards the answer. Only treat
		// a pause as completion once there is substantive output.
		if isSubstantive(current) {
			break
		}
	}
	return s.Drain(), nil
}

// promptLike matches shell prompts and the banners programs print about
// themselves, none of which mean a command has produced its answer.
var promptLike = regexp.MustCompile(`(?i)^[\s>$#%❯➜]*$|starship|^\s*\S+@\S+[:\s]|^\s*\[\S+\]\s*[$#>]`)

// minSubstantiveBytes is the output below which a pause is more likely to be a
// prompt than a result.
const minSubstantiveBytes = 24

// isSubstantive reports whether captured output looks like an actual result.
func isSubstantive(raw string) bool {
	cleaned := Clean(raw)
	if len(cleaned) < minSubstantiveBytes {
		return false
	}
	// Strip lines that are only prompts or self-announcements and see whether
	// anything of substance remains.
	var real int
	for _, line := range strings.Split(cleaned, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || promptLike.MatchString(t) {
			continue
		}
		real += len(t)
	}
	return real >= minSubstantiveBytes
}

// runWithMarker executes a command and waits for a definite completion signal.
func (s *Session) runWithMarker(command string, timeout time.Duration) (string, error) {
	marker := fmt.Sprintf("__FREYA_DONE_%d_%d__", time.Now().UnixNano(), markerCounter.Add(1))

	// Joined with a semicolon on a single line, deliberately. Sending the
	// command and the marker as two lines makes the shell print a prompt
	// between them, which then lands inside the captured output — the answer
	// arrived followed by a prompt, every time.
	//
	// printf rather than echo: it does not interpret escapes, and the marker is
	// split so the echoed command line cannot itself match.
	sep := " ; "
	if strings.Contains(command, "\n") {
		// A multi-line command cannot be joined with a semicolon; wrap it so the
		// marker still runs exactly once, after the whole thing.
		return s.runMultiline(command, marker, timeout)
	}
	wrapped := fmt.Sprintf("%s%sprintf '%%s%%s\\n' '%s' '%s'",
		command, sep, marker[:len(marker)/2], marker[len(marker)/2:])

	if err := s.Send(wrapped); err != nil {
		return "", err
	}
	return s.awaitMarker(marker, timeout)
}

// runMultiline handles commands containing newlines, where a semicolon join is
// not valid syntax.
func (s *Session) runMultiline(command, marker string, timeout time.Duration) (string, error) {
	// A brace group keeps the whole thing one command list, so the marker runs
	// after it and only one prompt follows.
	wrapped := fmt.Sprintf("{\n%s\n} ; printf '%%s%%s\\n' '%s' '%s'",
		command, marker[:len(marker)/2], marker[len(marker)/2:])
	if err := s.Send(wrapped); err != nil {
		return "", err
	}
	return s.awaitMarker(marker, timeout)
}

// awaitMarker reads until the completion marker appears.
func (s *Session) awaitMarker(marker string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		current := s.buf.String()
		done := s.exited || s.closed
		s.mu.Unlock()

		if idx := strings.Index(current, marker); idx >= 0 {
			s.mu.Lock()
			s.buf.Reset()
			s.mu.Unlock()
			return current[:idx], nil
		}
		if done {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return s.Drain(), fmt.Errorf("command did not finish within %s (output so far returned)", timeout)
}

// Read returns buffered output without clearing it.
func (s *Session) Read() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// Screen returns what the terminal currently displays.
//
// Use this for full-screen programs. They do not stream output — they position
// the cursor and paint — so the accumulated byte stream is every frame run
// together, while this is the single frame on screen now.
func (s *Session) Screen() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.screen.Text()
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

// Clean renders captured output as text.
//
// This runs the bytes through the emulator rather than deleting escapes with a
// pattern. Regex stripping missed OSC sequences terminated by ST rather than
// BEL — which is exactly what shell integration emits — leaving "]133;C" in the
// output and, worse, satisfying the "has output settled" check so answers were
// returned a turn late.
func Clean(s string) string {
	if s == "" {
		return ""
	}
	screen := NewScreen(2000, defaultCols)
	screen.Write(s)
	s = screen.Text()
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
