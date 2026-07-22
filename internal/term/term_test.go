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

// TestSlowCommandNotCutShort is a regression test. A command that emits a
// prompt or a warning and then pauses while it works was treated as finished,
// so the caller got the noise and lost the answer.
func TestSlowCommandNotCutShort(t *testing.T) {
	_, s := newSession(t)

	// Print something prompt-like, pause well past the settle window, then
	// produce the real answer.
	out, err := s.Run(`echo "starship disabled"; sleep 1.2; echo "THE-ACTUAL-ANSWER-IS-HERE"`,
		20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	clean := Clean(out)
	if !strings.Contains(clean, "THE-ACTUAL-ANSWER-IS-HERE") {
		t.Errorf("returned before the real output arrived:\n%q", clean)
	}
}

func TestIsSubstantive(t *testing.T) {
	for _, noise := range []string{
		"", "   ", ">", "❯ ", "$",
		"Starship disabled due to TERM=dumb >",
		"akins@elijah:~$ ",
	} {
		if isSubstantive(noise) {
			t.Errorf("treated %q as substantive output", noise)
		}
	}
	for _, real := range []string{
		"Hello Freya — nice to meet you, and my compliments.",
		"total 48\ndrwxr-xr-x 2 akins akins 4096 Jul 22 19:00 .",
	} {
		if !isSubstantive(real) {
			t.Errorf("treated real output as noise: %q", real)
		}
	}
}

func TestScreenHandlesCursorPositioning(t *testing.T) {
	s := NewScreen(5, 20)
	s.Write("hello")
	s.Write("\x1b[1;1H") // home
	s.Write("HELLO")     // overwrite in place
	if got := s.Text(); got != "HELLO" {
		t.Errorf("cursor positioning wrong: %q", got)
	}

	s = NewScreen(5, 20)
	s.Write("line one\r\nline two")
	s.Write("\x1b[1;1H\x1b[2K") // home, erase line
	if got := s.Text(); strings.Contains(got, "line one") {
		t.Errorf("erase-line did not clear: %q", got)
	}
}

func TestScreenStripsOSCWithSTTerminator(t *testing.T) {
	// Shell integration emits OSC ending in ST (ESC backslash), not BEL. A
	// regex expecting BEL left the payload behind as visible text.
	s := NewScreen(5, 60)
	s.Write("\x1b]133;C\x1b\\\x1b]666;vte.shell.preexec!\x1b\\real output here")
	got := s.Text()
	if strings.Contains(got, "133") || strings.Contains(got, "vte") {
		t.Errorf("OSC payload leaked into the text: %q", got)
	}
	if !strings.Contains(got, "real output here") {
		t.Errorf("real content lost: %q", got)
	}
}

func TestScreenRepaintShowsOnlyCurrentFrame(t *testing.T) {
	// A full-screen program repaints rather than streams. The screen must show
	// the latest frame, not every frame concatenated.
	s := NewScreen(4, 20)
	for _, frame := range []string{"frame one", "frame two", "frame three"} {
		s.Write("\x1b[2J\x1b[1;1H") // clear, home
		s.Write(frame)
	}
	got := s.Text()
	if strings.Contains(got, "frame one") || strings.Contains(got, "frame two") {
		t.Errorf("stale frames still visible: %q", got)
	}
	if !strings.Contains(got, "frame three") {
		t.Errorf("current frame missing: %q", got)
	}
}

func TestScreenScrolls(t *testing.T) {
	s := NewScreen(3, 20)
	s.Write("one\r\ntwo\r\nthree\r\nfour")
	got := s.Text()
	if strings.Contains(got, "one") {
		t.Errorf("did not scroll off the top: %q", got)
	}
	if !strings.Contains(got, "four") {
		t.Errorf("newest line missing: %q", got)
	}
}

func TestCleanUsesTheEmulator(t *testing.T) {
	got := Clean("\x1b[32mgreen\x1b[0m\x1b]0;title\x07 plain\r\n")
	for _, bad := range []string{"\x1b", "[32m", "title"} {
		if strings.Contains(got, bad) {
			t.Errorf("escape residue %q in %q", bad, got)
		}
	}
	for _, want := range []string{"green", "plain"} {
		if !strings.Contains(got, want) {
			t.Errorf("content lost: %q", got)
		}
	}
}

// TestCompletionIsExactNotGuessed is the regression test for answers arriving a
// turn late. A shell prompt is just more output, so waiting for quiet returned
// the prompt and left the real reply for the next command to pick up.
func TestCompletionIsExactNotGuessed(t *testing.T) {
	_, s := newSession(t)

	// Emit something prompt-shaped, pause well past the settle window, then the
	// real answer. Under the old heuristic this returned the first line only.
	out, err := s.Run(`printf 'akins  /tmp    v1.26.3    20:15 \n'; sleep 1.5; echo REAL-ANSWER-HERE`,
		30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	clean := Clean(out)
	if !strings.Contains(clean, "REAL-ANSWER-HERE") {
		t.Errorf("returned before the answer arrived:\n%q", clean)
	}

	// And the next command must not inherit the previous one's output.
	next, err := s.Run("echo SECOND", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(Clean(next), "REAL-ANSWER-HERE") {
		t.Errorf("previous output leaked into the next command:\n%q", Clean(next))
	}
}

func TestMarkerDoesNotAppearInOutput(t *testing.T) {
	_, s := newSession(t)
	out, err := s.Run("echo clean-output", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "FREYA_DONE") {
		t.Errorf("completion marker leaked into the result: %q", Clean(out))
	}
	if !strings.Contains(Clean(out), "clean-output") {
		t.Errorf("output missing: %q", Clean(out))
	}
}

func TestSeveralCommandsStayInSync(t *testing.T) {
	_, s := newSession(t)
	for i, want := range []string{"alpha", "bravo", "charlie", "delta"} {
		out, err := s.Run("echo "+want, 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		got := Clean(out)
		if !strings.Contains(got, want) {
			t.Errorf("command %d returned %q, expected %q", i+1, got, want)
		}
	}
}

func TestNonShellStillUsesSettle(t *testing.T) {
	m := NewManager()
	defer m.CloseAll()
	s, err := m.Start("py", "python3", "")
	if err != nil {
		t.Skipf("python3 unavailable: %v", err)
	}
	time.Sleep(800 * time.Millisecond)
	s.Drain()

	// A REPL cannot echo a marker, so the heuristic must still work there.
	out, err := s.Run("print(2+2)", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(Clean(out), "4") {
		t.Errorf("REPL output wrong: %q", Clean(out))
	}
}
