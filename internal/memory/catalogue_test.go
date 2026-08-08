package memory

import (
	"strings"
	"testing"
)

// The catalogue is only cheap if it caches, and it only caches if it sits with
// the stable tiers. Presence is not enough to assert: a block placed after the
// clock would still "appear in the system prompt" and would still be re-billed
// on every single turn, because everything after the clock changes every minute.
//
// So this pins POSITION, which nothing else in this package does. The plan that
// produced this work noted the same gap for the clock itself — that it could
// drift up into the identity tier and every existing test would still pass.
func TestTheCatalogueSitsInTheCachedPrefix(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendTurn(Turn{Role: "user", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutFact(Fact{Key: "loose-fact", Text: "Prefers dark mode"}); err != nil {
		t.Fatal(err)
	}

	b := NewContextBuilder(s, BuildIndex(s), "PERSONA-MARKER")
	b.Catalogue = "CATALOGUE-MARKER — every tool she has"
	system, _, _ := b.Build("anything")

	at := func(needle, what string) int {
		i := strings.Index(system, needle)
		if i < 0 {
			t.Fatalf("%s missing from the system prompt entirely", what)
		}
		return i
	}

	persona := at("PERSONA-MARKER", "persona")
	catalogue := at("CATALOGUE-MARKER", "catalogue")
	facts := at("loose-fact", "facts tier")
	clock := at("# Right now", "the clock")

	if catalogue < persona {
		t.Error("the catalogue precedes identity — identity must lead the prefix")
	}
	if catalogue > facts {
		t.Error("the catalogue follows the facts tier; facts are appended to as she " +
			"learns, so every new fact would re-bill the catalogue behind it")
	}
	if catalogue > clock {
		t.Fatal("the catalogue follows the clock — the clock changes every minute, so " +
			"the catalogue would be re-processed on literally every turn")
	}
}

// An empty catalogue must add nothing at all. Every caller that has not adopted
// it — a test, the one-shot CLI, a background job — keeps the prompt it had.
func TestNoCatalogueMeansNoHeading(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := NewContextBuilder(s, BuildIndex(s), "persona")
	system, _, _ := b.Build("anything")
	if strings.Contains(system, "# What I can do") {
		t.Error("an unset catalogue still emitted its heading")
	}
}
