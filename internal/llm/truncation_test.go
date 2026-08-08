package llm

import (
	"strings"
	"testing"
)

// finishReason has been decoded since this file was written and never once read,
// so a response cut off mid-sentence arrived looking exactly like a finished one.
//
// It matters most where the output IS the artefact. Asked to write a web page,
// she emits HTML until the ceiling, the tail is dropped, and what lands is a file
// that ends inside a tag — while the tool result says "wrote 8,000 bytes" and is
// telling the truth.
func TestHittingTheOutputCeilingIsNotSilent(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"text":"<html><body><div class=\"hero\">"}]},
	          "finishReason":"MAX_TOKENS"}]}`
	out, err := decodeGemini([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated {
		t.Fatal("a response that ran out of room did not report it")
	}
	if !strings.Contains(out.Text, "CUT OFF") {
		t.Errorf("nothing in the text says it is incomplete: %q", out.Text)
	}
	if !strings.Contains(out.Text, "do not save it") {
		t.Errorf("nothing warns against saving a half-written file: %q", out.Text)
	}
	// The partial output is still there — it is worth having, it just has to be
	// known to be partial.
	if !strings.Contains(out.Text, "<div class=\"hero\">") {
		t.Errorf("the partial output was discarded: %q", out.Text)
	}
}

// A normal finish must stay clean, or every reply grows a warning and the
// warning stops being read.
func TestANormalFinishSaysNothing(t *testing.T) {
	for _, reason := range []string{"STOP", "", "stop"} {
		body := `{"candidates":[{"content":{"parts":[{"text":"all done"}]},
		          "finishReason":"` + reason + `"}]}`
		out, err := decodeGemini([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if out.Truncated || strings.Contains(out.Text, "CUT OFF") {
			t.Errorf("finishReason %q was treated as truncation: %q", reason, out.Text)
		}
	}
}
