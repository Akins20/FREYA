package claude

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		task string
		want Complexity
	}{
		// Lookups and readings.
		{"what does this function do?", Simple},
		{"list the files in internal/", Simple},
		{"read config.go and tell me the default port", Simple},
		{"show me the git log", Simple},

		// Ordinary single-place work.
		{"add a timeout parameter to the HTTP client in client.go and update its callers", Moderate},
		{"write a test for the parseDue function covering relative offsets", Moderate},

		// Work that spans or requires holding a system in mind.
		{"refactor the storage layer across the entire codebase to use interfaces", Hard},
		{"debug the intermittent race condition in the session manager", Hard},
		{"audit this repository for security vulnerabilities", Hard},
		{"/review", Hard},
		{"/security-review", Hard},
	}
	for _, c := range cases {
		if got := Classify(c.task); got != c.want {
			t.Errorf("Classify(%q) = %s, want %s", c.task, got, c.want)
		}
	}
}

func TestComplexityPicksSensibleModels(t *testing.T) {
	if Simple.Model() != "haiku" {
		t.Errorf("simple -> %s", Simple.Model())
	}
	if Hard.Model() != "opus" {
		t.Errorf("hard -> %s", Hard.Model())
	}
	// Budget should scale with expected work, not be uniform.
	if Simple.Budget() >= Hard.Budget() {
		t.Error("a simple task is allowed as much budget as a hard one")
	}
}

func TestExplicitChoiceAlwaysWins(t *testing.T) {
	p := PlanFor("what time is it?", "opus", "max", 9.0)
	if p.Model != "opus" || p.Effort != "max" || p.BudgetUSD != 9.0 {
		t.Errorf("explicit settings overridden: %+v", p)
	}
	if p.Reason == "" {
		t.Error("no explanation of the choice")
	}
}

// TestResumeStaysOnItsModel is the important one. Switching models mid-thread
// hands reasoning produced by one model to another that cannot reconstruct the
// premises behind it.
func TestResumeStaysOnItsModel(t *testing.T) {
	// A trivial follow-up would classify as simple and drop to haiku.
	p := PlanForResume("opus", "and what about the other one?", "", "", 0)
	if p.Model != "opus" {
		t.Errorf("resumed thread switched to %s, losing the session's reasoning", p.Model)
	}

	// An explicit override still wins.
	p = PlanForResume("opus", "and what about the other one?", "haiku", "", 0)
	if p.Model != "haiku" {
		t.Errorf("explicit override ignored on resume: %s", p.Model)
	}

	// With no prior model recorded, fall back to classification.
	p = PlanForResume("", "refactor the entire codebase across all packages", "", "", 0)
	if p.Model != "opus" {
		t.Errorf("unknown prior model did not fall back to classification: %s", p.Model)
	}
}

func TestAliasFor(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-8[1m]":       "opus",
		"claude-sonnet-5":           "sonnet",
		"claude-haiku-4-5-20251001": "haiku",
		"something-else":            "",
	}
	for full, want := range cases {
		if got := aliasFor(full); got != want {
			t.Errorf("aliasFor(%q) = %q, want %q", full, got, want)
		}
	}
}
