package a11y

import (
	"context"
	"strings"
	"testing"
)

// gdbus wraps every reply in a tuple, and some in a variant as well. The address
// is the one that matters most: it contains a comma before guid=, and a parser
// that strips commas produces a path that does not exist. A shell version of this
// did exactly that and spent a run connecting to nothing.
func TestUnquoteHandlesTheShapesGdbusReturns(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"('zenity',)", "zenity"},
		{"(<'zenity'>,)", "zenity"},
		{"(<1>,)", "1"},
		{"('dialog',)", "dialog"},
		{"", ""},
		{"('',)", ""},
		// The address, which must survive its internal comma intact.
		{"('unix:path=/root/.cache/at-spi/bus_99,guid=49d93246',)",
			"unix:path=/root/.cache/at-spi/bus_99,guid=49d93246"},
	} {
		if got := unquote(c.in); got != c.want {
			t.Errorf("unquote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// GetChildren returns an array of (bus, objectpath) pairs, and the reply shape is
// exactly what the container showed.
func TestChildPairsAreReadFromTheReply(t *testing.T) {
	reply := "([(':1.1', objectpath '/org/gnome/Zenity/a11y/2ad318c1'), " +
		"(':1.2', objectpath '/org/a11y/atspi/accessible/7')],)"
	got := reChild.FindAllStringSubmatch(reply, -1)
	if len(got) != 2 {
		t.Fatalf("read %d children from a reply with two", len(got))
	}
	if got[0][1] != ":1.1" || got[0][2] != "/org/gnome/Zenity/a11y/2ad318c1" {
		t.Errorf("first child parsed as %q %q", got[0][1], got[0][2])
	}
	if got[1][1] != ":1.2" {
		t.Errorf("second child bus parsed as %q", got[1][1])
	}
	if n := len(reChild.FindAllStringSubmatch("([],)", -1)); n != 0 {
		t.Errorf("an empty reply produced %d children", n)
	}
}

// An anonymous container is recognised by having no name and having children,
// never by its role.
//
// Measured: the same GTK node pyatspi calls "panel" comes back from the D-Bus
// GetRoleName as "generic". A skip list written against either vocabulary stops
// working against the other, which is the GTK-dialog versus Qt-filler lesson one
// level further down.
func TestAnonymousContainersAreSkippedWhateverTheyAreCalled(t *testing.T) {
	tree := &Node{Role: "dialog", Name: "GtkTarget", Children: []*Node{
		{Role: "generic", Children: []*Node{
			{Role: "panel", Children: []*Node{
				{Role: "filler", Children: []*Node{
					{Role: "button", Name: "OK"},
				}},
			}},
		}},
	}}
	got := Describe(tree)
	want := "dialog \"GtkTarget\"\n  button \"OK\""
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// GTK gives every button a label child repeating its own text. Keeping both
// reports every button twice and doubles what she has to read.
func TestAButtonsEchoOfItselfIsDropped(t *testing.T) {
	tree := &Node{Role: "dialog", Name: "D", Children: []*Node{
		{Role: "button", Name: "Cancel", Children: []*Node{{Role: "label", Name: "Cancel"}}},
		// A label that says something different is real content and stays.
		{Role: "button", Name: "OK", Children: []*Node{{Role: "label", Name: "Confirm it"}}},
	}}
	got := Describe(tree)
	want := "dialog \"D\"\n  button \"Cancel\"\n  button \"OK\"\n    label \"Confirm it\""
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A named leaf with no children is kept even with an empty name nowhere to go,
// and nil is not a panic.
func TestDescribeSurvivesTheEdges(t *testing.T) {
	if got := Describe(nil); got != "" {
		t.Errorf("nil rendered as %q", got)
	}
	if got := Describe(&Node{Role: "separator"}); got != "separator" {
		t.Errorf("an anonymous leaf rendered as %q, want it kept", got)
	}
}

// The rendering gdbus actually produces for a (iiii) struct.
//
// Parsing is the half of this that fails quietly: a misread produces a plausible
// rectangle rather than an error, and she clicks a coordinate nobody chose.
func TestExtentsParsingMatchesWhatTheBusReturns(t *testing.T) {
	for _, c := range []struct {
		reply string
		want  Rect
		ok    bool
	}{
		{"((100, 200, 80, 30),)", Rect{100, 200, 80, 30}, true},
		{"((0, 0, 0, 0),)", Rect{}, true},
		// A window scrolled off the left edge answers with negatives rather than
		// failing, which is why Offscreen exists.
		{"((-1920, 40, 300, 20),)", Rect{-1920, 40, 300, 20}, true},
		{"(( 12,  34,  56,  78 ),)", Rect{12, 34, 56, 78}, true},
		// Not a struct: the node has no Component interface and gdbus said so.
		{"", Rect{}, false},
		{"()", Rect{}, false},
		{"GDBus.Error:org.freedesktop.DBus.Error.UnknownMethod", Rect{}, false},
	} {
		got, ok := parseExtents(c.reply)
		if ok != c.ok {
			t.Errorf("%q: ok=%v want %v", c.reply, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("%q: got %+v want %+v", c.reply, got, c.want)
		}
	}
}

// "No position" and "at the origin with no size" are different answers and must
// stay different, because one means the node cannot be clicked and the other
// means the toolkit does not say where it is.
func TestAZeroRectangleIsAnAnswerAndAMissingOneIsNot(t *testing.T) {
	zero, ok := parseExtents("((0, 0, 0, 0),)")
	if !ok {
		t.Fatal("a zero rectangle was treated as no answer at all")
	}
	if !zero.Offscreen() {
		t.Error("a zero-sized box reported as clickable")
	}
	if _, ok := parseExtents("no such interface"); ok {
		t.Error("a failed call was treated as a position")
	}
	for _, r := range []Rect{{0, 0, 10, 10}, {5, 5, 1, 1}} {
		if r.Offscreen() {
			t.Errorf("%+v reported as offscreen", r)
		}
	}
	// Entirely off the left or top edge is unreachable, not merely unusual.
	for _, r := range []Rect{{-100, 10, 50, 10}, {10, -40, 10, 20}} {
		if !r.Offscreen() {
			t.Errorf("%+v reported as clickable", r)
		}
	}
}

func TestCentreIsTheMiddle(t *testing.T) {
	x, y := Rect{100, 200, 80, 30}.Centre()
	if x != 140 || y != 215 {
		t.Errorf("centre = %d,%d want 140,215", x, y)
	}
}

// The shape a real GTK dialog has: every button contains a label carrying the
// same text, so a naive search finds the label and clicking it does nothing.
func TestFindPrefersTheExactMatchAndTheRightRole(t *testing.T) {
	tree := &Node{Role: "dialog", Name: "Save file", Children: []*Node{
		{Role: "push button", Name: "Save As…"},
		{Role: "push button", Name: "Save", Children: []*Node{
			{Role: "label", Name: "Save"},
		}},
		{Role: "push button", Name: "Don't Save"},
	}}

	// Exact beats containing, even though "Save As…" comes first in walk order.
	if got := Find(tree, "Save", ""); got == nil || got.Name != "Save" {
		t.Errorf("got %v, want the exact Save", got)
	}
	// Role disambiguates the button from the label inside it.
	got := Find(tree, "Save", "push button")
	if got == nil || got.Role != "push button" {
		t.Errorf("got %v, want the button rather than its label", got)
	}
	// No exact match: the shortest containing one, not the first.
	if got := Find(tree, "ave", ""); got == nil || got.Name != "Save" {
		t.Errorf("got %v, want the shortest containing match", got)
	}
	if got := Find(tree, "Print", ""); got != nil {
		t.Errorf("found %v for a name that is not there", got)
	}
	if got := Find(nil, "Save", ""); got != nil {
		t.Error("found something in a nil tree")
	}
}

// The fingerprint has to move when the window does and hold still when it does
// not, or "nothing observably changed" is a sentence nobody checked.
func TestFingerprintMovesOnlyWhenTheTreeDoes(t *testing.T) {
	before := &Node{Role: "dialog", Name: "Save", Children: []*Node{
		{Role: "push button", Name: "OK"},
	}}
	same := &Node{Role: "dialog", Name: "Save", Children: []*Node{
		{Role: "push button", Name: "OK"},
	}}
	if Fingerprint(before) != Fingerprint(same) {
		t.Error("two identical trees fingerprinted differently")
	}

	renamed := &Node{Role: "dialog", Name: "Save", Children: []*Node{
		{Role: "push button", Name: "Saving…"},
	}}
	grown := &Node{Role: "dialog", Name: "Save", Children: []*Node{
		{Role: "push button", Name: "OK"},
		{Role: "alert", Name: "File exists"},
	}}
	for name, other := range map[string]*Node{"a relabelled button": renamed, "a new alert": grown} {
		if Fingerprint(before) == Fingerprint(other) {
			t.Errorf("%s did not change the fingerprint", name)
		}
	}

	// Depth is part of it: the same nodes reparented is a different window.
	nested := &Node{Role: "dialog", Name: "Save", Children: []*Node{
		{Role: "panel", Name: "", Children: []*Node{{Role: "push button", Name: "OK"}}},
	}}
	if Fingerprint(before) == Fingerprint(nested) {
		t.Error("moving a button into a panel did not change the fingerprint")
	}
}

// A node with no Action interface must report no actions rather than erroring
// or inventing one. Most nodes are in this position: a window, a panel and a
// label are not actionable, and asking them errors with "No such interface",
// which is the correct answer.
func TestANodeWithoutActionsSaysSo(t *testing.T) {
	r := &Reader{addr: "unix:path=/nonexistent-so-every-call-fails"}
	if got := r.Actions(context.Background(), &Node{Bus: "x", Path: "/y"}); got != nil {
		t.Errorf("a node on a dead bus reported actions: %v", got)
	}
	if got := r.Actions(context.Background(), nil); got != nil {
		t.Errorf("a nil node reported actions: %v", got)
	}
}

// And Do refuses in terms that say why, rather than failing silently or
// pretending the press happened.
func TestDoRefusesWhatCannotBeActioned(t *testing.T) {
	r := &Reader{addr: "unix:path=/nonexistent-so-every-call-fails"}
	err := r.Do(context.Background(), &Node{Name: "Some Label", Role: "label"}, 0)
	if err == nil {
		t.Fatal("performing an action on a label with no Action interface succeeded")
	}
	for _, want := range []string{"Some Label", "label", "no action"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal is missing %q: %v", want, err)
		}
	}
}

// A password field is recognised by its role, because the toolkit knows and a
// guess from the label does not: a field called "PIN" may be plain and a field
// called "Key" may be masked.
func TestAPasswordFieldIsRecognisedByRole(t *testing.T) {
	for _, role := range []string{"password text", "password_text", "PASSWORD TEXT"} {
		if !(&Node{Role: role, Name: "PIN"}).Secret() {
			t.Errorf("role %q was not treated as a credential", role)
		}
	}
	for _, role := range []string{"text", "entry", "label", "button"} {
		if (&Node{Role: role, Name: "Password"}).Secret() {
			t.Errorf("role %q was treated as a credential because of its name", role)
		}
	}
}

// gdbus parses its arguments as GVariant text, so a bare string needs quoting
// and a quote inside it needs escaping. Without this, typing an apostrophe
// silently sends a different string or fails to parse.
func TestTextIsQuotedForTheWire(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"hello", "'hello'"},
		{"it's", `'it\'s'`},
		{`back\slash`, `'back\slash'`},
		{"", "''"},
	} {
		if got := quoteArg(c.in); got != c.want {
			t.Errorf("quoteArg(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Text and SetText must distinguish "cannot be asked" from "is empty". The Text
// interface is optional, and a node that does not implement it is unreadable
// rather than blank.
func TestUnreadableIsNotEmpty(t *testing.T) {
	r := &Reader{addr: "unix:path=/nonexistent-so-every-call-fails"}
	if _, ok := r.Text(context.Background(), &Node{Bus: "x", Path: "/y"}); ok {
		t.Error("a node on a dead bus reported readable text")
	}
	if _, ok := r.Text(context.Background(), nil); ok {
		t.Error("a nil node reported readable text")
	}
	if _, err := r.SetText(context.Background(), nil, "x"); err == nil {
		t.Error("typing into nothing succeeded")
	}
}
