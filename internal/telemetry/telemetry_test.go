package telemetry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecordAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	r.Tool("file_read", 120*time.Millisecond, nil)
	r.Tool("web_search", 900*time.Millisecond, errors.New("timed out"))
	r.ModelCall("gemini-3.1-flash-lite", time.Second, 5000, 200, 4000, 0, nil)
	r.Guard("high", "denied", "rm -rf /")
	r.Close()

	events, err := Load(filepath.Join(dir, logFile), time.Time{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4", len(events))
	}

	s := Summarise(events)
	if s.ToolCalls != 2 {
		t.Errorf("tool calls = %d, want 2", s.ToolCalls)
	}
	if s.ToolFailures != 1 {
		t.Errorf("tool failures = %d, want 1", s.ToolFailures)
	}
	if s.TotalInTokens != 5000 || s.TotalOutTokens != 200 {
		t.Errorf("tokens in/out = %d/%d, want 5000/200", s.TotalInTokens, s.TotalOutTokens)
	}
	if s.TotalCached != 4000 {
		t.Errorf("cached = %d, want 4000", s.TotalCached)
	}
	if s.TotalCostUSD <= 0 {
		t.Error("no cost was estimated")
	}
}

// TestRecordNeverBlocks is the property the whole package exists for.
//
// If instrumentation can block, a stalled disk becomes a stalled assistant. The
// writer here is wedged deliberately; recording must still return promptly, and
// the events that could not fit must be counted as dropped rather than lost
// silently.
func TestRecordNeverBlocks(t *testing.T) {
	// A recorder whose writer never drains: no goroutine consumes the channel.
	r := &Recorder{events: make(chan Event, bufferSize), done: make(chan struct{})}

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		// Ten times the buffer, so the overwhelming majority cannot fit.
		for i := range bufferSize * 10 {
			r.Tool(fmt.Sprintf("tool_%d", i), time.Millisecond, nil)
		}
		done <- time.Since(start)
	}()

	select {
	case elapsed := <-done:
		// The bound is generous; the point is that it finished at all rather
		// than blocking forever on a full channel.
		if elapsed > 2*time.Second {
			t.Errorf("recording %d events took %s — it is blocking somewhere",
				bufferSize*10, elapsed)
		}
		t.Logf("recorded %d events into a wedged writer in %s", bufferSize*10, elapsed)
	case <-time.After(5 * time.Second):
		t.Fatal("Record blocked when the buffer filled — the whole point of the package is that it cannot")
	}

	_, dropped := r.Stats()
	if dropped == 0 {
		t.Error("nothing was reported as dropped, so silence is masquerading as accuracy")
	}
	if want := int64(bufferSize*10 - bufferSize); dropped != want {
		t.Errorf("dropped = %d, want %d (everything past the buffer)", dropped, want)
	}
}

func TestRecordIsSafeFromManyGoroutines(t *testing.T) {
	dir := t.TempDir()
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Tools run concurrently in the agent, so this is the real access pattern.
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range 20 {
				r.Tool(fmt.Sprintf("tool_%d", i%7), time.Duration(j)*time.Millisecond, nil)
			}
		}(i)
	}
	wg.Wait()
	r.Close()

	written, dropped := r.Stats()
	t.Logf("written=%d dropped=%d of 1000", written, dropped)
	if written+dropped != 1000 {
		t.Errorf("written+dropped = %d, want 1000 — events vanished unaccounted for",
			written+dropped)
	}
}

// TestNilRecorderIsUsable matters because the agent holds one optionally: a
// nil recorder must never be the reason a turn fails.
func TestNilRecorderIsUsable(t *testing.T) {
	var r *Recorder
	r.Tool("anything", time.Second, nil)
	r.ModelCall("model", time.Second, 1, 1, 0, 0, nil)
	r.Guard("low", "ok", "")
	r.Voice("stt", time.Second, nil)
	r.Record(Event{Kind: KindWatch, Name: "disk"})
	if err := r.Close(); err != nil {
		t.Errorf("closing a nil recorder: %v", err)
	}
	if w, d := r.Stats(); w != 0 || d != 0 {
		t.Errorf("nil recorder reported %d/%d", w, d)
	}
	if r.Path() != "" {
		t.Error("nil recorder reported a path")
	}
}

func TestOpenFailureStillAcceptsEvents(t *testing.T) {
	// A path that cannot be a directory, because it is a file.
	file := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := Open(filepath.Join(file, "sub"))
	if err == nil {
		t.Error("expected an error when the directory cannot be created")
	}
	if r == nil {
		t.Fatal("a failed Open must still return a usable recorder")
	}
	// Must not panic, must not block.
	r.Tool("file_read", time.Millisecond, nil)
	r.Close()

	if _, dropped := r.Stats(); dropped == 0 {
		t.Error("events sent to a failed recorder should count as dropped")
	}
}

func TestEstimateCostUsesRealRates(t *testing.T) {
	// One million input tokens of 3.1-flash-lite at $0.25.
	got := EstimateCost("gemini-3.1-flash-lite", 1_000_000, 0, 0)
	if diff := got - 0.25; diff > 0.001 || diff < -0.001 {
		t.Errorf("1M input tokens = $%.4f, want $0.25", got)
	}

	// One million output tokens at $1.50.
	got = EstimateCost("gemini-3.1-flash-lite", 0, 1_000_000, 0)
	if diff := got - 1.50; diff > 0.001 || diff < -0.001 {
		t.Errorf("1M output tokens = $%.4f, want $1.50", got)
	}
}

// TestCachedTokensAreCheaper guards the arithmetic that makes the memory
// architecture's stable-prefix ordering worth having.
func TestCachedTokensAreCheaper(t *testing.T) {
	fresh := EstimateCost("gemini-3.1-flash-lite", 100_000, 0, 0)
	cached := EstimateCost("gemini-3.1-flash-lite", 100_000, 0, 100_000)
	if cached >= fresh {
		t.Errorf("fully cached input ($%.5f) is not cheaper than fresh ($%.5f)", cached, fresh)
	}
	// The published ratio is 0.025 against 0.25, a tenth.
	if ratio := cached / fresh; ratio > 0.15 || ratio < 0.05 {
		t.Errorf("cached/fresh ratio = %.3f, expected about 0.1", ratio)
	}
}

// TestAudioCostsMoreThanText is why audio is tracked separately at all.
func TestAudioCostsMoreThanText(t *testing.T) {
	text := EstimateCostAudio("gemini-3.1-flash-lite", 100_000, 0, 0, 0)
	audio := EstimateCostAudio("gemini-3.1-flash-lite", 100_000, 0, 0, 100_000)
	if audio <= text {
		t.Errorf("audio input ($%.5f) should cost more than text ($%.5f)", audio, text)
	}
	if ratio := audio / text; ratio < 1.5 || ratio > 2.5 {
		t.Errorf("audio/text ratio = %.2f, expected about 2", ratio)
	}
}

// TestLongestModelMatchWins catches a subtle failure: map iteration in Go is
// randomised, so a first-match-wins loop would price flash-lite as flash on
// some runs and not others.
func TestLongestModelMatchWins(t *testing.T) {
	// Run it enough times that a randomised map order would show up.
	first := EstimateCost("gemini-2.5-flash-lite", 1_000_000, 0, 0)
	for range 200 {
		if got := EstimateCost("gemini-2.5-flash-lite", 1_000_000, 0, 0); got != first {
			t.Fatalf("same model priced differently across calls: $%.4f then $%.4f", first, got)
		}
	}
	// And it must be the lite price, not the full flash price.
	if diff := first - 0.10; diff > 0.001 || diff < -0.001 {
		t.Errorf("flash-lite priced at $%.4f, want $0.10 — it is matching plain 'flash'", first)
	}
}

func TestUnknownModelIsNotFree(t *testing.T) {
	if got := EstimateCost("some-model-nobody-has-heard-of", 1_000_000, 0, 0); got <= 0 {
		t.Error("an unknown model costed as free, which hides spend rather than flagging it")
	}
}

func TestErrorClassGroupsSimilarFailures(t *testing.T) {
	cases := map[string]string{
		"context deadline exceeded: timed out":   "timeout",
		"refused: destructive command":           "refused by guard",
		"open /tmp/x: no such file or directory": "not found",
		"declined by user":                       "declined",
	}
	for msg, want := range cases {
		if got := errorClass(msg); got != want {
			t.Errorf("errorClass(%q) = %q want %q", msg, got, want)
		}
	}
}

func TestReportMentionsThatCostIsAnEstimate(t *testing.T) {
	events := []Event{
		{At: time.Now(), Kind: KindModel, Name: "gemini-3.1-flash-lite",
			InputTokens: 1000, OutputTokens: 100, CostUSD: 0.01, OK: true},
	}
	report := Summarise(events).Report(10)
	lower := strings.ToLower(report)
	if !strings.Contains(lower, "estimate") {
		t.Error("the report presents a cost without saying it is an estimate")
	}
	if !strings.Contains(report, "~$") {
		t.Error("costs are not marked as approximate")
	}
}

func TestEmptySummaryDoesNotPretend(t *testing.T) {
	s := Summarise(nil)
	if s.Events != 0 {
		t.Error("empty input produced events")
	}
	if !strings.Contains(Summarise(nil).Report(10), "Nothing recorded") {
		t.Error("an empty report should say so plainly")
	}
}

func TestLoadSkipsTornLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, logFile)
	// A process killed mid-write leaves a half-line; it must cost one row, not
	// the whole file.
	content := `{"at":"2026-07-22T10:00:00Z","kind":"tool","name":"a","ok":true}
{"at":"2026-07-22T10:00:01Z","kind":"tool","name":"b","ok":true}
{"at":"2026-07-22T10:00:02Z","kind":"to`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	events, err := Load(path, time.Time{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("got %d events, want 2 — a torn final line should not lose the file", len(events))
	}
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	events, err := Load(filepath.Join(t.TempDir(), "nothing.jsonl"), time.Time{})
	if err != nil {
		t.Errorf("a missing log should be empty, not an error: %v", err)
	}
	if len(events) != 0 {
		t.Error("got events from a missing file")
	}
}

func TestLoadRespectsSince(t *testing.T) {
	dir := t.TempDir()
	r, _ := Open(dir)
	r.Record(Event{At: time.Now().Add(-48 * time.Hour), Kind: KindTool, Name: "old", OK: true})
	r.Record(Event{At: time.Now(), Kind: KindTool, Name: "new", OK: true})
	r.Close()

	events, err := Load(filepath.Join(dir, logFile), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Name != "new" {
		t.Errorf("got %d events, want just the recent one", len(events))
	}
}

func TestToolRankingIsByFrequency(t *testing.T) {
	var events []Event
	for range 10 {
		events = append(events, Event{At: time.Now(), Kind: KindTool, Name: "common", OK: true})
	}
	events = append(events, Event{At: time.Now(), Kind: KindTool, Name: "rare", OK: true})

	s := Summarise(events)
	if len(s.Tools) != 2 || s.Tools[0].Name != "common" {
		t.Errorf("tools not ranked by use: %+v", s.Tools)
	}
	if s.Tools[0].N != 10 {
		t.Errorf("count = %d, want 10", s.Tools[0].N)
	}
}

func TestRatesTableIsPrintable(t *testing.T) {
	out := Rates()
	for _, want := range []string{"gemini-3.1-flash-lite", "audio in", "subscription"} {
		if !strings.Contains(out, want) {
			t.Errorf("rate table missing %q", want)
		}
	}
}

func TestDoubleCloseIsSafe(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r.Tool("x", time.Millisecond, nil)
	if err := r.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	// Shutdown paths overlap — a daemon closing on signal while a defer also
	// fires. A second close must not panic on a closed channel.
	if err := r.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}
