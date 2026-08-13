package agent

import (
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

// What actually happened this exchange, kept so that running out of road
// produces a report on the work rather than an apology for it.
//
// # The failure this replaces
//
// She finished two quizzes and was partway through a third when the round cap
// stopped her. What she said was, in effect, "I couldn't finish". That answer was
// worse than useless: the user had to go and look at the browser to learn that
// two thirds of the job was done.
//
// # Two wrong turns, both worth recording
//
// The first attempt summarised the TOOLS — "clicked something twice, read the
// page". Nobody asked her to click anything; they asked her to do the quizzes.
//
// The second attempt kept the first line of each tool's output, which sounded
// principled and was not. The real outputs are shaped like this:
//
//	browser_read       "<title>\n<url>\n\n<page text>"      → first line is the TITLE
//	browser_click_text "Clicked %q. Now on %q\n<url>"       → first line is the CLICK
//	browser_fill       "Filled #answer3."                   → says nothing at all
//
// So "first line" threw away every page body — the only place a score or a
// question number lives — and kept the click log. It was the same mistake in a
// better disguise, and it passed its tests only because the tests fed it output
// no skill in this repo actually produces.
//
// What the world SAID is in the body. What she DID is in the header. This file
// keeps both and leads with the body.
//
// # Two paths, and which one matters
//
// The good report comes from the model, the only thing here that knows what
// "done" means for a given request. Its prompt is the important part of the fix
// (roundCapBrief); digest() serves it with a chronological spine so the summary
// lands concrete rather than vague.
//
// account() is the backstop for when that call cannot be made. It runs without
// the network, because the salvage call may be the very thing that failed. It
// cannot judge what "done" means, so it does not pretend to — it hands over the
// evidence, in the pages' own words, and says that is what it is doing.
type trail struct {
	mu    sync.Mutex
	steps []step
}

type step struct {
	tool   string
	round  int
	output string
	failed bool
}

// add appends one executed tool call.
//
// Called after the round's goroutines have joined, in REQUEST order rather than
// completion order. Recording from inside each goroutine put the steps in
// whichever order the handlers happened to finish, so a slow read could land
// behind a fast click and the "chronological record" would then say the quiz was
// submitted and afterwards reopened at question four — the report asserting the
// reverse of what happened, differently on every run.
func (t *trail) add(s step) {
	t.mu.Lock()
	t.steps = append(t.steps, s)
	t.mu.Unlock()
}

func (t *trail) snapshot() []step {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]step(nil), t.steps...)
}

// worked reports whether anything at all succeeded.
//
// The distinction matters for honesty: an exchange in which every call failed is
// entitled to say so, and must never be pushed to claim achievement. "No steps"
// is the wrong test — six failed clicks are six steps and no work.
func (t *trail) worked() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, s := range t.steps {
		if !s.failed {
			return true
		}
	}
	return false
}

// digest is the chronological spine handed to the model when it has to report on
// an exchange it could not finish.
//
// The message list already holds every one of these outputs, so this adds no
// information — it adds *shape*. Reading back forty interleaved tool results and
// reconstructing the arc is exactly what the model did badly when left to it.
//
// Failures are included. What went wrong is half the report, and left to itself
// the model narrates only the parts that worked.
func (t *trail) digest() string {
	steps := t.snapshot()
	if len(steps) == 0 {
		return "(no tools ran)"
	}

	var b strings.Builder
	round := -1
	for _, s := range steps {
		if s.round != round {
			round = s.round
			fmt.Fprintf(&b, "\n[round %d] ", round)
		}
		state, body := substance(s.output)
		switch {
		case s.failed:
			fmt.Fprintf(&b, "%s FAILED: %s. ", s.tool, strings.TrimPrefix(state, "ERROR: "))
		case state == "" && body == "":
			fmt.Fprintf(&b, "%s (no output). ", s.tool)
		case body == "":
			fmt.Fprintf(&b, "%s: %s. ", s.tool, state)
		default:
			// The body is what the page said, and it is the half that was being
			// thrown away. It leads.
			fmt.Fprintf(&b, "%s: %s — page said: %s. ", s.tool, state, body)
		}
	}
	return strings.TrimSpace(b.String())
}

// account is the no-network backstop: what the tools reported, in their own
// words, framed against what was asked.
//
// It deliberately claims nothing is "done". Only the model knows what done means
// for a given request, and this runs precisely when the model is unavailable — so
// it hands over the evidence rather than being confidently wrong about whether
// the third quiz was submitted.
//
// reason carries the whole explanation, including why there is no proper summary.
// An earlier version hardcoded "I lost the model before I could sum it up", which
// was a lie on the path where the user had simply pressed stop — and since this
// text is archived, she would then report a provider outage that never happened.
func (t *trail) account(goal, reason string) string {
	steps := t.snapshot()

	var b strings.Builder
	b.WriteString(reason)
	if g := firstLine(goal); g != "" {
		// Quoted rather than grammatically absorbed. "You'd asked me to " + the
		// user's own sentence produced "You'd asked me to can you do my quizzes?."
		// — the most visible line in the whole feature, mangled.
		fmt.Fprintf(&b, " What you'd asked for: %q.", clipRunes(g, 120))
	}

	if len(steps) == 0 {
		b.WriteString(" I didn't get as far as doing anything, so there's genuinely nothing to report.")
		return b.String()
	}

	fmt.Fprintf(&b, " I can't summarise how far that got, so here's the raw account: %s across %d rounds.",
		actions(len(steps)), lastRound(steps))

	if ev := milestones(steps); len(ev) > 0 {
		// Fenced, not merely labelled. These lines are page text copied with no
		// model in between, and this reply is archived as an ASSISTANT turn that
		// later prompts replay as her own words — a higher-trust role than a tool
		// result. A prose disclaimer is not the boundary the rest of the agent
		// teaches the model to respect; fenceBlock is.
		b.WriteString("\n\n" + fenceBlock("the pages she read this exchange",
			strings.Join(ev, "\n")))
	}

	if failed := failures(steps); len(failed) > 0 {
		fmt.Fprintf(&b, "\n\nWhat went wrong: %s.", strings.Join(failed, "; "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// milestones keeps what the world said and drops what she did to make it say it.
func milestones(steps []step) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range steps {
		if s.failed || mechanical[s.tool] {
			continue
		}
		state, body := substance(s.output)
		var line string
		switch {
		case body != "" && state != "":
			line = state + " — " + body
		case body != "":
			line = body
		default:
			line = state
		}
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, "• "+line)
	}
	// The arc is at the ends: where she started and where she ended up. The middle
	// of a forty-round run is repetition, and dropping it silently would be the
	// same dishonesty this file exists to remove.
	const maxLines = 10
	if len(out) > maxLines {
		head := out[:4]
		tail := out[len(out)-5:]
		mid := fmt.Sprintf("… %d more steps in between …", len(out)-len(head)-len(tail))
		out = append(append(append([]string{}, head...), mid), tail...)
	}
	return out
}

// substance splits a tool's output into what she DID and what the world SAID.
//
// The convention across the browser skills is a header of one or two lines — a
// title, a URL, a "Clicked X. Now on Y" — then a blank line, then the page. Both
// halves matter and they matter differently: the header says where she is, the
// body says what is true there. Reporting only the header is what produced a
// click log; reporting only the body loses the place.
func substance(output string) (state, body string) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 {
		return "", ""
	}
	state = clipRunes(strings.TrimSpace(lines[0]), 120)

	// The body begins after the first blank line; failing that, after any leading
	// lines that are bare URLs (the browser skills print the landing URL on its
	// own line under the title).
	start := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		start = 1
	}

	// The body is a SPAN of the page, not its first line.
	//
	// Taking one line was the first-line rule again, one layer down. Every
	// accessible site — Indeed, LinkedIn, GitHub, gov.uk — opens its body with a
	// skip link, so the first line of the body is "Skip to main content" and the
	// content is on the next one. A forty-round job-application run reported nine
	// page reads as `page said: Skip to main content.` nine times: the reads had
	// worked, and this function threw away everything they returned. The report
	// then read as though she had been staring at a blank page, which is the exact
	// dishonesty this file exists to remove.
	//
	// There is no boilerplate blocklist here on purpose — guessing which lines are
	// navigation is how you drop the one line that mattered. The budget was always
	// 200 characters; it just used to be spent entirely on whatever came first.
	// Filling it makes the skip link cost twenty characters instead of all of them.
	const bodyBudget = 200
	var b strings.Builder
	for ; start < len(lines); start++ {
		s := strings.TrimSpace(lines[start])
		if s == "" || isBareURL(s) {
			continue
		}
		if b.Len() > 0 {
			// Separated, not run together: two lines of page text are two
			// statements, and "Skip to main content Software Developer Lagos" reads
			// as one sentence that no page contains.
			b.WriteString(" / ")
		}
		b.WriteString(s)
		if b.Len() >= bodyBudget {
			break
		}
	}
	return state, clipRunes(b.String(), bodyBudget)
}

func isBareURL(s string) bool {
	return (strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")) &&
		!strings.ContainsAny(s, " \t")
}

// mechanical names the tools whose output is about the machinery rather than the
// task — including the ones that report only that an action occurred ("Filled
// #answer3."), which is a mechanism with no news in it. Everything not listed is
// assumed to carry news, so a skill added later shows up by default rather than
// being silently dropped.
var mechanical = map[string]bool{
	"browser_scroll":    true,
	"browser_scroll_to": true,
	"browser_press":     true,
	"browser_focus":     true,
	"browser_wait":      true,
	"browser_wait_for":  true,
	"browser_inspect":   true,
	"browser_tabs":      true,
	"browser_fill":      true,
	"browser_type":      true,
	"browser_hover":     true,
	"change_dir":        true,
}

// failures lists the distinct reasons things went wrong, newest first, so the
// account names the obstacle instead of implying she simply gave up.
func failures(steps []step) []string {
	seen := map[string]bool{}
	var out []string
	for i := len(steps) - 1; i >= 0 && len(out) < 3; i-- {
		if !steps[i].failed {
			continue
		}
		msg := firstLine(strings.TrimPrefix(strings.TrimSpace(steps[i].output), "ERROR: "))
		if msg == "" || seen[msg] {
			continue
		}
		seen[msg] = true
		out = append(out, msg)
	}
	return out
}

func actions(n int) string {
	if n == 1 {
		return "1 action"
	}
	return fmt.Sprintf("%d actions", n)
}

func lastRound(steps []step) int {
	if len(steps) == 0 {
		return 0
	}
	return steps[len(steps)-1].round
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return clipRunes(strings.TrimSpace(s), 160)
}

// clipRunes truncates on a character boundary.
//
// Cutting at a byte offset splits multi-byte runes, and everything downstream is
// UTF-8: the digest goes into a JSON request body and the account into
// archive.jsonl, where the invalid tail is silently replaced by U+FFFD. A course
// title with an accent or an em dash at the cut point became mojibake in the
// permanent archive.
func clipRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
