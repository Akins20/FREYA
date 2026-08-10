package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// planFixture builds a registry with plan_set and plan_step registered against a
// fresh plan, and returns the plan so a test can read it directly rather than
// parsing the rendered list back.
func planFixture(t *testing.T, steps ...string) (*Registry, context.Context, *Plan) {
	t.Helper()
	plan := NewPlan()
	r := New()
	RegisterPlan(r)
	ctx := WithScope(context.Background(), NewScopeWithPlan(NewWorkspace(t.TempDir()), plan))
	if len(steps) > 0 {
		joined := strings.Join(steps, "\n")
		if _, err := r.Execute(ctx, "plan_set", map[string]any{"steps": joined}); err != nil {
			t.Fatal(err)
		}
	}
	return r, ctx, plan
}

// at reads one step, one-based, for assertions.
func (p *Plan) at(n int) Step {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.steps[n]
}

// writeInScope puts a file where the plan will look for it, so a step that names
// what it produces can legitimately be completed.
func writeInScope(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ScopeFrom(ctx).Dir(), name), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Finishing one step and starting the next is one event, and it was costing two
// calls.
//
// Measured across five live builds: plan_step was 7 of 18 tool calls, 8 of 14,
// 8 of 18, 12 of 30 and 6 of 14 — between a third and a half of everything she
// did — and the pairs were consecutive rounds doing exactly this.
func TestSeveralStepsMoveInOneCall(t *testing.T) {
	r, ctx, plan := planFixture(t, "write index.html", "write about.html", "check the links")
	writeInScope(t, ctx, "index.html")

	out, err := r.Execute(ctx, "plan_step", map[string]any{"steps": "1:done, 2:doing"})
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.at(0).State; got != StepDone {
		t.Errorf("step 1 is %q, want done", got)
	}
	if got := plan.at(1).State; got != StepDoing {
		t.Errorf("step 2 is %q, want doing", got)
	}
	if !strings.Contains(out, "about.html") {
		t.Errorf("the reply is not the plan: %s", out)
	}
}

// Batching must not become a way past the one check there is. A step that names
// a file it will produce still cannot be completed while that file is absent,
// and the whole call fails rather than landing the moves before it.
func TestABatchCannotTickOffAFileThatIsNotThere(t *testing.T) {
	r, ctx, plan := planFixture(t, "write index.html", "write about.html")

	if _, err := r.Execute(ctx, "plan_step", map[string]any{"steps": "1:done, 2:doing"}); err == nil {
		t.Fatal("a batch completed a step whose file was never written")
	}
	if got := plan.at(0).State; got == StepDone {
		t.Error("step 1 was marked done anyway")
	}
}

// The spellings a model actually reaches for all have to land, because the one
// that does not is a wasted round and a retry.
func TestTheBatchedFormIsForgivingAboutPunctuation(t *testing.T) {
	for _, form := range []string{
		"1:done, 2:doing",
		"1: done , 2: doing",
		"step 1: done, step 2: doing",
		"1=done,2=doing",
		"1->done; 2->doing",
		"1 => done, 2 => doing",
	} {
		r, ctx, plan := planFixture(t, "one", "two")
		if _, err := r.Execute(ctx, "plan_step", map[string]any{"steps": form}); err != nil {
			t.Errorf("%q: %v", form, err)
			continue
		}
		if plan.at(0).State != StepDone || plan.at(1).State != StepDoing {
			t.Errorf("%q left the plan at %q/%q", form, plan.at(0).State, plan.at(1).State)
		}
	}
}

// One step at a time still works. A schema that forces a list to move one thing
// is the same overhead somewhere else.
func TestTheSingleFormStillWorks(t *testing.T) {
	r, ctx, plan := planFixture(t, "one", "two")
	if _, err := r.Execute(ctx, "plan_step", map[string]any{"step": 2, "state": "done"}); err != nil {
		t.Fatal(err)
	}
	if plan.at(1).State != StepDone {
		t.Errorf("step 2 is %q, want done", plan.at(1).State)
	}
}

// A note on a batched call belongs to the last move, not stamped across every
// one of them — that would be a record of something that did not happen.
func TestANoteOnABatchLandsOnce(t *testing.T) {
	r, ctx, plan := planFixture(t, "one", "two")
	_, err := r.Execute(ctx, "plan_step", map[string]any{
		"steps": "1:done, 2:dropped", "note": "the client cut this page",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.at(0).Note != "" {
		t.Errorf("the note was stamped on step 1 too: %q", plan.at(0).Note)
	}
	if plan.at(1).Note != "the client cut this page" {
		t.Errorf("step 2 note is %q", plan.at(1).Note)
	}
}

// Marking a step into the state it already holds is not an error. She
// re-confirms her own bookkeeping constantly, and refusing it spends a round
// teaching her not to.
func TestMarkingAStepTwiceIsFine(t *testing.T) {
	r, ctx, plan := planFixture(t, "one")
	for i := range 3 {
		if _, err := r.Execute(ctx, "plan_step", map[string]any{"step": 1, "state": "done"}); err != nil {
			t.Fatalf("mark %d: %v", i+1, err)
		}
	}
	if plan.at(0).State != StepDone {
		t.Errorf("step 1 is %q after three marks", plan.at(0).State)
	}
}

// Garbage comes back as garbage. A move it cannot read must not be silently
// dropped, or half a batch lands and the plan says the other half never moved.
func TestAnUnreadableMoveIsRefused(t *testing.T) {
	for _, bad := range []string{"1:finished", "one:done", "1:done, banana", "  ", "2"} {
		r, ctx, _ := planFixture(t, "one", "two")
		if _, err := r.Execute(ctx, "plan_step", map[string]any{"steps": bad}); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// Naming no step at all has to say so rather than move step zero.
func TestAMoveWithNoStepIsRefused(t *testing.T) {
	r, ctx, _ := planFixture(t, "one")
	if _, err := r.Execute(ctx, "plan_step", map[string]any{"state": "done"}); err == nil {
		t.Error("a call naming no step was accepted")
	}
}
