package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A machine with several profiles must not be answered from whichever one came
// first out of a map.
//
// This is the whole point of the file. "My account" on a machine with a work
// profile and a personal one has two possible answers, and picking wrong does
// not produce a worse answer, it produces a confident answer about somebody
// else's inbox.
func TestProfilesComeBackNamedAndOrdered(t *testing.T) {
	dir := fakeChrome(t, `{
	  "profile": {
	    "last_used": "Profile 2",
	    "info_cache": {
	      "Default":   {"name": "Person 1", "user_name": "old@example.com"},
	      "Profile 2": {"name": "Elijah",   "user_name": "me@example.com"},
	      "Profile 3": {"name": "Work",     "user_name": "me@company.example"}
	    }
	  }
	}`, "Default", "Profile 2", "Profile 3")

	ps, err := Profiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 3 {
		t.Fatalf("found %d profiles, want 3", len(ps))
	}
	// The one Chrome used last leads, because it is the best available guess.
	if ps[0].Dir != "Profile 2" || !ps[0].Active {
		t.Errorf("the last-used profile is not first: %+v", ps[0])
	}
	if ps[0].Label() != "Elijah (me@example.com)" {
		t.Errorf("label is %q", ps[0].Label())
	}
}

// A directory Chrome still lists but has deleted is bookkeeping, not a profile,
// and offering it would point the sync at nothing.
func TestAProfileMissingFromDiskIsNotOffered(t *testing.T) {
	dir := fakeChrome(t, `{
	  "profile": {
	    "last_used": "Default",
	    "info_cache": {
	      "Default":   {"name": "Mine",  "user_name": "me@example.com"},
	      "Profile 9": {"name": "Gone",  "user_name": "gone@example.com"}
	    }
	  }
	}`, "Default")

	ps, err := Profiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Dir != "Default" {
		t.Errorf("a deleted profile was offered: %+v", ps)
	}
}

// Naming one resolves it; naming something that fits two refuses.
//
// Refusing matters more than guessing here for the same reason as everywhere
// else in this codebase: a wrong pick is silent and its consequences arrive
// several steps later, as an answer about the wrong account.
func TestNamingAProfileResolvesItAndAmbiguityRefuses(t *testing.T) {
	dir := fakeChrome(t, `{
	  "profile": {
	    "last_used": "Profile 2",
	    "info_cache": {
	      "Default":   {"name": "Personal", "user_name": "me@example.com"},
	      "Profile 2": {"name": "Work",     "user_name": "me@company.example"},
	      "Profile 3": {"name": "Work Two", "user_name": "other@company.example"}
	    }
	  }
	}`, "Default", "Profile 2", "Profile 3")

	// By display name.
	if p, err := FindProfile(dir, "personal"); err != nil || p.Dir != "Default" {
		t.Errorf("by name gave %+v (%v)", p, err)
	}
	// By account.
	if p, err := FindProfile(dir, "other@company.example"); err != nil || p.Dir != "Profile 3" {
		t.Errorf("by account gave %+v (%v)", p, err)
	}
	// By directory, which is what a person reads off a previous answer.
	if p, err := FindProfile(dir, "Profile 2"); err != nil || p.Dir != "Profile 2" {
		t.Errorf("by directory gave %+v (%v)", p, err)
	}
	// Ambiguous: "work" fits two, and the refusal has to name both so the next
	// question can be precise.
	_, err := FindProfile(dir, "work")
	if err == nil {
		t.Fatal("an ambiguous name silently picked one")
	}
	for _, want := range []string{"Work", "Work Two", "wrong account"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal is missing %q: %v", want, err)
		}
	}
	// Nothing matching says what there is, rather than only that it failed.
	if _, err := FindProfile(dir, "nonesuch"); err == nil ||
		!strings.Contains(err.Error(), "Personal") {
		t.Errorf("an unmatched name does not list the real ones: %v", err)
	}
	// No name asked for is the last-used one, which is the sensible default and
	// is marked as a guess wherever it is reported.
	if p, err := FindProfile(dir, ""); err != nil || p.Dir != "Profile 2" {
		t.Errorf("the default gave %+v (%v)", p, err)
	}
}

// fakeChrome builds a user data directory with a Local State and some profile
// directories in it.
func fakeChrome(t *testing.T, localState string, dirs ...string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Local State"), []byte(localState), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
