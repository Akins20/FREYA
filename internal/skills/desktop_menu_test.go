package skills

import (
	"reflect"
	"testing"

	"github.com/Akins20/FREYA/internal/a11y"
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
