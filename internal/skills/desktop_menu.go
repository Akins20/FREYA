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
// A button is in the tree before it is pressed. A submenu need not be: some
// toolkits fill a menu only when it is first shown, so a tree read before the
// click shows an empty "File" and one read after it shows Open, Save and the
// rest. The same node answers differently at two moments, which means each
// level has to be opened and then looked at again rather than planned from a
// single reading.
//
// Not all of them, which is worth saying because the original wording here said
// menus do not contain their items until shown, flatly, and GTK contradicts it:
// a GTK menu lists Save As before anything opens it. The opening is still
// necessary, because Qt and Chromium do not, and there is nothing on the node
// that says which kind it is.
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
// Choosing an item is not one of the paths that closes it, which took a Qt
// window to find out. GTK dismisses its own menu when an item fires, so the
// success path assumed the work was done. Qt does not: the popup keeps SHOWING
// and keeps its pointer grab, and the next click anywhere in the application is
// spent dismissing it rather than landing where it was aimed — silently, since
// a synthetic click cannot tell it was eaten. So the success path asks each
// menu whether it is still on screen and dismisses only those that are. Asking
// matters as much as dismissing: an unconditional Escape after a successful
// choice would cancel the dialog that choice had just opened.
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
				"Each level is opened and then read, because some toolkits do not fill a " +
				"menu until it is shown, so this reaches things no screenshot lists and no " +
				"keyboard shortcut exists for.\n\n" +
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
		Serial:  true,
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
				// A bus that was read short cannot settle whether the window is
				// there, and saying it is not would be the same unearned claim the
				// tree makes when it stops early.
				return "", fmt.Errorf("%w%s", err, unreadNote(reader.Incomplete()))
			}

			action := guard.Action{
				Kind:    guard.KindInput,
				Command: "menu",
				Args:    steps,
				Reason: fmt.Sprintf("choose %s from the menus of %s",
					strings.Join(steps, " > "), quoteOrAny(window.Name)),
			}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				return walkMenu(ctx, reader, window, title, steps)
			})
		},
	})
}

// walkMenu opens each level in turn, and closes what it opened whatever happens.
func walkMenu(ctx context.Context, reader *a11y.Reader, window *a11y.Node, title string, steps []string) (string, error) {
	var opened []*a11y.Node
	chosen := false
	// Two ways out, and they need different tidying. A failure leaves menus
	// definitely open, so they are dismissed without asking. A success used to
	// assume the toolkit had closed them, which is true of GTK and false of Qt,
	// where the popup stays up holding a pointer grab and swallows the next
	// click whole. So on success the menus are asked whether they are still on
	// screen, and only the ones that say yes are dismissed — a blind Escape
	// after a successful choice would cancel whatever the choice opened.
	defer func() {
		if !chosen {
			closeMenus(ctx, len(opened))
			return
		}
		for i := len(opened) - 1; i >= 0; i-- {
			if reader.StillOpen(ctx, opened[i]) {
				closeMenus(ctx, 1)
			}
		}
	}()

	here := window
	for i, step := range steps {
		node := a11y.Find(here, step, "")
		if node == nil {
			return "", notInTree(reader.Incomplete(), step, whereWeAre(steps, i, window),
				clip(a11y.Describe(here), 900))
		}
		last := i == len(steps)-1
		if last {
			// By name rather than by position, for the same reason as
			// desktop_click: Qt lists SetFocus among a widget's actions, and
			// choosing a menu entry by focusing it would report success without
			// choosing anything.
			acts := reader.Actions(ctx, node)
			pick, ok := a11y.PreferredAction(acts)
			if !ok {
				return "", fmt.Errorf("%q is the end of the path and publishes no way to "+
					"choose it — it may be a submenu you have not finished naming, or a "+
					"heading%s", step, listing(acts))
			}
			// Sampled before the choice, for the same reason desktop_click does it:
			// "Chose File > Save As." reads as success whether or not anything
			// happened, and a menu entry is the more consequential of the two.
			// DoAction answering true means the toolkit accepted the action, not
			// that the application did anything a person would notice.
			before := a11y.Fingerprint(window)
			if err := reader.Do(ctx, node, pick); err != nil {
				return "", fmt.Errorf("%q is the end of the path and does nothing when "+
					"chosen — it may be a submenu you have not finished naming, or a "+
					"heading: %w", step, err)
			}
			time.Sleep(400 * time.Millisecond)
			// Chosen. Whether anything is left hanging open is a question for
			// the menus themselves, asked on the way out.
			chosen = true
			return fmt.Sprintf("Chose %s.%s", strings.Join(steps, " > "),
				treeChange(ctx, title, before)), nil
		}
		// Noted before the menu opens, so a menu that opens somewhere else can
		// be recognised by being somewhere that was not there a moment ago.
		windows := reader.WindowPaths(ctx)
		if err := reader.OpenAndRefresh(ctx, node); err != nil {
			return "", fmt.Errorf("%q would not open: %w", step, err)
		}

		// Three toolkits put the items in three different places. GTK hangs
		// them off the menu, Qt hides them behind an unnamed popup wrapper
		// under it, and Chromium opens a whole second top-level window and
		// leaves the button childless. Only the last one cannot be reached by
		// looking under the node, so when there is nothing named under it, the
		// window that just appeared is the menu.
		level := node
		if !a11y.Named(level) {
			if popup := reader.OpenedElsewhere(ctx, windows); popup != nil {
				level = popup
			}
		}
		opened = append(opened, level)

		// Named rather than non-empty. Qt wraps a menu's items in an unnamed
		// popup, which exists the instant the menu opens and is empty for a
		// moment after — so counting children called an opening menu populated
		// and then found nothing in it.
		if !a11y.Named(level) {
			// Three things produce an empty menu and only two of them are about
			// the menu. The third is a read that came back short, and it must not
			// be reported as a menu with nothing in it.
			return "", fmt.Errorf("%q opened and is empty, so there is no %q inside it. "+
				"Either the path is wrong or this application fills that menu only when "+
				"a person opens it%s", step, steps[i+1], unreadNote(reader.Incomplete()))
		}
		here = level
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

// listing names what a node did offer, so a refusal says why rather than only
// that. An entry that publishes nothing and one that publishes only SetFocus
// fail for different reasons and want different next moves.
func listing(acts []string) string {
	if len(acts) == 0 {
		return ""
	}
	return fmt.Sprintf(" (it offers only %s)", strings.Join(acts, ", "))
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
