package skills

import (
	"strings"
	"testing"
)

// Asked to replicate a site exactly, she read it once, got the top of it, and
// built a copy of the top of it. She was not being careless: browser_read fetches
// the whole page and then clips it, and the clip said "[truncated at 12000
// characters]" — which reads as a tidy-up, not as "you have seen a third of this
// document". Nothing she saw distinguished the page from the first part of it.
//
// The user then had to explain what thorough meant, which is the wrong shape for
// a fix. A disposition cannot be installed by instruction; a fact can be made
// impossible to miss.
func TestAPartialReadSaysHowMuchIsMissing(t *testing.T) {
	note := coverage(40000, 12000)
	if note == "" {
		t.Fatal("reading 12k of a 40k page said nothing about the other 28k")
	}
	for _, want := range []string{"FIRST 30%", "70%", "higher limit"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note is missing %q: %s", want, note)
		}
	}
	// It has to name the consequence, not just the arithmetic.
	if !strings.Contains(note, "complete picture") {
		t.Errorf("nothing warns against treating a partial read as the whole: %s", note)
	}
}

// A page that fitted must say nothing at all, or every read grows a caveat and
// the caveat stops being read.
func TestAWholeReadIsSilent(t *testing.T) {
	for _, c := range [][2]int{{500, 12000}, {12000, 12000}, {0, 12000}} {
		if got := coverage(c[0], c[1]); got != "" {
			t.Errorf("a complete read of %d chars still warned: %s", c[0], got)
		}
	}
}

// The percentages must be the right way round — the failure this exists for is
// her believing she has more than she does.
func TestTheFractionIsNotFlattering(t *testing.T) {
	note := coverage(100000, 10000)
	if !strings.Contains(note, "FIRST 10%") || !strings.Contains(note, "90%") {
		t.Errorf("seeing a tenth of a page did not report a tenth: %s", note)
	}
}
