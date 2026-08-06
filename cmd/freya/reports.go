package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/akins/jarvis/internal/work"
)

// Telling you about work that finished while you were talking.
//
// # Why a finished job does not just speak
//
// The obvious wiring — job ends, she says so — is wrong twice over. It talks over
// whatever is happening, and it says it in a format string: "Finished in the
// background (do my self-quizzes 1, 2 and 3): …". Nobody talks like that, and
// nobody wants to be interrupted by it.
//
// What a person does is hold it and drop it into the next natural gap: "oh, and
// those quizzes are done — 8 and 9 out of 10." So a finished job goes here, and
// leaves in one of two ways:
//
//   - woven into her next reply, because the pending report is handed to the
//     model as context for that turn along with an instruction to mention it
//     naturally rather than recite it;
//   - spoken on its own, if nothing has been said for a while and she is idle,
//     because holding a finished report forever is its own kind of unhelpful.
//
// Either way the words are HERS. The job's own reply is what gets carried
// through; nothing here writes prose on her behalf.

// report is one finished job, waiting to be mentioned.
type report struct {
	id      string
	goal    string
	outcome string
	state   work.State
	at      time.Time
}

// reportQueue holds what she has not yet had a chance to tell you.
type reportQueue struct {
	mu      sync.Mutex
	pending []report
}

var reports reportQueue

// add records a finished job for mention.
func (q *reportQueue) add(r report) {
	if strings.TrimSpace(r.outcome) == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending = append(q.pending, r)
	// Bounded, because an unattended machine could otherwise queue a day's work
	// and then recite all of it at whoever says good morning.
	if len(q.pending) > maxPendingReports {
		q.pending = q.pending[len(q.pending)-maxPendingReports:]
	}
}

const maxPendingReports = 5

// waiting reports how many are held.
func (q *reportQueue) waiting() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

// oldest returns when the longest-held report arrived, or the zero time.
func (q *reportQueue) oldest() time.Time {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return time.Time{}
	}
	return q.pending[0].at
}

// drain takes everything pending, for delivery.
func (q *reportQueue) drain() []report {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.pending
	q.pending = nil
	return out
}

// brief renders pending reports as context for her next turn.
//
// It is an instruction about HOW to mention them, not text to read out. The
// difference is the whole point: "weave it in" produces "oh, and those quizzes
// are done"; handing over a formatted block produces her reading a formatted
// block aloud.
func brief(rs []report) string {
	if len(rs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[Background work finished while you were busy. Bring it up in your reply the " +
		"way a person would — briefly, in passing, leading with the outcome: \"oh, and those " +
		"quizzes are done\". Do not read this out, do not use headings, and do not repeat the " +
		"whole thing if only part of it matters to what was just asked. If it is genuinely " +
		"irrelevant to the moment, one short clause is enough.]\n")
	for _, r := range rs {
		verb := "finished"
		switch r.state {
		case work.Failed:
			verb = "failed"
		case work.Cancelled:
			verb = "was stopped partway"
		}
		fmt.Fprintf(&b, "\n- %s (%s) — you asked for: %s\n  What came of it: %s\n",
			r.id, verb, clipLine(r.goal, 90), strings.TrimSpace(r.outcome))
	}
	return b.String()
}

// speakable renders a report she is telling you unprompted, because nothing has
// been said for a while.
//
// The outcome is already her own writing — it is the reply her background thread
// produced — so this adds an opening and otherwise gets out of the way.
func speakable(rs []report) string {
	if len(rs) == 0 {
		return ""
	}
	var parts []string
	for _, r := range rs {
		switch r.state {
		case work.Failed:
			parts = append(parts, fmt.Sprintf("That %s didn't work out: %s",
				clipLine(r.goal, 60), strings.TrimSpace(r.outcome)))
		case work.Cancelled:
			parts = append(parts, fmt.Sprintf("I stopped partway through %s. %s",
				clipLine(r.goal, 60), strings.TrimSpace(r.outcome)))
		default:
			parts = append(parts, strings.TrimSpace(r.outcome))
		}
	}
	if len(rs) == 1 {
		return "Finished that in the background — " + parts[0]
	}
	return "A couple of things finished in the background. " + strings.Join(parts, " ")
}

// reportIdle is how long she waits for a natural opening before speaking up.
//
// Long enough that an ordinary pause in conversation does not trigger it, short
// enough that a result is still news. If the user says anything in the meantime,
// the report rides on that reply instead and this never fires.
const reportIdle = 90 * time.Second
