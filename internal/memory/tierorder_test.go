package memory

import (
	"fmt"
	"strings"
	"testing"
)

// The order of the tiers is the whole cache economy, and nothing asserted it.
//
// Gemini caches stable prompt PREFIXES. The architecture is ordered
// most-stable-first for that single reason, and the measured hit rate is 79%
// across 196M input tokens. Move one volatile tier above a stable one and every
// request after it misses, quadrupling the input bill — with nothing failing, no
// error, and no test going red. The existing builder test asserts that each tier
// is PRESENT, which the clock landing inside the identity block would satisfy
// perfectly.
//
// This is the invariant the architecture notes call load-bearing, and it was the
// one thing about it nobody checked.
func TestTiersAreOrderedMostStableFirst(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for i := 0; i < 30; i++ {
		if _, err := s.AppendTurn(Turn{Role: "user",
			Text: fmt.Sprintf("message %d about compilers and parsers", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutFact(Fact{Key: "loose-fact", Text: "Prefers dark mode"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddEpisode(Episode{Summary: "Talked at length about compilers"}); err != nil {
		t.Fatal(err)
	}

	b := NewContextBuilder(s, BuildIndex(s), "PERSONA-MARKER")
	// The catalogue is optional, and its position is the point of this test — it
	// sits directly after identity because it is as static as identity is, and
	// ahead of facts, which grow. Set it so the ordering is actually exercised
	// rather than skipped.
	b.Catalogue = "browser_open — open a page"
	system, _, _ := b.Build("tell me about compilers")

	// Most stable first. Each name is what the section is called in the prompt.
	tiers := []struct{ name, marker string }{
		{"persona (identity)", "PERSONA-MARKER"},
		{"capabilities", "# What I can do"},
		{"facts", "# What I know"},
		{"episodes", "# Earlier sessions"},
		{"the clock", "# Right now"},
	}

	last, lastName := -1, ""
	for _, tier := range tiers {
		at := strings.Index(system, tier.marker)
		if at < 0 {
			t.Fatalf("%s is missing from the system prompt entirely (marker %q)",
				tier.name, tier.marker)
		}
		if at < last {
			t.Errorf("%s appears BEFORE %s. The tiers are ordered most-stable-first "+
				"because the prompt cache keys on a stable prefix; putting a more "+
				"volatile tier earlier invalidates the cache on every request and "+
				"nothing else will tell you.", tier.name, lastName)
		}
		last, lastName = at, tier.name
	}

	// The clock is the most volatile thing in the prompt — it changes every
	// single turn — so it must be last of all, in the trailing block.
	clock := strings.Index(system, "# Right now")
	for _, earlier := range []string{"# What I can do", "# What I know", "# Earlier sessions"} {
		if i := strings.Index(system, earlier); i > clock {
			t.Errorf("%s sits after the clock. Everything below the clock is re-sent "+
				"uncached on every turn.", earlier)
		}
	}
	if tail := system[clock:]; strings.Contains(tail, "# What I know") {
		t.Error("a stable tier is repeated after the clock")
	}
}
