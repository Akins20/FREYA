package skills

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/browser"
	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/llm"
)

// Fetching the user for the part only a person can do.
//
// # What she did before
//
// Hit a Cloudflare check on a job site, waited five seconds for a phrase that
// was never coming, waited ten more, read the page again, and reported that she
// could not proceed. Which was true, and useless: the user could not act on it
// either, because the challenge was sitting in a browser window they did not
// know existed.
//
// She already says "I am stuck". The three things missing are that she does not
// know WHY she is stuck in a way that distinguishes this from an ordinary
// failure, the user cannot SEE the thing blocking her, and the task dies rather
// than pausing.
//
// # Why this raises her window rather than opening theirs
//
// The obvious move is system_open, which puts a page in the user's own Chrome —
// and it is wrong here. A Cloudflare clearance is a cookie, and it lands in
// whichever profile solved the challenge. Solved in their browser it does
// nothing for hers; she would still be blocked, now with a solved challenge
// sitting somewhere useless.
//
// Her Chrome is a real mapped X11 window, not a headless one, so the answer is
// to bring it forward and let them click in it. The cookie lands where it is
// needed, and she is still driving the same tab afterwards.
//
// # Why it waits rather than returning
//
// So the task survives. Returning immediately would end the exchange and the
// user would have to start it again from the beginning; the tool holds until the
// wall clears and then hands control back with the page already through it. The
// wait is bounded, and a timeout is reported as itself rather than as a failure
// of the task.

// handoverWait is how long to hold while the user deals with it. Long enough to
// walk back to the machine, short enough that an unattended run is not wedged
// for the rest of the night.
const handoverWait = 3 * time.Minute

// RegisterHandover adds the tool that pauses for a human.
func RegisterHandover(r *Registry, g *guard.Guard, tabs *Tabs) {
	if g == nil || tabs == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_hand_over",
			Description: "Bring your browser window to the user's screen and wait while " +
				"they do a bit you cannot.\n\n" +
				"For anything that exists to prove a person is present: a Cloudflare " +
				"check, a CAPTCHA, a 'verify you are human' gate, a two-factor code, a " +
				"payment confirmation. None of those can be clicked past — that is what " +
				"they are for — so retrying, waiting longer or looking for another route " +
				"is time spent for nothing.\n\n" +
				"This is not the same as giving up. Say what you need them to do, and " +
				"this waits for it and hands the page back to you once it is done, with " +
				"your session intact and your task still going. Do NOT use it for an " +
				"ordinary failure you could recover from yourself.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "Tab name."},
				"asking": {Type: "string", Description: "What they need to do, in one line — " +
					"'tick the Cloudflare box on Indeed', 'enter the 2FA code'."},
			}, "asking"),
		},
		// The world does change here — a person clears the wall — so the framework
		// samples it like any other mutating call. Identical readings mean they
		// never got to it, which is exactly what should be reported.
		Mutates:     true,
		Observe:     tabs.observe,
		Affordances: tabs.affordances,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, note, err := tabNoted(tabs, args)
			if err != nil {
				return "", err
			}
			asking := argString(args, "asking")
			if asking == "" {
				return "", fmt.Errorf("say what you need them to do, or they cannot help")
			}

			before := tab.client.State(ctx)
			raised, rerr := raiseFreyaWindow(ctx)

			var sb strings.Builder
			fmt.Fprintf(&sb, "Asked the user: %s\n", asking)
			if rerr != nil {
				// Not fatal. Telling them which window to look for is worse than
				// raising it and still better than silence.
				fmt.Fprintf(&sb, "(Could not bring the window forward: %v — tell them to "+
					"look for the second Chrome window, the one showing %q.)\n",
					rerr, clip(before.Title, 60))
			} else {
				fmt.Fprintf(&sb, "(Brought %s to the front so they can act on it.)\n", raised)
			}

			// Hold, and let the page tell us when it is done.
			cleared, waited := waitForHuman(ctx, tab, handoverWait)
			if cleared {
				after := tab.client.State(ctx)
				fmt.Fprintf(&sb, "\nDone after %s. The page is now %q — %s. Carry on with "+
					"what you were doing; the session is intact.",
					waited.Round(time.Second), clip(after.Title, 60), clip(after.URL, 80))
				return sb.String() + note, nil
			}
			fmt.Fprintf(&sb, "\nStill not cleared after %s. They may not be at the machine. "+
				"Stop here and tell them plainly what is waiting for them rather than "+
				"trying to work around it — there is no way around this one.",
				waited.Round(time.Second))
			return sb.String() + note, nil
		},
	})
}

// waitForHuman polls until the verification wall is gone, or time runs out.
//
// The condition is the wall's absence rather than any particular success, so it
// works for a Cloudflare check, a two-factor prompt and a payment confirmation
// without knowing which it was looking at.
func waitForHuman(ctx context.Context, tab *openTab, limit time.Duration) (bool, time.Duration) {
	start := time.Now()
	deadline := start.Add(limit)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false, time.Since(start)
		case <-time.After(2 * time.Second):
		}
		st := tab.client.State(ctx)
		if !st.NeedsHuman && !st.Loading {
			// One more look, because a challenge often flickers through a blank
			// moment on its way to the real page.
			time.Sleep(1500 * time.Millisecond)
			if again := tab.client.State(ctx); !again.NeedsHuman {
				return true, time.Since(start)
			}
		}
	}
	return false, time.Since(start)
}

// raiseFreyaWindow brings her own Chrome to the front.
//
// Found by process rather than by title. Her window and the user's are both
// "… - Google Chrome", and raising the wrong one would put the user in front of
// their own browser wondering what they were meant to click. The automation
// instance is the one running with her profile directory, so the pid is the only
// reliable handle.
func raiseFreyaWindow(ctx context.Context) (string, error) {
	pid, err := freyaChromePID(ctx)
	if err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, "wmctrl", "-lp").Output()
	if err != nil {
		return "", fmt.Errorf("list windows: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[2] != pid {
			continue
		}
		if _, err := exec.CommandContext(ctx, "wmctrl", "-i", "-a", f[0]).Output(); err != nil {
			return "", fmt.Errorf("raise window: %w", err)
		}
		title := strings.Join(f[4:], " ")
		if title == "" {
			title = "the browser window"
		}
		return title, nil
	}
	return "", fmt.Errorf("her browser has no visible window to raise")
}

// freyaChromePID finds the Chrome running on her profile.
func freyaChromePID(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "pgrep", "-f", browser.ProfileMarker).Output()
	if err != nil {
		return "", fmt.Errorf("her browser is not running")
	}
	for _, p := range strings.Fields(string(out)) {
		// The first match is the browser process; the zygote and crashpad helpers
		// carry the same flag and own no window, which the wmctrl lookup filters
		// out anyway.
		return p, nil
	}
	return "", fmt.Errorf("her browser is not running")
}
