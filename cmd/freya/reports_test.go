package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Akins20/FREYA/internal/work"
)

func freshReports(t *testing.T) *reportQueue {
	t.Helper()
	reports.drain()
	t.Cleanup(func() { reports.drain() })
	return &reports
}

// The behaviour asked for: a job that finishes mid-conversation is held and
// brought up naturally on her next turn, not announced over the top of whatever
// is being said.
func TestAFinishedJobIsHeldForHerNextTurn(t *testing.T) {
	q := freshReports(t)

	q.add(report{
		id:      "job2",
		goal:    "do my self-quizzes 1, 2 and 3 for CS 3340",
		outcome: "Quizzes 1 and 2 are submitted, 8/10 and 9/10. The third needs a code I don't have.",
		state:   work.Done,
		at:      time.Now(),
	})
	if q.waiting() != 1 {
		t.Fatal("the finished job was not held")
	}

	b := brief(q.drain())
	// The outcome has to be there, in the job's own words.
	if !strings.Contains(b, "8/10 and 9/10") {
		t.Errorf("what came of it was lost:\n%s", b)
	}
	if !strings.Contains(b, "self-quizzes 1, 2 and 3") {
		t.Errorf("what was asked for was lost:\n%s", b)
	}
	// And it must read as an instruction about HOW to mention it, or she reads
	// the block out instead of saying "oh, and those quizzes are done".
	for _, want := range []string{"the way a person would", "Do not read this out", "in passing"} {
		if !strings.Contains(b, want) {
			t.Errorf("the brief does not tell her to weave it in (%q):\n%s", want, b)
		}
	}

	// Offered exactly once: draining is what makes it so.
	if q.waiting() != 0 {
		t.Error("the report is still pending after being delivered; she would raise it twice")
	}
	if brief(q.drain()) != "" {
		t.Error("an empty queue still produced a brief")
	}
}

// A failure and a stop are different news from a success, and saying "finished"
// for either would be a lie.
func TestTheBriefDistinguishesHowAJobEnded(t *testing.T) {
	q := freshReports(t)
	q.add(report{id: "job1", goal: "book the flight", outcome: "the card was declined",
		state: work.Failed, at: time.Now()})
	q.add(report{id: "job2", goal: "read the paper", outcome: "got through the first half",
		state: work.Cancelled, at: time.Now()})

	b := brief(q.drain())
	if !strings.Contains(b, "job1 (failed)") {
		t.Errorf("a failure reads as a success:\n%s", b)
	}
	if !strings.Contains(b, "job2 (was stopped partway)") {
		t.Errorf("a stop reads as a completion:\n%s", b)
	}
}

// When nobody says anything, holding it forever is its own kind of unhelpful —
// and what she says then is the job's own words, not a template.
func TestSheSpeaksUpWhenNoOpeningArrives(t *testing.T) {
	one := []report{{
		id: "job1", goal: "the quizzes",
		outcome: "Quizzes 1 and 2 are submitted, 8/10 and 9/10.",
		state:   work.Done, at: time.Now(),
	}}
	line := speakable(one)
	if !strings.Contains(line, "Quizzes 1 and 2 are submitted") {
		t.Errorf("her own account of the work was replaced by a template: %q", line)
	}
	if strings.Contains(line, "job1") {
		t.Errorf("she read out an internal job id: %q", line)
	}

	failed := speakable([]report{{
		id: "job1", goal: "booking the flight", outcome: "the card was declined",
		state: work.Failed, at: time.Now(),
	}})
	if !strings.Contains(failed, "didn't work out") || !strings.Contains(failed, "card was declined") {
		t.Errorf("a failure was announced as a success: %q", failed)
	}
}

// An unattended machine could queue a day of work and then recite all of it at
// whoever says good morning.
func TestHeldReportsAreBounded(t *testing.T) {
	q := freshReports(t)
	for i := 0; i < maxPendingReports+5; i++ {
		q.add(report{id: "job", goal: "g", outcome: "o", state: work.Done, at: time.Now()})
	}
	if n := q.waiting(); n > maxPendingReports {
		t.Fatalf("holding %d reports, cap is %d", n, maxPendingReports)
	}
}

// A job that produced nothing is not news.
func TestAnEmptyOutcomeIsNotHeld(t *testing.T) {
	q := freshReports(t)
	q.add(report{id: "job1", goal: "g", outcome: "   ", state: work.Done, at: time.Now()})
	if q.waiting() != 0 {
		t.Error("a job with nothing to say was queued as a report")
	}
}

// The idle path fires on the OLDEST held report, so one that has waited long
// enough is not kept back by a newer one arriving.
func TestTheIdleClockRunsFromTheOldestReport(t *testing.T) {
	q := freshReports(t)
	old := time.Now().Add(-2 * reportIdle)
	q.add(report{id: "job1", goal: "g", outcome: "done a while ago", state: work.Done, at: old})
	q.add(report{id: "job2", goal: "g", outcome: "just now", state: work.Done, at: time.Now()})

	if got := q.oldest(); !got.Equal(old) {
		t.Fatalf("oldest = %v, want the first report's time %v", got, old)
	}
	if time.Since(q.oldest()) < reportIdle {
		t.Error("a report that has waited twice the idle window would still be held back")
	}
}
