package memory

import (
	"fmt"
	"strings"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Fatalf("empty string: want 0, got %d", got)
	}
	// The estimate must run high, never low: under-counting overflows the window.
	text := strings.Repeat("word ", 100) // 500 chars, ~102 real tokens
	if got := EstimateTokens(text); got < 100 {
		t.Fatalf("estimate %d is suspiciously low for %d chars", got, len(text))
	}
}

func TestBudgetCascade(t *testing.T) {
	a := newAllocator(1000)

	got := a.take(400)
	if got != 400 {
		t.Fatalf("take(400) = %d", got)
	}
	a.spend(100) // used far less than reserved

	// The 300 unspent tokens must remain available to later tiers.
	if got := a.take(900); got != 900 {
		t.Fatalf("after spending 100 of 1000, take(900) = %d, want 900", got)
	}
	a.spend(900)
	if got := a.take(50); got != 0 {
		t.Fatalf("exhausted allocator returned %d, want 0", got)
	}
}

func TestBudgetScaleTo(t *testing.T) {
	big := DefaultBudget()
	small := big.ScaleTo(128_000)

	if small.Working >= big.Working {
		t.Fatalf("scaling down did not shrink working tier: %d >= %d", small.Working, big.Working)
	}
	total := small.Identity + small.Facts + small.Episodes + small.Working + small.Retrieved
	if total > small.Usable() {
		t.Fatalf("scaled tiers (%d) exceed usable window (%d)", total, small.Usable())
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.AppendTurn(Turn{Role: "user", Text: "the NTFS drive is nearly full"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.PutFact(Fact{Key: "prefers-go", Text: "Builds in Go, not Python"}); err != nil {
		t.Fatalf("put fact: %v", err)
	}
	s.Close()

	// Reopening must recover everything from disk.
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	if s2.TurnCount() != 1 {
		t.Fatalf("turns after reopen = %d, want 1", s2.TurnCount())
	}
	facts := s2.Facts()
	if len(facts) != 1 || facts[0].Key != "prefers-go" {
		t.Fatalf("facts after reopen = %+v", facts)
	}
	if facts[0].Tokens == 0 {
		t.Fatal("fact tokens were not computed")
	}
}

func TestPutFactUpdatesRatherThanDuplicates(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.PutFact(Fact{Key: "editor", Text: "uses vim"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutFact(Fact{Key: "editor", Text: "uses neovim"}); err != nil {
		t.Fatal(err)
	}

	facts := s.Facts()
	if len(facts) != 1 {
		t.Fatalf("same key produced %d facts, want 1", len(facts))
	}
	if facts[0].Text != "uses neovim" {
		t.Fatalf("fact not updated: %q", facts[0].Text)
	}
	if facts[0].Created.After(facts[0].Updated) {
		t.Fatal("created timestamp should be preserved and precede updated")
	}
}

func TestWorkingSetEvictsInChunks(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for i := 0; i < 100; i++ {
		if _, err := s.AppendTurn(Turn{
			Role: "user",
			Text: fmt.Sprintf("turn number %d with some padding text to give it weight", i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	budget := 200
	working, evicted := s.WorkingSet(budget, 0.25)
	if len(evicted) == 0 {
		t.Fatal("expected eviction when history exceeds budget")
	}
	if len(working) == 0 {
		t.Fatal("working set must never be emptied entirely")
	}

	// The point of chunked eviction: the next call evicts nothing, so the
	// cached prompt prefix survives many turns.
	_, evicted2 := s.WorkingSet(budget, 0.25)
	if len(evicted2) != 0 {
		t.Fatalf("second call evicted %d turns; chunking should leave headroom", len(evicted2))
	}

	// The most recent turn must always survive.
	last := working[len(working)-1]
	if !strings.Contains(last.Text, "turn number 99") {
		t.Fatalf("newest turn missing from working set, got %q", last.Text)
	}
}

func TestWorkingSetProgressesOnOversizedTurn(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// A single turn larger than the entire budget must not deadlock eviction.
	for i := 0; i < 3; i++ {
		if _, err := s.AppendTurn(Turn{Role: "user", Text: strings.Repeat("x", 4000)}); err != nil {
			t.Fatal(err)
		}
	}
	working, evicted := s.WorkingSet(10, 0.25)
	if len(evicted) == 0 {
		t.Fatal("expected forward progress when one turn exceeds the budget")
	}
	if len(working) == 0 {
		t.Fatal("working set emptied entirely")
	}
}

func TestBM25FindsRelevantDocuments(t *testing.T) {
	ix := NewIndex()
	ix.Add("a", "turn", "the external NTFS drive is at ninety nine percent capacity")
	ix.Add("b", "turn", "we chose Go because it compiles to a single static binary")
	ix.Add("c", "turn", "piper handles text to speech synthesis locally")

	hits := ix.Search("why did we pick golang", 3)
	if len(hits) == 0 {
		t.Fatal("no hits for a query sharing vocabulary with document b")
	}

	hits = ix.Search("NTFS drive capacity", 3)
	if len(hits) == 0 || hits[0].ID != "a" {
		t.Fatalf("expected document a first, got %+v", hits)
	}
}

func TestBM25IgnoresStopwordOnlyQueries(t *testing.T) {
	ix := NewIndex()
	ix.Add("a", "turn", "something meaningful about compilers")

	if hits := ix.Search("the and for you", 5); len(hits) != 0 {
		t.Fatalf("stopword-only query returned %d hits, want 0", len(hits))
	}
	if hits := ix.Search("", 5); len(hits) != 0 {
		t.Fatalf("empty query returned %d hits, want 0", len(hits))
	}
}

func TestContextBuilderRespectsBudgetAndOrder(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for i := 0; i < 20; i++ {
		if _, err := s.AppendTurn(Turn{Role: "user", Text: fmt.Sprintf("message %d about compilers", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutFact(Fact{Key: "pinned-rule", Text: "Never use tabs", Pinned: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutFact(Fact{Key: "loose-fact", Text: "Prefers dark mode"}); err != nil {
		t.Fatal(err)
	}

	b := NewContextBuilder(s, BuildIndex(s), "PERSONA-MARKER")
	system, msgs, snap := b.Build("tell me about compilers")

	if !strings.Contains(system, "PERSONA-MARKER") {
		t.Fatal("persona missing from system prompt")
	}
	if !strings.Contains(system, "Never use tabs") {
		t.Fatal("pinned fact must appear in the identity tier")
	}
	if !strings.Contains(system, "loose-fact") {
		t.Fatal("unpinned fact missing from the facts tier")
	}
	// Pinned facts belong to identity only; they must not be repeated below.
	if strings.Count(system, "Never use tabs") != 1 {
		t.Fatal("pinned fact duplicated across tiers")
	}
	if len(msgs) == 0 {
		t.Fatal("no working-set messages produced")
	}
	if snap.TotalTokens > b.Budget.Usable() {
		t.Fatalf("assembled %d tokens, over the %d usable budget", snap.TotalTokens, b.Budget.Usable())
	}
	if snap.WorkingTurns != 20 {
		t.Fatalf("working turns = %d, want 20", snap.WorkingTurns)
	}
}

func TestContextBuilderSkipsRetrievalAlreadyInWorkingSet(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.AppendTurn(Turn{Role: "user", Text: "quantum entanglement notes"}); err != nil {
		t.Fatal(err)
	}

	b := NewContextBuilder(s, BuildIndex(s), "persona")
	_, msgs, snap := b.Build("quantum entanglement")

	// The only matching document is already verbatim in the working set, so
	// re-injecting it would waste tokens on a duplicate.
	if snap.RetrievedCount != 0 {
		t.Fatalf("retrieved %d docs already present verbatim", snap.RetrievedCount)
	}
	for _, m := range msgs {
		if strings.Contains(m.Text, "Recalled from memory") {
			t.Fatal("duplicate recall block injected")
		}
	}
}
