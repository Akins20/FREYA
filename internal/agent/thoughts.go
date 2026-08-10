package agent

import (
	"regexp"
	"strings"
)

// Taking the summariser's throat-clearing off the front of a thought.
//
// # What this is for
//
// The thinking window shows Response.Reasoning, which for Gemini is a summary
// the model writes of its own reasoning. It writes that summary to an
// instruction, and it keeps narrating the instruction back:
//
//	"Okay, here's my take on those thoughts, tailored for an expert audience,
//	 and written in the first person"
//	"Okay, I'm ready to summarize my "thoughts" in the first person, assuming an
//	 expert audience. Here's my take:"
//	"Here's a summary of my thinking, presented as if it were my own internal
//	 monologue:"
//
// Counted over six real runs: 428 thought lines, 75 of them opening on one of
// these, 18 mentioning an expert audience and 7 mentioning the first person.
// None of it is her reasoning. It is the wrapper around her reasoning, and it is
// the first thing anyone sees in the terminal.
//
// # Why it is a display filter and not a prompt
//
// There is nothing here to prompt. The summary is produced inside the provider
// from includeThoughts and arrives already written. Rewording anything on our
// side would not reach it.
//
// # Deliberately timid
//
// It only looks at the top, it stops at the first line that is about the work,
// and it never returns nothing: a thought that is preamble all the way down is
// handed back untouched, on the grounds that a wrong strip costs more than a
// stray line. The patterns are all framings of the summary itself. "Alright,
// let's get down to brass tacks. The instructions are clear: I need to generate
// a sales.xlsx spreadsheet and a report.docx report, which will summarize the
// key takeaways" is real content that mentions summarising, and it survives —
// there is a test for exactly that line.
var thoughtPreamble = regexp.MustCompile(`(?i)^\s*(?:` +
	// "Okay, here's my take on ...", "Here is my interpretation of ...",
	// "Here's my thought process, distilled:".
	`(?:ok(?:ay)?|alright|right|sure)?[,.]?\s*here(?:'s| is| goes)\s+(?:my|a|the|your)\s+` +
	`(?:take|summary|attempt|interpretation|reading|version|account|rendering|stab` +
	`|thought process|thinking|thoughts)\b` +
	`|` +
	// "Okay, here's what's going through my mind:" — the same move without a noun
	// for the summary, so it has to be matched on the whole phrase.
	`(?:ok(?:ay)?|alright|right|sure)?[,.]?\s*here(?:'s| is)\s+what(?:'s| is)\s+` +
	`(?:going through my mind|on my mind|running through my head)` +
	`|` +
	// "I'm ready to summarize my thoughts ...", "Let me summarise the thinking ...".
	`(?:ok(?:ay)?|alright|right)?[,.]?\s*(?:i'?m ready to|let me|i'?ll)\s+summari[sz]` +
	`|` +
	// "Summarising the thought process ...", "My attempt at summarizing ...".
	`(?:my\s+)?(?:attempt at\s+)?summari[sz]\w*\s+(?:my|the|those)\s+(?:thought|thinking)` +
	`)`)

// thoughtMetaPhrase catches the framings that do not start the line but give the
// whole line away: it is describing the audience or the voice of the summary
// rather than saying anything about the work.
var thoughtMetaPhrase = regexp.MustCompile(`(?i)` +
	`expert audience|assuming (?:an|the user is an|i'?m an) expert|expert'?s perspective` +
	`|first[- ]person|internal (?:monologue|dialogue)` +
	`|persona you requested|as you requested|tailored for`)

// trimThoughtPreamble removes leading lines that are about the summary rather
// than about the work.
//
// Stops at the first line that is neither, so a preamble buried in the middle is
// left alone — by then it is part of what she is saying.
func trimThoughtPreamble(s string) string {
	lines := strings.Split(s, "\n")
	i := 0
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			i++
			continue
		}
		if !isThoughtPreamble(line) {
			break
		}
		i++
	}
	// Never hand back nothing. A thought that is preamble the whole way down is
	// still the only thought there was.
	trimmed := strings.TrimSpace(strings.Join(lines[i:], "\n"))
	if trimmed == "" {
		return strings.TrimSpace(s)
	}
	return trimmed
}

// isThoughtPreamble reports a line that frames the summary instead of saying
// something.
//
// The length bound matters. These openers are one sentence; a long paragraph
// that happens to begin "here's my reading of" is her actually reading
// something, and taking it would remove the substance along with the frame.
func isThoughtPreamble(line string) bool {
	if len(line) > 320 {
		return false
	}
	if thoughtPreamble.MatchString(line) {
		return true
	}
	// A phrase about the audience or the voice only counts when the line is
	// short enough to be doing nothing else.
	return len(line) <= 200 && thoughtMetaPhrase.MatchString(line)
}
