package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/Akins20/FREYA/internal/guard"
	"os"
)

// The tool has to exist and be gated, because it moves a real pointer on the
// user's screen.
func TestDesktopClickIsRegisteredAndGuarded(t *testing.T) {
	r := New()
	registerDesktopClick(r, guard.New(func(context.Context, guard.Action, guard.Assessment) bool {
		return true
	}, nil))
	if !r.Has("desktop_click") {
		t.Fatal("desktop_click is not registered")
	}

	// And not at all without a guard, like every other input action here: a
	// pointer that moves with nothing classifying the action is the one thing
	// this package refuses to build.
	bare := New()
	registerDesktopClick(bare, nil)
	if bare.Has("desktop_click") {
		t.Error("desktop_click registered without a guard")
	}
}

// A name is the one thing it cannot guess at, and the refusal has to say so
// before anything touches the bus.
func TestDesktopClickRefusesWithoutAName(t *testing.T) {
	r := New()
	registerDesktopClick(r, guard.New(func(context.Context, guard.Action, guard.Assessment) bool {
		return true
	}, nil))

	_, err := r.Execute(context.Background(), "desktop_click", map[string]any{"name": "   "})
	if err == nil {
		t.Fatal("a blank name was accepted")
	}
	// Which layer refuses is not the point and varies by machine: the registry
	// rejects a blank required argument before the handler runs, and on a
	// headless one the environment checks fire first. Any of those is correct.
	// What matters is that none of them is a click.
	for _, ok := range []string{"name", "xdotool", "display", "Wayland", "accessibility"} {
		if strings.Contains(err.Error(), ok) {
			return
		}
	}
	t.Errorf("refused for an unexpected reason: %v", err)
}

// A screenshot has to say it is pixels.
//
// The two ways she can learn about a native window produce claims of very
// different strength and read identically in a reply. "The Save button is greyed
// out" is a fact when the toolkit said so and a guess about a colour when it came
// from an image — and wrong on any low-contrast theme. Nothing else in the reply
// distinguishes them.
func TestAScreenshotSaysItIsPixels(t *testing.T) {
	src, err := os.ReadFile("desktop.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, want := range []string{"This is pixels", "desktop_inspect asks"} {
		if !strings.Contains(body, want) {
			t.Errorf("the screenshot result no longer says %q, so a pixel guess and a "+
				"toolkit fact come back looking the same", want)
		}
	}
}

// A control that is missing from a tree she could not fully read is not a
// control that is missing.
//
// This is the wording that decides what she does next, and the run that found
// the GetChildren parser bug shows the cost of getting it wrong. The tree came
// back as one node of a twenty-node window, desktop_type_into answered "nothing
// in ControlTarget is called Full Name" about a window with a field called Full
// Name, and she believed it: five rounds spent looking for another way in,
// then a confident wrong answer. The tool never returned an error, so nothing
// else in the stack had a reason to doubt it.
func TestAMissingControlSaysWhetherTheTreeWasFullyRead(t *testing.T) {
	tree := "frame \"ControlTarget\"\n  separator"

	// Read in full: the window genuinely does not contain it, and the answer is
	// allowed to be flat.
	whole := notInTree("", "Full Name", "\"ControlTarget\"", tree).Error()
	if !strings.Contains(whole, "nothing in") {
		t.Errorf("a complete read hedges anyway: %q", whole)
	}
	if strings.Contains(whole, "not fully") || strings.Contains(whole, "unread") {
		t.Errorf("a complete read warns about a gap it does not have: %q", whole)
	}

	// Read partially: the same miss is now a question about the reading, and it
	// has to say what to do about it.
	partial := notInTree("3 of 4 elements in one part of this window could not be read",
		"Full Name", "\"ControlTarget\"", tree).Error()
	for _, want := range []string{"Full Name", "not read", "Read it again"} {
		if !strings.Contains(partial, want) {
			t.Errorf("a partial read does not say %q: %s", want, partial)
		}
	}
	// And it must not assert the thing it cannot know.
	if strings.Contains(partial, "nothing in") {
		t.Errorf("a partial read still claims the window does not contain it: %s", partial)
	}
	// The tree it did get is kept either way, or she has nothing to work from.
	if !strings.Contains(partial, "separator") || !strings.Contains(whole, "separator") {
		t.Error("the elements that were read were thrown away with the failure")
	}
}

// The caveat on a tree is absent when there is nothing to caveat, because a
// warning printed on every answer is a warning nobody reads.
func TestATreeOnlyWarnsWhenItIsIncomplete(t *testing.T) {
	if got := unreadNote(""); got != "" {
		t.Errorf("a complete tree carried a warning: %q", got)
	}
	got := unreadNote("this window has more than 4000 elements and the rest was not read")
	if !strings.Contains(got, "NOT all of the window") {
		t.Errorf("the warning does not say the tree is partial: %q", got)
	}
	if !strings.Contains(got, "4000") {
		t.Errorf("the warning drops the reason: %q", got)
	}
}
