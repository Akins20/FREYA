package skills

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/akins/jarvis/internal/browser"
	"github.com/akins/jarvis/internal/llm"
)

// A second pair of eyes that has never met her.
//
// # Why this is not just asking her to check her own work
//
// "Characterizing False Success in LLM Agents" (arXiv 2606.09863) measures the
// failure directly: agents asserting completion against an environment that says
// otherwise, in 75.8% of self-assessing coding trajectories that made an
// explicit status claim. The part that matters for the design is what it says
// about the obvious fix. LLM judges reach AUROC 0.65 and 0.54 — close enough to
// chance to be worthless — because they key on confident closing language and on
// how much the agent did. Detectors that look at STATE reach 0.83 to 0.95.
//
// So a reviewer that reads her account of the work is measurably useless, and
// worse than useless here, because her account is fluent and confident and that
// is precisely the signal that misleads a judge. This one never sees it.
// AnalyzeImage is a single stateless call: no conversation, no persona, no tool
// trail. Its entire input is a screenshot of the rendered page and a brief. It
// can only report what is actually on the screen.
//
// # Why a picture rather than the markup
//
// site_check already reads the markup, and it is the right tool for anything
// countable — a link with no destination, a stylesheet that is not on disk. What
// no amount of parsing can see is whether a section communicates anything,
// whether the spacing is broken, whether the hero says nothing, whether two
// blocks are fighting for the same job. That is what the user keeps asking for
// and it only exists once the thing is drawn.
//
// So the order is: mechanical checks first, because they are free and they are
// better at what they cover; then this, for everything a regex is blind to.
//
// # It is told to find things
//
// A reviewer asked "is this good?" says yes. The brief below asks for the three
// weakest things on the page and forbids praise, because the useful output is
// the list of what to fix, and an encouraging review of a mediocre page is how
// a mediocre page ships.

// reviewBrief is the whole instruction the reviewer gets. Deliberately says
// nothing about who made the page, what they were asked for, or how hard they
// worked — all three are the surface signals that mislead a judge.
const reviewBrief = `You are looking at a screenshot of a web page. You have no context about
who made it or why. Judge only what is in front of you.

Report the THREE WEAKEST THINGS about this page, worst first. For each one, say
where it is and what specifically to change. Be concrete: "the hero headline
says nothing about what the business does — it reads as a slogan where it
should say what they sell" is useful; "the hero could be stronger" is not.

Look hardest at these, because they are what people actually notice:
- Text that says nothing. Headings that are two vague words. Copy that could
  belong to any business in any industry. Filler.
- Space used badly. Sections all the same height and rhythm. Things crammed or
  marooned. A page that scrolls without ever changing pace.
- Hierarchy that does not lead the eye. Everything the same weight, so nothing
  is first.
- Anything that looks like a template rather than this specific business.

Do NOT open with praise and do NOT summarise what the page contains. If a
section is genuinely good, one clause is enough, and only to explain why
something next to it looks weak by comparison.

End with one line: the single change that would most improve this page.`

// RegisterReview adds the fresh-eyes reviewer. Registered only when the
// provider can see, since there is nothing useful to do without that.
func RegisterReview(r *Registry, provider llm.Provider) {
	eyes, ok := provider.(llm.VisionAnalyzer)
	if !ok {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "review",
			Description: "Have a page looked at by someone who has not seen your work — " +
				"they get the rendered screenshot and nothing else, and they report the " +
				"three weakest things on it.\n\n" +
				"Use this after site_check passes, before you hand a site over. The two " +
				"answer different questions: site_check says whether everything is wired " +
				"up, this says whether it is any good. Neither can do the other's job, " +
				"and a page can pass every mechanical check and still be flat, generic " +
				"and badly spaced.\n\n" +
				"You cannot do this for yourself. You know what you meant, so you read the " +
				"page as the thing you intended rather than the thing that is there. They " +
				"do not have that problem.\n\n" +
				"Expect it to find things. That is the point, not a failure — act on what " +
				"comes back rather than explaining why it is fine.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path": {Type: "string", Description: "An .html file, or a folder — every " +
					"page in it gets looked at."},
			}, "path"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			root := expandIn(ctx, argString(args, "path"))
			if root == "" {
				return "", fmt.Errorf("path is required")
			}
			pages, err := htmlFilesUnder(root)
			if err != nil {
				return "", err
			}
			if len(pages) == 0 {
				return "", fmt.Errorf("no .html files at %s to look at", root)
			}
			sort.Strings(pages)
			// Bounded, and said out loud when it bites. A silent cap reads as "all
			// of it was reviewed" when it was not.
			const most = 4
			capped := ""
			if len(pages) > most {
				capped = fmt.Sprintf("\n\n[%d pages, the first %d were looked at. Run this "+
					"again on the rest.]", len(pages), most)
				pages = pages[:most]
			}

			// Counted, because a review that looked at nothing must not return
			// success. Every page failing to render used to leave the failures in
			// the text, append the closing line about someone seeing the page cold,
			// and hand all of it back with a nil error — so the trail recorded a
			// successful review, the completion gate that exists to force this call
			// was satisfied by it, and the turn ended with nobody having looked.
			// Measured: two builds where the renderer could not start, both reported
			// "review skipped due to renderer environment" in her own plan notes and
			// finished anyway. That is the silent capability loss this codebase
			// spends most of its checks trying to make impossible.
			var sb strings.Builder
			seen := 0
			var blind []string
			for _, page := range pages {
				shot, err := renderShot(ctx, page)
				if err != nil {
					blind = append(blind, fmt.Sprintf("%s (%v)", filepath.Base(page), err))
					continue
				}
				verdict, err := eyes.AnalyzeImage(ctx, reviewBrief, [][]byte{shot}, []string{"image/png"})
				if err != nil {
					return "", fmt.Errorf("the reviewer could not look at %s: %w",
						filepath.Base(page), err)
				}
				seen++
				fmt.Fprintf(&sb, "## %s\n\n%s\n\n", filepath.Base(page), strings.TrimSpace(verdict))
			}

			// Nothing was looked at, so this is a failed call and not a thin one.
			// Returning an error is what puts it in the trail as failed, which is
			// what keeps the gate unsatisfied.
			if seen == 0 {
				return "", fmt.Errorf("nothing could be rendered, so nobody looked at this: %s. "+
					"The reviewer needs a browser it can start; the page is unreviewed and "+
					"saying otherwise would be a claim nobody checked",
					strings.Join(blind, "; "))
			}

			// A partial review says which pages went unseen, in the same breath as
			// the verdicts, so acting on it cannot be mistaken for acting on all of
			// them.
			unseen := ""
			if len(blind) > 0 {
				unseen = fmt.Sprintf("\n\n[%d of %d pages could not be rendered and were NOT "+
					"looked at: %s. Nothing below speaks to them.]",
					len(blind), len(blind)+seen, strings.Join(blind, "; "))
			}

			return strings.TrimSpace(sb.String()) + capped + unseen +
				"\n\n[This is someone seeing the page cold, with no idea what you were " +
				"aiming for. Where they misread something, the page is what misled them.]", nil
		},
	})
}

// renderShot draws one page and captures the whole of it.
//
// The full page rather than the viewport, because a review of the top eight
// hundred pixels is a review of the hero and nothing else — and the sections
// that go generic are the ones further down, where attention ran out.
func renderShot(ctx context.Context, page string) ([]byte, error) {
	abs, err := filepath.Abs(page)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, err
	}

	// Guest, like the PDF renderer: this is her own markup and needs no session,
	// so the profile holding the user's real cookies has no business rendering it.
	const bctx = browser.ContextGuest
	if err := browser.Launch(ctx, bctx); err != nil {
		return nil, fmt.Errorf("start the renderer: %w", err)
	}
	target, err := browser.NewTab(bctx, "about:blank")
	if err != nil {
		return nil, err
	}
	defer browser.CloseTab(bctx, target.ID)

	client, err := browser.Connect(bctx, target)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.Navigate(ctx, "file://"+abs); err != nil {
		return nil, err
	}
	client.WaitStable(ctx, pdfRenderWait)

	shot, err := client.ScreenshotFull(ctx)
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(shot)
}
