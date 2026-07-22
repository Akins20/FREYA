// Package memory implements Freya's tiered memory and the context assembler
// that packs those tiers into a 1M-token prompt.
//
// # Why tiers
//
// A 1M window is not an excuse to dump everything into every request. Two
// forces shape the design:
//
//  1. Cost and latency scale with tokens sent, even on flash-lite. Context
//     should grow only as the relationship does.
//  2. Gemini caches stable prompt *prefixes*. A prompt whose opening bytes
//     change every turn is a prompt that never hits cache.
//
// So tiers are ordered most-stable first, and the volatile tiers sit at the
// end where they cannot invalidate the cached prefix:
//
//	[identity] [facts] [episodes] [working history] [retrieved] [current turn]
//	 static     rare     append      append-only      volatile    volatile
//
// # Where things live
//
//	Archive   append-only JSONL, every turn ever, never rewritten — source of truth
//	Working   the verbatim tail of the archive that fits the budget
//	Episodes  distilled summaries of turns that aged out of the working set
//	Facts     durable extracted knowledge, deduped and updatable
//	Identity  hand-editable markdown the user curates directly
//
// Nothing is ever destroyed. Turns degrade verbatim -> summarised -> searchable,
// and BM25 retrieval can pull any of it back into context on demand.
package memory

import "time"

// Turn is a single conversational event. Turns are appended to the archive and
// never mutated.
type Turn struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Timestamp time.Time `json:"ts"`
	Role      string    `json:"role"` // user | assistant | tool
	Text      string    `json:"text"`
	ToolName  string    `json:"tool_name,omitempty"`
	Tokens    int       `json:"tokens"`
}

// Episode is a distilled summary of turns that aged out of the working set.
// Episodes are how the system forgets gracefully: detail is traded for span.
type Episode struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Summary string    `json:"summary"`
	Topics  []string  `json:"topics,omitempty"`
	TurnIDs []string  `json:"turn_ids,omitempty"`
	Tokens  int       `json:"tokens"`
}

// Fact is durable extracted knowledge — the things worth remembering after the
// conversation that produced them is long gone.
type Fact struct {
	ID      string    `json:"id"`
	Key     string    `json:"key"` // stable handle, e.g. "prefers-go"
	Text    string    `json:"text"`
	Source  string    `json:"source,omitempty"` // provenance: turn ID or "user"
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	// Pinned facts are promoted into the identity tier and always sent.
	Pinned bool `json:"pinned,omitempty"`
	Tokens int  `json:"tokens"`
}

// Snapshot is the assembled result of one context build, returned alongside the
// prompt so callers can log or display what was actually sent.
type Snapshot struct {
	IdentityTokens  int
	FactTokens      int
	EpisodeTokens   int
	WorkingTokens   int
	RetrievedTokens int
	TotalTokens     int

	WorkingTurns   int
	EpisodeCount   int
	FactCount      int
	RetrievedCount int
}
