package guard

import (
	"context"
	"errors"
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

// Note must be safe on a nil guard.
//
// Its whole contract is that recording cannot affect the caller: a tool that
// notes something also has to work in a session assembled without a guard, which
// the tests build routinely. Written without the check, the first such caller
// panicked and took the package's suite with it — a bookkeeping call bringing
// down the thing it was only supposed to observe.
func TestNoteOnANilGuardDoesNothingRatherThanPanic(t *testing.T) {
	var g *Guard
	g.Note(Action{Kind: KindWrite, Command: "learn service mail"}, "ok", nil)
	g.Note(Action{Kind: KindExec, Command: "stop server 1"}, "ok", errors.New("boom"))
}
