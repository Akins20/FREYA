package skills

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Akins20/FREYA/internal/llm"
)

func boolp(b bool) *bool { return &b }

// The no-change note is the whole point of the type: an action that ran without
// error and moved nothing is the failure the model cannot see, because it
// arrives looking exactly like success.
func TestOutcomeRendersTheUncomfortableTruth(t *testing.T) {
	got := Outcome{Text: "Clicked \"Submit\".", Changed: boolp(false)}.Render()
	if !strings.Contains(got, "Nothing observably changed") {
		t.Fatalf("a no-op click rendered as plain success: %q", got)
	}

	// A change, or an unknown, must not carry the warning.
	if s := (Outcome{Text: "Clicked.", Changed: boolp(true)}).Render(); strings.Contains(s, "Nothing observably") {
		t.Errorf("a real change was reported as a no-op: %q", s)
	}
	if s := (Outcome{Text: "Read the page."}).Render(); strings.Contains(s, "Nothing observably") {
		t.Errorf("an unknown was reported as a no-op: %q", s)
	}

	// Evidence — what actually happened — rides alongside the request.
	ev := Outcome{Text: `Clicked "Submit".`}.WithEvidence("The element it actually hit reads %q.", "Submit and add another")
	if !strings.Contains(ev.Render(), "Submit and add another") {
		t.Errorf("evidence was dropped: %q", ev.Render())
	}
}

// A failure should hand back the state needed to succeed.
func TestExecuteAttachesAffordancesOnFailure(t *testing.T) {
	r := New()
	r.Register(Skill{
		Tool:    llm.Tool{Name: "pick", Description: "d", Params: llm.ObjectSchema(nil)},
		Handler: func(context.Context, map[string]any) (string, error) { return "", errors.New("no such option") },
		Affordances: func(context.Context, map[string]any) []string {
			return []string{"Basic Accounting", "Macroeconomics"}
		},
	})

	_, err := r.Execute(context.Background(), "pick", nil)
	if err == nil {
		t.Fatal("expected the failure to propagate")
	}
	if !strings.Contains(err.Error(), "Macroeconomics") {
		t.Fatalf("the failure did not carry what was available: %v", err)
	}
}

// A mutating skill whose world fingerprint is unchanged is reported as having
// done nothing, without the skill itself having to check.
func TestExecuteVerifiesMutatingSkills(t *testing.T) {
	world := "before"
	r := New()
	r.Register(Skill{
		Tool:    llm.Tool{Name: "poke", Description: "d", Params: llm.ObjectSchema(nil)},
		Handler: func(context.Context, map[string]any) (string, error) { return "Poked it.", nil },
		Mutates: true,
		Observe: func(context.Context, map[string]any) string { return world },
	})

	// Nothing moves: the result says so.
	out, err := r.Execute(context.Background(), "poke", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Nothing observably changed") {
		t.Fatalf("an ineffective action reported clean success: %q", out)
	}

	// Now make the action actually change the world.
	r.Register(Skill{
		Tool: llm.Tool{Name: "poke", Description: "d", Params: llm.ObjectSchema(nil)},
		Handler: func(context.Context, map[string]any) (string, error) {
			world = "after"
			return "Poked it.", nil
		},
		Mutates: true,
		Observe: func(context.Context, map[string]any) string { return world },
	})
	out, err = r.Execute(context.Background(), "poke", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Nothing observably changed") {
		t.Fatalf("a real change was reported as a no-op: %q", out)
	}
}

// A skill that determines for itself whether it changed anything outranks the
// fingerprint — it knows more than a generic before/after can.
func TestSkillOwnChangedVerdictWins(t *testing.T) {
	r := New()
	r.Register(Skill{
		Tool:    llm.Tool{Name: "act", Description: "d", Params: llm.ObjectSchema(nil)},
		Mutates: true,
		Observe: func(context.Context, map[string]any) string { return "identical" },
		Act: func(context.Context, map[string]any) (Outcome, error) {
			return Changedf(true, "It landed, and I watched it land."), nil
		},
	})
	out, err := r.Execute(context.Background(), "act", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Nothing observably changed") {
		t.Fatalf("the skill's own verdict was overridden: %q", out)
	}
}

// A read must not pay for verification it does not need.
func TestNonMutatingSkillIsNotObserved(t *testing.T) {
	observed := 0
	r := New()
	r.Register(Skill{
		Tool:    llm.Tool{Name: "look", Description: "d", Params: llm.ObjectSchema(nil)},
		Handler: func(context.Context, map[string]any) (string, error) { return "text", nil },
		Observe: func(context.Context, map[string]any) string { observed++; return "x" },
	})
	if _, err := r.Execute(context.Background(), "look", nil); err != nil {
		t.Fatal(err)
	}
	if observed != 0 {
		t.Fatalf("a read sampled the world %d times; it should not sample at all", observed)
	}
}
