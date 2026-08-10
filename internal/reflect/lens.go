// Package reflect gives Freya more than one reading of her own memory.
//
// # Why lenses rather than personalities
//
// A single retrieval pass returns a single angle: BM25 takes the user's words
// and finds text sharing that vocabulary. It cannot surface "you tried this in
// March and it went badly" unless March happened to use the same nouns.
// Experience is the accumulation of many readings of the same events, and one
// query produces one reading.
//
// The obvious fix is to run a crowd of background agents with different
// personalities. That fails for a reason worth writing down: agents differing
// only by a persona label, over the same model and the same memory, converge.
// You get twenty rephrasings of the same three observations, at twenty times
// the cost, and no way to threshold subjective opinions that all overlap.
//
// Diversity has to come from the *input*, not the costume. So each lens uses a
// different search strategy over the archive — different query construction,
// different time slice, different comparison — and therefore finds genuinely
// different things. Most need no model call at all: they are arithmetic and
// string comparison over data already in memory.
//
// # Cost discipline
//
// Lenses run *after* a turn completes, never in front of the reply. An
// assistant that pauses to be insightful is just slow. Findings surface on the
// next exchange or as a proactive interjection, gated by the same salience
// machinery that governs the sentinel.
package reflect

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Akins20/FREYA/internal/memory"
)

// Insight is something a lens noticed that the main conversational pass did not.
type Insight struct {
	// Lens names the strategy that found it.
	Lens string
	// Key identifies the observation for deduplication.
	Key string
	// Summary is the point, phrased for Freya to deliver.
	Summary string
	// Evidence is what the lens actually found, so the claim can be checked.
	Evidence string
	// Weight is confidence times consequence, in [0,1]. Only insights above
	// the surfacing threshold are ever mentioned.
	Weight float64
	// Found is when the lens ran.
	Found time.Time
}

// Lens examines memory from one angle.
//
// Look must be cheap and must not call a model. Lenses that want language-model
// phrasing return raw findings and let the caller decide whether the finding is
// worth the call.
type Lens interface {
	Name() string
	// Look inspects memory in the light of the latest exchange.
	Look(ctx context.Context, in Input) ([]Insight, error)
}

// Input is what the lenses get to reason over.
type Input struct {
	// Query is the user's latest message.
	Query string
	// Reply is what Freya just said.
	Reply string
	// Store is the memory archive — or, for a background job, that job's
	// isolated view of it.
	Store memory.Journal
	// Index is the search index.
	Index *memory.Index
	// Now is injectable for testing.
	Now time.Time
}

// surfacingThreshold is the weight an insight must clear to be mentioned.
//
// Set deliberately high. The failure mode of this whole idea is a stream of
// mildly-interesting observations that train the user to ignore them, and an
// ignored insight is worse than no insight because it cost tokens to produce.
const surfacingThreshold = 0.55

// Reflector runs the lenses and arbitrates between what they find.
type Reflector struct {
	// Threshold overrides surfacingThreshold.
	Threshold float64
	// MaxConcurrent bounds how many lenses run at once. Zero means all of them;
	// they are cheap.
	MaxConcurrent int

	mu       sync.Mutex
	lenses   []Lens
	surfaced map[string]time.Time
	pending  []Insight
}

// New builds a reflector with the default lens set.
func New() *Reflector {
	r := &Reflector{surfaced: map[string]time.Time{}}
	r.Add(&ContradictionLens{})
	r.Add(&PrecedentLens{})
	r.Add(&PatternLens{})
	r.Add(&StalenessLens{})
	r.Add(&ConsequenceLens{})
	r.Add(&ArcLens{})
	return r
}

// Add registers a lens, replacing any existing one of the same name.
//
// Replacing rather than appending matters: New() installs a default set, and a
// caller that supplies a better-configured version of one — a ConsequenceLens
// wired to real deadlines, say — must not end up with both running and
// double-reporting.
func (r *Reflector) Add(l Lens) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, existing := range r.lenses {
		if existing.Name() == l.Name() {
			r.lenses[i] = l
			return
		}
	}
	r.lenses = append(r.lenses, l)
}

// Lenses lists registered lens names.
func (r *Reflector) Lenses() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.lenses))
	for _, l := range r.lenses {
		out = append(out, l.Name())
	}
	sort.Strings(out)
	return out
}

// Reflect runs every lens over the latest exchange and queues what survives
// arbitration.
//
// It is safe to call in a goroutine after replying; nothing here blocks the
// conversation.
func (r *Reflector) Reflect(ctx context.Context, in Input) []Insight {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}

	r.mu.Lock()
	lenses := append([]Lens{}, r.lenses...)
	threshold := r.Threshold
	r.mu.Unlock()
	if threshold == 0 {
		threshold = surfacingThreshold
	}

	// Lenses are independent and cheap, so run them together.
	var wg sync.WaitGroup
	results := make([][]Insight, len(lenses))
	for i, l := range lenses {
		wg.Add(1)
		go func(i int, l Lens) {
			defer wg.Done()
			found, err := l.Look(ctx, in)
			if err != nil {
				return // a failing lens is silent, like a failing watcher
			}
			for j := range found {
				found[j].Lens = l.Name()
				found[j].Found = in.Now
			}
			results[i] = found
		}(i, l)
	}
	wg.Wait()

	var all []Insight
	for _, batch := range results {
		all = append(all, batch...)
	}
	return r.arbitrate(all, threshold, in.Now)
}

// arbitrate applies weighting, deduplication and novelty decay.
//
// This reuses the sentinel's discipline rather than inventing a second one:
// something already said recently is not said again, and only the strongest
// finding per key survives.
func (r *Reflector) arbitrate(candidates []Insight, threshold float64, now time.Time) []Insight {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Strongest first, so the survivor of a key collision is the best one.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Weight > candidates[j].Weight
	})

	seen := map[string]bool{}
	var kept []Insight

	for _, c := range candidates {
		if c.Weight < threshold {
			continue
		}
		if seen[c.Key] {
			continue // a stronger finding already covers this
		}
		// Novelty: the same point may not be made twice in a day.
		if last, ok := r.surfaced[c.Key]; ok && now.Sub(last) < 24*time.Hour {
			continue
		}
		seen[c.Key] = true
		r.surfaced[c.Key] = now
		kept = append(kept, c)
	}

	// One insight per exchange. Two competing "by the way" interjections in a
	// single reply is exactly the mess this is meant to avoid.
	if len(kept) > 1 {
		kept = kept[:1]
	}
	r.pending = append(r.pending, kept...)
	return kept
}

// Pending returns queued insights and clears the queue.
func (r *Reflector) Pending() []Insight {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]Insight{}, r.pending...)
	r.pending = nil
	return out
}

// Peek returns queued insights without clearing them.
func (r *Reflector) Peek() []Insight {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Insight{}, r.pending...)
}

// Describe renders an insight for display.
func (i Insight) Describe() string {
	s := fmt.Sprintf("[%s %.2f] %s", i.Lens, i.Weight, i.Summary)
	if i.Evidence != "" {
		s += "\n    evidence: " + strings.TrimSpace(i.Evidence)
	}
	return s
}
