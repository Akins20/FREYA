package memory

import (
	"strings"
	"testing"
	"time"
)

func TestSuspendResumeHandsOffWriting(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.AppendTurn(Turn{Role: "user", Text: "first"}); err != nil {
		t.Fatal(err)
	}

	// A owns the store; hand it to B.
	if err := a.Suspend(); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if a.Writing() {
		t.Error("still writing after suspend")
	}
	// A write while suspended must fail loudly, not vanish.
	if _, err := a.AppendTurn(Turn{Role: "user", Text: "lost"}); err == nil {
		t.Error("a suspended store accepted a write — it will be lost on restart")
	}

	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.AppendTurn(Turn{Role: "assistant", Text: "second"}); err != nil {
		t.Fatalf("B could not write after taking over: %v", err)
	}
	if err := b.Suspend(); err != nil {
		t.Fatal(err)
	}

	// A resumes and must see what B wrote.
	if err := a.Resume(); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !a.Writing() {
		t.Error("not writing after resume")
	}
	var texts []string
	for _, turn := range a.Turns() {
		texts = append(texts, turn.Text)
	}
	joined := strings.Join(texts, ",")
	if !strings.Contains(joined, "first") || !strings.Contains(joined, "second") {
		t.Errorf("resumed store missing history: %q", joined)
	}
	if strings.Contains(joined, "lost") {
		t.Error("the write rejected during suspend leaked into the archive")
	}
}

func TestDoubleResumeIsSafe(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Resuming an already-writing store is a no-op, not a second open handle.
	if err := s.Resume(); err != nil {
		t.Errorf("resume while already writing: %v", err)
	}
	if _, err := s.AppendTurn(Turn{Role: "user", Text: "ok"}); err != nil {
		t.Errorf("append after redundant resume: %v", err)
	}
}

// NewSession must return.
//
// It deadlocked: it takes the store lock and then called saveJSON, which takes
// the same non-reentrant lock. Nothing crashed and nothing logged — the request
// hung, and with it every later write to the archive, because the lock was never
// released. The window's "New conversation" button wedged the daemon.
//
// Timed rather than plain, so the failure is a message in a second instead of a
// ten-minute test timeout with a goroutine dump.
func TestNewSessionDoesNotDeadlockTheStore(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately NOT deferred. Close takes the same lock, so on the failure this
	// test exists to catch it would block forever and turn a one-second failure
	// into a ten-minute timeout with a goroutine dump.
	first := s.SessionID()
	done := make(chan string, 1)
	go func() {
		id, err := s.NewSession()
		if err != nil {
			t.Errorf("NewSession on a writing store: %v", err)
		}
		done <- id
	}()

	select {
	case got := <-done:
		if got == first {
			t.Errorf("the session id did not advance: still %s", got)
		}
		if s.SessionID() != got {
			t.Errorf("SessionID says %s, NewSession returned %s", s.SessionID(), got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("NewSession never returned — the store lock is held by something " +
			"NewSession itself is waiting on, which wedges every later write too")
	}

	// And the increment survives a reopen, or the rail regroups everything into
	// one row on the next restart.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	// Open advances the count too, so the reopened store must be past what
	// NewSession left behind rather than back at the start.
	if again.SessionID() <= first {
		t.Errorf("after a reopen the session is %s, no later than %s", again.SessionID(), first)
	}

	// And a suspended store refuses rather than clobbering the session count the
	// process that owns the archive is keeping.
	if err := again.Suspend(); err != nil {
		t.Fatal(err)
	}
	if _, err := again.NewSession(); err == nil {
		t.Error("a suspended store rolled the session anyway")
	}
	if err := again.Resume(); err != nil {
		t.Fatal(err)
	}

	// A turn written after the roll carries the new id, which is the whole point.
	turn, err := again.AppendTurn(Turn{Role: "user", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if turn.SessionID != again.SessionID() {
		t.Errorf("the turn was stamped %s, not %s", turn.SessionID, again.SessionID())
	}
}
