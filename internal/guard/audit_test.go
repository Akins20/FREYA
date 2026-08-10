package guard

import (
	"context"
	"testing"
)

// A log that cannot be written must say so, because an incomplete audit log
// looks exactly like a quiet day.
//
// Append's error was discarded at its only call site, so a full disk or a
// revoked permission stopped the log growing with nothing anywhere saying it
// had. This is the one file whose whole purpose is telling the user what she
// did; silence from it is the worst possible failure, and this machine's drive
// sits near capacity.
func TestAnUnwritableLogIsCountedRatherThanIgnored(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenLog(dir)
	if err != nil {
		t.Fatal(err)
	}

	g := New(func(context.Context, Action, Assessment) bool { return true }, log)
	if _, err := g.Run(context.Background(), Action{Kind: KindBrowser, Command: "click x"},
		func(context.Context) (string, error) { return "ok", nil }); err != nil {
		t.Fatal(err)
	}
	if n := log.Dropped(); n != 0 {
		t.Fatalf("precondition: a working log dropped %d", n)
	}

	// The descriptor is closed underneath the log, which is what a full disk or a
	// revoked permission looks like from here: the handle is still set, and the
	// write fails. Deliberately not log.Close(), which nils the handle and is
	// correctly read as "no log configured" rather than as a failure.
	log.file.Close()

	g.Note(Action{Kind: KindExec, Command: "stop server(s) 1"}, "ok", nil)
	if n := log.Dropped(); n == 0 {
		t.Error("a write to a closed log was discarded silently — the audit log can " +
			"stop recording and nothing will ever say so")
	}
}
