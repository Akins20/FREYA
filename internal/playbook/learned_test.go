package playbook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func add(t *testing.T, l *Learned, name, summary, body string) {
	t.Helper()
	if err := l.Add(Skill{Name: name, Summary: summary, Body: body}); err != nil {
		t.Fatalf("Add(%q): %v", name, err)
	}
}

// The whole point: a procedure worked out in one session has to survive into the
// next one. The case that named this is the UoPeople sign-in — she found the real
// door after an evening of failing at the wrong one, and a human had to dig it
// out of her archive days later because she had nowhere to keep it.
func TestALearnedProcedureSurvivesARestart(t *testing.T) {
	dir := t.TempDir()

	first, err := OpenLearned(dir)
	if err != nil {
		t.Fatal(err)
	}
	add(t, first, "uopeople-signin",
		"signing in to UoPeople — the portal door, not the courses one",
		"Go to my.uopeople.edu, NOT learn.uopeople.edu/d2l/login. The second looks "+
			"like the sign-in page and silently will not take the credentials.")

	// A new process, the same data directory.
	second, err := OpenLearned(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := second.Get("uopeople-signin")
	if !ok {
		t.Fatal("the procedure did not survive a restart — she cannot learn anything " +
			"that outlives one session")
	}
	if !strings.Contains(got.Body, "my.uopeople.edu") {
		t.Errorf("the body came back wrong: %q", got.Body)
	}
	if idx := second.Index(); !strings.Contains(idx, "uopeople-signin") {
		t.Errorf("it is stored but not in the index, so she will never know to open it: %q", idx)
	}
}

// A summary is the only part she always sees. Without one the entry is dead
// weight in the tail — present, costed every turn, and never opened.
func TestASkillWithoutASummaryIsRefused(t *testing.T) {
	l, _ := OpenLearned(t.TempDir())
	err := l.Add(Skill{Name: "thing", Body: "some steps"})
	if err == nil {
		t.Fatal("a summary-less skill was accepted")
	}
	if !strings.Contains(err.Error(), "always in front of you") {
		t.Errorf("the refusal does not explain why the summary matters: %v", err)
	}

	if err := l.Add(Skill{Name: "thing", Summary: "when to use it"}); err == nil {
		t.Error("a body-less skill was accepted")
	}
}

// Authored practice outranks one evening's conclusion. Shadowing an embedded
// playbook would silently replace it everywhere, with nothing to notice.
func TestALearnedSkillCannotShadowABuiltInOne(t *testing.T) {
	l, _ := OpenLearned(t.TempDir())
	builtin := Names()
	if len(builtin) == 0 {
		t.Skip("no embedded playbooks to collide with")
	}
	err := l.Add(Skill{Name: builtin[0], Summary: "mine", Body: "steps"})
	if err == nil {
		t.Fatalf("%q shadowed the built-in playbook of the same name", builtin[0])
	}
	if !strings.Contains(err.Error(), "built-in") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// One name, one entry. Two procedures under one name is a choice she has to make
// on every lookup — exactly the trap two selector-click tools were.
func TestLearningTheSameThingTwiceReplacesRatherThanDuplicates(t *testing.T) {
	l, _ := OpenLearned(t.TempDir())
	add(t, l, "portal", "first try", "step one")
	add(t, l, "portal", "what actually worked", "step one, then step two")

	if n := len(l.Names()); n != 1 {
		t.Fatalf("%d entries under one name, want 1: %v", n, l.Names())
	}
	got, _ := l.Get("portal")
	if got.Summary != "what actually worked" {
		t.Errorf("the older attempt won: %q", got.Summary)
	}
}

// "Portal Signin", "portal-signin" and "portal_signin" are one thing.
func TestNamesAreNormalisedToOneShape(t *testing.T) {
	l, _ := OpenLearned(t.TempDir())
	add(t, l, "Portal Signin", "s", "b")
	if _, ok := l.Get("portal-signin"); !ok {
		t.Error("a differently-spelled name missed an entry that exists")
	}
	if _, ok := l.Get("PORTAL_SIGNIN"); !ok {
		t.Error("case and underscores made a second thing")
	}
}

// The index is sent on every turn, so it cannot grow without bound. Forgetting
// is the unsolved part of agent memory; until there is a real merge pass this
// crude version is what stops twenty near-identical entries costing tokens
// forever.
func TestTheStoreIsCappedAndForgetsWhatSheStoppedUsing(t *testing.T) {
	l, _ := OpenLearned(t.TempDir())

	add(t, l, "kept-because-used", "s", "b")
	for i := 0; i < learnedCap+5; i++ {
		add(t, l, "filler-"+string(rune('a'+i%26))+string(rune('a'+i/26)), "s", "b")
		// Keep touching the one that matters, so it is not the least recently used.
		l.Get("kept-because-used")
	}

	if n := len(l.Names()); n > learnedCap {
		t.Errorf("the store grew to %d, past the cap of %d", n, learnedCap)
	}
	if _, ok := l.Get("kept-because-used"); !ok {
		t.Error("eviction dropped the one she kept reaching for, which is exactly " +
			"backwards")
	}
}

// Losing what she taught herself is bad. Refusing to start is worse.
func TestACorruptStoreStartsEmptyRatherThanFailing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "learned.json"), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := OpenLearned(dir)
	if err == nil {
		t.Error("a corrupt store reported no problem at all")
	}
	if l == nil {
		t.Fatal("a corrupt store returned no usable store — the session cannot start")
	}
	if n := len(l.Names()); n != 0 {
		t.Errorf("expected an empty store, got %d entries", n)
	}
	// And it is still writable, so she recovers by learning again.
	add(t, l, "fresh", "s", "b")
}

// An empty store must add nothing to the prompt. The tail is paid for every
// turn, so "she has learned nothing yet" has to cost zero.
func TestAnEmptyStoreCostsNothing(t *testing.T) {
	l, _ := OpenLearned(t.TempDir())
	if idx := l.Index(); idx != "" {
		t.Errorf("an empty store emitted %q", idx)
	}
}
