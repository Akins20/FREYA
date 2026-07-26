package memory

import "strings"

// Token accounting.
//
// Measured against Gemini's countTokens endpoint, ordinary English prose runs
// about 4.9 characters per token. We deliberately estimate at 4.0 instead: the
// error then lands on the safe side, reporting more tokens than really exist so
// budgets under-fill rather than overflow the window mid-request.
//
// Code and structured text tokenize denser than prose, which the conservative
// ratio also absorbs.
const charsPerToken = 4.0

// EstimateTokens approximates the token cost of a string. It never returns 0
// for non-empty input, so empty-ish entries still consume a slot in budgeting.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	n := int(float64(len(s))/charsPerToken) + 1
	// Newlines and structural punctuation tend to cost their own token.
	n += strings.Count(s, "\n")
	return n
}

// Budget describes how the context window is divided between tiers.
//
// The defaults target gemini-3.5-flash-lite's 1,048,576-token input window.
// Roughly 48K is held back for tool schemas, formatting overhead, and
// estimation error.
type Budget struct {
	// Window is the model's total input capacity.
	Window int
	// Reserve is held back from Window for tool declarations and slack.
	Reserve int

	Identity  int
	Facts     int
	Episodes  int
	Working   int
	Retrieved int

	// EvictionChunk is the fraction of the working set dropped at once when it
	// overflows. Evicting in large blocks rather than one turn at a time keeps
	// the cached prompt prefix stable across many subsequent requests; sliding
	// by a single turn would invalidate the cache on every exchange.
	EvictionChunk float64
}

// DefaultBudget returns the tier allocation for a 1M-token window.
func DefaultBudget() Budget {
	return Budget{
		Window:  1_048_576,
		Reserve: 48_576,

		Identity:  8_000,   // persona + curated profile + pinned facts
		Facts:     32_000,  // durable extracted knowledge
		Episodes:  96_000,  // summaries of aged-out conversation
		Working:   704_000, // verbatim recent turns — the point of a 1M window
		Retrieved: 160_000, // BM25 hits pulled back from the archive

		EvictionChunk: 0.25,
	}
}

// ScaleTo proportionally rescales tier budgets for a different window size, so
// the same architecture runs unchanged against a 128K or 32K model.
func (b Budget) ScaleTo(window int) Budget {
	oldUsable := b.Usable()
	b.Window = window
	newUsable := b.Usable()
	if oldUsable <= 0 || newUsable <= 0 {
		return b
	}
	ratio := float64(newUsable) / float64(oldUsable)
	scale := func(v int) int { return int(float64(v) * ratio) }

	b.Identity = scale(b.Identity)
	b.Facts = scale(b.Facts)
	b.Episodes = scale(b.Episodes)
	b.Working = scale(b.Working)
	b.Retrieved = scale(b.Retrieved)
	return b
}

// Usable is the token budget available to all tiers combined.
func (b Budget) Usable() int {
	u := b.Window - b.Reserve
	if u < 0 {
		return 0
	}
	return u
}

// allocator hands out tokens tier by tier, cascading whatever a tier does not
// spend into the tiers that follow. A short conversation therefore sends a
// short prompt, and the window fills only as the relationship accumulates.
type allocator struct {
	remaining int
}

func newAllocator(total int) *allocator { return &allocator{remaining: total} }

// take reserves up to want tokens, capped by what is left.
func (a *allocator) take(want int) int {
	if want > a.remaining {
		want = a.remaining
	}
	if want < 0 {
		want = 0
	}
	return want
}

// spend records actual usage, releasing any unspent remainder to later tiers.
func (a *allocator) spend(used int) {
	a.remaining -= used
	if a.remaining < 0 {
		a.remaining = 0
	}
}
