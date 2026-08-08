package guard

import (
	"path/filepath"
	"testing"
)

// Asked to build a copy of a website — write the files, open them, refine until
// they match — she was refused on the first file, then the folder, then the
// shell fallback, and ended up pasting the whole page into her reply for the
// user to save by hand. Writing hello.html into her OWN workspace assessed as
// medium, medium needs a confirmation, and the daemon has no terminal: it asks
// aloud, once per file. For a task that is dozens of writes that is not a
// safeguard, it is a wall.
func TestWritingInsideHerOwnWorkspaceDoesNotStopToAsk(t *testing.T) {
	ws := "/home/akins/freya-workspace"
	a := assess(Action{Kind: KindWrite,
		Paths: []string{filepath.Join(ws, "gisada", "index.html")}}, nil, ws)

	if a.Risk >= RiskMedium {
		t.Errorf("a write inside her own workspace scored %v, which needs a "+
			"confirmation she has no way to obtain in the daemon", a.Risk)
	}
}

// And everything outside is untouched. This carve-out is about her scratch
// space, not about writing generally.
func TestWritingOutsideTheWorkspaceStillAsks(t *testing.T) {
	ws := "/home/akins/freya-workspace"
	for _, p := range []string{
		"/home/akins/Documents/thesis.docx",
		"/home/akins/.bashrc",
		"/etc/hosts",
		"/home/akins/freya-workspace-old/notes.txt",  // prefix, not a child
		"/home/akins/freya-workspace/../Documents/x", // escapes via ..
	} {
		a := assess(Action{Kind: KindWrite, Paths: []string{p}}, nil, ws)
		if a.Risk < RiskMedium {
			t.Errorf("%s scored %v — outside the workspace must still ask", p, a.Risk)
		}
	}
}

// Every path, not any. An action touching her workspace AND somewhere else is
// not a workspace action, and taking the lenient half would be the wrong read.
func TestAMixedWriteIsJudgedByTheOutsidePath(t *testing.T) {
	ws := "/home/akins/freya-workspace"
	a := assess(Action{Kind: KindWrite, Paths: []string{
		filepath.Join(ws, "ok.html"),
		"/home/akins/Documents/not-ok.docx",
	}}, nil, ws)
	if a.Risk < RiskMedium {
		t.Errorf("a write that also touches Documents scored %v", a.Risk)
	}
}

// An unset workspace must not turn into a wildcard. Empty matches nothing.
func TestNoWorkspaceMeansNoCarveOut(t *testing.T) {
	for _, ws := range []string{"", "   ", "relative/path"} {
		a := assess(Action{Kind: KindWrite,
			Paths: []string{"/home/akins/anything.txt"}}, nil, ws)
		if a.Risk < RiskMedium {
			t.Errorf("workspace %q let a write through at %v", ws, a.Risk)
		}
	}
}
