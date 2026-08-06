package agent

import (
	"fmt"
	"strings"
	"unicode"
)

// What she says when she runs out of tool calls partway through a job.
//
// # The measured failure
//
// She had submitted two quizzes and was inside the third when the cap stopped
// her. Her telemetry shows the salvage call went through fine — 49 output tokens,
// no error — and what came back amounted to "I couldn't finish". The machinery
// worked; the question was wrong.
//
// The old instruction was: "Answer now using only what you have already gathered.
// Do not request more tools. If it is genuinely not enough, say briefly what you
// still need." Every clause points away from the work. It asks for an ANSWER —
// and there is no answer to "do my three quizzes", only a state of affairs — then
// offers, as the alternative, saying what she still needs. Given those two doors
// a model takes the second, briefly, which is exactly what came out.
//
// The cure is to ask for the right thing: a progress report against the goal, in
// the goal's terms, naming the items.

// roundCapBrief is appended to the system prompt for the tool-free salvage call.
//
// didWork must be false when every tool call failed. Without that, the closing
// paragraph asserts "you did a substantial amount of work" to a model that did
// none, which is an instruction to invent achievement — the opposite of the
// honesty this exists for, and a direct collision with the unconditional
// anti-sycophancy rule.
func roundCapBrief(goal string, didWork bool) string {
	var b strings.Builder
	b.WriteString("\n\nYou have used every tool call allowed for this exchange. " +
		"You cannot call another tool, so do not try, and do not ask for permission to continue.\n\n")

	if g := firstLine(goal); g != "" {
		fmt.Fprintf(&b, "What you were asked to do: %q\n\n", g)
	}

	b.WriteString("Give a progress report on THAT, not on your tool use. Cover, in plain sentences:\n" +
		"  · which parts are finished — name them specifically;\n" +
		"  · which part you were in the middle of, and how far into it you got;\n" +
		"  · what is left.\n\n" +
		"Ground every claim in what the tools actually returned. If a page said a quiz was " +
		"submitted, that quiz is done and you should say so; if you never saw such confirmation, " +
		"say it is unconfirmed rather than claiming it either way.\n\n" +
		"Write it the way you would say it out loud to someone who was not watching: a few " +
		"sentences, no bullet points, no headings. Lead with what got done, and name the actual " +
		"things — the quiz, the file, the booking — so the user never has to go and look for " +
		"themselves. That is the one thing this report exists to prevent.\n\n")

	if didWork {
		b.WriteString("Do not reply with a bare \"I couldn't finish\" or \"I was unable to complete this\". " +
			"Work was done; say what it amounted to.")
	} else {
		b.WriteString("Every tool call this exchange failed. Say so plainly and say what blocked you. " +
			"Do not manufacture progress that did not happen.")
	}
	return b.String()
}

// retryNudge is sent as a trailing message rather than appended to the system
// prompt, and that placement is the point.
//
// Gemini caches stable prompt PREFIXES, and the system instruction goes on the
// wire ahead of every message. Putting the retry's only difference at the tail of
// the system block would move the divergence in front of the entire ~180k-token
// history, forfeiting the cached prefix and re-billing the lot — on the second of
// two calls, at the moment the user has already waited through forty rounds. At
// the tail, the retry is a strict prefix extension of the first attempt.
const retryNudge = "[Your report said nothing about what was actually achieved. " +
	"Try again: state plainly which parts are finished, name them, and say how far " +
	"into the remaining one you got.]"

// nonAnswer reports whether a reply dodges the question.
//
// # Why this is not a phrase list
//
// It was one, and the phrase list was measurably inverted — it rejected good
// reports and passed real dodges:
//
//	"Quizzes one and two are submitted; I couldn't finish the third."  → rejected
//	"I made some progress but ran into issues."                        → accepted
//
// Two forces drove that. The brief asks for spoken prose, and spoken prose spells
// numbers as words, which closed the "contains a digit" escape hatch; and the
// brief's own opening line ("you have used every tool call") invites the
// paraphrase "I ran out of tool calls", which was itself a dodge phrase. So the
// test fired hardest on exactly the honest, specific reports it was meant to
// protect — and a model that phrased its dodge in any other words sailed through.
//
// What actually separates a report from a dodge is not vocabulary about HER; it
// is whether the reply says anything about the WORLD. So the test is now overlap
// with the evidence: a reply that names things the tools actually saw is
// concrete, whatever phrasing surrounds it.
func nonAnswer(reply string, work *trail) bool {
	if work == nil || !work.worked() {
		return false // nothing succeeded; "I couldn't" is the honest answer
	}
	r := strings.TrimSpace(reply)
	if r == "" {
		return true
	}
	return !concrete(r, evidenceTerms(work))
}

// concrete judges a reply by what it shares with what was actually observed.
func concrete(reply string, terms map[string]bool) bool {
	seen := map[string]bool{}
	overlap := 0
	for _, w := range significantWords(reply) {
		if terms[w] && !seen[w] {
			seen[w] = true
			overlap++
		}
	}
	n := len(reply)
	switch {
	case overlap >= 2:
		return true
	case overlap >= 1 && n >= 60:
		return true
	case n >= 180:
		// Long-form prose that names nothing from the evidence is unusual, and
		// erring towards accepting it costs one uninformative reply, where erring
		// the other way costs a good report thrown away and rewritten.
		return true
	case hasDigit(reply) && n >= 40:
		return true
	}
	return false
}

// evidenceTerms is the vocabulary of what she actually saw and was asked for.
func evidenceTerms(work *trail) map[string]bool {
	terms := map[string]bool{}
	for _, s := range work.snapshot() {
		state, body := substance(s.output)
		for _, w := range significantWords(state + " " + body) {
			terms[w] = true
		}
	}
	return terms
}

// significantWords lowercases and splits on anything that is not a letter or
// digit, keeping words long enough to mean something. Short words and filler
// carry no evidence of specificity — "the page was done" must not read as a
// match for "the".
func significantWords(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) < 4 || filler[f] {
			continue
		}
		out = append(out, f)
	}
	return out
}

// filler is the vocabulary that appears in every reply regardless of content, so
// matching on it would make any sentence look concrete.
var filler = map[string]bool{
	"that": true, "this": true, "with": true, "from": true, "have": true,
	"been": true, "were": true, "will": true, "your": true, "there": true,
	"then": true, "them": true, "they": true, "what": true, "when": true,
	"which": true, "about": true, "into": true, "than": true, "some": true,
	"more": true, "most": true, "just": true, "also": true, "here": true,
	"page": true, "http": true, "https": true, "html": true, "www": true,
}

func hasDigit(s string) bool {
	return strings.ContainsAny(s, "0123456789")
}
