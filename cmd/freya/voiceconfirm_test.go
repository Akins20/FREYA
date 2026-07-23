package main

import "testing"

func TestParseYesNo(t *testing.T) {
	cases := []struct {
		answer  string
		decided bool
		yes     bool
	}{
		{"yes", true, true},
		{"yeah go ahead", true, true},
		{"yep do it", true, true},
		{"sure, that's fine", true, true},
		{"okay", true, true},
		{"no", true, false},
		{"no don't", true, false},
		{"nope, cancel that", true, false},
		{"stop", true, false},
		{"wait, hold on", true, false},
		// A "no" anywhere must beat an incidental "okay" — the safe reading.
		{"no that's okay leave it", true, false},
		{"", false, false},
		{"hmm let me think about the weather", false, false},
	}
	for _, c := range cases {
		decided, yes := parseYesNo(c.answer)
		if decided != c.decided || (decided && yes != c.yes) {
			t.Errorf("parseYesNo(%q) = decided=%v yes=%v, want decided=%v yes=%v",
				c.answer, decided, yes, c.decided, c.yes)
		}
	}
}
