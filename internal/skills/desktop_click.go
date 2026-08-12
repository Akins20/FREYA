package skills

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/a11y"
	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/llm"
	"github.com/Akins20/FREYA/internal/platform"
)

// Clicking a native control by its name.
//
// # The gap this closes
//
// Outside the browser she could focus a window, type into it and press keys, and
// that was all. There was no click. Every native interaction had to be composed
// out of keystrokes and a screenshot to check them against, which works for a
// menu with an accelerator and not at all for a toolbar button, a checkbox, or
// anything a designer put somewhere and gave no keyboard route to.
//
// desktop_inspect made the tree readable. Reading a control she cannot press is
// most of the way to nowhere, so this is the other half: find the node she named,
// ask the toolkit where it is, and click the middle of it.
//
// # Why it refuses more often than it clicks
//
// Because the failure it is guarding against is the one this codebase keeps
// meeting: an action that reports success while nothing happened. Four ways that
// arises here, and all four are refusals rather than clicks:
//
//   - the window publishes no tree, so there is nothing to aim at;
//   - the control is not in the tree under that name;
//   - the toolkit does not implement Component, so nobody knows where it is;
//   - the position is real but off-screen, scrolled away or minimised.
//
// The fourth is the dangerous one. AT-SPI answers for a node that is scrolled out
// of view — with real numbers, no error — and clicking the middle of a rectangle
// at negative coordinates lands on whatever else is there. That is not a missed
// click, it is a click somewhere nobody chose, and it looks like it worked.
//
// # And it says whether anything moved
//
// The tree is fingerprinted before and after. A click that changed nothing reads
// exactly like a click that worked, which is the sentence the browser side spent
// four job applications learning. Here the answer is cheap: roles and names in
// walk order, which move when a dialog opens or a label flips and hold still when
// the pointer merely travels.
func registerDesktopClick(r *Registry, g *guard.Guard) {
	if g == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "desktop_click",
			Description: "Click a control in a native application window by what it is " +
				"called — a button, checkbox, tab or menu item. This is " +
				"browser_click_text for things outside the browser.\n\n" +
				"Run desktop_inspect first and click something you saw there. It reports " +
				"whether the window changed afterwards, so a click that did nothing says " +
				"so instead of reading like a click that worked.\n\n" +
				"It refuses rather than guessing when the window publishes no " +
				"accessibility tree, when nothing in it is called that, or when the " +
				"control is scrolled out of view. In those cases use desktop_screenshot " +
				"with desktop_key — a keyboard route usually exists even where this " +
				"cannot help.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "What the control is called, as " +
					"desktop_inspect showed it."},
				"window": {Type: "string", Description: "Part of the window title. Omit to " +
					"use the first window that publishes a tree."},
				"role": {Type: "string", Description: "Optional, to disambiguate — 'push " +
					"button', 'check box'. A GTK button contains a label with the same " +
					"text, and only the button does anything when pressed."},
			}, "name"),
		},
		Mutates: true,
		Serial:  true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if err := requireX11(); err != nil {
				return "", err
			}
			if !have("xdotool") {
				return "", fmt.Errorf("xdotool is not installed, so nothing here can move " +
					"the pointer")
			}
			if c := platform.Current().Accessibility; !c.Available {
				return "", fmt.Errorf("%s", c.Why)
			}
			name := strings.TrimSpace(argString(args, "name"))
			if name == "" {
				return "", fmt.Errorf("name is required — what does the control say?")
			}

			reader, err := a11y.Open(ctx)
			if err != nil {
				return "", fmt.Errorf("%w — so there is no tree to aim at. desktop_screenshot "+
					"with desktop_key still works on this window", err)
			}
			title := argString(args, "window")
			window, err := reader.Window(ctx, title)
			if err != nil {
				// "publishes nothing" is a claim about the application, and a bus
				// that was read short cannot support it: the application list goes
				// through the same parser as the tree inside a window.
				return "", fmt.Errorf("%w — %s publishes nothing to click. Take a "+
					"screenshot and use keystrokes%s", err, quoteOrAny(title),
					unreadNote(reader.Incomplete()))
			}

			node := a11y.Find(window, name, argString(args, "role"))
			if node == nil {
				// The failure carries what IS there, which is the rule everywhere else
				// in this package: a miss she cannot act on costs a whole round.
				return "", notInTree(reader.Incomplete(), name, quoteOrAny(window.Name),
					clip(a11y.Describe(window), 1200)+emptyWindowNote(ctx, reader, window))
			}

			// The widget's own action first, and a pointer only when there is none.
			//
			// DoAction runs the handler the application registered, which is what a
			// screen reader uses. It needs no position, so it removes both of the
			// refusals below: a toolkit that publishes no extents can still be
			// actioned, and a control scrolled out of view is actioned correctly
			// rather than refused — or worse, clicked at coordinates that are real
			// and point at something else.
			//
			// It also does not need the window raised or focused, so acting on a
			// background window stops disturbing what the user is looking at.
			//
			// Chosen by name, never by position. Qt publishes SetFocus alongside
			// Press, and publishes it alone on a text field, so taking whichever
			// action came first would focus a field and report having pressed it.
			// A node whose only actions are focus-like falls through to the
			// pointer below, which is what a click actually means.
			acts := reader.Actions(ctx, node)
			if i, ok := a11y.PreferredAction(acts); ok {
				before := a11y.Fingerprint(window)
				act := guard.Action{
					Kind:    guard.KindInput,
					Command: "accessibility action",
					Args:    []string{acts[i], node.Name},
					Reason: fmt.Sprintf("perform %q on the %s reading %q in %s",
						acts[i], node.Role, node.Name, quoteOrAny(window.Name)),
				}
				return g.Run(ctx, act, func(ctx context.Context) (string, error) {
					if err := reader.Do(ctx, node, i); err != nil {
						return "", err
					}
					// Applications redraw after the handler returns, not during it.
					time.Sleep(400 * time.Millisecond)
					return fmt.Sprintf("Performed %q on %q (%s), through the application's "+
						"own handler rather than the pointer.%s",
						acts[i], node.Name, node.Role, treeChange(ctx, title, before)), nil
				})
			}

			where, ok := reader.Extents(ctx, node)
			if !ok {
				return "", fmt.Errorf("%q publishes no action to perform and its toolkit "+
					"does not say where it is, so there is neither a handler to call nor a "+
					"point to click. This is normal for some widgets and whole toolkits — "+
					"use desktop_screenshot to see it and desktop_key to reach it", node.Name)
			}
			if where.Offscreen() {
				return "", fmt.Errorf("%q publishes no action, and it is at %d,%d and %dx%d "+
					"— off-screen or collapsed, so clicking its middle would land on whatever "+
					"else is there. Scroll it into view or raise the window first",
					node.Name, where.X, where.Y, where.W, where.H)
			}
			x, y := where.Centre()

			// Before the guard, so the comparison spans the click and nothing else.
			before := a11y.Fingerprint(window)

			action := guard.Action{
				Kind:    guard.KindInput,
				Command: "xdotool",
				Args:    []string{"mousemove", fmt.Sprint(x), fmt.Sprint(y), "click", "1"},
				Reason:  fmt.Sprintf("click %q in %s", node.Name, quoteOrAny(window.Name)),
			}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				if _, err := run(ctx, 10*time.Second, "xdotool", "mousemove",
					fmt.Sprint(x), fmt.Sprint(y), "click", "1"); err != nil {
					return "", fmt.Errorf("the pointer could not be moved to %d,%d: %w", x, y, err)
				}
				// Applications redraw after the event, not during it.
				time.Sleep(400 * time.Millisecond)

				return fmt.Sprintf("Clicked %q (%s) at %d,%d.%s", node.Name, node.Role, x, y,
					treeChange(ctx, title, before)), nil
			})
		},
	})
}

// treeChange re-reads the window and says whether the click moved anything.
//
// A fresh Reader on purpose. The one that found the node has a node budget it has
// already spent walking there, and reusing it would silently truncate the second
// reading — which would then differ from the first for a reason that has nothing
// to do with the click.
//
// Failing to re-read is reported as failing to re-read. The whole point is that
// "nothing changed" is a claim, and a claim nobody checked is the thing this
// file exists to stop making.
func treeChange(ctx context.Context, title, before string) string {
	reader, err := a11y.Open(ctx)
	if err != nil {
		return "\n\n[The tree could not be read again afterwards, so whether that changed " +
			"anything is unknown — take a screenshot to see.]"
	}
	window, err := reader.Window(ctx, title)
	if err != nil {
		// A window that has gone is a change, and a large one: the click closed it.
		return "\n\n[That window is no longer on the accessibility bus — the click " +
			"probably closed or replaced it.]"
	}
	if a11y.Fingerprint(window) == before {
		return "\n\n[Nothing in the window changed. Either the control does nothing from " +
			"here, or what it did happens somewhere this tree does not cover — check with " +
			"desktop_screenshot before clicking it again.]"
	}
	return "\n\n[The window changed, so that reached something. Read it again with " +
		"desktop_inspect to see what is there now.]"
}

// notInTree is the failure for a name that is not in the tree, and it is worded
// by whether the tree can be trusted to settle the question.
//
// "Nothing in this window is called Full Name" is a claim about the window. A
// reader that stopped early is not entitled to make it, and the difference is
// not academic: on the run that found the GetChildren parser bug the tree came
// back as one node of a twenty-node window, she was told the field did not
// exist, and she believed it — five rounds of looking for another way in, then
// a confident wrong answer, from a tool that never returned an error. Once the
// reader admits it read partially, the same miss becomes "read it again", which
// costs one round and gets there.
// The gap is passed in rather than the reader, so both halves of this can be
// tested from here: a Reader's record of what it failed to read is its own
// business, and reaching into one to fake it would test the faking.
func notInTree(gap, name, where, tree string) error {
	if gap != "" {
		return fmt.Errorf("%q was not found in %s — but this window did not read fully "+
			"(%s), so it may be there and simply unread. Read it again before concluding "+
			"it is absent. What did come back:\n%s", name, where, gap, tree)
	}
	return fmt.Errorf("nothing in %s is called %q. What is there:\n%s", where, name, tree)
}

// emptyWindowNote explains a window that is on the bus with nothing inside it.
//
// A window publishing one node renders as a one-line tree, which reads as an
// empty application and is almost never what it means. The commonest cause by
// far is Chromium: measured on Electron 31, a window started without
// --force-renderer-accessibility publishes its frame and never anything under
// it, no matter how long you wait and regardless of ScreenReaderEnabled being
// true on the accessibility bus. The contents are withheld rather than absent,
// and nothing in the reply said so.
//
// That covers an enormous share of a modern desktop — VS Code, Slack, Discord,
// Teams and everything else built the same way — so the difference between "I
// cannot see inside this" and "this is empty" is worth a paragraph.
// Asked as "is there anything in here with a name" rather than "does it have
// children", because a Chromium window without the flag does have children: it
// answers GetChildren with placeholder nodes whose role and name both fail to
// read. Counting them said the window was populated while there was still
// nothing anywhere in it to aim at, so the note never appeared on the one case
// it was written for.
func emptyWindowNote(ctx context.Context, reader *a11y.Reader, window *a11y.Node) string {
	if window == nil || a11y.Named(window) {
		return ""
	}
	if a11y.ChromiumLike(reader.Actions(ctx, window)) {
		return "\n\n[This is a Chromium application — Electron, so VS Code, Slack, Discord, " +
			"Teams and anything else built that way. Chromium publishes nothing inside its " +
			"window unless it was started with --force-renderer-accessibility, so what is " +
			"in there is being withheld rather than missing. Restart it with that flag to " +
			"read it, or work with desktop_screenshot and desktop_key, which reach it as " +
			"they are.]"
	}
	return "\n\n[Nothing inside this window has a name, so there is nothing in here to aim at. " +
		"That is not the same as the window being empty. desktop_screenshot with desktop_key " +
		"still reaches it.]"
}

// unreadNote marks a tree that is not the whole tree.
//
// On a complete read this is empty, because a caveat on every answer is a
// caveat nobody reads.
func unreadNote(gap string) string {
	if gap == "" {
		return ""
	}
	return fmt.Sprintf("\n\n[This is NOT all of the window: %s. Something you cannot see "+
		"above may still be there.]", gap)
}
