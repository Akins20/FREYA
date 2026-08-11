package skills

import (
	"context"
	"strings"
	"testing"
)

// The three answers must stay three. Collapsing "on screen but publishing
// nothing" into "no elements" is the failure this whole path exists to avoid:
// a Tk window, an xterm and anything under Wine are all on screen, focusable and
// typeable, and completely invisible to the accessibility bus.
func TestATitleThatIsOnScreenIsRecognised(t *testing.T) {
	on := []string{"GtkTarget", "XtermTarget", "Mozilla Firefox"}
	for _, want := range []string{"GtkTarget", "gtktarget", "Xterm", "firefox", ""} {
		if !matchesTitle(on, want) {
			t.Errorf("%q was not matched against %v", want, on)
		}
	}
	for _, missing := range []string{"Inkscape", "GtkTargetX"} {
		if matchesTitle(on, missing) {
			t.Errorf("%q was matched against %v and is not there", missing, on)
		}
	}
	// Nothing on screen means an empty fragment matches nothing either, or the
	// message would claim a window exists on a machine with no windows.
	if matchesTitle(nil, "") {
		t.Error("an empty fragment matched on a machine with no windows open")
	}
}

// desktop_inspect has to exist even where it cannot work, so the refusal can
// explain itself rather than the tool simply being absent.
func TestDesktopInspectIsAlwaysRegistered(t *testing.T) {
	r := New()
	RegisterDesktop(r, approveAll())
	if !r.Has("desktop_inspect") {
		t.Error("desktop_inspect is not registered")
	}
}

// A window manager that cannot answer is not an empty desktop.
//
// Measured live. With no EWMH window manager running, wmctrl exits non-zero with
// "Cannot get client list properties", and the first version of onScreenTitles
// returned nil for that exactly as it did for a genuinely empty screen. She was
// told "no window matching XtermTarget is open, and nothing else is either"
// while the xterm was on screen the whole time.
//
// That is a tool's failure reported as a fact about the world, which is the same
// mistake as a download path that was never stat'd and a review that returned
// success having rendered nothing. Written by me, in the middle of a day spent
// fixing it elsewhere.
func TestAWindowManagerThatCannotAnswerIsNotAnEmptyScreen(t *testing.T) {
	// No wmctrl on PATH stands in for the same condition: the question could not
	// be asked, so the answer is an error and never an empty list.
	t.Setenv("PATH", t.TempDir())
	titles, err := onScreenTitles(context.Background())
	if err == nil {
		t.Fatal("an unanswerable question returned a clean empty list")
	}
	if titles != nil {
		t.Errorf("titles came back non-nil alongside an error: %v", titles)
	}
	// And the error has to name what to do instead, like every other refusal here.
	if !strings.Contains(err.Error(), "wmctrl") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
}
