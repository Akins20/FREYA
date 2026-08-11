package schedule

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func storeAt(t *testing.T, now *time.Time) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return *now }
	return s
}

// The at-most-once guarantee is the one that matters: a self-task that re-fired
// on every poll would loop forever, and one lost on a crash is better than that.
func TestDueNowFiresExactlyOnce(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := storeAt(t, &now)

	if _, err := s.Add("check the download", now.Add(5*time.Minute), "dl"); err != nil {
		t.Fatal(err)
	}
	if due, _ := s.DueNow(); len(due) != 0 {
		t.Fatalf("fired before it was due: %d", len(due))
	}

	now = now.Add(6 * time.Minute)
	due, _ := s.DueNow()
	if len(due) != 1 || due[0].Prompt != "check the download" {
		t.Fatalf("did not fire when due: %v", due)
	}
	// The whole point: a second poll must not fire it again.
	if again, _ := s.DueNow(); len(again) != 0 {
		t.Fatalf("re-fired an already-run task: %d", len(again))
	}
}

func TestPendingOrderAndCancel(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := storeAt(t, &now)

	a, _ := s.Add("task A far", now.Add(10*time.Minute), "")
	now = now.Add(time.Second) // advance so the second task gets a distinct id
	_, _ = s.Add("task B soon", now.Add(2*time.Minute), "")

	pending, _ := s.Pending()
	if len(pending) != 2 {
		t.Fatalf("pending=%d, want 2", len(pending))
	}
	if pending[0].Prompt != "task B soon" {
		t.Fatalf("pending should be soonest-first, got %q first", pending[0].Prompt)
	}

	if ok, _ := s.Cancel(a.ID); !ok {
		t.Fatal("cancel of a real task returned false")
	}
	if pending, _ := s.Pending(); len(pending) != 1 || pending[0].Prompt != "task B soon" {
		t.Fatalf("after cancel: %v", pending)
	}
	if ok, _ := s.Cancel("no-such-id"); ok {
		t.Fatal("cancelled a task that does not exist")
	}
}

// A task set before a daemon restart must still fire after it — the file is the
// source of truth, not memory.
func TestSurvivesRestart(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()

	s1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s1.now = func() time.Time { return now }
	if _, err := s1.Add("persist me", now.Add(1*time.Minute), ""); err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Minute)
	s2, err := Open(dir) // a fresh Store over the same file, as a restart would build
	if err != nil {
		t.Fatal(err)
	}
	s2.now = func() time.Time { return now }
	if due, _ := s2.DueNow(); len(due) != 1 {
		t.Fatalf("task did not survive restart: got %d due", len(due))
	}
}

// An unreadable task list must not vanish quietly.
//
// A corrupt file cannot be allowed to wedge every poll, so load starts fresh.
// What it must not do is start fresh in silence: everything she has promised to
// come back and do lives in that file, and an empty schedule is indistinguishable
// from one that was thrown away. The symptom is her saying she will follow up and
// then never following up, which is the failure the daemon exists to prevent.
func TestAnUnreadableTaskListIsKeptAndReported(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("write the report", time.Now().Add(time.Hour), "a real task"); err != nil {
		t.Fatal(err)
	}
	if p, err := s.Pending(); err != nil || len(p) != 1 {
		t.Fatalf("the task did not land: %v %v", p, err)
	}

	// Something corrupts it between runs.
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	pending, err := fresh.Pending()
	if err != nil {
		t.Fatalf("a corrupt list wedged the poll: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("unreadable tasks were returned anyway: %v", pending)
	}
	lost := fresh.Lost()
	if lost == "" {
		t.Fatal("the schedule was thrown away and nothing recorded it")
	}
	if _, err := os.Stat(lost); err != nil {
		t.Errorf("the unreadable list was destroyed rather than set aside: %v", err)
	}
	// And scheduling still works afterwards, or the cure is worse.
	if _, err := fresh.Add("carry on", time.Now().Add(time.Hour), ""); err != nil {
		t.Errorf("scheduling stayed broken after the bad file was set aside: %v", err)
	}
}

// A healthy store reports no loss, or the warning becomes noise nobody reads.
func TestAHealthyScheduleReportsNoLoss(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pending(); err != nil {
		t.Fatal(err)
	}
	if got := s.Lost(); got != "" {
		t.Errorf("a healthy store claimed a loss at %q", got)
	}
}
