package guard

import (
	"context"
	"testing"
)

// A paste into an open document outranks autonomy.
//
// Every other synthetic input types into a window the user is watching, where a
// wrong keystroke is visible and undone. This one overwrites a region of a
// document that may hold work existing in no file at all — the file on disk is
// the last save. At medium it auto-approves under -yes and in the daemon, which
// would let her paste over an afternoon's work with nobody watching and then be
// asked to save it.
func TestPastingIntoAnOpenDocumentAlwaysAsks(t *testing.T) {
	g := New(func(context.Context, Action, Assessment) bool { return true }, nil)
	g.AutoApprove = RiskMedium // what -yes and the daemon set

	a := g.Assess(Action{
		Kind:    KindInput,
		Command: "paste into LibreOffice Calc",
		Reason:  "add a row",
	})
	if a.Risk < RiskHigh {
		t.Errorf("a paste into an open document assessed %s; it auto-approves under "+
			"-yes below high", a.Risk)
	}
	if !a.Confirm {
		t.Error("it does not require confirmation, so the daemon would do it unwatched")
	}
	if a.Reversible {
		t.Error("it is marked reversible; what it overwrites may exist in no file")
	}

	// Ordinary synthetic input is unchanged — typing into a focused window still
	// sits at medium and still auto-approves when autonomy is on.
	typing := g.Assess(Action{Kind: KindInput, Command: "xdotool type", Reason: "type"})
	if typing.Risk != RiskMedium {
		t.Errorf("ordinary typing assessed %s, want medium", typing.Risk)
	}
}
