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

// Typing into a named field rather than at whatever has focus.
//
// # Why not desktop_type
//
// desktop_type sends keystrokes to the focused window and hopes. That is fine
// for a shortcut and wrong for a form: it needs the right field already focused,
// which means tabbing a guessed number of times, and it cannot tell afterwards
// whether the text went where it was aimed. The same argument as clicking a
// named control instead of a coordinate, one step along.
//
// # It reads the field back, and reports that rather than what it sent
//
// SetTextContents returns a boolean the toolkit is free to get wrong, and a field
// can refuse or reshape text for reasons nothing here can see: a validator, a
// maximum length, a mask that reformats as you type. Reporting the string that
// was sent is reporting an intention. The fact is what the field holds
// afterwards.
//
// # Credentials are refused, not handled carefully
//
// A password field is never typed into and never read. browser_inspect once
// labelled an input with its own value, which put a real password into the
// archive and into every request after it. The cheapest way not to repeat that
// is to have no code path that can.
func registerDesktopTypeInto(r *Registry, g *guard.Guard) {
	r.Register(Skill{
		Tool: llm.Tool{
			Name: "desktop_type_into",
			Description: "Put text into a named field of a native application window, and " +
				"report what the field holds afterwards.\n\n" +
				"Use this rather than desktop_type whenever the field has a name: it goes " +
				"to that field rather than to whatever happens to have focus, needs no " +
				"tabbing and no guessing, and tells you what actually landed — which is " +
				"not always what was sent, because fields validate and reformat.\n\n" +
				"Run desktop_inspect first and use the name it showed. Password fields are " +
				"refused: ask the user to type those themselves.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"field": {Type: "string", Description: "The name of the field, as desktop_inspect showed it."},
				"text":  {Type: "string", Description: "What to put in it. Replaces what is there."},
				"window": {Type: "string", Description: "Part of the window title. Omit for the " +
					"first window that publishes a tree."},
			}, "field", "text"),
		},
		Mutates: true,
		Serial:  true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			field := strings.TrimSpace(argString(args, "field"))
			if field == "" {
				return "", fmt.Errorf("say which field")
			}
			text := argString(args, "text")

			reader, err := a11y.Open(ctx)
			if err != nil {
				return "", fmt.Errorf("%w — fall back to desktop_key and desktop_type", err)
			}
			title := argString(args, "window")
			window, err := reader.Window(ctx, title)
			if err != nil {
				// A bus that was read short cannot settle whether the window is
				// there, and saying it is not would be the same unearned claim the
				// tree makes when it stops early.
				return "", fmt.Errorf("%w%s", err, unreadNote(reader.Incomplete()))
			}
			node := a11y.Find(window, field, "")
			if node == nil {
				return "", notInTree(reader.Incomplete(), field, quoteOrAny(window.Name),
					clip(a11y.Describe(window), 1200)+emptyWindowNote(ctx, reader, window))
			}
			if node.Secret() {
				return "", fmt.Errorf("%q is a password field. Nothing here types into one or "+
					"reads one, deliberately — ask them to enter it themselves, and say why "+
					"you needed it", node.Name)
			}

			action := guard.Action{
				Kind:    guard.KindInput,
				Command: "set text",
				Args:    []string{node.Name},
				Reason: fmt.Sprintf("put %d characters into the %s reading %q in %s",
					len(text), node.Role, node.Name, quoteOrAny(window.Name)),
			}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				got, err := reader.SetText(ctx, node, text)
				if err != nil {
					// No writable interface is not the end of it. See
					// typeWithKeyboard: Chromium publishes none at all, on every
					// input in every Electron application.
					got, err = typeWithKeyboard(ctx, reader, node, text)
				}
				if err != nil {
					return "", err
				}
				time.Sleep(200 * time.Millisecond)
				if got == text {
					return fmt.Sprintf("%q now holds %q.", node.Name, got), nil
				}
				// The difference is the whole point of reading back. A field that
				// reformatted, truncated or rejected the text looks identical to one
				// that took it, from the sending side.
				return fmt.Sprintf("%q now holds %q, which is not what was sent (%q). The "+
					"field changed it — a length limit, a validator or an input mask. Work "+
					"from what it holds, not from what you meant.", node.Name, got, text), nil
			})
		},
	})
}

// typeWithKeyboard fills a field that has no writable accessibility interface.
//
// # Why this exists
//
// Chromium publishes no EditableText anywhere. Measured on Electron 31, a text
// field implements Accessible, Action, Collection, Component, Document, Socket
// and Text, and SetTextContents answers UnknownMethod because the interface is
// not there — so there is nothing on the node to write to. That is every input
// in every Electron application, which is VS Code, Slack, Discord, Teams and a
// large part of a modern desktop, and too much to hand back as unsupported.
//
// So the field is focused through the toolkit, selected, typed over with the
// keyboard, and then read back through Text — which Chromium does implement.
//
// # The guarantee is unchanged, and doing more work
//
// What comes back is still what the field holds rather than what was sent, and
// here that matters more than it did before: a direct write either lands or
// errors, while keystrokes can go to the wrong window and report nothing at
// all. Focus is asked of the toolkit rather than assumed, and the read-back
// catches the case where it went somewhere else anyway.
func typeWithKeyboard(ctx context.Context, reader *a11y.Reader, node *a11y.Node, text string) (string, error) {
	if err := requireX11(); err != nil {
		return "", fmt.Errorf("%q publishes no writable interface, and the keyboard is the "+
			"only way in: %w", node.Name, err)
	}
	if !have("xdotool") {
		return "", fmt.Errorf("%q publishes no writable interface, so this needs the keyboard, "+
			"and xdotool is not installed", node.Name)
	}
	if err := reader.GrabFocus(ctx, node); err != nil {
		return "", fmt.Errorf("%q publishes no writable interface and would not take focus "+
			"either, so there is no way into it from here: %w", node.Name, err)
	}
	time.Sleep(150 * time.Millisecond)

	// Select first, because setting a field means replacing what is in it and
	// typing alone would append to it.
	if _, err := run(ctx, 5*time.Second, "xdotool", "key", "--clearmodifiers", "ctrl+a"); err != nil {
		return "", err
	}
	if _, err := run(ctx, 20*time.Second, "xdotool", "type", "--clearmodifiers",
		"--delay", "20", text); err != nil {
		return "", err
	}
	time.Sleep(300 * time.Millisecond)

	got, ok := reader.Text(ctx, node)
	if !ok {
		return "", fmt.Errorf("%q has no writable interface, so it was typed into with the "+
			"keyboard, and it cannot be read back either — whether that landed is unknown. "+
			"Check with desktop_inspect before relying on it", node.Name)
	}
	return got, nil
}
