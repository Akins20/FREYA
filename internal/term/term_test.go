package term

import (
	"strings"
	"testing"
	"time"
)

func newSession(t *testing.T) (*Manager, *Session) {
	t.Helper()
	m := NewManager()
	s, err := m.Start("test", "/bin/bash", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { m.CloseAll() })
	// Let the shell finish its own startup output.
	time.Sleep(300 * time.Millisecond)
	s.Drain()
	return m, s
}

func TestRunCommandCapturesOutput(t *testing.T) {
	_, s := newSession(t)
	out, err := s.Run("echo hello-from-pty", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(Clean(out), "hello-from-pty") {
		t.Errorf("output missing: %q", out)
	}
}

// TestSessionKeepsState is the difference between a session and running a
// command: the shell remembers what happened before.
func TestSessionKeepsState(t *testing.T) {
	_, s := newSession(t)

	if _, err := s.Run("MYVAR=persisted", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	out, err := s.Run("echo $MYVAR", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(Clean(out), "persisted") {
		t.Errorf("variable did not survive between commands: %q", Clean(out))
	}
}

func TestWorkingDirectoryPersists(t *testing.T) {
	_, s := newSession(t)
	if _, err := s.Run("cd /tmp", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	out, _ := s.Run("pwd", 5*time.Second)
	if !strings.Contains(Clean(out), "/tmp") {
		t.Errorf("cd did not persist: %q", Clean(out))
	}
}

// TestInteractivePrompt is the case pipes cannot handle: a program that only
// prompts when it believes a person is watching.
func TestInteractivePrompt(t *testing.T) {
	_, s := newSession(t)

	if err := s.Send(`read -p "Name: " answer && echo "got:$answer"`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)

	prompt := Clean(s.Read())
	if !strings.Contains(prompt, "Name:") {
		t.Fatalf("no prompt appeared — a pipe would behave this way, a pty should not: %q", prompt)
	}

	if err := s.Send("Freya"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	if got := Clean(s.Drain()); !strings.Contains(got, "got:Freya") {
		t.Errorf("answering the prompt did not work: %q", got)
	}
}

// TestLongRunningWorkContinues is the background-work guarantee: a command
// keeps running while the conversation moves on.
func TestLongRunningWorkContinues(t *testing.T) {
	_, s := newSession(t)

	if err := s.Send("for i in 1 2 3 4 5; do echo tick-$i; sleep 0.3; done; echo FINISHED"); err != nil {
		t.Fatal(err)
	}

	// Come back early: some output, but not all of it.
	time.Sleep(500 * time.Millisecond)
	early := Clean(s.Read())
	if !strings.Contains(early, "tick-1") {
		t.Errorf("no early output: %q", early)
	}
	if strings.Contains(early, "FINISHED") {
		t.Error("the command completed instantly; it was not actually long-running")
	}

	// Come back later: it carried on without being waited on.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(Clean(s.Read()), "FINISHED") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("work did not continue in the background: %q", Clean(s.Read()))
}

func TestPythonREPL(t *testing.T) {
	m := NewManager()
	defer m.CloseAll()

	s, err := m.Start("py", "python3", "")
	if err != nil {
		t.Skipf("python3 unavailable: %v", err)
	}
	time.Sleep(800 * time.Millisecond)
	s.Drain()

	out, err := s.Run("print(6*7)", 6*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(Clean(out), "42") {
		t.Errorf("REPL did not evaluate: %q", Clean(out))
	}

	// State must persist inside the REPL too.
	if _, err := s.Run("x = 'remembered'", 6*time.Second); err != nil {
		t.Fatal(err)
	}
	out, _ = s.Run("print(x)", 6*time.Second)
	if !strings.Contains(Clean(out), "remembered") {
		t.Errorf("REPL lost its state: %q", Clean(out))
	}
}

func TestControlCharacterInterrupts(t *testing.T) {
	_, s := newSession(t)

	if err := s.Send("sleep 30"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)

	// Ctrl-C. This is why the pty has a controlling terminal: without one,
	// signal delivery does not reach the foreground job.
	if err := s.SendRaw("\x03"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)

	out, _ := s.Run("echo recovered", 5*time.Second)
	if !strings.Contains(Clean(out), "recovered") {
		t.Errorf("session did not recover after an interrupt: %q", Clean(out))
	}
}

func TestDuplicateSessionRefused(t *testing.T) {
	m, _ := newSession(t)
	if _, err := m.Start("test", "/bin/bash", ""); err == nil {
		t.Error("starting a second session with the same name was allowed")
	}
}

func TestCloseEndsTheSession(t *testing.T) {
	m, s := newSession(t)
	if err := m.Close("test"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if !s.Done() {
		t.Error("session still reports as running after close")
	}
	if err := s.Send("echo x"); err == nil {
		t.Error("writing to a closed session should error")
	}
}

func TestBufferIsBounded(t *testing.T) {
	_, s := newSession(t)
	// Produce far more than the buffer limit.
	if err := s.Send("for i in $(seq 1 20000); do echo 'padding line of text for buffer testing'; done"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && s.Idle() < idleSettle {
		time.Sleep(100 * time.Millisecond)
	}
	if got := len(s.Read()); got > bufferLimit+16384 {
		t.Errorf("buffer grew to %d bytes, limit is %d", got, bufferLimit)
	}
}

func TestCleanStripsEscapes(t *testing.T) {
	got := Clean("\x1b[32mgreen\x1b[0m and \x1b[1mbold\x1b[0m\r\n\n\n\nplain")
	if strings.Contains(got, "\x1b") {
		t.Errorf("escape sequences survived: %q", got)
	}
	for _, want := range []string{"green", "bold", "plain"} {
		if !strings.Contains(got, want) {
			t.Errorf("content lost: %q", got)
		}
	}
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("blank runs survived: %q", got)
	}
}

// TestInputIsNotEchoed is a regression test. A pty echoes what is written to
// it, so captured output contained the command itself — searching a result for
// a word would find it in the command rather than in the reply.
func TestInputIsNotEchoed(t *testing.T) {
	_, s := newSession(t)

	out, err := s.Run("echo UNIQUEMARKER_OUTPUT", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	clean := Clean(out)

	// The result should carry the value once, from the echo's output — not
	// twice, with the second occurrence being the echoed command line.
	if n := strings.Count(clean, "UNIQUEMARKER_OUTPUT"); n != 1 {
		t.Errorf("marker appears %d times; input is being echoed back:\n%q", n, clean)
	}
	if strings.Contains(clean, "echo UNIQUEMARKER") {
		t.Errorf("the command text itself appears in the output:\n%q", clean)
	}
}
