package a11y

import (
	"context"
	"fmt"
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

// gdbus tags the type once, and everything after the first child arrives bare.
//
// This is the reply a live GTK window actually sends, and the expression above
// was written against a two-child reply where both carried the tag — which is
// what a reply looks like when you construct it by hand from the documentation
// rather than by reading one off the bus:
//
//	([(':1.1', objectpath '/…/1'), (':1.1', '/…/2')],)
//
// GVariant's textual form annotates a value only where the type would otherwise
// be ambiguous. Requiring the keyword matched exactly one child of every node,
// the walk descended into that one, and a GTK window with a menu bar, a text
// field and a button was reported as a frame containing a single separator: the
// leftmost path to the first leaf, handed over as the whole window with no
// error anywhere.
func TestEveryChildIsReadWhenGdbusTagsOnlyTheFirst(t *testing.T) {
	reply := "([(':1.1', objectpath '/org/a11y/atspi/accessible/1'), " +
		"(':1.1', '/org/a11y/atspi/accessible/2'), " +
		"(':1.1', '/org/a11y/atspi/accessible/3')],)"

	kids, sent := parseChildren(reply)
	if len(kids) != 3 {
		t.Fatalf("read %d children from a reply with three: %v", len(kids), kids)
	}
	if sent != 3 {
		t.Fatalf("counted %d children in a reply with three", sent)
	}
	for i, want := range []string{"/org/a11y/atspi/accessible/1",
		"/org/a11y/atspi/accessible/2", "/org/a11y/atspi/accessible/3"} {
		if kids[i].Path != want {
			t.Errorf("child %d is %q, want %q", i, kids[i].Path, want)
		}
		if kids[i].Bus != ":1.1" {
			t.Errorf("child %d has bus %q", i, kids[i].Bus)
		}
	}
}

// The two counts disagree when the expression stops matching, and disagreeing is
// the whole point of counting twice.
//
// Without this a parser that reads some of a reply reports a smaller window,
// and a smaller window is indistinguishable from a simpler one. Nothing above
// this could have caught the tagging bug, because every layer faithfully passed
// on what it was given.
func TestAReplyThatOnlyPartlyParsesIsCaught(t *testing.T) {
	// Three tuples, one of them shaped so the expression cannot read it.
	reply := "([(':1.1', objectpath '/a'), (':1.1', '/b'), (':1.1', broken)],)"
	kids, sent := parseChildren(reply)
	if sent <= len(kids) {
		t.Fatalf("a reply with %d tuples and %d parsed children was not noticed as short",
			sent, len(kids))
	}

	// And an ordinary reply must not trip it, or the warning is on every read.
	kids, sent = parseChildren("([(':1.1', objectpath '/a'), (':1.1', '/b')],)")
	if sent != len(kids) {
		t.Errorf("a complete reply was reported as short: %d tuples, %d children", sent, len(kids))
	}
	if kids, sent = parseChildren("([],)"); sent != 0 || len(kids) != 0 {
		t.Errorf("an empty reply gave %d tuples and %d children", sent, len(kids))
	}
}

// A reader that read everything says nothing, and one that did not says what.
//
// The empty case matters as much as the other: a caveat attached to every
// answer is a caveat that stops being read, and then the one answer that needed
// it looks like all the rest.
func TestAReaderSaysWhatItCouldNotRead(t *testing.T) {
	r := &Reader{}
	if got := r.Incomplete(); got != "" {
		t.Errorf("a reader that read everything still complained: %q", got)
	}

	r.cannot("this window nests deeper than 40 levels and the rest was not read")
	r.cannot("this window nests deeper than 40 levels and the rest was not read")
	if got := r.Incomplete(); !strings.Contains(got, "nests deeper") {
		t.Errorf("the gap is not reported: %q", got)
	}
	// Deduped, because a bound that fires on every branch of a wide tree would
	// otherwise repeat itself hundreds of times and bury the answer.
	if got := r.Incomplete(); strings.Count(got, "nests deeper") != 1 {
		t.Errorf("the same gap was recorded twice: %q", got)
	}

	r.cannot("part of this window would not answer when asked what it contains")
	if got := r.Incomplete(); !strings.Contains(got, "would not answer") {
		t.Errorf("the second gap was lost: %q", got)
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

// fakeBus answers exactly what a GTK 3 application answered on a live
// accessibility bus, including the two refusals that matter.
//
// The replies are transcribed from a gdbus introspection of a running window,
// not written from the specification. That is the point: every bug this file
// found was a disagreement between what the interface looks like it should be
// and what it is, and a fake built from the same assumption as the code cannot
// catch those.
func fakeBus(t *testing.T) func(context.Context, string, string, string, ...string) (string, error) {
	t.Helper()
	return func(_ context.Context, _, path, method string, args ...string) (string, error) {
		switch {
		// There is no GetNActions. The real bus says so with UnknownMethod, and
		// reading that as "no actions" is what disabled the whole feature.
		case method == "org.a11y.atspi.Action.GetNActions":
			return "", fmt.Errorf(`GDBus.Error:org.freedesktop.DBus.Error.UnknownMethod: ` +
				`Method "GetNActions" with signature "" on interface ` +
				`"org.a11y.atspi.Action" doesn't exist`)

		case method == propsGet && len(args) == 2 && args[0] == "org.a11y.atspi.Action" &&
			args[1] == "NActions":
			if path == "/button" {
				return "(<1>,)", nil
			}
			// A label has no Action interface at all, which is a real error and
			// must stay one.
			return "", fmt.Errorf("GDBus.Error:org.freedesktop.DBus.Error.InvalidArgs: " +
				"No such interface")

		case method == "org.a11y.atspi.Action.GetName":
			return "('click',)", nil

		case method == "org.a11y.atspi.Action.DoAction":
			return "(true,)", nil

		// gdbus's own option parser eats a leading dash, so GetText 0 -1 never
		// reaches the bus at all — it exits with its usage text.
		case method == "org.a11y.atspi.Text.GetText" && len(args) == 2 && args[1] == "-1":
			return "", fmt.Errorf("gdbus: unrecognised option '-1'")

		case method == propsGet && len(args) == 2 && args[0] == "org.a11y.atspi.Text" &&
			args[1] == "CharacterCount":
			return "(<12>,)", nil

		case method == "org.a11y.atspi.Text.GetText":
			return "('Ada Lovelace',)", nil

		case method == "org.a11y.atspi.EditableText.SetTextContents":
			return "(true,)", nil
		}
		return "", fmt.Errorf("unexpected call %s", method)
	}
}

// How many actions a node has is a property, and asking for it as a method
// disables every action on every toolkit while looking exactly like an
// application that supports none.
//
// This is the bug that made desktop_click fall back to aiming a pointer at
// coordinates and desktop_menu refuse every menu, for as long as both existed.
// It survived a probe because the probe only asked nodes that genuinely have no
// Action interface — a frame, an application, a dialog, some panels, a label —
// where an error is the right answer, so every reply confirmed it.
func TestActionCountIsAPropertyAndNotAMethod(t *testing.T) {
	r := &Reader{invoke: fakeBus(t)}
	button := &Node{Bus: ":1.1", Path: "/button", Role: "push button", Name: "Submit"}

	got := r.Actions(context.Background(), button)
	if len(got) != 1 || got[0] != "click" {
		t.Fatalf("a button with one action came back with %v", got)
	}
	if err := r.Do(context.Background(), button, 0); err != nil {
		t.Errorf("pressing a button through its own handler failed: %v", err)
	}

	// And a node with no Action interface must still come back with nothing,
	// or the fix has simply moved the false answer to the other side.
	label := &Node{Bus: ":1.1", Path: "/label", Role: "label", Name: "Full Name"}
	if got := r.Actions(context.Background(), label); len(got) != 0 {
		t.Errorf("a label reported actions: %v", got)
	}
	if err := r.Do(context.Background(), label, 0); err == nil {
		t.Error("a label was actioned without complaint")
	}
}

// Reading a field back must not pass an argument gdbus mistakes for an option.
//
// AT-SPI takes -1 as "to the end of the text", and gdbus exits with its usage
// message before the call is ever made. Every field on every toolkit therefore
// read as unreadable, and desktop_type_into set the text correctly and then
// reported that it could not tell whether it had landed.
func TestAFieldIsReadBackWithoutADashArgument(t *testing.T) {
	r := &Reader{invoke: fakeBus(t)}
	field := &Node{Bus: ":1.1", Path: "/entry", Role: "text", Name: "Full Name"}

	got, ok := r.Text(context.Background(), field)
	if !ok {
		t.Fatal("a readable field came back unreadable")
	}
	if got != "Ada Lovelace" {
		t.Errorf("read %q", got)
	}

	// And the whole round trip, which is what the tool actually calls.
	back, err := r.SetText(context.Background(), field, "Ada Lovelace")
	if err != nil {
		t.Fatalf("setting text failed: %v", err)
	}
	if back != "Ada Lovelace" {
		t.Errorf("the field read back as %q", back)
	}
}

// A click must never be satisfied by an action that only moves focus.
//
// The two toolkits do not share a vocabulary and one of them is a trap. GTK
// gives a button the single action "click". Qt gives it "Press" and also
// "SetFocus", and gives a text field "SetFocus" alone — so taking whichever
// action came first would focus a field, return success, and report that the
// field had been pressed. Nothing downstream could tell: the tool returns, the
// guard records an action, and the click never happened.
func TestAnActionIsChosenByNameAndFocusIsNotAClick(t *testing.T) {
	for _, c := range []struct {
		what  string
		acts  []string
		want  int
		found bool
	}{
		{"a GTK button", []string{"click"}, 0, true},
		{"a Qt button", []string{"Press", "SetFocus"}, 0, true},
		{"a Qt button listing focus first", []string{"SetFocus", "Press"}, 1, true},
		{"a Qt menu bar entry", []string{"ShowMenu"}, 0, true},
		{"a Qt menu item", []string{"Press"}, 0, true},
		{"a GTK text field", []string{"activate"}, 0, true},
		// The one that matters: nothing here presses anything, so the caller has
		// to fall through to the pointer rather than claim a click.
		{"a Qt text field", []string{"SetFocus"}, 0, false},
		{"a node with an unknown verb", []string{"Frobnicate"}, 0, false},
		{"a node with no actions", nil, 0, false},
	} {
		got, ok := PreferredAction(c.acts)
		if ok != c.found {
			t.Errorf("%s: found=%v, want %v (from %v)", c.what, ok, c.found, c.acts)
			continue
		}
		if ok && got != c.want {
			t.Errorf("%s: chose %d (%q), want %d (%q)", c.what, got, c.acts[got], c.want, c.acts[c.want])
		}
	}
}

// An opened menu is populated when something inside it has a name, not when it
// has children.
//
// Qt wraps a menu's items in an unnamed popup menu. That wrapper exists the
// instant the menu opens and its items arrive a moment later, so waiting for
// children stopped waiting too early, and searching one level down found the
// wrapper and nothing else: "nothing in File is called Save As", about a File
// menu containing Save As.
func TestAnOpenedMenuIsEmptyUntilSomethingInItHasAName(t *testing.T) {
	// The Qt shape, mid-open: the wrapper is there and holds nothing.
	opening := &Node{Role: "menu item", Name: "File", Children: []*Node{
		{Role: "popup menu"},
	}}
	if Named(opening) {
		t.Error("a menu holding an empty unnamed wrapper was called populated")
	}

	// And once the items land, through the wrapper.
	opened := &Node{Role: "menu item", Name: "File", Children: []*Node{
		{Role: "popup menu", Children: []*Node{
			{Role: "menu item", Name: "Save As"},
		}},
	}}
	if !Named(opened) {
		t.Error("a Qt menu with items behind its wrapper was called empty")
	}

	// The GTK shape, where the items hang directly off the menu.
	if !Named(&Node{Role: "menu", Name: "File", Children: []*Node{
		{Role: "menu item", Name: "Save As"},
	}}) {
		t.Error("a GTK menu with items was called empty")
	}
	if Named(nil) || Named(&Node{Role: "menu", Name: "File"}) {
		t.Error("a menu with nothing in it was called populated")
	}
}

// SHOWING is bit 25 of the first state word, and the reply tags its type once.
//
// Both halves were measured against a live Qt popup rather than read off a
// header. Closed it answers ENABLED, RESIZABLE and SENSITIVE; opened it gains
// exactly SHOWING and VISIBLE; and after the item's own action fires and the
// application's handler runs, it answers the open value unchanged. That last
// one is the finding: choosing something does not close the menu it was in, on
// every toolkit.
func TestShowingIsReadFromTheStateBitmap(t *testing.T) {
	closed := "([uint32 18874624, 0],)"
	open := "([uint32 1126170882, 0],)"

	bit := func(reply string) bool {
		m := reState.FindStringSubmatch(reply)
		if m == nil {
			t.Fatalf("no state word in %q", reply)
		}
		var v uint64
		if _, err := fmt.Sscanf(m[1], "%d", &v); err != nil {
			t.Fatal(err)
		}
		return v&(1<<stateShowing) != 0
	}
	if bit(closed) {
		t.Error("a closed popup was read as showing")
	}
	if !bit(open) {
		t.Error("an open popup was read as hidden")
	}
	// The second word carries no type tag, exactly like every other array reply
	// from gdbus, so a parser that insists on one reads nothing at all.
	if m := reState.FindAllStringSubmatch(open, -1); len(m) != 1 {
		t.Errorf("expected the tag on the first word only, matched %d", len(m))
	}
}

// Chromium is a third vocabulary, and it disagrees with itself inside one tree.
//
// Measured on Electron 31: the menu bar entry answers doDefault, the button
// press, the text field activate, a clickable div click, and the text node
// inside that div clickAncestor — which is the useful one, because the ancestor
// carries the handler. showContextMenu is on every node and must never count as
// a click: performing it opens a context menu and reports a press.
func TestChromiumsActionNamesAreRecognised(t *testing.T) {
	for _, c := range []struct {
		what  string
		acts  []string
		want  string
		found bool
	}{
		{"a Chromium menu bar entry", []string{"doDefault", "showContextMenu"}, "doDefault", true},
		{"a Chromium button", []string{"press", "showContextMenu"}, "press", true},
		{"a Chromium text field", []string{"activate", "showContextMenu"}, "activate", true},
		{"a clickable div", []string{"click", "showContextMenu"}, "click", true},
		{"text inside a clickable div", []string{"clickAncestor", "showContextMenu"}, "clickAncestor", true},
		// The right button is not a click, and it is on every single node.
		{"a node offering only the context menu", []string{"showContextMenu"}, "", false},
	} {
		i, ok := PreferredAction(c.acts)
		if ok != c.found {
			t.Errorf("%s: found=%v, want %v (from %v)", c.what, ok, c.found, c.acts)
			continue
		}
		if ok && c.acts[i] != c.want {
			t.Errorf("%s: chose %q, want %q", c.what, c.acts[i], c.want)
		}
	}
}

// A window whose contents are withheld has to be told apart from an empty one,
// and the signature is the action vocabulary.
//
// Chromium publishes showContextMenu on every node; GTK and Qt publish it
// nowhere. It is only ever used to decide what to say, never what to do, so
// being wrong costs a sentence of advice.
func TestChromiumIsRecognisedByItsActions(t *testing.T) {
	if !ChromiumLike([]string{"doDefault", "showContextMenu"}) {
		t.Error("a Chromium frame was not recognised")
	}
	if ChromiumLike([]string{"click"}) || ChromiumLike([]string{"Press", "SetFocus"}) {
		t.Error("a GTK or Qt node was mistaken for Chromium")
	}
	if ChromiumLike(nil) {
		t.Error("a node with no actions was mistaken for Chromium")
	}
}
