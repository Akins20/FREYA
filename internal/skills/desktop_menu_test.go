package skills

import (
	"reflect"
	"testing"

	"github.com/Akins20/FREYA/internal/a11y"
	"os"
	"strings"
)

// The separators a person or a model actually writes, and none of them should
// need the caller to know which one this expects.
func TestAMenuPathSplitsOnWhatPeopleWrite(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"File > Save As", []string{"File", "Save As"}},
		{"File>Save As", []string{"File", "Save As"}},
		{"File / Save As", []string{"File", "Save As"}},
		{"View > Zoom > Zoom In", []string{"View", "Zoom", "Zoom In"}},
		{"  File  >  Save As  ", []string{"File", "Save As"}},
		// An arrow leaves a stray dash on the segment when split on > alone.
		{"File -> Save As", []string{"File", "Save As"}},
		{"File | Preferences", []string{"File", "Preferences"}},
		// One level is a valid path: a menu bar item that acts on its own.
		{"Help", []string{"Help"}},
		{"", nil},
		{"   ", nil},
		{">>>", nil},
	} {
		if got := menuPath(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("menuPath(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// A missing step names the menu it was looked for in, not the window. "There is
// no Save As in File" is the useful sentence; naming the window every time
// buries which level actually failed.
func TestAMissingStepNamesTheMenuItWasLookedForIn(t *testing.T) {
	window := &a11y.Node{Name: "Notepad", Role: "frame"}
	steps := []string{"File", "Save As"}
	if got := whereWeAre(steps, 0, window); got != `"Notepad"` {
		t.Errorf("the first level should name the window, got %s", got)
	}
	if got := whereWeAre(steps, 1, window); got != `"File"` {
		t.Errorf("a later level should name the menu above it, got %s", got)
	}
}

// Choosing a menu entry must say whether anything happened, like clicking does.
//
// "Chose File > Save As." reads as success whether or not the application did
// anything, and a menu entry is the more consequential of the two actions — it
// is where Quit, Delete and Save As live. DoAction answering true means the
// toolkit accepted the call, not that a person would notice a difference.
//
// Asserted against the source, because the failure is a missing call rather than
// a wrong result: every behavioural test of the walker passes either way.
func TestChoosingAMenuEntryReportsWhetherAnythingChanged(t *testing.T) {
	src, err := os.ReadFile("desktop_menu.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "a11y.Fingerprint(window)") {
		t.Error("the menu walker does not sample the window before choosing, so a " +
			"choice that did nothing reads exactly like one that worked")
	}
	if !strings.Contains(body, "treeChange(ctx, title, before)") {
		t.Error("the menu walker does not report whether the window changed")
	}
}
