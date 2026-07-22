// Package sentinel makes Freya proactive.
//
// # The hard part is not noticing, it is deciding
//
// Polling for state changes is trivial. The difficulty is judgement: an
// assistant that reports everything it sees gets muted within a day, and a
// muted assistant is worth less than a silent one, because the user has now
// learned to ignore it.
//
// So every observation carries a salience score, and speaking has a cost that
// the observation must clear:
//
//	notice → score → deduplicate → decide → speak
//
// # What earns an interruption
//
// Three things raise salience, and they multiply rather than add:
//
//   - consequence: what happens if this is ignored? A full disk stops work.
//     A battery at 8% loses it.
//   - actionability: can the user do something about it right now? A warning
//     they cannot act on is noise wearing the costume of information.
//   - novelty: has this already been said? The second telling is worth a
//     fraction of the first, and the fifth is an irritation.
//
// Novelty is where the memory layer earns its keep. Sentinel checks what Freya
// has already told the user, and decays repeats rather than re-reporting a
// disk that has been at 86% for a week.
package sentinel

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Urgency is how much consequence an observation carries.
type Urgency int

const (
	// UrgencyAmbient is conversational texture: worth mentioning if she is
	// already talking, never worth an interruption.
	UrgencyAmbient Urgency = iota
	// UrgencyNotable is worth saying next time the user is present.
	UrgencyNotable
	// UrgencyImportant should be raised soon.
	UrgencyImportant
	// UrgencyCritical interrupts whatever is happening.
	UrgencyCritical
)

func (u Urgency) String() string {
	switch u {
	case UrgencyAmbient:
		return "ambient"
	case UrgencyNotable:
		return "notable"
	case UrgencyImportant:
		return "important"
	case UrgencyCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// Observation is something a watcher noticed.
type Observation struct {
	// Key identifies the *kind* of observation, stable across occurrences, so
	// repeats of the same condition can be recognised and decayed.
	Key string
	// Summary is what Freya would say, in her own register.
	Summary string
	// Detail carries supporting facts for when she is asked to elaborate.
	Detail string
	// Urgency is the consequence of ignoring it.
	Urgency Urgency
	// Actionable marks observations the user can do something about now.
	Actionable bool
	// Seen is when the watcher noticed.
	Seen time.Time
	// Source names the watcher, for tracing.
	Source string
}

// Watcher inspects some part of the world and reports what it finds.
//
// Watchers must be cheap and must not block: they run on a timer, on a laptop,
// and a watcher that takes ten seconds is a watcher that gets removed.
type Watcher interface {
	Name() string
	// Interval is how often this watcher wants to run.
	Interval() time.Duration
	// Check returns any observations. Returning none is the common case and
	// must be cheap.
	Check(ctx context.Context) ([]Observation, error)
}

// Chattiness controls how much ambient commentary is allowed through.
//
// This is the single knob the user asked for: critical things always get
// through, and this decides how much *else* does.
type Chattiness int

const (
	// ChattyQuiet speaks only for important and critical observations.
	ChattyQuiet Chattiness = iota
	// ChattyBalanced adds notable ones. The default.
	ChattyBalanced
	// ChattyCompanion lets ambient commentary through too.
	ChattyCompanion
)

// ParseChattiness reads a setting name.
func ParseChattiness(s string) Chattiness {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "quiet", "minimal", "critical":
		return ChattyQuiet
	case "companion", "chatty", "talkative":
		return ChattyCompanion
	default:
		return ChattyBalanced
	}
}

func (c Chattiness) String() string {
	switch c {
	case ChattyQuiet:
		return "quiet"
	case ChattyCompanion:
		return "companion"
	default:
		return "balanced"
	}
}

// threshold is the minimum urgency this chattiness lets through.
func (c Chattiness) threshold() Urgency {
	switch c {
	case ChattyQuiet:
		return UrgencyImportant
	case ChattyCompanion:
		return UrgencyAmbient
	default:
		return UrgencyNotable
	}
}

// repeatPolicy governs how quickly a repeated observation may be raised again.
//
// The intervals climb steeply on purpose. Telling someone their disk is filling
// is useful once, tolerable twice, and insulting the fifth time.
var repeatPolicy = []time.Duration{
	0,                  // first time: immediately
	2 * time.Hour,      // second
	12 * time.Hour,     // third
	48 * time.Hour,     // fourth
	7 * 24 * time.Hour, // fifth and beyond
}

// history tracks what has already been said about a given key.
type history struct {
	Count int
	Last  time.Time
}

// Sentinel runs watchers and decides what is worth saying.
type Sentinel struct {
	// Chattiness gates ambient commentary.
	Chattiness Chattiness
	// Notify delivers an observation the sentinel has decided to raise.
	Notify func(Observation)

	mu       sync.Mutex
	watchers []Watcher
	said     map[string]*history
	pending  []Observation
	running  bool
	stop     chan struct{}
}

// New builds a sentinel.
func New(chattiness Chattiness, notify func(Observation)) *Sentinel {
	return &Sentinel{
		Chattiness: chattiness,
		Notify:     notify,
		said:       map[string]*history{},
		stop:       make(chan struct{}),
	}
}

// Add registers a watcher.
func (s *Sentinel) Add(w Watcher) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watchers = append(s.watchers, w)
}

// Watchers lists registered watcher names.
func (s *Sentinel) Watchers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.watchers))
	for _, w := range s.watchers {
		out = append(out, w.Name())
	}
	sort.Strings(out)
	return out
}

// Start runs every watcher on its own schedule until Stop is called.
func (s *Sentinel) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	watchers := append([]Watcher{}, s.watchers...)
	s.mu.Unlock()

	for _, w := range watchers {
		go s.runWatcher(ctx, w)
	}
}

// Stop halts the watchers.
func (s *Sentinel) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	s.running = false
	close(s.stop)
}

func (s *Sentinel) runWatcher(ctx context.Context, w Watcher) {
	// Run once immediately so a session starts with current state rather than
	// waiting a full interval to notice a disk that is already full.
	s.checkOnce(ctx, w)

	ticker := time.NewTicker(w.Interval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-ticker.C:
			s.checkOnce(ctx, w)
		}
	}
}

func (s *Sentinel) checkOnce(ctx context.Context, w Watcher) {
	// A watcher that hangs must not wedge the whole sentinel.
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	observations, err := w.Check(checkCtx)
	if err != nil {
		return // a failing watcher is silent, not noisy
	}
	for _, o := range observations {
		if o.Seen.IsZero() {
			o.Seen = time.Now()
		}
		if o.Source == "" {
			o.Source = w.Name()
		}
		s.consider(o)
	}
}

// consider decides whether an observation is worth *interrupting* for.
//
// Retrievability and interruption are deliberately independent. Every current
// observation stays in the queue whether or not it was announced, because the
// alternative is incoherence: an early version interrupted with "your disk is
// 86% full" and then, asked what was going on moments later, answered
// "nothing, you're running clean". Announcing something must never remove it
// from what she knows.
func (s *Sentinel) consider(o Observation) {
	s.mu.Lock()
	s.queue(o)

	// Below the chattiness threshold: retrievable on request, never announced.
	if o.Urgency < s.Chattiness.threshold() {
		s.mu.Unlock()
		return
	}

	h := s.said[o.Key]
	if h == nil {
		h = &history{}
		s.said[o.Key] = h
	}

	// Novelty decay: the same condition earns silence unless enough time has
	// passed, and the bar rises each time.
	if h.Count > 0 {
		idx := h.Count
		if idx >= len(repeatPolicy) {
			idx = len(repeatPolicy) - 1
		}
		wait := repeatPolicy[idx]
		// Critical conditions still repeat, just far less often.
		if o.Urgency == UrgencyCritical {
			wait /= 4
		}
		if time.Since(h.Last) < wait {
			s.mu.Unlock()
			return
		}
	}

	h.Count++
	h.Last = time.Now()
	notify := s.Notify
	s.mu.Unlock()

	if notify != nil {
		notify(o)
	}
}

// queue holds an observation for later retrieval, replacing any older entry
// with the same key so the backlog does not accumulate duplicates.
func (s *Sentinel) queue(o Observation) {
	for i, existing := range s.pending {
		if existing.Key == o.Key {
			s.pending[i] = o
			return
		}
	}
	s.pending = append(s.pending, o)
	// Bound the backlog; the oldest low-urgency items go first.
	const maxPending = 50
	if len(s.pending) > maxPending {
		s.pending = s.pending[len(s.pending)-maxPending:]
	}
}

// Pending returns queued observations, most urgent first, and clears the queue.
func (s *Sentinel) Pending() []Observation {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := append([]Observation{}, s.pending...)
	s.pending = nil
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Urgency != out[j].Urgency {
			return out[i].Urgency > out[j].Urgency
		}
		return out[i].Seen.After(out[j].Seen)
	})
	return out
}

// Peek returns queued observations without clearing them.
func (s *Sentinel) Peek() []Observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Observation{}, s.pending...)
}

// MarkSaid records that something was mentioned, so repeats decay even when the
// mention came from elsewhere — a direct answer rather than an interruption.
func (s *Sentinel) MarkSaid(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.said[key]
	if h == nil {
		h = &history{}
		s.said[key] = h
	}
	h.Count++
	h.Last = time.Now()
}

// Describe renders an observation for display.
func (o Observation) Describe() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s] %s", o.Urgency, o.Summary)
	if o.Detail != "" {
		fmt.Fprintf(&sb, " (%s)", o.Detail)
	}
	return sb.String()
}
