package memory

import (
	"strings"
	"testing"
)

// The worst failure this system has produced, pinned.
//
// Having just finished a set of quizzes and said so, she was asked to build a
// copy of a website. She did MORE quizzes, in a third course, and replied about
// quiz scores — never mentioning the website, opening with a greeting from the
// exchange before. She did not refuse or misunderstand. She never registered
// that the request had arrived.
//
// The prompt was 196,000 tokens holding 618 verbatim turns of quiz-clicking, and
// the new request was one sentence at the end of it carrying no more weight than
// any other line. Every tier supplies CONTEXT; none of them marked the REQUEST.
func TestTheCurrentRequestIsMarkedAsSuch(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A history that looks nothing like the new request, as it did that evening.
	for i := 0; i < 40; i++ {
		if _, err := s.AppendTurn(Turn{Role: "tool",
			Text: "Clicked \"Submit Quiz\". Now on Quizzes - BUS 2202-01 E-Commerce"}); err != nil {
			t.Fatal(err)
		}
	}
	b := NewContextBuilder(s, BuildIndex(s), "persona")

	request := "copy the site at agisada.com in HTML, CSS and JavaScript"
	_, msgs, _ := b.Build(request)

	if len(msgs) == 0 {
		t.Fatal("no messages produced")
	}
	last := msgs[len(msgs)-1].Text
	if !strings.Contains(last, request) {
		t.Fatalf("the last thing before her own message is not the request:\n%s", last)
	}
	if !strings.Contains(last, "RIGHT NOW") {
		t.Error("the request is present but not marked as the current one — that is " +
			"the state that produced the failure")
	}
	if !strings.Contains(last, "Do not restart") {
		t.Error("nothing tells her the history is finished work; she restarted it")
	}
}

// It must go last. Anything after it is something else competing to be the final
// word, which is the position that was empty in the first place.
func TestTheRequestMarkerGoesLast(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendTurn(Turn{Role: "user", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	b := NewContextBuilder(s, BuildIndex(s), "persona")
	b.Learned = func() string { return "- a-procedure — some summary" }
	b.Insights = func() []string { return []string{"an insight"} }

	_, msgs, _ := b.Build("the actual request")

	last := msgs[len(msgs)-1].Text
	if !strings.Contains(last, "the actual request") {
		t.Errorf("something was appended after the request marker:\n%s", last)
	}
}

// An empty query adds nothing — a background job with no user message must not
// grow a marker pointing at nothing.
func TestNoRequestMeansNoMarker(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := NewContextBuilder(s, BuildIndex(s), "persona")
	_, msgs, _ := b.Build("   ")
	for _, m := range msgs {
		if strings.Contains(m.Text, "RIGHT NOW") {
			t.Error("an empty request still produced a marker")
		}
	}
}
