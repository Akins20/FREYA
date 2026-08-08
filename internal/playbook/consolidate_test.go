package playbook

import (
	"strings"
	"testing"
)

// The junk this exists to notice: she learns the same job twice, under two
// names, and neither entry knows about the other. The index rides the volatile
// tail, so duplicates are paid for on every single turn.
func TestTwoNamesForOneJobAreNoticed(t *testing.T) {
	l, _ := OpenLearned(t.TempDir())
	add(t, l, "portal-signin", "signing in to the UoPeople portal", "go to my.uopeople.edu")
	add(t, l, "uopeople-login", "signing in to the UoPeople portal each morning", "use my.uopeople.edu")
	add(t, l, "grocery-order", "reordering the weekly shop from the supermarket site", "open the basket")

	overlaps := l.Overlaps()
	if len(overlaps) == 0 {
		t.Fatal("two entries for one job were not noticed")
	}
	got := overlaps[0].Names
	if !(contains(got, "portal-signin") && contains(got, "uopeople-login")) {
		t.Errorf("wrong pair flagged: %v", got)
	}
	for _, o := range overlaps {
		if contains(o.Names, "grocery-order") {
			t.Errorf("an unrelated procedure was flagged as a duplicate: %v", o.Names)
		}
	}
}

// A merge must never be the only copy. Consolidation is judgement, judgement is
// sometimes wrong, and this store is the only place she keeps what she works out.
func TestAMergeSupersedesRatherThanDeletes(t *testing.T) {
	l, _ := OpenLearned(t.TempDir())
	add(t, l, "portal-signin", "signing in", "go to my.uopeople.edu")
	add(t, l, "uopeople-login", "signing in each morning", "the d2l door does not work")

	err := l.Supersede([]string{"portal-signin", "uopeople-login"}, Skill{
		Name:    "uopeople-access",
		Summary: "signing in to UoPeople",
		Body:    "my.uopeople.edu works; learn.uopeople.edu/d2l/login does not",
	})
	if err != nil {
		t.Fatal(err)
	}

	if n := len(l.Names()); n != 1 {
		t.Errorf("after a merge there are %d live playbooks, want 1: %v", n, l.Names())
	}
	if _, ok := l.Get("uopeople-access"); !ok {
		t.Fatal("the merged playbook is not there")
	}

	gone := l.Superseded()
	if len(gone) != 2 {
		t.Fatalf("%d superseded entries kept, want 2 — a bad merge would be unrecoverable", len(gone))
	}
	var foundBody bool
	for _, g := range gone {
		if strings.Contains(g.Body, "d2l door does not work") {
			foundBody = true
		}
		if !strings.Contains(g.Summary, "superseded by uopeople-access") {
			t.Errorf("a superseded entry does not say what replaced it: %q", g.Summary)
		}
	}
	if !foundBody {
		t.Error("the original body was not kept, so the merge destroyed what it replaced")
	}

	// And it survives a restart, or "recoverable" is a claim rather than a fact.
	second, err := OpenLearned(l.path[:strings.LastIndex(l.path, "/")])
	if err != nil {
		t.Fatal(err)
	}
	if n := len(second.Superseded()); n != 2 {
		t.Errorf("superseded entries did not persist: %d", n)
	}
}

// A merge naming nothing that exists would quietly delete both originals and
// leave an invented entry in their place.
func TestAMergeOfNothingIsRefused(t *testing.T) {
	l, _ := OpenLearned(t.TempDir())
	add(t, l, "real", "a real one", "steps")
	err := l.Supersede([]string{"nope", "also-nope"}, Skill{
		Name: "invented", Summary: "s", Body: "b",
	})
	if err == nil {
		t.Fatal("a merge of playbooks that do not exist was accepted")
	}
	if _, ok := l.Get("invented"); ok {
		t.Error("the invented merge was written anyway")
	}
}

// Detection must err towards leaving things alone: merging two genuinely
// different procedures loses a distinction she earned, while missing a duplicate
// costs only a line in the index.
func TestUnrelatedProceduresAreNotFlagged(t *testing.T) {
	l, _ := OpenLearned(t.TempDir())
	add(t, l, "portal-signin", "signing in to the university portal", "b")
	add(t, l, "backup-photos", "copying the camera roll to the external drive", "b")
	add(t, l, "build-failing", "what to check when the go build breaks", "b")

	if o := l.Overlaps(); len(o) != 0 {
		t.Errorf("unrelated procedures were flagged for merging: %v", o)
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
