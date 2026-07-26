package bench

import (
	"strings"
	"testing"

	"github.com/akins/jarvis/internal/telemetry"
)

// The harness is the instrument every reliability claim rests on, so its own
// defects are worth tests. Each of these pins a way it previously lied.

// Rounds were always 0: the footer they are parsed from is printed only on the
// verbose accounting line, which the one-shot path the harness drives now emits.
func TestExtractRoundsReadsTheAccountingLine(t *testing.T) {
	trace := "  context: 4531 tok (identity 4531 · facts 0 · episodes 0 · " +
		"working 0/0 turns · recalled 0) · 7 round(s)\n"
	if got := extractRounds(trace); got != 7 {
		t.Fatalf("rounds = %d, want 7", got)
	}
	// A tool that happens to print "3 rounds" of its own must not be mistaken for
	// the footer — the pattern is anchored on the literal "round(s)".
	if got := extractRounds("the fight went 3 rounds\n"); got != 0 {
		t.Fatalf("prose 'rounds' parsed as the footer: got %d", got)
	}
}

// Her reasoning must never be graded as her answer. With thinking on by default,
// leaving 💭 lines in let a reply check pass on a token she only thought about
// and fail on a stalling phrase she considered and rejected.
func TestExtractReplyExcludesReasoningAndAccounting(t *testing.T) {
	trace := strings.Join([]string{
		"  💭 I should probably say I'll get started and come back to it later.",
		"  💭 The password on the page is APOLLO-5.",
		"  → browser_read name=portal",
		"  ✓ browser_read",
		"I opened the portal and read the list.",
		"  context: 100 tok (identity 1 · facts 0 · episodes 0 · working 0/0 turns · recalled 0) · 2 round(s)",
		"  tools: browser_open, browser_read",
	}, "\n")

	reply := extractReply(trace)
	if reply != "I opened the portal and read the list." {
		t.Fatalf("reply = %q, want only the spoken answer", reply)
	}

	w := &World{Reply: reply}
	// A token that appeared only in her reasoning must not satisfy a reply check.
	if ok, _ := ReplyHas("APOLLO-5")(w); ok {
		t.Error("a token she only thought about satisfied ReplyHas")
	}
	// A stall she considered and did not say must not fail FinishedCleanly.
	if ok, why := FinishedCleanly()(w); !ok {
		t.Errorf("a chatbot tell from her reasoning failed the reply: %s", why)
	}
	// And the tool accounting line must not be readable as an answer.
	if ok, _ := ReplyHas("browser_open")(w); ok {
		t.Error("the tools: accounting line leaked into the reply")
	}
}

// A previous run's progress must not satisfy this run's check.
func TestPortalStateResetClearsPriorProgress(t *testing.T) {
	var st PortalState
	st.OpenedQuiz.Store(5)
	st.SubmittedTo.Store("/quiz/5")
	st.loginDone.Store(true)

	st.Reset()

	if got := st.OpenedQuiz.Load(); got != 0 {
		t.Errorf("OpenedQuiz = %d after reset, want 0", got)
	}
	if got, _ := st.SubmittedTo.Load().(string); got != "" {
		t.Errorf("SubmittedTo = %q after reset, want empty", got)
	}
	if st.loginDone.Load() {
		t.Error("loginDone still set after reset")
	}
}

// toolEvent builds a recorded tool call for the reliability predicates.
func toolEvent(seq int64, round int, name, args string, ok bool) telemetry.Event {
	e := telemetry.Event{
		Kind: telemetry.KindTool, Name: name, Seq: seq, Round: round,
		ArgsHash: args, OK: ok, Outcome: telemetry.OutcomeOK,
	}
	if !ok {
		e.Outcome = telemetry.OutcomeError
		e.Error = "no element matches"
	}
	return e
}

func TestReliabilityPredicates(t *testing.T) {
	// Round 1 achieved nothing; round 2 mixed; round 3 worked.
	w := &World{Events: []telemetry.Event{
		toolEvent(1, 1, "browser_click", "aaa", false),
		toolEvent(2, 1, "browser_click", "bbb", false),
		toolEvent(3, 2, "browser_click", "ccc", false),
		toolEvent(4, 2, "browser_read", "ddd", true),
		toolEvent(5, 3, "browser_read", "ddd", true),
	}}
	if got := w.WastedRounds(); got != 1 {
		t.Errorf("WastedRounds = %d, want 1 (only round 1 was a total loss)", got)
	}
	if got := w.FailedTools(); got != 3 {
		t.Errorf("FailedTools = %d, want 3", got)
	}
	if ok, why := MaxWastedRounds(1)(w); !ok {
		t.Errorf("one wasted round should clear a max of 1: %s", why)
	}
	if ok, _ := MaxWastedRounds(0)(w); ok {
		t.Error("one wasted round should not clear a max of 0")
	}

	// Different arguments each time is exploration, not thrashing.
	if n, _ := w.LongestRepeatRun(); n != 1 {
		t.Errorf("LongestRepeatRun = %d over distinct arguments, want 1", n)
	}

	// The same call, failing, over and over is the stuck loop.
	stuck := &World{Events: []telemetry.Event{
		toolEvent(1, 1, "browser_click_real", "same", false),
		toolEvent(2, 2, "browser_click_real", "same", false),
		toolEvent(3, 3, "browser_click_real", "same", false),
		toolEvent(4, 4, "browser_click_real", "same", false),
	}}
	n, name := stuck.LongestRepeatRun()
	if n != 4 || name != "browser_click_real" {
		t.Fatalf("LongestRepeatRun = (%d, %q), want (4, browser_click_real)", n, name)
	}
	if ok, _ := NoThrash(3)(stuck); ok {
		t.Error("four identical failing calls should be reported as thrash")
	}

	// A success in the middle breaks the run.
	broken := &World{Events: []telemetry.Event{
		toolEvent(1, 1, "browser_click", "same", false),
		toolEvent(2, 2, "browser_click", "same", true),
		toolEvent(3, 3, "browser_click", "same", false),
	}}
	if n, _ := broken.LongestRepeatRun(); n != 1 {
		t.Errorf("a success should break the failing run, got %d", n)
	}
}

func TestSilentNoopsAreVisible(t *testing.T) {
	empty := toolEvent(1, 1, "browser_scroll", "x", true)
	empty.Outcome = telemetry.OutcomeEmpty
	w := &World{Events: []telemetry.Event{empty, toolEvent(2, 1, "browser_read", "y", true)}}

	if got := w.SilentNoops(); got != 1 {
		t.Fatalf("SilentNoops = %d, want 1", got)
	}
	if ok, why := NoSilentNoops()(w); ok {
		t.Error("a success that returned nothing should be reported")
	} else if !strings.Contains(why, "returned nothing") {
		t.Errorf("unhelpful reason: %s", why)
	}
}

// Events arrive from concurrent tool goroutines, so file order is not causal
// order; the predicates must sort by the sequence stamped at record time.
func TestToolEventsSortByRecordedSequence(t *testing.T) {
	w := &World{Events: []telemetry.Event{
		toolEvent(3, 1, "third", "c", true),
		toolEvent(1, 1, "first", "a", true),
		toolEvent(2, 1, "second", "b", true),
	}}
	got := w.toolEvents()
	want := []string{"first", "second", "third"}
	for i, name := range want {
		if got[i].Name != name {
			t.Fatalf("position %d = %q, want %q (events not reordered by Seq)", i, got[i].Name, name)
		}
	}
}
