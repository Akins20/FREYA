package agent

import (
	"strings"
	"testing"
)

func trailWith(outputs ...string) *trail {
	t := &trail{}
	for _, o := range outputs {
		t.add(step{tool: "dev_projects", output: o})
	}
	return t
}

// The measured case. A tool told her there were 28 projects; she audited
// nineteen, wrote a good report, and finished with "I've audited all 28
// projects". The omissions were mostly defensible — four are not projects and
// three are folders holding other projects. The sentence was not.
func TestClaimingAllOfAnEnumeratedSetIsCaught(t *testing.T) {
	work := trailWith("28 projects in /run/media/akins/Akins Drive1/Development:\n- AIOB2B\n- Admin-Web")
	reply := "I've audited all 28 projects in your Development folder, categorized what " +
		"they are and their tech stacks."

	n, noun := overclaimed(reply, work)
	if n != 28 {
		t.Fatalf("claimed coverage of 28 was not detected (got %d)", n)
	}
	if noun != "projects" {
		t.Errorf("noun = %q, want projects", noun)
	}

	brief := coverageBrief("audit my Development folder", n, noun)
	for _, want := range []string{"all 28", "what you actually covered", "is fine and often correct"} {
		if !strings.Contains(brief, want) {
			t.Errorf("the brief is missing %q", want)
		}
	}
	// It must not read as an accusation — she may well have covered them all.
	for _, banned := range []string{"lying", "dishonest", "false"} {
		if strings.Contains(strings.ToLower(brief), banned) {
			t.Errorf("the brief accuses rather than asks: contains %q", banned)
		}
	}
}

// Everything below is the half that matters more. A check that fires on honest
// replies costs a round every time and teaches her the warning means nothing.
func TestOrdinaryRepliesAreNotStopped(t *testing.T) {
	work := trailWith("28 projects in /home/x:\n- a\n- b")

	for _, reply := range []string{
		// No completeness word.
		"I looked at 28 projects and here is what I found.",
		// Completeness word, but no number — nothing to check it against.
		"I audited all the projects I could reach.",
		// A different number from the one enumerated: she is counting something
		// else, and guessing which would be worse than staying quiet.
		"All 12 of the git repositories are clean.",
		// Hedged, and honest already.
		"19 of the 28 — I skipped nine that aren't really projects.",
		"",
	} {
		if n, _ := overclaimed(reply, work); n != 0 {
			t.Errorf("an honest reply was stopped (%d): %q", n, reply)
		}
	}
}

// With nothing enumerated there is no number of hers to check against, so the
// check must stay silent however the reply is phrased.
func TestNoEnumerationMeansNoCheck(t *testing.T) {
	if n, _ := overclaimed("I fixed all 5 of the bugs.", &trail{}); n != 0 {
		t.Errorf("a claim with no enumerated set behind it was stopped (%d)", n)
	}
	// A failed tool call enumerates nothing.
	work := &trail{}
	work.add(step{tool: "dev_projects", output: "28 projects listed", failed: true})
	if n, _ := overclaimed("all 28 done", work); n != 0 {
		t.Error("a failed tool's output was treated as an enumeration")
	}
}

// Numbers that are not counts of a listed set must not become one. A byte count,
// a score and a line number are all a number next to a word.
func TestUnrelatedNumbersAreNotEnumerations(t *testing.T) {
	work := trailWith(
		"Wrote 4096 bytes to report.html.",
		"Score: 4 out of 5.",
		"Completed in 28 seconds.",
	)
	if n, _ := overclaimed("I handled all 28 of them.", work); n != 0 {
		t.Errorf("a duration was read as a set of 28 things (%d)", n)
	}
}

// Two of something is not a set worth auditing, and stopping a reply over it
// would be noise.
func TestTinySetsAreIgnored(t *testing.T) {
	work := trailWith("2 files in /tmp:\n- a\n- b")
	if n, _ := overclaimed("I converted all 2 files.", work); n != 0 {
		t.Errorf("a set of two triggered the check (%d)", n)
	}
}
