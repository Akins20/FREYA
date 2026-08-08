package main

import (
	"strings"
	"testing"
)

// Declining is a real answer, and the parser has to be able to hear it. A
// reviewer that says "these are different jobs" must not be read as a merge —
// the failure mode being avoided is a prose reply parsed into an empty playbook
// that then supersedes two good ones.
func TestADeclineIsNotMistakenForAMerge(t *testing.T) {
	for _, prose := range []string{
		"These are different jobs: one signs in, the other reorders groceries.",
		"",
		"I would leave these alone.",
		`{"name": "", "summary": "", "body": ""}`,
		`{"name": "x", "summary": "y"}`, // no body
	} {
		if _, ok := parseMerge(prose); ok {
			t.Errorf("a decline was read as a merge: %q", prose)
		}
	}
}

// And a merge has to survive the fences a model wraps JSON in.
func TestAFencedMergeIsRead(t *testing.T) {
	reply := "Here you go:\n```json\n" +
		`{"name": "uopeople-access", "summary": "signing in to UoPeople", ` +
		`"body": "my.uopeople.edu works; the d2l door does not"}` +
		"\n```\nThat keeps both steps."

	got, ok := parseMerge(reply)
	if !ok {
		t.Fatal("a perfectly good merge wrapped in a fence was refused")
	}
	if got.Name != "uopeople-access" {
		t.Errorf("name = %q", got.Name)
	}
	if !strings.Contains(got.Body, "d2l door does not") {
		t.Errorf("body = %q", got.Body)
	}
}

// The brief must state the asymmetry, because that is the judgement being asked
// for: a duplicate costs a line of context, a bad merge costs something she
// cannot work out again.
func TestTheBriefSaysDecliningIsAllowed(t *testing.T) {
	brief := mergeBrief(nil)
	for _, want := range []string{
		"change nothing", "not a failure", "cannot work",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("the brief never says %q, so declining reads as non-compliance", want)
		}
	}
}
