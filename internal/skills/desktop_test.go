package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/Akins20/FREYA/internal/a11y"
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

// wmctrl pads its columns, so the title is the remainder and never a field.
//
// Splitting on a single space produced an empty second field and glued the host
// onto the front of every title. Both symptoms were quiet: listings reported
// "N/A Some Window" and "<hostname> Some Window", putting a machine name into
// answers nobody asked one about, and the focused marker never appeared once,
// because the focused title arrives from xdotool bare and could not equal a
// padded one.
func TestAWindowTitleIsEverythingAfterTheThirdColumn(t *testing.T) {
	for _, c := range []struct{ line, want string }{
		// The real shape, with the desktop number padded.
		{"0x00600018  0 N/A TkTarget", "TkTarget"},
		{"0x00800003  0 somehost ControlTarget", "ControlTarget"},
		// A title with spaces in it, which is why the remainder is taken whole.
		{"0x00400007  1 somehost Untitled Document 1 - Writer", "Untitled Document 1 - Writer"},
		// Single-spaced, which is what the old parser assumed and must still work.
		{"0x00400007 0 N/A Terminal", "Terminal"},
		// A sticky window, where wmctrl writes -1 for the desktop.
		{"0x00400009 -1 N/A Conky", "Conky"},
		// Nothing to take.
		{"", ""},
		{"0x00400007  0 N/A", ""},
		{"garbage", ""},
	} {
		if got := wmctrlTitle(c.line); got != c.want {
			t.Errorf("wmctrlTitle(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

// A window with nothing named in it is explained, and the explanation names
// Chromium when the actions say so.
//
// The test that matters is the first assertion. A Chromium window started
// without --force-renderer-accessibility does have children: it answers
// GetChildren with placeholder nodes whose role and name both fail to read. So
// a check for "has children" called it populated and stayed silent on exactly
// the case the note exists for, which is most of a modern desktop.
func TestAWindowWithNothingNamedInItIsExplained(t *testing.T) {
	// Placeholders, as Chromium hands them over: present, and empty.
	withheld := &a11y.Node{Role: "frame", Name: "ControlTarget", Children: []*a11y.Node{{}, {}}}
	if a11y.Named(withheld) {
		t.Fatal("nameless placeholder children counted as something to aim at")
	}

	// And a window with a real control in it must stay silent, or every answer
	// grows a paragraph.
	real := &a11y.Node{Role: "frame", Name: "ControlTarget", Children: []*a11y.Node{
		{Role: "push button", Name: "Submit"},
	}}
	if !a11y.Named(real) {
		t.Error("a window with a button in it was called nameless")
	}
}
