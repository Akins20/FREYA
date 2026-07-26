// Package telemetry records what Freya does, without ever getting in the way.
//
// # Fire and forget, meant literally
//
// Instrumentation that can block the work it measures is worse than no
// instrumentation: it converts an observability problem into a latency problem,
// and the failure shows up as "the assistant is slow" rather than as anything
// resembling its cause.
//
// So recording is a non-blocking send to a buffered channel. If the buffer is
// full the event is dropped and a counter is incremented. Losing a few rows of
// analytics is a rounding error; making the user wait to write them is not. The
// dropped count is reported, so silence never masquerades as accuracy.
//
// # What is counted, and what is estimated
//
// Token counts come from the provider and are exact. Money is not: this machine
// authenticates Claude with a subscription, and Gemini prices change. Costs here
// are computed from a rate table that is explicitly an estimate, and reported as
// such — a figure labelled "$0.0231" invites a trust it has not earned.
package telemetry

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Kind classifies an event.
type Kind string

const (
	KindTool  Kind = "tool"  // a skill was executed
	KindModel Kind = "model" // a language model was called
	KindGuard Kind = "guard" // an action was assessed
	KindVoice Kind = "voice" // speech in or out
	KindWatch Kind = "watch" // a background observation
)

// Event is one recorded occurrence.
type Event struct {
	At   time.Time `json:"at"`
	Kind Kind      `json:"kind"`
	// Name is the tool, model or watcher involved.
	Name string `json:"name"`
	// DurationMS is how long it took.
	DurationMS int64 `json:"ms,omitempty"`
	// OK reports success.
	OK bool `json:"ok"`
	// Error is the failure message, when there was one.
	Error string `json:"error,omitempty"`

	// Token counts, exact when the provider reports them.
	InputTokens  int `json:"in,omitempty"`
	OutputTokens int `json:"out,omitempty"`
	// CachedTokens are input tokens served from the provider's cache.
	CachedTokens int `json:"cached,omitempty"`
	// AudioTokens are input tokens that came from sound, billed at a higher rate.
	AudioTokens int `json:"audio,omitempty"`
	// ThoughtTokens are reasoning tokens spent with the thinking window on. They
	// are already inside OutputTokens for billing; recorded separately so the
	// cost of thinking can be seen on its own.
	ThoughtTokens int `json:"thought,omitempty"`

	// --- correlation ---
	//
	// Without these, the log answers "how many calls failed" but not "what did
	// she do next" — and the second question is the one that says whether she
	// recovered or thrashed. Tool calls run in concurrent goroutines, so wall
	// order is not causal order; Seq is stamped as the event is recorded, and
	// ExchangeID plus Round place it in the conversation.

	// ExchangeID groups every event produced by one Ask().
	ExchangeID string `json:"xid,omitempty"`
	// Round is the agent-loop iteration the event belongs to, 1-based.
	Round int `json:"round,omitempty"`
	// Seq is a process-monotonic counter stamped at record time, so a reader can
	// reconstruct the order events were produced in.
	Seq int64 `json:"seq,omitempty"`
	// ArgsHash fingerprints what a tool was called WITH, so twenty attempts at the
	// same thing are distinguishable from twenty different attempts. A hash, not
	// the arguments: they routinely carry page content and must not be logged.
	ArgsHash string `json:"args,omitempty"`
	// Outcome is what actually happened, one level finer than OK: an empty result
	// with no error is a silent no-op, and it used to be indistinguishable from
	// success.
	Outcome Outcome `json:"outcome,omitempty"`

	// CostUSD is an estimate. See the package doc.
	CostUSD float64 `json:"cost,omitempty"`
	// Detail carries anything else worth keeping, kept short.
	Detail string `json:"detail,omitempty"`
}

// Outcome classifies how a call ended, finer than a success bool.
type Outcome string

const (
	// OutcomeOK is a call that returned something.
	OutcomeOK Outcome = "ok"
	// OutcomeEmpty is a call that succeeded and returned nothing — the silent
	// no-op that reads as success to the model and is invisible in a pass/fail log.
	OutcomeEmpty Outcome = "empty"
	// OutcomeError is a call that failed.
	OutcomeError Outcome = "error"
	// OutcomeRefused is a call a guard or a budget declined to run.
	OutcomeRefused Outcome = "refused"
	// OutcomeTimeout is a call that ran out of time.
	OutcomeTimeout Outcome = "timeout"
)

// ClassifyOutcome derives the outcome of a tool call from what it returned.
func ClassifyOutcome(output string, err error) Outcome {
	switch {
	case err == nil && strings.TrimSpace(output) == "":
		return OutcomeEmpty
	case err == nil:
		return OutcomeOK
	case errors.Is(err, context.DeadlineExceeded):
		return OutcomeTimeout
	}
	low := strings.ToLower(err.Error())
	switch {
	case strings.Contains(low, "declined"), strings.Contains(low, "refus"),
		strings.Contains(low, "is off for this page"):
		return OutcomeRefused
	case strings.Contains(low, "timed out"), strings.Contains(low, "timeout"):
		return OutcomeTimeout
	}
	return OutcomeError
}

// bufferSize is how many events may queue before dropping begins.
//
// Large enough to absorb a burst of tool calls, small enough that a stalled
// writer cannot consume meaningful memory.
const bufferSize = 512

// Recorder accepts events and writes them in the background.
type Recorder struct {
	events  chan Event
	dropped atomic.Int64
	written atomic.Int64
	seq     atomic.Int64

	mu     sync.Mutex
	file   *os.File
	path   string
	closed bool
	done   chan struct{}
}

const logFile = "telemetry.jsonl"

// Open starts a recorder writing to a directory.
//
// A failure to open the log is not fatal: telemetry is diagnostics, and losing
// it must never stop the assistant from working. The error is returned so the
// caller can mention it, and the recorder still functions as a no-op.
func Open(dir string) (*Recorder, error) {
	r := &Recorder{
		events: make(chan Event, bufferSize),
		done:   make(chan struct{}),
		path:   filepath.Join(dir, logFile),
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		go r.drain()
		return r, fmt.Errorf("telemetry: %w", err)
	}
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		go r.drain()
		return r, fmt.Errorf("telemetry: %w", err)
	}
	r.file = f

	go r.writeLoop()
	return r, nil
}

// Record queues an event. It never blocks and never returns an error.
func (r *Recorder) Record(e Event) {
	if r == nil {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	// Stamp the sequence here, before the hand-off. Events are produced by
	// concurrent tool goroutines and drained by one writer, so the order they
	// land in the file is not the order they happened; this is what lets a reader
	// put them back in causal order.
	if e.Seq == 0 {
		e.Seq = r.seq.Add(1)
	}
	select {
	case r.events <- e:
	default:
		// Full. Dropping is the correct behaviour: the alternative is making
		// the user wait for a metrics write.
		r.dropped.Add(1)
	}
}

// Tool records a skill execution.
func (r *Recorder) Tool(name string, d time.Duration, err error) {
	e := Event{Kind: KindTool, Name: name, DurationMS: d.Milliseconds(), OK: err == nil,
		Outcome: ClassifyOutcome("", err)}
	if err != nil {
		e.Error = clip(err.Error(), 200)
	}
	r.Record(e)
}

// ToolCall records a tool call with the correlation a reader needs to tell
// recovery from thrashing: which exchange and round it belonged to, a
// fingerprint of its arguments, and how it actually ended.
func (r *Recorder) ToolCall(name, exchangeID string, round int, args map[string]any,
	d time.Duration, output string, err error) {
	e := Event{
		Kind: KindTool, Name: name, DurationMS: d.Milliseconds(), OK: err == nil,
		ExchangeID: exchangeID, Round: round, ArgsHash: HashArgs(args),
		Outcome: ClassifyOutcome(output, err),
	}
	if err != nil {
		e.Error = clip(err.Error(), 200)
	}
	r.Record(e)
}

// HashArgs fingerprints a tool's arguments so repeated identical calls are
// recognisable without ever writing the arguments themselves to disk — they
// routinely carry page text, file contents and search queries.
func HashArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys) // map order is random; the hash must not be
	h := sha1.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%v\x00", k, args[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// ModelCall records a language-model call.
//
// audio is the share of the input that came from sound, counted within in
// rather than in addition to it. It is billed at a different rate, so a spoken
// exchange and a typed one of the same length do not cost the same.
func (r *Recorder) ModelCall(name string, d time.Duration, in, out, cached, audio, thought int, err error) {
	e := Event{
		Kind: KindModel, Name: name, DurationMS: d.Milliseconds(),
		InputTokens: in, OutputTokens: out, CachedTokens: cached, AudioTokens: audio,
		ThoughtTokens: thought,
		OK:            err == nil,
		// out already includes thought tokens (they bill as output), so the cost
		// is complete; thought is passed only to record it on its own.
		CostUSD: EstimateCostAudio(name, in, out, cached, audio),
	}
	if err != nil {
		e.Error = clip(err.Error(), 200)
	}
	r.Record(e)
}

// Model records a text-only model call.
func (r *Recorder) Model(name string, d time.Duration, in, out, cached int, err error) {
	r.ModelCall(name, d, in, out, cached, 0, 0, err)
}

// Voice records speech in or out.
func (r *Recorder) Voice(what string, d time.Duration, err error) {
	e := Event{Kind: KindVoice, Name: what, DurationMS: d.Milliseconds(), OK: err == nil}
	if err != nil {
		e.Error = clip(err.Error(), 200)
	}
	r.Record(e)
}

// Guard records an assessment outcome.
func (r *Recorder) Guard(risk, outcome, detail string) {
	r.Record(Event{
		Kind: KindGuard, Name: risk, OK: outcome == "ok",
		Detail: outcome + ": " + clip(detail, 120),
	})
}

// flushInterval is how long an event may sit in the buffer before it is
// written.
//
// Telemetry that is one second stale is no less useful, and batching turns a
// syscall per event into a syscall per second. Measured: unbuffered, a burst of
// a thousand concurrent events dropped nearly half of them because the writer
// could not drain the channel fast enough. Buffered, it keeps up.
const flushInterval = time.Second

// writeLoop persists events until the recorder is closed.
func (r *Recorder) writeLoop() {
	defer close(r.done)

	buf := bufio.NewWriterSize(r.file, 32*1024)
	encoder := json.NewEncoder(buf)

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	flush := func() {
		if err := buf.Flush(); err == nil {
			return
		}
		// A failed flush would otherwise be retried forever against a buffer
		// that cannot drain. Dropping its contents keeps the loop alive, which
		// matters more than the rows.
		buf.Reset(r.file)
	}

	for {
		select {
		case e, ok := <-r.events:
			if !ok {
				flush()
				r.mu.Lock()
				if r.file != nil {
					_ = r.file.Close()
					r.file = nil
				}
				r.mu.Unlock()
				return
			}
			if err := encoder.Encode(e); err != nil {
				// A write failure must not kill the loop, or the channel fills
				// and every later event is dropped for a transient disk problem.
				continue
			}
			r.written.Add(1)

		case <-ticker.C:
			flush()
		}
	}
}

// drain discards events when no log could be opened.
func (r *Recorder) drain() {
	defer close(r.done)
	for range r.events {
		r.dropped.Add(1)
	}
}

// Close flushes and stops the recorder.
func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()

	close(r.events)
	select {
	case <-r.done:
	case <-time.After(3 * time.Second):
		// A stuck writer must not hold up shutdown.
	}
	return nil
}

// Stats reports what the recorder has handled this run.
func (r *Recorder) Stats() (written, dropped int64) {
	if r == nil {
		return 0, 0
	}
	return r.written.Load(), r.dropped.Load()
}

// Path reports where events are written.
func (r *Recorder) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// --- pricing ----------------------------------------------------------------

// Rates are per million tokens, in US dollars, verified against Google's
// published pricing page on 22 July 2026.
//
// # Why audio is separate
//
// Speech recognition here goes through Gemini, and audio input is billed at
// roughly double the text rate. Folding it into one input number would make
// voice look as cheap as typing, which it is not — a spoken session is the
// most expensive way to talk to this assistant, and the ledger should say so.
//
// # Why these are still called estimates
//
// The numbers are right; the total may not be. Free-tier requests cost
// nothing, batch requests cost half, cache storage is billed per hour and is
// not counted here at all, and Claude runs on a subscription where no token is
// billed individually. Treat a figure here as a sense of proportion — which
// habit is expensive — rather than as a bill.
type rate struct {
	input, output, cached float64
	// audioIn is the input rate for audio tokens. Zero means audio is billed
	// at the text rate.
	audioIn float64
	// audioCached is the cached rate for audio.
	audioCached float64
}

// rates is keyed by model-name substring, longest match winning so that
// "flash-lite" is never mistaken for "flash".
var rates = map[string]rate{
	"gemini-3.6-flash":      {input: 1.50, output: 7.50, cached: 0.15},
	"gemini-3.5-flash":      {input: 1.50, output: 9.00, cached: 0.15},
	"gemini-3.5-flash-lite": {input: 0.30, output: 2.50, cached: 0.03},
	"gemini-3.1-flash-lite": {input: 0.25, output: 1.50, cached: 0.025, audioIn: 0.50, audioCached: 0.05},
	"gemini-2.5-flash":      {input: 0.30, output: 2.50, cached: 0.03, audioIn: 1.00, audioCached: 0.10},
	"gemini-2.5-flash-lite": {input: 0.10, output: 0.40, cached: 0.01, audioIn: 0.30, audioCached: 0.03},
	"gemini-2.5-pro":        {input: 1.25, output: 10.00, cached: 0.31},
}

// defaultRate is used for an unrecognised model, so an unknown never silently
// reads as free. It is deliberately on the expensive side of the range: an
// overstated cost prompts a question, an understated one does not.
var defaultRate = rate{input: 0.30, output: 2.50, cached: 0.03}

// rateFor finds the most specific matching rate.
//
// Longest match wins because "gemini-3.1-flash-lite" contains
// "gemini-3.1-flash" as a prefix in spirit if not in fact, and map iteration
// order in Go is random — a first-match-wins loop over a map would price the
// same model differently between runs.
func rateFor(model string) rate {
	best, bestLen := defaultRate, 0
	for name, candidate := range rates {
		if len(name) > bestLen && strings.Contains(model, name) {
			best, bestLen = candidate, len(name)
		}
	}
	return best
}

const perMillion = 1_000_000.0

// EstimateCost approximates the cost of a text call.
func EstimateCost(model string, in, out, cached int) float64 {
	return EstimateCostAudio(model, in, out, cached, 0)
}

// EstimateCostAudio approximates a call where some input was audio.
//
// audio is counted within in, not in addition to it: the provider reports one
// input total, of which some tokens came from sound.
func EstimateCostAudio(model string, in, out, cached, audio int) float64 {
	r := rateFor(model)

	if audio > in {
		audio = in
	}
	if cached > in {
		cached = in
	}

	// Cached tokens are billed at the cached rate whatever their medium. What
	// remains is split between audio and text at their own rates.
	fresh := in - cached
	if fresh < 0 {
		fresh = 0
	}
	freshAudio := audio
	if freshAudio > fresh {
		freshAudio = fresh
	}
	freshText := fresh - freshAudio

	audioRate, cachedAudioRate := r.audioIn, r.audioCached
	if audioRate == 0 {
		audioRate = r.input
	}
	if cachedAudioRate == 0 {
		cachedAudioRate = r.cached
	}

	// Cached audio is rare enough that attributing all cached tokens to text is
	// the safer simplification, except when the call was entirely audio.
	cachedRate := r.cached
	if audio > 0 && audio >= in-cached {
		cachedRate = cachedAudioRate
	}

	return float64(freshText)*r.input/perMillion +
		float64(freshAudio)*audioRate/perMillion +
		float64(cached)*cachedRate/perMillion +
		float64(out)*r.output/perMillion
}

// Rates reports the table, for showing the user what the arithmetic assumes.
func Rates() string {
	var names []string
	for name := range rates {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("Assumed prices, US dollars per million tokens (verified 22 Jul 2026):\n")
	fmt.Fprintf(&b, "  %-24s %8s %8s %8s %8s\n", "model", "input", "output", "cached", "audio in")
	for _, name := range names {
		r := rates[name]
		audio := "same"
		if r.audioIn > 0 {
			audio = fmt.Sprintf("$%.2f", r.audioIn)
		}
		fmt.Fprintf(&b, "  %-24s %8s %8s %8s %8s\n", name,
			fmt.Sprintf("$%.2f", r.input), fmt.Sprintf("$%.2f", r.output),
			fmt.Sprintf("$%.3f", r.cached), audio)
	}
	b.WriteString("\nClaude runs on a subscription: its calls are counted, not billed per token.")
	return b.String()
}
