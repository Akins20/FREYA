package agent

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Akins20/FREYA/internal/llm"
)

// Claiming to have done all of something.
//
// # The measurement
//
// Asked to audit every project in a folder, she enumerated them — a tool told her
// there were 28 — worked through eleven, wrote a genuinely good report covering
// nineteen, and finished with "I've audited all 28 projects in your Development
// folder". Nine were missing. Most of those were defensibly skipped: four are not
// projects at all and three are folders holding other projects. One was a git
// repository with seventy-two files, and one she had actually run git status on
// and then left out of the report.
//
// The omissions were not really the failure. Had she written "19 audited, 9
// skipped, and here is why", that would have been excellent work. The failure was
// the sentence — a blanket claim over a set she had not covered, made in a reply
// that gave the user no way to notice.
//
// # What is actually checkable, and what is not
//
// Not the truth of the claim. Nothing on this side can count what a written
// report covers, and trying would produce a verifier that is confidently wrong —
// the mistake made twice already this week.
//
// What IS checkable is the SHAPE: a tool enumerated N things, and the reply
// asserts having done all N. That pattern is exact, it is deterministic, and it
// costs nothing to detect. So this does not accuse her of anything. It sends the
// reply back with the count in front of her and asks it to carry its own
// evidence: how many, and which ones did you leave. A reply that was honest to
// begin with survives that unchanged.

// enumerated is a count a tool reported.
type enumerated struct {
	n    int
	noun string
	tool string
}

// countPattern reads "28 projects", "6 entries", "12 files" out of tool output.
//
// Anchored on nouns that mean a set that was listed, not any number at all: a
// byte count, a line number and a score are all numbers followed by a word, and
// none of them is a claim about how many things exist.
var countPattern = regexp.MustCompile(
	`(?i)\b(\d{1,4})\s+(projects?|entries|items?|files?|folders?|repositor(?:y|ies)|tabs?|results?|courses?|quizzes)\b`)

// enumerations finds the sets her tools listed for her this exchange.
func enumerations(work *trail) []enumerated {
	var out []enumerated
	for _, s := range work.snapshot() {
		if s.failed {
			continue
		}
		for _, m := range countPattern.FindAllStringSubmatch(s.output, -1) {
			n, err := strconv.Atoi(m[1])
			if err != nil || n < 3 {
				continue // two of something is not a set worth auditing
			}
			out = append(out, enumerated{n: n, noun: strings.ToLower(m[2]), tool: s.tool})
		}
	}
	return out
}

// wholeClaim reports the count a reply claims to have covered entirely, or zero.
//
// It requires BOTH a completeness word and the number, adjacent — "all 28",
// "every one of the 28", "all 28 projects". A reply that merely mentions 28, or
// that says "all of the ones I could reach", is not making this claim and is not
// stopped.
var wholeClaim = regexp.MustCompile(
	`(?i)\b(?:all|every|each)\s+(?:one\s+of\s+)?(?:the\s+)?(\d{1,4})\b`)

// overclaimed reports the count she claimed in full but which no tool result
// supports her having worked through, or zero when the reply is not making that
// claim.
func overclaimed(reply string, work *trail) (int, string) {
	sets := enumerations(work)
	if len(sets) == 0 {
		return 0, ""
	}
	for _, m := range wholeClaim.FindAllStringSubmatch(reply, -1) {
		claimed, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		for _, s := range sets {
			if s.n == claimed {
				return claimed, s.noun
			}
		}
	}
	return 0, ""
}

// coverageBrief is the instruction for the re-ask.
//
// Deliberately not "you are lying". She may well have covered all of them, and
// the point is that the reply should say so in a way the user can check. It also
// says plainly that skipping is allowed — because the version of this reply that
// would have been right ("19 audited, 9 skipped, here is why") is better work
// than the one that claimed everything, and she should not read this as pressure
// to go back and grind through folders that are not projects.
func coverageBrief(goal string, n int, noun string) string {
	return fmt.Sprintf(`

# Before you answer

You are about to tell them you handled all %d %s. A tool listed %d of them for
you this turn, so that number is theirs, not a figure of speech — they will read
it as a promise that nothing was left out.

Say what you actually covered. If it was all %d, say so plainly and the sentence
stands. If it was fewer, give the number you did and name what you left, with one
clause each on why. Leaving things out is fine and often correct — several may
not be what the request meant at all — but it has to be visible. "%d of %d, and
here is what I skipped and why" is a better answer than "all %d", and it is the
only one they can check.

Do not pad the reply, apologise, or explain this instruction. Rewrite the answer
to the original request — %s — with the coverage stated.`,
		n, noun, n, n, n, n, n, clipGoal(goal, 120))
}

// clipGoal shortens the restated goal so a long request does not swamp the
// instruction that follows it.
func clipGoal(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// reask puts one instruction in front of the model and takes its rewrite.
//
// The instruction rides in the system tail rather than as another message,
// because it is guidance about how to answer rather than a turn in the
// conversation — and it must never end up archived as something the user said.
// A failure here returns empty and the original reply stands: the network going
// down is not a reason to lose her answer.
func (a *Agent) reask(ctx context.Context, system string, msgs []llm.Message) string {
	second, err := a.chat(ctx, llm.Request{
		System:         system,
		Messages:       msgs,
		ThinkingBudget: a.ThinkBudget,
		ShowThoughts:   a.ThinkBudget != 0,
	})
	if err != nil || second == nil {
		return ""
	}
	if thought := strings.TrimSpace(second.Reasoning); thought != "" && a.OnThought != nil {
		a.OnThought(thought)
	}
	return strings.TrimSpace(second.Text)
}
