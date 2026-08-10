package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Akins20/FREYA/internal/skills"
)

// A plan left open in one conversation must not block the answer to the next.
//
// The scope — and so the plan inside it — is built once at startup and lives for
// the whole process. The ledger already learned this the hard way and has
// BeginExchange for it; its own comment records the budget being spent within
// minutes of the daemon starting and never coming back, so every later request
// was refused on the strength of two guesses made in an unrelated conversation.
//
// The plan shipped without the equivalent. So: she plans five steps on Monday,
// leaves one open, and on Tuesday is refused an answer about the weather and
// told to go and finish it.
func TestAStalePlanDoesNotBlockAnUnrelatedAnswer(t *testing.T) {
	scope := skills.NewScope(skills.NewWorkspace(t.TempDir()), "", "")
	ctx := skills.WithScope(context.Background(), scope)
	plan := scope.Plan()

	// Monday: a plan, worked, one step left open when the exchange ends.
	plan.BeginExchange()
	plan.Set([]string{"find three plumbers", "compare their quotes", "book one"})
	if err := plan.Mark(1, skills.StepDone, "", ""); err != nil {
		t.Fatal(err)
	}
	if ends := stillOpen(ctx, &trail{}); len(ends) != 2 {
		t.Fatalf("precondition: the gate should hold this exchange, got %v", ends)
	}

	// Tuesday: a completely different question, and she touches no plan at all.
	plan.BeginExchange()
	if ends := stillOpen(ctx, &trail{}); len(ends) != 0 {
		t.Errorf("a plan from an earlier conversation is blocking this answer: %v\n"+
			"She would be told to go and finish work the user has moved on from.", ends)
	}

	// But the list is not thrown away — "carry on with that" has to still work,
	// and the moment she touches it again the gate applies again.
	if err := plan.Mark(2, skills.StepDoing, "", ""); err != nil {
		t.Fatalf("the earlier plan was destroyed rather than parked: %v", err)
	}
	ends := stillOpen(ctx, &trail{})
	if len(ends) != 2 {
		t.Fatalf("resuming the plan did not re-arm the gate: %v", ends)
	}
	if !strings.Contains(strings.Join(ends, " "), "compare their quotes") {
		t.Errorf("the resumed plan lost its steps: %v", ends)
	}
}

// Her own server is not a source, and neither is a link the user just handed
// her.
//
// unopenedSources compares URLs in the answer against the pages she actually
// fetched. Two kinds of URL are in an answer without ever having been fetched
// and are entirely legitimate: the localhost address of a site she has just
// built and served, and a link the user typed in the request she is answering.
// Both would be reported as citations she invented.
func TestHerOwnServerAndTheUsersOwnLinkAreNotFakedCitations(t *testing.T) {
	scope := skills.NewScope(skills.NewWorkspace(t.TempDir()), "", "")
	ctx := skills.WithScope(context.Background(), scope)
	scope.Ledger().Retrieved("https://example.com/spec")

	// A build that also read one page for reference, which is what arms the check.
	w := &trail{}
	w.add(step{tool: "web_fetch", output: "…the spec…"})
	w.add(step{tool: "serve", output: "Serving /w/shop at http://localhost:38371"})

	input := "make it match https://competitor.example/pricing please"
	reply := "Done — it's at http://localhost:38371, built to match " +
		"https://competitor.example/pricing and following https://example.com/spec."

	bad := unopenedSources(ctx, input, reply, w)
	for _, wrong := range []string{"localhost", "competitor.example"} {
		for _, b := range bad {
			if strings.Contains(b, wrong) {
				t.Errorf("%s was reported as a citation she never opened: %q", wrong, b)
			}
		}
	}
	if len(bad) != 0 {
		t.Errorf("nothing here is a faked citation, got %v", bad)
	}

	// And a genuinely invented one still gets caught, or this has just disabled
	// the check.
	bad = unopenedSources(ctx, input, reply+" See also https://madeup.example/report.", w)
	if len(bad) != 1 || !strings.Contains(bad[0], "madeup") {
		t.Errorf("the invented source was not caught: %v", bad)
	}
}
