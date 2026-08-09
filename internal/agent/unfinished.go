package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/akins/jarvis/internal/skills"
	"github.com/akins/jarvis/internal/wiring"
)

// Refusing to call it finished while something measurable is still broken.
//
// # What a note is worth
//
// Three interventions were tried against the same failure — a page whose links
// go nowhere — and measured on real builds:
//
//	nothing            flower shop      5 dead of 15 links
//	persona rule       grooming shop    2 dead of 13
//	rule + a note on the write   bike shop   1 dead of 16
//
// The last run is the interesting one. The note fired, correctly, naming the
// href="#" the moment she wrote the file. It is in the archive. She read it,
// wrote two more files, ran code_check three times, served the site, opened it
// on the user's screen, and said it was done — with the dead link still in it.
//
// So a note in a tool result is advice, and advice loses to the momentum of a
// task that feels finished. The thing that was actually missing is the refusal:
// the exchange should not be allowed to END while a defect it was told about is
// still standing.
//
// # The verdict comes from disk, never from the trail
//
// Because she might have fixed it, with any tool, in any order. The first
// version worked out whether a page was still broken by matching the wording of
// the write tool's output back out of the trail, and the very first time she
// repaired a page with file_edit instead of a second file_write it accused her
// of leaving a dead link in a file that was, on disk, clean. So the trail
// supplies the list of pages she touched and nothing else; internal/wiring reads
// each one as it stands now.
//
// # One push, then it lets go
//
// Fired once per exchange. If she is told plainly and still answers without
// fixing it, that is her call and the turn ends; a gate that will not take an
// answer is a hang, and the round cap is not a good place to discover that.

// touchedPath pulls the file out of a write or edit result. Both lead with a
// verb and the path; only the name is needed, because the verdict comes from
// disk afterwards rather than from anything the tool said.
var touchedPath = regexp.MustCompile(`^(?:Wrote \d+ bytes to|Created|Replaced|Rewrote|Edited) (\S+)`)

// stillOpen returns the dead ends standing at the end of the exchange, read
// from the files as they are now.
//
// The trail only supplies the LIST OF PAGES she touched. Whether any of them is
// still broken is answered by internal/wiring against the current file — the
// first version asked the trail that question too, by matching the wording of
// the write tool's output, and told her a page was unfinished after she had
// fixed it with file_edit. The file was clean and the exchange still ended with
// an accusation.
func stillOpen(ctx context.Context, work *trail) []string {
	// Steps she wrote down herself and has not settled. First, because an
	// unfinished step is a bigger omission than a dead link and reads as the
	// more useful thing to be told.
	out := skills.ScopeFrom(ctx).Plan().Outstanding()

	if work == nil {
		return out
	}
	var pages []string
	seen := map[string]bool{}
	for _, s := range work.snapshot() {
		if s.failed || (s.tool != "file_write" && s.tool != "file_edit") {
			continue
		}
		m := touchedPath.FindStringSubmatch(strings.TrimSpace(s.output))
		if m == nil {
			continue
		}
		path := strings.TrimRight(m[1], ".,:")
		// file_edit reports a base name, which cannot be re-read on its own. The
		// write that created it in the same exchange carries the full path, so an
		// edited page is already in the list; one that is not was written in an
		// earlier turn and is not this exchange's promise to keep.
		if !strings.Contains(path, "/") || !wiring.IsHTML(path) || seen[path] {
			continue
		}
		seen[path] = true
		pages = append(pages, path)
	}

	for _, p := range pages {
		for _, problem := range wiring.Open(p) {
			out = append(out, shortPath(p)+": "+problem)
		}
	}
	return out
}

// shortPath keeps the filename and its folder, which is enough to act on and
// short enough to read.
func shortPath(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) <= 2 {
		return p
	}
	return strings.Join(parts[len(parts)-2:], "/")
}

// finishBrief is the push. Written as a tool result rather than as the user
// speaking, because it is the machine reporting the state of the work — and
// putting words in the user's mouth is how a turn ends up archived as a request
// they never made.
func finishBrief(ends []string) string {
	var sb strings.Builder
	sb.WriteString("HOLD ON — you are about to call this finished and it is not.\n\n")
	sb.WriteString("Still standing, from your own plan and your own writes this turn:\n")
	for _, e := range ends {
		sb.WriteString("  · " + e + "\n")
	}
	sb.WriteString("\nYou were told about each of these at the time and nothing has been done " +
		"about them since. An unfinished step is work they asked for and are not getting; a " +
		"dead link is a promise on the screen with nothing behind it, and they find out by " +
		"clicking.\n\n" +
		"Go back and deal with all of them now. For a step: do it, or drop it with plan_step " +
		"and say why — deciding it was unnecessary is honest, leaving it open is not. For a " +
		"link: add what it points at, or take the link out. Then check the whole folder with " +
		"site_check before you answer.\n\n" +
		"While you are in there, look again with fresh eyes rather than only patching these " +
		"lines: a section that is merely acceptable is worth making good, and you are the " +
		"one who can see it. Do not answer until the work is actually done.")
	return sb.String()
}

// nudgeNote is the last word when the push did not take. The framework is the
// only party here that knows this for a fact, so it says so rather than leaving
// the user to discover it.
func nudgeNote(ends []string) string {
	if len(ends) == 1 {
		return fmt.Sprintf("\n\n[Not finished: %s. It was flagged twice and left as it is.]", ends[0])
	}
	return fmt.Sprintf("\n\n[Not finished — %d dead links left in place after being flagged "+
		"twice: %s]", len(ends), strings.Join(ends, "; "))
}

// reCited finds the URLs an answer presents as sources: bare, in a markdown
// link, or in parentheses. Trailing punctuation is stripped because a sentence
// ending in a URL is normal writing, not part of the address.
var reCited = regexp.MustCompile(`https?://[^\s<>"')\]]+`)

// unopenedSources returns the pages she cited without ever opening.
//
// # Why this is the research half of "leads nowhere"
//
// A dead link is a promise on a page. A source in an answer that nobody read is
// the same promise in prose, and it is worse, because the reader has no way to
// tell: the URL resolves, the page exists, and the claim attached to it may have
// come from anywhere. She has walked identifiers by pattern before — quiz ids
// incremented one at a time, every one returning HTTP 200 — so a plausible URL
// that was never fetched is a shape this codebase already knows she produces.
//
// Anthropic's research system runs a whole agent for this, matching every claim
// in a report to a source location. The ledger already knows which pages were
// actually retrieved, so here it is a lookup.
//
// # Narrow on purpose
//
// Only fires when the exchange did some reading, so an answer that quotes a URL
// from memory or from the user's own message is not accused of anything. And a
// URL she was shown in a search result but did not open still counts as
// unopened, which is the whole point — that is the distinction between a source
// and a search hit.
func unopenedSources(ctx context.Context, reply string, work *trail) []string {
	ledger := skills.ScopeFrom(ctx).Ledger()
	if ledger == nil || work == nil {
		return nil
	}
	// Did she read anything at all this exchange? If not, the URLs came from
	// somewhere else — the user, memory, a page from yesterday — and accusing an
	// answer that did no research of citing badly would be nonsense.
	//
	// snippetsOnly covers the other half: searched and never opened.
	searched, read := webActivity(work)
	if !read {
		_ = searched
		return nil
	}

	var out []string
	seen := map[string]bool{}
	for _, raw := range reCited.FindAllString(reply, -1) {
		u := strings.TrimRight(raw, ".,;:!?)")
		key := strings.ToLower(u)
		if seen[key] {
			continue
		}
		seen[key] = true
		if ledger.WasRetrieved(u) {
			continue
		}
		out = append(out, u)
	}
	return out
}

// webActivity counts what kind of web work happened, from the tools that ran
// rather than from what the answer sounds like. The false-success work is clear
// that judging an answer by how it reads is the mistake.
func webActivity(work *trail) (searched, read bool) {
	if work == nil {
		return false, false
	}
	for _, s := range work.snapshot() {
		if s.failed {
			continue
		}
		if readingTool[s.tool] {
			read = true
		}
		if s.tool == "web_search" || s.tool == "news_search" {
			searched = true
		}
	}
	return searched, read
}

// snippetsOnly reports an answer built on search results with no page ever
// opened.
//
// # The measurement
//
// Asked to compare the three most popular family cargo bikes in the UK and pick
// one, she ran two searches and answered. No page was fetched. The answer named
// specific mechanicals — full front and rear suspension, parking a longtail
// vertically — and implied prices, from titles and two-line snippets, and cited
// nothing, so the citation check above had no citation to test.
//
// That is the worse failure of the two. An unopened citation at least tells the
// reader where to look and can be checked; an answer with no sources at all,
// assembled from result summaries, is indistinguishable from one she wrote out
// of the model's own memory — including where that memory is two years stale,
// which for "right now, in the UK" is the entire question.
//
// # Why the threshold
//
// A search that answers a small question from its own result line is fine and
// common: opening hours, a score, a spelling. The failure needs volume — a long
// answer resting on snippets. Length is a coarse stand-in for how many claims
// were made, and it is at least a fact about the output rather than a judgement
// of it. Deliberately generous, because the cost of firing on a legitimate quick
// lookup is teaching her to ignore this.
const snippetAnswerFloor = 700

func snippetsOnly(reply string, work *trail) bool {
	searched, read := webActivity(work)
	return searched && !read && len(strings.TrimSpace(reply)) > snippetAnswerFloor
}

// snippetsBrief is the push for an answer assembled from result summaries.
func snippetsBrief() string {
	return "HOLD ON — you searched, opened nothing, and wrote a long answer.\n\n" +
		"Every claim in there came from titles and two-line snippets, or from what you " +
		"already believed before you searched. You cannot tell those apart afterwards, and " +
		"neither can they.\n\n" +
		"Open the pages. web_fetch the two or three that actually matter for this, read " +
		"them, and then say what they say — with the specifics that only exist on the page: " +
		"the current price, the actual spec, whether the thing is still sold. Where a page " +
		"contradicts what you wrote, the page wins.\n\n" +
		"If after reading you genuinely cannot confirm something, say so in the answer. " +
		"'I could not find a current UK price for this' is worth more than a confident " +
		"number nobody sourced."
}

// readingTool names the calls that return a page's text, so "did any research
// happen" is answered by what ran rather than by what the answer sounds like.
var readingTool = map[string]bool{
	"web_fetch": true, "web_research": true, "browser_open": true,
	"browser_read": true, "browser_goto": true, "browser_scrape": true,
}

// sourcesBrief is the push for citations that were never opened.
func sourcesBrief(urls []string) string {
	var sb strings.Builder
	sb.WriteString("HOLD ON — you are citing pages you never opened.\n\n")
	for _, u := range urls {
		sb.WriteString("  · " + u + "\n")
	}
	sb.WriteString("\nNone of these was fetched this exchange. Seeing a URL in a list of " +
		"search results is not reading it: a result is a title and two lines of snippet, and " +
		"whatever you have attached to it did not come from the page.\n\n" +
		"So either open each one with web_fetch and keep the claim if the page supports it, " +
		"or take the citation out and say plainly where the claim actually came from. A " +
		"reference nobody can check is worse than no reference, because it reads as evidence.")
	return sb.String()
}
