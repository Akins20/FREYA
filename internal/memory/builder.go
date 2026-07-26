package memory

import (
	"fmt"
	"strings"
	"time"

	"github.com/akins/jarvis/internal/llm"
	"github.com/akins/jarvis/internal/sentinel"
)

// ContextBuilder assembles the tiered prompt sent to the model.
//
// Tier order is deliberate and load-bearing — see the package doc. Stable
// tiers lead so Gemini's implicit prefix cache keeps hitting; volatile tiers
// trail so a changed retrieval set cannot invalidate everything before it.
type ContextBuilder struct {
	Store  *Store
	Index  *Index
	Budget Budget

	// Persona is Freya's character prompt, prepended to the identity tier.
	Persona string
	// RetrieveLimit caps how many archived documents may be pulled back in.
	RetrieveLimit int
	// SessionStart is when this run began, for temporal grounding.
	SessionStart time.Time
	// LastSeen is when the user was last active, for noticing long gaps.
	LastSeen time.Time
	// Insights supplies background observations from the reflection lenses.
	// Placed in the volatile tail so it never disturbs the cached prefix.
	Insights func() []string
}

// NewContextBuilder wires a builder with sensible defaults.
func NewContextBuilder(store *Store, index *Index, persona string) *ContextBuilder {
	return &ContextBuilder{
		Store:         store,
		Index:         index,
		Budget:        DefaultBudget(),
		Persona:       persona,
		RetrieveLimit: 12,
		SessionStart:  time.Now(),
	}
}

// Build produces the system prompt and message history for the next request.
//
// query is the user's incoming turn; it drives retrieval but is not itself
// appended — the caller adds it, so it can be recorded and echoed separately.
func (b *ContextBuilder) Build(query string) (system string, msgs []llm.Message, snap Snapshot) {
	alloc := newAllocator(b.Budget.Usable())

	// --- Tier 1: identity (static, leads the cached prefix) -----------------
	identity := b.buildIdentity(alloc.take(b.Budget.Identity))
	snap.IdentityTokens = EstimateTokens(identity)
	alloc.spend(snap.IdentityTokens)

	// --- Tier 2: durable facts (rarely change) ------------------------------
	factText, factCount := b.buildFacts(alloc.take(b.Budget.Facts))
	snap.FactTokens = EstimateTokens(factText)
	snap.FactCount = factCount
	alloc.spend(snap.FactTokens)

	// --- Tier 3: episodes (append-only summaries) ---------------------------
	episodeText, episodeCount := b.buildEpisodes(alloc.take(b.Budget.Episodes))
	snap.EpisodeTokens = EstimateTokens(episodeText)
	snap.EpisodeCount = episodeCount
	alloc.spend(snap.EpisodeTokens)

	// The system prompt holds every stable tier, so its bytes change rarely.
	var sys strings.Builder
	sys.WriteString(identity)
	if factText != "" {
		sys.WriteString("\n\n# What I know\n")
		sys.WriteString(factText)
	}
	if episodeText != "" {
		sys.WriteString("\n\n# Earlier sessions\n")
		sys.WriteString(episodeText)
	}

	// Temporal grounding, placed AFTER the stable tiers so its every-minute change
	// re-processes only this trailing block and leaves the cached prefix intact.
	// It is framed as a live clock she is expected to keep in mind, not passive
	// trivia: the hour, how long the session has run, how long since she last saw
	// the user — all of it should colour her replies and her sense of what is
	// time-sensitive without being asked. Read fresh here every turn, so it is
	// always current.
	sys.WriteString("\n\n# Right now — the time, always keep it in mind\n")
	sys.WriteString(sentinel.TimeContext(time.Now(), b.SessionStart, b.LastSeen))
	system = sys.String()

	// --- Tier 4: working set (verbatim, append-only) ------------------------
	working, evicted := b.Store.WorkingSet(alloc.take(b.Budget.Working), b.Budget.EvictionChunk)
	if len(evicted) > 0 {
		// Nothing is lost: aged-out turns become a summary and stay searchable.
		b.archiveEvicted(evicted)
	}
	for _, t := range working {
		snap.WorkingTokens += t.Tokens
		msgs = append(msgs, turnToMessage(t))
	}
	snap.WorkingTurns = len(working)
	alloc.spend(snap.WorkingTokens)

	// --- Tier 5: retrieval (volatile, trails the stable prefix) -------------
	if b.Index != nil && query != "" {
		inWorking := make(map[string]bool, len(working))
		for _, t := range working {
			inWorking[t.ID] = true
		}
		retrieved, count := b.buildRetrieved(query, inWorking, alloc.take(b.Budget.Retrieved))
		if retrieved != "" {
			msgs = append(msgs, llm.Message{Role: llm.RoleUser, Text: retrieved})
			snap.RetrievedTokens = EstimateTokens(retrieved)
			snap.RetrievedCount = count
			alloc.spend(snap.RetrievedTokens)
		}
	}

	// --- Tier 6: reflection (most volatile of all) --------------------------
	// Angles the lenses found that a single retrieval pass would not. Kept
	// last so a changing insight cannot invalidate anything cached before it.
	if b.Insights != nil {
		if notes := b.Insights(); len(notes) > 0 {
			text := "[Something you noticed while thinking, worth raising only if it " +
				"genuinely helps — do not force it into the reply]\n- " +
				strings.Join(notes, "\n- ")
			msgs = append(msgs, llm.Message{Role: llm.RoleUser, Text: text})
			snap.RetrievedTokens += EstimateTokens(text)
		}
	}

	snap.TotalTokens = snap.IdentityTokens + snap.FactTokens + snap.EpisodeTokens +
		snap.WorkingTokens + snap.RetrievedTokens
	return system, msgs, snap
}

func (b *ContextBuilder) buildIdentity(budget int) string {
	var sb strings.Builder
	sb.WriteString(b.Persona)

	if curated := strings.TrimSpace(b.Store.Identity()); curated != "" {
		sb.WriteString("\n\n# Operator profile\n")
		sb.WriteString(curated)
	}

	// Pinned facts are identity, not trivia — they always ship.
	var pinned []string
	for _, f := range b.Store.Facts() {
		if f.Pinned {
			pinned = append(pinned, "- "+f.Text)
		}
	}
	if len(pinned) > 0 {
		sb.WriteString("\n\n# Standing directives\n")
		sb.WriteString(strings.Join(pinned, "\n"))
	}

	// The clock deliberately does NOT live here. This is the identity tier — the
	// opening, most-stable bytes that Gemini caches as a prefix — and a time that
	// changes every minute would invalidate that cache each turn and push the
	// facts and episodes below it out of the cacheable prefix. Temporal grounding
	// is volatile, so it trails the stable tiers instead (see Build).
	return truncateTokens(sb.String(), budget)
}

func (b *ContextBuilder) buildFacts(budget int) (string, int) {
	facts := b.Store.Facts()
	if len(facts) == 0 || budget <= 0 {
		return "", 0
	}

	var sb strings.Builder
	used, count := 0, 0
	for _, f := range facts {
		if f.Pinned {
			continue // already sent in the identity tier
		}
		line := fmt.Sprintf("- [%s] %s\n", f.Key, f.Text)
		cost := EstimateTokens(line)
		if used+cost > budget {
			break
		}
		sb.WriteString(line)
		used += cost
		count++
	}
	return sb.String(), count
}

func (b *ContextBuilder) buildEpisodes(budget int) (string, int) {
	episodes := b.Store.Episodes()
	if len(episodes) == 0 || budget <= 0 {
		return "", 0
	}

	var sb strings.Builder
	used, count := 0, 0
	for _, e := range episodes { // newest first
		line := fmt.Sprintf("- %s — %s\n",
			e.From.Format("2 Jan 2006 15:04"), strings.TrimSpace(e.Summary))
		cost := EstimateTokens(line)
		if used+cost > budget {
			break
		}
		sb.WriteString(line)
		used += cost
		count++
	}
	return sb.String(), count
}

func (b *ContextBuilder) buildRetrieved(query string, skip map[string]bool, budget int) (string, int) {
	if budget <= 0 {
		return "", 0
	}
	hits := b.Index.Search(query, b.RetrieveLimit*3)
	if len(hits) == 0 {
		return "", 0
	}

	var sb strings.Builder
	sb.WriteString("[Recalled from memory — older material matching this request]\n")
	used, count := EstimateTokens(sb.String()), 0

	for _, h := range hits {
		if count >= b.RetrieveLimit {
			break
		}
		if skip[h.ID] {
			continue // already present verbatim in the working set
		}
		line := fmt.Sprintf("- (%s) %s\n", h.Kind, collapse(h.Text, 600))
		cost := EstimateTokens(line)
		if used+cost > budget {
			break
		}
		sb.WriteString(line)
		used += cost
		count++
	}
	if count == 0 {
		return "", 0
	}
	return sb.String(), count
}

// archiveEvicted records a placeholder episode for turns leaving the verbatim
// tier. The summary is mechanical; agent.Distil replaces it with a model-written
// one when a provider is available.
func (b *ContextBuilder) archiveEvicted(evicted []Turn) {
	if len(evicted) == 0 {
		return
	}
	var topics []string
	var sb strings.Builder
	for _, t := range evicted {
		if t.Role == "user" {
			sb.WriteString(collapse(t.Text, 160))
			sb.WriteString("; ")
			if len(topics) < 8 {
				topics = append(topics, firstWords(t.Text, 4))
			}
		}
	}

	ids := make([]string, 0, len(evicted))
	for _, t := range evicted {
		ids = append(ids, t.ID)
	}

	_ = b.Store.AddEpisode(Episode{
		From:    evicted[0].Timestamp,
		To:      evicted[len(evicted)-1].Timestamp,
		Summary: fmt.Sprintf("%d turns. Topics raised: %s", len(evicted), collapse(sb.String(), 1200)),
		Topics:  topics,
		TurnIDs: ids,
	})
}

func turnToMessage(t Turn) llm.Message {
	switch t.Role {
	case "assistant":
		return llm.Message{Role: llm.RoleAssistant, Text: t.Text}
	case "tool":
		return llm.Message{Role: llm.RoleTool, Text: t.Text, ToolName: t.ToolName, ToolCallID: t.ID}
	default:
		return llm.Message{Role: llm.RoleUser, Text: t.Text}
	}
}

// truncateTokens trims text to an approximate token budget, cutting at a line
// boundary where possible so the result stays readable.
func truncateTokens(s string, budget int) string {
	if budget <= 0 || EstimateTokens(s) <= budget {
		return s
	}
	limit := int(float64(budget) * charsPerToken)
	if limit >= len(s) {
		return s
	}
	cut := s[:limit]
	if nl := strings.LastIndex(cut, "\n"); nl > limit/2 {
		cut = cut[:nl]
	}
	return cut + "\n[...truncated]"
}

// collapse flattens whitespace and clips to n characters.
func collapse(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

func firstWords(s string, n int) string {
	f := strings.Fields(s)
	if len(f) > n {
		f = f[:n]
	}
	return strings.Join(f, " ")
}
