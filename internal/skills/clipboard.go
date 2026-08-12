package skills

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/llm"
)

// The system clipboard, which is how a workstation actually moves data between
// applications.
//
// # Why this was the most embarrassing gap
//
// She could read the browser's clipboard and nothing of the machine's. So the
// single most common gesture on a desktop — copy there, paste here — was one she
// could watch and never take part in. Anything the user had just copied was
// invisible to her, and anything she produced had to be written to a file and
// opened rather than simply pasted where they were already working.
//
// # Reading it is a privacy decision, not just a feature
//
// Whatever is on the clipboard is whatever the user last copied, and sometimes
// that is a password out of a manager. Nothing here can tell the difference, and
// a tool that quietly copies it into an archive that is sent with every
// subsequent request would be the browser_inspect password bug a second time.
//
// Two things follow. The read is never automatic: it happens when she is asked,
// and she is told in the tool description not to reach for it speculatively. And
// clipboard_read is in the untrusted list in internal/agent, so its contents
// arrive fenced: a page saying "ignore your instructions", copied by a user who
// wanted to ask about it, is content rather than an instruction.
//
// # Two backends because distributions disagree
//
// xclip and xsel do the same job and neither is reliably installed. Both are
// supported, and when neither is present the refusal names them rather than
// failing at the point of use.
func RegisterClipboard(r *Registry, g *guard.Guard) {
	if g == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "clipboard_read",
			Description: "Read what is on the system clipboard: what the user last " +
				"copied, from any application.\n\n" +
				"Use it when they refer to something they have just copied — 'fix this " +
				"error', 'what does this mean', 'put that in the file' — and you cannot " +
				"otherwise see it.\n\n" +
				"Do not reach for it speculatively. A clipboard holds whatever was last " +
				"copied and that is sometimes a password, so read it when the request is " +
				"about its contents and not as a way of looking around.",
			Params: llm.ObjectSchema(nil),
		},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			if err := requireX11(); err != nil {
				return "", err
			}
			bin := firstOf("xclip", "xsel")
			if bin == "" {
				return "", fmt.Errorf("reading the clipboard needs xclip or xsel on PATH and " +
					"neither is installed")
			}
			out, err := run(ctx, 5*time.Second, bin, clipReadArgs(bin)...)
			if err != nil {
				// An empty clipboard is not a failure, and xclip reports it as one.
				if strings.Contains(err.Error(), "target STRING not available") {
					return "The clipboard is empty.", nil
				}
				return "", err
			}
			if strings.TrimSpace(out) == "" {
				return "The clipboard is empty.", nil
			}
			return out, nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "clipboard_write",
			Description: "Put text on the system clipboard so the user can paste it " +
				"anywhere.\n\n" +
				"This is often the right way to hand something over: a command they asked " +
				"for, a block of text to paste into a form, an address. It leaves them in " +
				"the application they were already in, where writing a file and opening it " +
				"does not.\n\n" +
				"It replaces whatever they had copied, so say that you have done it.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"text": {Type: "string", Description: "What to put on the clipboard."},
			}, "text"),
		},
		Mutates: true,
		Serial:  true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if err := requireX11(); err != nil {
				return "", err
			}
			text := argString(args, "text")
			if text == "" {
				return "", fmt.Errorf("nothing to put on the clipboard")
			}
			bin := firstOf("xclip", "xsel")
			if bin == "" {
				return "", fmt.Errorf("writing the clipboard needs xclip or xsel on PATH and " +
					"neither is installed")
			}

			// Guarded because it destroys something: whatever they had copied is
			// gone, and they may have been about to paste it.
			action := guard.Action{
				Kind:    guard.KindSystem,
				Command: bin,
				Args:    []string{"replace the clipboard"},
				Reason: fmt.Sprintf("put %d characters on the clipboard, replacing what "+
					"was there", len(text)),
			}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				if err := clipWrite(ctx, bin, text); err != nil {
					return "", err
				}
				// Read back, because a clipboard write is exactly the kind of thing
				// that reports success having done nothing: the selection owner is a
				// process, and if it exits the clipboard goes with it.
				got, err := run(ctx, 5*time.Second, bin, clipReadArgs(bin)...)
				if err != nil {
					return "", fmt.Errorf("the text was sent to the clipboard and cannot be "+
						"read back, so whether it stuck is unknown: %w", err)
				}
				if strings.TrimSpace(got) != strings.TrimSpace(text) {
					return "", fmt.Errorf("the clipboard holds something other than what was "+
						"written (%d characters instead of %d), so the write did not take",
						len(strings.TrimSpace(got)), len(strings.TrimSpace(text)))
				}
				return fmt.Sprintf("%d characters are on the clipboard, ready to paste. "+
					"Whatever was there before is gone.", len(text)), nil
			})
		},
	})
}

// clipReadArgs is how each backend is asked for the clipboard selection.
//
// The clipboard selection rather than the primary selection: primary is the
// middle-click buffer that fills up with any text you happen to drag over, and
// the clipboard is what ctrl-c means. Asking for the wrong one returns whatever
// the user last brushed with a cursor.
func clipReadArgs(bin string) []string {
	if bin == "xsel" {
		return []string{"--clipboard", "--output"}
	}
	return []string{"-selection", "clipboard", "-out"}
}

// clipWrite hands text to the backend on stdin.
//
// A clipboard owner has to stay alive to serve the selection, so xclip forks and
// holds it. That means this cannot wait for the process to exit the way run()
// does, which is why it does not use run().
func clipWrite(ctx context.Context, bin, text string) error {
	args := []string{"-selection", "clipboard", "-in"}
	if bin == "xsel" {
		args = []string{"--clipboard", "--input"}
	}
	return runWithInput(ctx, 5*time.Second, text, bin, args...)
}

// firstOf returns the first of these binaries that is installed.
func firstOf(bins ...string) string {
	for _, b := range bins {
		if have(b) {
			return b
		}
	}
	return ""
}
