package memory

import (
	"strings"
	"testing"
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
