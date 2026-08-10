package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Akins20/FREYA/internal/llm"
	"github.com/Akins20/FREYA/internal/skills"
)

// The measured failure, end to end: fourteen tool calls, every one failed, and
// she reported the quiz submitted.
//
// Worse than the failure it followed — a failure rate is visible and recoverable,
// a confident false completion is neither. The user goes to bed believing it is
// done.
func TestSheIsNotAllowedToClaimSuccessAfterAFullyFailedExchange(t *testing.T) {
	p := &scriptedProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "1", Name: "click"}}},
		{ToolCalls: []llm.ToolCall{{ID: "2", Name: "click"}}},
		{ToolCalls: []llm.ToolCall{{ID: "3", Name: "click"}}},
		// The lie she actually told.
		{Text: "Self-Quiz Unit 5 is submitted. I'm moving on to Unit 6 now."},
		// What she says once the facts are put in front of her.
		{Text: "I couldn't submit Unit 5 — the Submit button never took the click. " +
			"It's still sitting on the confirmation page."},
	}}
	a, store := newTestAgent(t, p)
	a.Skills.Register(skills.Skill{
		Tool: llm.Tool{Name: "click", Params: llm.ObjectSchema(nil)},
		Handler: func(context.Context, map[string]any) (string, error) {
			return "", errors.New(`no element matches "Submit Quiz"`)
		},
	})

	res, err := a.Ask(context.Background(), "submit self-quiz unit 5")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Reply, "is submitted") {
		t.Fatalf("she claimed success after an exchange in which nothing worked: %q", res.Reply)
	}
	if !strings.Contains(res.Reply, "couldn't submit") {
		t.Errorf("the corrected answer was not used: %q", res.Reply)
	}

	// The archive must hold the truth, not the claim — the next turn reads it.
	turns := store.Turns()
	last := turns[len(turns)-1]
	if strings.Contains(last.Text, "is submitted. I'm moving on") {
		t.Errorf("the false claim was archived: %q", last.Text)
	}

	// And she was told the facts, not merely asked to try again.
	sys := p.lastReq.System
	for _, want := range []string{"EVERY ONE of them failed", "must NOT report any part of it as done"} {
		if !strings.Contains(sys, want) {
			t.Errorf("the correction does not state the fact (%q):\n%s", want, sys)
		}
	}
	if len(p.lastReq.Tools) != 0 {
		t.Error("the correction offered tools; the question is what to tell the user, not what to try next")
	}
}

// One success is enough to make the exchange ordinary. The check must not fire
// on partial failure, or every recovered turn pays for an extra call.
func TestOneSuccessMakesTheExchangeOrdinary(t *testing.T) {
	p := &scriptedProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "1", Name: "flaky"}}},
		{ToolCalls: []llm.ToolCall{{ID: "2", Name: "flaky"}}},
		{Text: "Done — submitted on the second try."},
	}}
	a, _ := newTestAgent(t, p)
	n := 0
	a.Skills.Register(skills.Skill{
		Tool: llm.Tool{Name: "flaky", Params: llm.ObjectSchema(nil)},
		Handler: func(context.Context, map[string]any) (string, error) {
			if n++; n == 1 {
				return "", errors.New("missed")
			}
			return "Clicked Submit. Quiz submitted.", nil
		},
	})

	res, err := a.Ask(context.Background(), "submit it")
	if err != nil {
		t.Fatal(err)
	}
	if res.Reply != "Done — submitted on the second try." {
		t.Errorf("a genuine success was second-guessed: %q", res.Reply)
	}
	if p.calls != 3 {
		t.Errorf("made %d model calls, want 3 — no correction should have fired", p.calls)
	}
}

// A conversational turn ran no tools, so there is nothing to have succeeded and
// nothing to correct.
func TestAConversationalTurnIsNotChallenged(t *testing.T) {
	p := &scriptedProvider{responses: []llm.Response{{Text: "It's about half past ten."}}}
	a, _ := newTestAgent(t, p)

	res, err := a.Ask(context.Background(), "what's the time")
	if err != nil {
		t.Fatal(err)
	}
	if res.Reply != "It's about half past ten." {
		t.Errorf("a plain answer was challenged: %q", res.Reply)
	}
	if p.calls != 1 {
		t.Errorf("made %d model calls, want 1", p.calls)
	}
}

// A refusal is a failure for this purpose: the breaker declined the call, so
// nothing touched the world. Six refusals and eight errors is still nothing done.
func TestRefusalsCountAsNothingDone(t *testing.T) {
	var work trail
	work.add(step{tool: "browser_click_text", output: "ERROR: nope", failed: true})
	work.add(step{tool: "browser_click_text", output: "REFUSED: already failed twice", failed: true})

	attempts, none := nothingWorked(&work)
	if !none || attempts != 2 {
		t.Fatalf("nothingWorked = (%d, %v), want (2, true)", attempts, none)
	}

	work.add(step{tool: "browser_read", output: "Quiz Results | 8 out of 10"})
	if _, none := nothingWorked(&work); none {
		t.Error("a successful read did not clear the condition")
	}

	var empty trail
	if _, none := nothingWorked(&empty); none {
		t.Error("an exchange that ran no tools was treated as a failed one")
	}
}

// If the correcting call cannot be made, her original answer is better than
// nothing — the network failing must not cost the user the whole reply.
func TestACorrectionThatCannotBeMadeKeepsTheOriginal(t *testing.T) {
	p := &scriptedProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "1", Name: "click"}}},
		{Text: "All done."},
	}}
	a, _ := newTestAgent(t, p)
	a.Skills.Register(skills.Skill{
		Tool: llm.Tool{Name: "click", Params: llm.ObjectSchema(nil)},
		Handler: func(context.Context, map[string]any) (string, error) {
			return "", errors.New("missed")
		},
	})
	// The provider fails exactly when the correction is attempted.
	p.failAfter = 2

	res, err := a.Ask(context.Background(), "click it")
	if err != nil {
		t.Fatal(err)
	}
	if res.Reply != "All done." {
		t.Errorf("a failed correction lost the reply entirely: %q", res.Reply)
	}
}

// The brief has to say what happened, not scold. She is not lying on purpose —
// she loses track across forty rounds of near-identical error text.
func TestTheBriefStatesFactsAndNamesTheGoal(t *testing.T) {
	b := truthBrief("submit self-quiz unit 5 for CS 3340", 14)
	for _, want := range []string{
		"14 tool calls", "EVERY ONE of them failed",
		"submit self-quiz unit 5 for CS 3340",
		"what state things are really in",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("the brief omits %q:\n%s", want, b)
		}
	}
	// Work genuinely finished in an earlier exchange must still be reportable.
	if !strings.Contains(b, "finished EARLIER in the conversation") {
		t.Errorf("the brief forbids reporting work that really was done:\n%s", b)
	}
}

// The free half: on any round where nothing has worked, the fact rides in the
// tail so she writes with it in front of her — no extra call, and as much a
// prompt to change approach as to report honestly.
func TestTheFactRidesInTheTailWithoutAnExtraCall(t *testing.T) {
	p := &scriptedProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "1", Name: "click"}}},
		{ToolCalls: []llm.ToolCall{{ID: "2", Name: "click"}}},
		{Text: "I can't get that button to take a click."},
	}}
	a, _ := newTestAgent(t, p)
	a.Skills.Register(skills.Skill{
		Tool: llm.Tool{Name: "click", Params: llm.ObjectSchema(nil)},
		Handler: func(context.Context, map[string]any) (string, error) {
			return "", errors.New("missed")
		},
	})

	res, err := a.Ask(context.Background(), "click submit")
	if err != nil {
		t.Fatal(err)
	}
	if p.calls != 3 {
		t.Errorf("made %d model calls, want 3 — two failures is not severe enough to re-ask", p.calls)
	}
	if res.Reply != "I can't get that button to take a click." {
		t.Errorf("an honest answer was second-guessed: %q", res.Reply)
	}

	// The note was present on the last request, at the end.
	last := p.lastReq.Messages[len(p.lastReq.Messages)-1]
	if !strings.Contains(last.Text, "not one of them has worked") {
		t.Errorf("the fact was not stated in the tail: %q", last.Text)
	}
	if !strings.Contains(last.Text, "change approach") {
		t.Errorf("the note does not offer the mid-loop reading: %q", last.Text)
	}
}

// It must not accumulate: one note per request, not one per round.
func TestTheTailNoteDoesNotAccumulate(t *testing.T) {
	p := &scriptedProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "1", Name: "click"}}},
		{ToolCalls: []llm.ToolCall{{ID: "2", Name: "click"}}},
		{ToolCalls: []llm.ToolCall{{ID: "3", Name: "click"}}},
		{Text: "blocked"},
		{Text: "blocked, and here is what stopped me"},
	}}
	a, _ := newTestAgent(t, p)
	a.Skills.Register(skills.Skill{
		Tool: llm.Tool{Name: "click", Params: llm.ObjectSchema(nil)},
		Handler: func(context.Context, map[string]any) (string, error) {
			return "", errors.New("missed")
		},
	})

	if _, err := a.Ask(context.Background(), "click submit"); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range p.lastReq.Messages {
		if strings.Contains(m.Text, "Note on this exchange so far") {
			n++
		}
	}
	if n > 1 {
		t.Errorf("the note appears %d times; it accumulates once per round", n)
	}
}

// A failure worth an engineer's attention has to reach one, with the evidence
// intact. Every diagnosis in this codebase came from exactly this material, and
// until now it existed only in the log and only while somebody was reading it.
func TestABadExchangeIsReportedWithItsEvidence(t *testing.T) {
	p := &scriptedProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "1", Name: "click"}}},
		{ToolCalls: []llm.ToolCall{{ID: "2", Name: "click"}}},
		{ToolCalls: []llm.ToolCall{{ID: "3", Name: "click"}}},
		{Text: "I couldn't get that to work."},
		{Text: "I couldn't get that to work."},
	}}
	a, _ := newTestAgent(t, p)
	a.Skills.Register(skills.Skill{
		Tool: llm.Tool{Name: "click", Params: llm.ObjectSchema(nil)},
		Handler: func(context.Context, map[string]any) (string, error) {
			return "", errors.New(`text is required`)
		},
	})

	got := make(chan Failure, 4)
	a.OnFailure = func(f Failure) { got <- f }

	if _, err := a.Ask(context.Background(), "submit self-quiz unit 5"); err != nil {
		t.Fatal(err)
	}

	select {
	case f := <-got:
		if f.Kind != "nothing-worked" {
			t.Errorf("kind = %q", f.Kind)
		}
		if f.Goal != "submit self-quiz unit 5" {
			t.Errorf("the report lost what was asked: %q", f.Goal)
		}
		if f.Attempts != 3 {
			t.Errorf("attempts = %d, want 3", f.Attempts)
		}
		if len(f.Failures) == 0 || !strings.Contains(f.Failures[0], "text is required") {
			t.Errorf("the report lost the error text: %v", f.Failures)
		}
		if !strings.Contains(f.Trail, "click") {
			t.Errorf("the report lost the trail: %q", f.Trail)
		}
		if f.Exchange == "" {
			t.Error("the report cannot be correlated with the telemetry")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an exchange where nothing worked was never reported")
	}
}

// An exchange that went fine must not file anything, or the journal fills with
// non-events and stops being read.
func TestAGoodExchangeIsNotReported(t *testing.T) {
	p := &scriptedProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "1", Name: "click"}}},
		{Text: "Done."},
	}}
	a, _ := newTestAgent(t, p)
	a.Skills.Register(skills.Skill{
		Tool:    llm.Tool{Name: "click", Params: llm.ObjectSchema(nil)},
		Handler: func(context.Context, map[string]any) (string, error) { return "clicked", nil },
	})

	var filed int32
	a.OnFailure = func(Failure) { atomic.AddInt32(&filed, 1) }

	if _, err := a.Ask(context.Background(), "click it"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := atomic.LoadInt32(&filed); n != 0 {
		t.Errorf("a successful exchange filed %d reports", n)
	}
}
