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
