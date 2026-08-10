package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Akins20/FREYA/internal/llm"
	"github.com/Akins20/FREYA/internal/skills"
)

// The measured failure: two quizzes submitted, a third underway, and the reply
// was "I couldn't finish". The salvage call did not fail — her telemetry shows it
// returned cleanly. What was wrong was the question it was asked, so this pins
// the question.
func TestTheRoundCapAsksForProgressNotAnApology(t *testing.T) {
	brief := roundCapBrief("do my self-quizzes 1, 2 and 3 for CS 3340", true)

	if !strings.Contains(brief, "self-quizzes 1, 2 and 3") {
		t.Error("the brief never states what was asked for, so the report cannot be about it")
	}
	for _, want := range []string{"finished", "in the middle of", "what is left"} {
		if !strings.Contains(strings.ToLower(brief), want) {
			t.Errorf("the brief does not ask what is %q", want)
		}
	}
	if !strings.Contains(brief, "I couldn't finish") {
		t.Error("the brief does not rule out the exact non-answer that was measured")
	}
	if strings.Contains(brief, "say briefly what you still need") {
		t.Error("the old instruction — which invited the non-answer — is still there")
	}
}

// The heuristic this replaced was measurably inverted: it rejected honest
// specific reports and accepted vague ones. Both directions are pinned, using
// spoken-register prose — which is what the brief asks for, and what closed the
// old "contains a digit" escape hatch.
func TestConcretenessIsJudgedByWhatTheReplySaysAboutTheWorld(t *testing.T) {
	var work trail
	work.add(step{tool: "browser_read", output: realReadResults})
	work.add(step{tool: "browser_read", output: realRead})

	reports := []string{
		// The exact sentence the phrase list rejected, numbers spelled out.
		"Quizzes one and two are submitted; I couldn't finish the third.",
		"Both self-quizzes went in fine. I ran out of room partway through unit three, " +
			"on question four.",
		"Quiz results came back at 8 out of 10, and the third is open on question 4.",
	}
	for _, r := range reports {
		if nonAnswer(r, &work) {
			t.Errorf("a specific report was thrown away as a dodge: %q", r)
		}
	}

	dodges := []string{
		"I made some progress but ran into issues.",
		"I got through most of it.",
		"I hit the limit before I could wrap this up.",
		"I didn't get through everything, sorry.",
		"",
	}
	for _, d := range dodges {
		if !nonAnswer(d, &work) {
			t.Errorf("a content-free reply was accepted as a report: %q", d)
		}
	}

	// The decision must not turn on orthography. The same sentence in words and
	// in numerals has to be judged the same way.
	words := "Quizzes one and two are submitted; I couldn't finish the third."
	digits := "Quizzes 1 and 2 are submitted; I couldn't finish the third."
	if nonAnswer(words, &work) != nonAnswer(digits, &work) {
		t.Error("the verdict flips on numeral form alone, which is what made the old rule inverted")
	}
}

// The whole path, through a real agent.
func TestHittingTheCapProducesAGoalRelativeReport(t *testing.T) {
	responses := capLoop(maxToolRounds)
	responses = append(responses, llm.Response{
		Text: "Quizzes 1 and 2 are submitted. Quiz 3 is open on question 4 of 10.",
	})
	p := &scriptedProvider{responses: responses}
	a, _ := newTestAgent(t, p)
	registerQuizTool(a)

	res, err := a.Ask(context.Background(), "do my self-quizzes 1, 2 and 3 for CS 3340")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Reply, "Quizzes 1 and 2 are submitted") {
		t.Errorf("the model's progress report was discarded: %q", res.Reply)
	}

	sys := p.lastReq.System
	if !strings.Contains(sys, "self-quizzes 1, 2 and 3 for CS 3340") {
		t.Error("the salvage call never told the model what was asked for")
	}
	if !strings.Contains(sys, "progress report") {
		t.Error("the salvage call asked for something other than a progress report")
	}
	if len(p.lastReq.Tools) != 0 {
		t.Error("tools were still offered at the cap, so she can keep calling them")
	}
	last := p.lastReq.Messages[len(p.lastReq.Messages)-1]
	if !strings.Contains(last.Text, "Chronological record") ||
		!strings.Contains(last.Text, "Your score: 8 out of 10") {
		t.Errorf("the record was not handed to the model, so its report has nothing to be "+
			"concrete about: %q", last.Text)
	}
}

// A dodging first attempt is challenged — and the challenge goes in the message
// tail, not the system block, so the second call extends the first call's cached
// prefix instead of re-billing ~180k tokens at the front of the request.
func TestADodgingReportIsRetriedWithoutBreakingTheCache(t *testing.T) {
	responses := capLoop(maxToolRounds)
	responses = append(responses,
		llm.Response{Text: "I got through most of it."},
		llm.Response{Text: "Quiz results came back 8 out of 10; the third is on question 4."})
	p := &scriptedProvider{responses: responses}
	a, _ := newTestAgent(t, p)
	registerQuizTool(a)

	firstSystem := ""
	p.onCall = func(req llm.Request) {
		if len(req.Tools) == 0 && firstSystem == "" {
			firstSystem = req.System
		}
	}

	res, err := a.Ask(context.Background(), "do my quizzes")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Reply, "most of it") {
		t.Errorf("a content-free reply was shipped without being challenged: %q", res.Reply)
	}
	if !strings.Contains(res.Reply, "8 out of 10") {
		t.Errorf("the retried report was not used: %q", res.Reply)
	}
	if p.lastReq.System != firstSystem {
		t.Error("the retry changed the system block, which sits ahead of every message — " +
			"the whole prompt is re-billed uncached")
	}
	last := p.lastReq.Messages[len(p.lastReq.Messages)-1]
	if !strings.Contains(last.Text, "said nothing about what was actually achieved") {
		t.Errorf("the retry never told the model why it was being asked again: %q", last.Text)
	}
}

// When both attempts dodge, shipping the first one archives the exact string this
// change exists to eliminate — after paying twice to detect it, with a
// deterministic account sitting ready and costing nothing.
func TestBothAttemptsDodgingFallsBackToTheAccount(t *testing.T) {
	responses := capLoop(maxToolRounds)
	responses = append(responses,
		llm.Response{Text: "I couldn't finish that."},
		llm.Response{Text: "I was unable to complete this."})
	p := &scriptedProvider{responses: responses}
	a, store := newTestAgent(t, p)
	registerQuizTool(a)

	res, err := a.Ask(context.Background(), "do my quizzes")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Reply, "couldn't finish") || strings.Contains(res.Reply, "unable to complete") {
		t.Errorf("both attempts dodged and the dodge was shipped anyway: %q", res.Reply)
	}
	if !strings.Contains(res.Reply, "Your score: 8 out of 10") {
		t.Errorf("the free account of the real work was never reached: %q", res.Reply)
	}
	// And it says why there is no proper summary, rather than blaming the network.
	if !strings.Contains(res.Reply, "told you nothing") {
		t.Errorf("the reply does not explain why it is a raw account: %q", res.Reply)
	}
	turns := store.Turns()
	if last := turns[len(turns)-1]; !strings.Contains(last.Text, "Your score") {
		t.Errorf("the archive kept the dodge instead of the account: %q", last.Text)
	}
}

// An exchange in which everything failed is entitled to say so, and must never be
// pushed to claim otherwise.
func TestATotalFailureIsReportedHonestly(t *testing.T) {
	responses := capLoop(maxToolRounds)
	responses = append(responses, llm.Response{Text: "I couldn't do it — the Start button never appeared."})
	p := &scriptedProvider{responses: responses}
	a, _ := newTestAgent(t, p)
	a.Skills.Register(skills.Skill{
		Tool: llm.Tool{Name: "do_quiz", Params: llm.ObjectSchema(nil)},
		Handler: func(context.Context, map[string]any) (string, error) {
			return "", errNoStartButton
		},
	})

	res, err := a.Ask(context.Background(), "start the quiz")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Reply, "Start button never appeared") {
		t.Errorf("an honest report of failure was retried away: %q", res.Reply)
	}
	if !strings.Contains(p.lastReq.System, "Do not manufacture progress") {
		t.Error("the brief told a model that achieved nothing to say what its work amounted to")
	}
}

var errNoStartButton = &simpleErr{"no element matches \"Start Quiz\""}

type simpleErr struct{ s string }

func (e *simpleErr) Error() string { return e.s }

func capLoop(n int) []llm.Response {
	out := make([]llm.Response, n)
	for i := range out {
		out[i] = llm.Response{ToolCalls: []llm.ToolCall{{ID: "c", Name: "do_quiz"}}}
	}
	return out
}

func registerQuizTool(a *Agent) {
	n := 0
	a.Skills.Register(skills.Skill{
		Tool: llm.Tool{Name: "do_quiz", Params: llm.ObjectSchema(nil)},
		Handler: func(context.Context, map[string]any) (string, error) {
			n++
			if n%3 == 0 {
				return realReadResults, nil
			}
			return realRead, nil
		},
	})
}
