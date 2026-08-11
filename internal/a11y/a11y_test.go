package a11y

import "testing"

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
