package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/akins/jarvis/internal/llm"
)

// Not saying it worked when it did not.
//
// # The measured failure
//
// One exchange, fourteen tool calls, every one of them failed or refused, nothing
// clicked, nothing submitted. Her answer:
//
//	"Self-Quiz Unit 5 for Systems and Application Security is submitted.
//	 I'm moving on to Unit 6 now."
//
// That is worse than the failure it followed. A failure rate is visible and
// recoverable; a confident false completion is neither — the user goes to bed
// believing the quiz is in.
//
// # Why the existing machinery did not catch it
//
// Everything needed was already known. The trail recorded every call and its
// outcome; not one had succeeded. But nothing compared the ANSWER against the
// WORK. Verification was built for individual tools — did this click change the
// page — and the final claim was left unchecked, which is precisely where it
// matters most, because that is the only part the user reads.
//
// # The rule, in two parts
//
// The cheap part runs always: on any round where nothing has worked yet, the fact
// is stated in the volatile tail of the request, so she writes with it in front of
// her rather than reconstructing it from forty near-identical error messages. It
// costs no extra call, and it helps mid-loop as much as at the end — "nothing you
// have tried has worked" is a reason to change approach, not only a reason to
// report honestly.
//
// The expensive part is a backstop for the severe case: several rounds deep and
// still nothing, and the answer is checked and re-asked. One extra call on an
// exchange that has already spent many — and not on the ordinary path where a
// single tool fails and she says so, which would tax every missing file and every
// failed search.
//
// No phrase-matching either way. The condition is a fact about the world, not a
// judgement about her wording, and the remedy is to put that fact in front of her.
//
// The re-ask offers no tools. She has already spent the round budget failing; the
// question now is what to TELL the user, not what to try next.

// severeFailure is how many all-failing attempts make an exchange worth a second
// call to get the answer right.
//
// Three, because after one or two she plainly remembers what just happened; the
// failure mode is losing track across many near-identical errors, and the measured
// case was fourteen.
const severeFailure = 3

// truthTail states the fact for the tail of an ordinary request. Empty when
// something has worked, which is the usual case and costs nothing.
func truthTail(work *trail) string {
	attempts, none := nothingWorked(work)
	if !none {
		return ""
	}
	return fmt.Sprintf("[Note on this exchange so far: %d tool calls, and not one of them "+
		"has worked — every attempt failed or was refused, so nothing you have tried has "+
		"taken effect yet. Do not describe any of it as done. Either change approach, or "+
		"tell the user plainly what is blocking you.]", attempts)
}

// nothingWorked reports whether an exchange ran tools and none of them succeeded.
//
// Refusals count as failures — a call the breaker declined never touched the
// world — which is the point: fourteen attempts of which six were refusals is
// still nothing done.
func nothingWorked(work *trail) (attempts int, none bool) {
	if work == nil {
		return 0, false
	}
	steps := work.snapshot()
	if len(steps) == 0 {
		return 0, false // a conversational turn; there was nothing to succeed
	}
	for _, s := range steps {
		if !s.failed {
			return len(steps), false
		}
	}
	return len(steps), true
}

// truthBrief tells her what actually happened, and forbids the claim.
//
// Written as a statement of fact rather than an accusation, because she is not
// lying on purpose — she loses track across forty rounds of near-identical error
// text, which is exactly the situation a plain restatement fixes.
func truthBrief(goal string, attempts int) string {
	var b strings.Builder
	b.WriteString("\n\n[Before you answer — a fact about this exchange, from the tool log.\n\n")
	fmt.Fprintf(&b, "You made %d tool calls and EVERY ONE of them failed or was refused. "+
		"Nothing you attempted took effect: no click landed, nothing was submitted, "+
		"nothing was saved.\n\n", attempts)

	if g := firstLine(goal); g != "" {
		fmt.Fprintf(&b, "What was asked: %q\n\n", clipRunes(g, 160))
	}

	b.WriteString("So you must NOT report any part of it as done, submitted, completed or " +
		"finished. Saying it worked when it did not is worse than the failure itself: the " +
		"user stops checking, and finds out much later.\n\n" +
		"Tell them the truth instead — what you were trying to do, what stopped you " +
		"(quote the actual error), and what state things are really in now. If something " +
		"was genuinely finished EARLIER in the conversation, you may say so, but be exact " +
		"about which part is done and which part is not.]")
	return b.String()
}

// checkTruthful re-asks when the answer is being written on top of an exchange in
// which nothing worked.
//
// Returns the reply to use. When the condition does not apply — the usual case —
// the original reply is returned untouched and no call is made.
func (a *Agent) checkTruthful(ctx context.Context, system string, msgs []llm.Message,
	goal, reply string, work *trail) string {

	attempts, none := nothingWorked(work)
	if !none || attempts < severeFailure {
		return reply
	}

	a.trace("retry", "truthfulness", fmt.Sprintf(
		"%d tool calls, none succeeded — restating the facts before answering", attempts))

	// The correction rides in the system tail rather than as another message,
	// because it is an instruction about how to answer, not a turn in the
	// conversation — and it must not end up archived as something the user said.
	second, err := a.chat(ctx, llm.Request{
		System:         system + truthBrief(goal, attempts),
		Messages:       msgs,
		ThinkingBudget: a.ThinkBudget,
		ShowThoughts:   a.ThinkBudget != 0,
	})
	if err != nil || second == nil {
		return reply // the network is not a reason to lose her answer entirely
	}
	if thought := strings.TrimSpace(second.Reasoning); thought != "" && a.OnThought != nil {
		a.OnThought(thought)
	}
	if corrected := strings.TrimSpace(second.Text); corrected != "" {
		return corrected
	}
	return reply
}

// Failure is what an exchange that went badly hands to whoever is watching.
//
// Deliberately plain data, and deliberately without a diagnosis: the agent knows
// what happened, not why, and a guess passed upward is a guess that whoever reads
// it will start from.
type Failure struct {
	Kind     string // "nothing-worked" or "cap-exhausted"
	Goal     string
	Attempts int
	Failures []string
	Trail    string
	Exchange string
}

// causes lists the errors an engineer should read first.
//
// Refusals are dropped when there is anything else, because a refusal is our own
// breaker talking rather than the world: leading a report with "already failed
// twice in this exchange" points whoever reads it at the breaker, when the thing
// that needs fixing is whatever made the first attempt fail. The refusals are
// still in the trail, and the count is in Attempts.
func causes(steps []step) []string {
	var real, refused []step
	for _, s := range steps {
		if !s.failed {
			continue
		}
		if strings.Contains(s.output, "REFUSED:") {
			refused = append(refused, s)
			continue
		}
		real = append(real, s)
	}
	if len(real) > 0 {
		return failures(real)
	}
	return failures(refused)
}

// reportFailure hands an exchange's wreckage to the watcher, if there is one.
//
// Fire-and-forget on its own goroutine: writing a report must never slow, and can
// never fail, the reply the user is waiting for.
func (a *Agent) reportFailure(kind, goal, exchange string, work *trail) {
	if a.OnFailure == nil || work == nil {
		return
	}
	steps := work.snapshot()
	if len(steps) == 0 {
		return
	}
	f := Failure{
		Kind:     kind,
		Goal:     goal,
		Attempts: len(steps),
		Failures: causes(steps),
		Trail:    work.digest(),
		Exchange: exchange,
	}
	go a.OnFailure(f)
}
