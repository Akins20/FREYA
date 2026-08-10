package agent

import (
	"strings"
	"testing"
)

// Every one of these was printed to a user, verbatim, during a real run. They
// are kept exactly as they arrived rather than tidied into examples, because the
// shapes the summariser actually produces are the only ones worth matching.
var realPreambles = []string{
	"Okay, here's my take on those thoughts, tailored for an expert audience, and written in the first person:",
	"Okay, I'm ready to summarize my \"thoughts\" in the first person, assuming an expert audience. Here's my take:",
	"Here's my attempt at summarizing the thought process, tailored for an expert audience:",
	"Here's my attempt at summarizing the thought process in the first person, assuming the user is an expert:",
	"Okay, here's my interpretation of that thought process, tailored for an expert audience, and written in the first person:",
	"Okay, here's my interpretation of that thought process, from an expert's perspective:",
	"Okay, here's my interpretation of that thought process, summarized as if it were my own:",
	"Okay, here's my interpretation of that thought process, summarized as you requested:",
	"Here's a summary of my thinking, presented as if it were my own internal monologue:",
	"Okay, here's my summary, taking on the persona you requested:",
	"Here's your summary:",
	"Here's my take on the process:",
	"Okay, here's my interpretation of that interaction:",
	"Here's my take on those instructions, thinking through them as a seasoned professional:",
	"Okay, here's my take on those notes.",
	"Okay, here's the summary of my thought process:",
	"Okay, here's the summary, expressed as though I'm the one thinking it:",
	"Okay, here's my take on those thoughts, framed as my own mental processing:",
	"Here's my summary, as the expert:",
	"Okay, here's my take on those \"thoughts,\" presented from my perspective as a seasoned professional:",
	"Here's a summary of my immediate thought process:",
	"Okay, here's my take on those thoughts, framed as an expert's internal monologue:",
}

// Run over every thought line captured across six live runs, the filter matched
// 54 of 428 and every one of them was a framing rather than a thought. The list
// above is that set, deduplicated. If a new shape turns up in a trace, add it
// here rather than widening the pattern from imagination.

// The wrapper comes off and the thought stays.
func TestTheSummarisersThroatClearingComesOff(t *testing.T) {
	const work = "The tool's output flagged that em dash usage. Let's track it down."
	for _, p := range realPreambles {
		got := trimThoughtPreamble(p + "\n" + work)
		if got != work {
			t.Errorf("preamble survived\n  in:  %q\n  out: %q", p, got)
		}
	}
}

// A preamble spread over two lines before the work starts comes off too.
func TestASecondLineOfPreambleAlsoGoes(t *testing.T) {
	in := "Okay, here's my take on those thoughts, tailored for an expert audience:\n" +
		"\n" +
		"Here's your summary:\n" +
		"\n" +
		"**Fixing the nav links**\n" +
		"The file write flagged two dead hrefs."
	got := trimThoughtPreamble(in)
	if !strings.HasPrefix(got, "**Fixing the nav links**") {
		t.Errorf("did not reach the work:\n%s", got)
	}
	if strings.Contains(got, "expert audience") || strings.Contains(got, "Here's your summary") {
		t.Errorf("preamble left behind:\n%s", got)
	}
}

// The half that matters more: real reasoning must survive untouched, including
// when it happens to talk about summarising something.
func TestRealThinkingIsNotTouched(t *testing.T) {
	keep := []string{
		// Printed in a real run, and it mentions summarising a report.
		"Alright, let's get down to brass tacks. The instructions are clear: I need to " +
			"generate a `sales.xlsx` spreadsheet and a `report.docx` report. The spreadsheet " +
			"is the foundation, and the report will summarize the key takeaways.",
		"**Fixing the \"Em Dash\" Problem**",
		"Okay, this feedback is gold! The file write is telling me the navigation links " +
			"for `#about` and `#contact` are dead.",
		"Here's my reading of the CSV: the South region declines every quarter while the " +
			"other three grow or hold, so the answer is South and the evidence is the four " +
			"quarterly figures rather than anything about why, which the numbers cannot " +
			"support and I was told not to guess at.",
		"I need to summarize the findings for the user before moving on.",
		"The review was positive. Now let's refine the details it raised.",
	}
	for _, s := range keep {
		if got := trimThoughtPreamble(s); got != strings.TrimSpace(s) {
			t.Errorf("real thinking was trimmed\n  in:  %q\n  out: %q", s, got)
		}
	}
}

// A thought that is preamble the whole way down is still the only thought there
// was, so it is handed back rather than swallowed. An empty thinking window
// looks like a broken one.
func TestAThoughtThatIsAllPreambleSurvives(t *testing.T) {
	in := "Okay, here's my take on those thoughts, tailored for an expert audience:"
	if got := trimThoughtPreamble(in); got != in {
		t.Errorf("an all-preamble thought was emptied: %q", got)
	}
}

// Nothing in, nothing out, and no panic on the way.
func TestEmptyThoughtStaysEmpty(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\n"} {
		if got := trimThoughtPreamble(in); got != "" {
			t.Errorf("%q became %q", in, got)
		}
	}
}
