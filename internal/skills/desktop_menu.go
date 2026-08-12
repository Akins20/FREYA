package skills

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/a11y"
	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/llm"
)

// Walking a menu by name, one level at a time.
//
// # Why a menu is not just another control
//
// A button is in the tree before it is pressed. A submenu is not: toolkits
// populate a menu when it is first shown, so a tree read before the click shows
// an empty "File" and one read after it shows Open, Save and the rest. The same
// node answers differently at two moments, which means each level has to be
// opened and then looked at again rather than planned from a single reading.
//
// It is also the thing a person uses constantly and she could not reach at all.
// Keystrokes get to a menu with an accelerator and to nothing a designer put
// behind a submenu.
//
// # It closes what it opened
//
// A refusal halfway through leaves a menu hanging over whatever the user is
// looking at, on their real screen. Every path out of here presses Escape as
// many times as levels were opened, including the error paths, because "it did
// not work" and "it did not work and there is now a menu covering your window"
// are different amounts of rude.
//
// # A miss lists what was actually in that menu
//
// Not the whole window. The interesting failure is "there is no Save As in File",
// and the answer to it is File's contents, which is short. Handing back the
// entire tree buries the one thing she needed to read.
func registerDesktopMenu(r *Registry, g *guard.Guard) {
	r.Register(Skill{
		Tool: llm.Tool{
			Name: "desktop_menu",
			Description: "Choose something from a native application's menus, by name: " +
				"'File > Save As', 'Edit > Preferences', 'View > Zoom > Zoom In'.\n\n" +
				"Each level is opened and then read, because menus do not contain their " +
				"items until they are shown — so this reaches things no screenshot lists " +
				"and no keyboard shortcut exists for.\n\n" +
				"If a level is missing it tells you what that menu does contain, and it " +
				"closes anything it opened on the way out.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path": {Type: "string", Description: "The menu path, separated by > or /, " +
					"e.g. 'File > Export > PDF'. Names as desktop_inspect shows them."},
				"window": {Type: "string", Description: "Part of the window title. Omit for the " +
					"first window that publishes a tree."},
			}, "path"),
		},
		Mutates: true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			steps := menuPath(argString(args, "path"))
			if len(steps) == 0 {
				return "", fmt.Errorf("give a menu path, like 'File > Save As'")
			}
			reader, err := a11y.Open(ctx)
			if err != nil {
				return "", fmt.Errorf("%w — fall back to desktop_key with the accelerator", err)
			}
			title := argString(args, "window")
			window, err := reader.Window(ctx, title)
			if err != nil {
				return "", err
			}

			action := guard.Action{
				Kind:    guard.KindInput,
				Command: "menu",
				Args:    steps,
				Reason: fmt.Sprintf("choose %s from the menus of %s",
					strings.Join(steps, " > "), quoteOrAny(window.Name)),
			}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				return walkMenu(ctx, reader, window, steps)
			})
		},
	})
}

// walkMenu opens each level in turn, and closes what it opened whatever happens.
func walkMenu(ctx context.Context, reader *a11y.Reader, window *a11y.Node, steps []string) (string, error) {
	opened := 0
	defer func() { closeMenus(ctx, opened) }()

	here := window
	for i, step := range steps {
		node := a11y.Find(here, step, "")
		if node == nil {
			return "", fmt.Errorf("there is no %q in %s. What is there:\n%s",
				step, whereWeAre(steps, i, window), clip(a11y.Describe(here), 900))
		}
		last := i == len(steps)-1
		if last {
			if err := reader.Do(ctx, node, 0); err != nil {
				return "", fmt.Errorf("%q is the end of the path and does nothing when "+
					"chosen — it may be a submenu you have not finished naming, or a "+
					"heading: %w", step, err)
			}
			time.Sleep(400 * time.Millisecond)
			// Chosen, so nothing is left hanging open.
			opened = 0
			return fmt.Sprintf("Chose %s.", strings.Join(steps, " > ")), nil
		}
		if err := reader.OpenAndRefresh(ctx, node); err != nil {
			return "", fmt.Errorf("%q would not open: %w", step, err)
		}
		opened++
		if len(node.Children) == 0 {
			return "", fmt.Errorf("%q opened and is empty, so there is no %q inside it. "+
				"Either the path is wrong or this application fills that menu only when "+
				"a person opens it", step, steps[i+1])
		}
		here = node
	}
	return "", fmt.Errorf("the path ran out before anything was chosen")
}

// closeMenus presses Escape once per level left open.
//
// Best effort on purpose: this runs on the way out of a failure, and a failure
// to tidy up must not replace the error that caused it.
func closeMenus(ctx context.Context, levels int) {
	for range levels {
		if _, err := run(ctx, 3*time.Second, "xdotool", "key", "Escape"); err != nil {
			return
		}
		time.Sleep(80 * time.Millisecond)
	}
}

// whereWeAre names the menu a missing step was looked for in, so the failure
// says "there is no Save As in File" rather than naming the window every time.
func whereWeAre(steps []string, i int, window *a11y.Node) string {
	if i == 0 {
		return quoteOrAny(window.Name)
	}
	return fmt.Sprintf("%q", steps[i-1])
}

// menuPath splits a path on the separators a person or a model actually writes.
func menuPath(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == '>' || r == '/' || r == '|'
	})
	var out []string
	for _, f := range fields {
		// A lone arrowhead from "->" leaves a stray dash on the segment.
		if f = strings.TrimSpace(strings.Trim(strings.TrimSpace(f), "-")); f != "" {
			out = append(out, strings.TrimSpace(f))
		}
	}
	return out
}
