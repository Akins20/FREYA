package agent

import (
	"fmt"
	"regexp"
	"strings"
)

// Saying you made something, having made nothing.
//
// # The measurement
//
// Asked to redo an audit, she made exactly one tool call — system_open on the
// report she had written half an hour earlier — and answered "I have updated and
// reopened the development status report on your screen. It provides a complete
// at-a-glance audit of all 28 projects". The file was byte-identical: same size,
// same modification time, same nineteen of twenty-eight projects it had covered
// before. Nothing was updated. Nothing was audited. Something was opened.
//
// That is worse than the run before it, where she at least did the work and only
// overstated the coverage. Here the work did not happen and the sentence said it
// had.
//
// # Why the coverage check could not see this
//
// coverage.go compares a completeness claim against a set a tool enumerated
// THIS exchange. Here no tool enumerated anything — the only call opened a file
// — so there was nothing to compare and it stayed silent, correctly and
// uselessly. This is the hole that check was shipped with, found within the hour.
//
// # What this checks, and what it deliberately does not
//
// Not whether the artifact is any good, and not whether the claim is true. Only
// this: the reply says she made or updated something, and no tool wrote anything
// this exchange. Both halves are exact — the tools that produce are a fixed list,
// and a first-person making verb is a regular expression.
//
// Reusing earlier work is frequently the right thing to do; the failure is not
// the reuse, it is presenting it as fresh. So the brief asks her to say WHEN it
// was made. "Here is the report I built earlier" is a complete and honest answer
// and costs her one sentence.

// producingTools write, create or modify something. run_shell and run_command
// are included because a redirection or a generator script produces a file just
// as much as file_write does.
var producingTools = map[string]bool{
	"file_write": true, "file_edit": true, "file_append": true,
	"file_copy": true, "file_move": true, "folder_create": true,
	"docx_write": true, "pdf_write": true, "xlsx_write": true,
	"document_convert": true, "archive_create": true, "archive_extract": true,
	"browser_save_pdf": true, "browser_screenshot": true,
	"run_shell": true, "run_command": true, "terminal_run": true,
	"memory_remember": true, "note_add": true, "skill_learn": true,
}

// wroteSomething reports whether anything was actually produced this exchange.
func wroteSomething(work *trail) bool {
	for _, s := range work.snapshot() {
		if !s.failed && producingTools[s.tool] {
			return true
		}
	}
	return false
}

// madeClaim matches a first-person claim of having produced something.
//
// First person and past tense, both required. "The report shows X" is not a
// claim to have written it; "I have updated the report" is. The verb list is the
// ones that mean an artefact came into being or changed, not ones that mean she
// looked at it.
// The adverb slot is deliberately generous. The first version allowed only
// "just", "now" and "completely", and she wrote "I've FRESHLY regenerated" — one
// unanticipated adverb and the whole check went silent. Anything ending in -ly
// now passes, and the gap is capped at one word so a matching verb far later in
// a long sentence cannot be dragged into a claim she never made.
var madeClaim = regexp.MustCompile(
	`(?i)\bI(?:'ve|\s+have)?\s+(?:(?:just|now|also|then)\s+)?(?:\w+ly\s+)?` +
		`(?:created|built|rebuilt|wrote|written|generated|regenerated|updated|` +
		`redone|produced|drafted|compiled|assembled)\b`)

// artifactWord ties the claim to a thing that lives on disk, so "I've updated my
// understanding" or "I have written to you before" cannot trigger it.
var artifactWord = regexp.MustCompile(
	`(?i)\b(report|document|file|page|site|website|spreadsheet|summary|draft|` +
		`presentation|slides|script|\S+\.(?:html|md|txt|docx|xlsx|pdf|csv|json|go|py|js))\b`)

// alreadyDated matches a reply that has already said when the thing was made.
//
// The whole ask here is one clause of provenance, so a reply that has supplied it
// must pass through untouched. "Here is the report I built earlier" is the exact
// answer this check exists to produce — challenging it would be both rude and a
// wasted round, and would teach her that saying so buys nothing.
var alreadyDated = regexp.MustCompile(
	`(?i)\b(earlier|previously|already|before|last time|yesterday|` +
		`a moment ago|just now|from (?:the )?(?:earlier|previous|last)|` +
		`no changes since|nothing has changed)\b`)

// claimedWithoutProducing reports a claim of having made something when nothing
// was written this exchange and the reply does not say when it was made.
func claimedWithoutProducing(reply string, work *trail) bool {
	if strings.TrimSpace(reply) == "" || wroteSomething(work) {
		return false
	}
	if alreadyDated.MatchString(reply) {
		return false
	}
	return madeClaim.MatchString(reply) && artifactWord.MatchString(reply)
}

// producedBrief asks the reply to say when the thing was made.
//
// Not an accusation and not an instruction to go and redo it: opening something
// she prepared earlier is often exactly right, and sending her off to rebuild a
// good artefact would be a worse outcome than the sentence that prompted this.
// What it wants is one clause of provenance.
func producedBrief(goal string) string {
	return fmt.Sprintf(`

# Before you answer

Your reply says you made or updated something, and you have not written anything
this turn — every tool you called only looked at things or opened them.

If what you are showing them was made earlier, say so: "here is the report I put
together earlier" is a complete answer and takes one clause. They will read "I
have updated the report" as work you did just now, and go looking for changes
that are not there.

If it genuinely needed remaking and you have not remade it, say that instead —
that is useful to know and they can decide.

Do not apologise or explain this instruction, and do not pad the reply. Answer
the original request — %s — with the provenance of anything you are handing
over made plain.`, clipGoal(goal, 120))
}

// provenanceNote is what the framework states when the re-ask does not take.
//
// Measured: asked to redo an audit she opened the file she had written twenty
// minutes earlier and said "I've freshly regenerated and opened" it. The re-ask
// fired, and her rewrite said "I've regenerated and reopened" — the same claim
// with fewer adverbs. That is not obstinacy; from where she sits she DID
// regenerate that report, just not in this exchange, and one instruction is not
// enough to make a small model hold that distinction.
//
// So the last word is the framework's, and it is a fact rather than a
// correction: no file was written this turn. It is appended rather than
// substituted because her account of the work may be perfectly good — the only
// thing wrong with it is the impression that it happened just now.
const provenanceNote = "\n\n[Nothing was written this turn — no file was created or " +
	"changed. Anything shown was produced earlier in the conversation.]"
