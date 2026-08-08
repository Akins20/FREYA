package skills

import (
	"strings"
	"testing"
)

// The last prose rule, and the one where refusing would be wrong.
//
// Every other rule audited today had the same asymmetry — a missed guard destroys
// something, a spurious guard costs a round — so refusing was right. This one runs
// the other way: a wrongly refused delegation blocks real work, while a wrongly
// allowed one costs quota that recovers at the next window. So it is reported.
func TestDelegatingSomethingSheCouldDoIsPointedOut(t *testing.T) {
	for _, task := range []string{
		"read the file /etc/hosts and tell me what is in it",
		"read the page and summarise it",
		"look up the current price of a thing",
	} {
		note := couldHaveDoneItHerself(task)
		if note == "" {
			t.Errorf("%q was delegated with no comment", task)
			continue
		}
		if !strings.Contains(note, "allowance") {
			t.Errorf("%q: the note does not say what it costs: %s", task, note)
		}
	}
}

// And real engineering must go through untouched — the whole point of having
// Claude available is the work she genuinely cannot do.
func TestRealWorkIsNotDiscouraged(t *testing.T) {
	for _, task := range []string{
		"find the race condition in the memory package and fix it",
		"refactor the browser gesture dispatcher so the fixes stop drifting apart",
		"/security-review",
		"work out why the build fails only on the second run",
	} {
		if note := couldHaveDoneItHerself(task); note != "" {
			t.Errorf("%q was discouraged, and it is exactly what delegation is for: %s",
				task, note)
		}
	}
}
