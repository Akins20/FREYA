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
					clip(a11y.Describe(window), 1200))
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
