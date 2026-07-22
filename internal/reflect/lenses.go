package reflect

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The six default lenses. Each answers a different question about the same
// archive, which is where the diversity comes from — not from personality.

// --- contradiction ----------------------------------------------------------

// ContradictionLens notices when the current request conflicts with something
// the user previously stated as settled.
//
// This is the highest-value lens because it catches the failure a single
// retrieval pass structurally cannot: agreeing enthusiastically with a request
// that undoes a decision made three weeks ago.
type ContradictionLens struct{}

func (ContradictionLens) Name() string { return "contradiction" }

// prohibitionPattern extracts what a remembered decision ruled *out*.
//
// The common shape of a real reversal is not two opposing adjectives — it is a
// fact that forbids something ("never MongoDB", "avoid global state") and a
// request that proposes exactly that thing. An earlier version looked for
// matching polarity word-pairs across the two texts and missed the obvious
// case entirely.
var prohibitionPattern = regexp.MustCompile(
	`(?i)\b(?:never|avoid|avoiding|don'?t use|do not use|stop using|no longer use|` +
		`instead of|rather than|not)\s+([a-z0-9][\w.+#-]{2,})`)

// preferencePattern extracts what a decision ruled *in*, so switching away
// from it also registers.
var preferencePattern = regexp.MustCompile(
	`(?i)\b(?:always use|prefer|stick (?:with|to)|standardis?e on|use)\s+([a-z0-9][\w.+#-]{2,})`)

// switchPattern marks a request that proposes replacing something.
var switchPattern = regexp.MustCompile(
	`(?i)\b(switch(?:ing)? to|move to|migrate to|replace .* with|change to|swap .* for|use)\b`)

func (ContradictionLens) Look(_ context.Context, in Input) ([]Insight, error) {
	if in.Store == nil || in.Query == "" {
		return nil, nil
	}
	query := strings.ToLower(in.Query)

	var out []Insight
	for _, f := range in.Store.Facts() {
		fact := strings.ToLower(f.Text)

		reversed := false

		// Case 1: the fact forbids something the request now proposes.
		for _, m := range prohibitionPattern.FindAllStringSubmatch(fact, -1) {
			banned := strings.Trim(m[1], ".,;:")
			if len(banned) < 3 || stopWords[banned] {
				continue
			}
			if strings.Contains(query, banned) {
				reversed = true
				break
			}
		}

		// Case 2: the request proposes switching away from a settled choice.
		if !reversed && switchPattern.MatchString(query) {
			for _, m := range preferencePattern.FindAllStringSubmatch(fact, -1) {
				chosen := strings.Trim(m[1], ".,;:")
				if len(chosen) < 3 || stopWords[chosen] {
					continue
				}
				// Same subject area, but the established choice is absent from
				// the request — something else is being proposed in its place.
				if !strings.Contains(query, chosen) && overlap(query, fact) >= 2 {
					reversed = true
					break
				}
			}
		}

		if !reversed {
			continue
		}

		// Older decisions carry more weight: reversing something settled long
		// ago is more surprising than changing yesterday's mind.
		age := in.Now.Sub(f.Updated)
		weight := 0.6
		switch {
		case f.Pinned:
			weight = 0.95 // reversing a standing directive always matters
		case age > 30*24*time.Hour:
			weight = 0.85
		case age > 7*24*time.Hour:
			weight = 0.75
		}

		out = append(out, Insight{
			Key:      "contradiction:" + f.Key,
			Summary:  fmt.Sprintf("this looks like it reverses something settled earlier: %q", f.Text),
			Evidence: fmt.Sprintf("fact [%s] recorded %s", f.Key, f.Updated.Format("2 Jan")),
			Weight:   weight,
		})
	}
	return out, nil
}

// --- precedent --------------------------------------------------------------

// PrecedentLens finds earlier attempts at the same thing and how they ended.
type PrecedentLens struct{}

func (PrecedentLens) Name() string { return "precedent" }

// failureWords mark a turn that recorded something going wrong.
var failureWords = regexp.MustCompile(`(?i)\b(failed|failing|broke|broken|error|crashed|didn't work|doesn't work|gave up|reverted|rolled back|wasted|never worked)\b`)

func (PrecedentLens) Look(_ context.Context, in Input) ([]Insight, error) {
	if in.Index == nil || in.Query == "" {
		return nil, nil
	}

	// Search deliberately wide, then filter for outcomes rather than topics —
	// the point is not "we discussed this" but "we discussed this and it went
	// badly", which vocabulary matching alone will not separate.
	hits := in.Index.Search(in.Query, 25)
	var out []Insight
	for _, h := range hits {
		if h.Kind == "fact" || !failureWords.MatchString(h.Text) {
			continue
		}
		if overlap(strings.ToLower(in.Query), strings.ToLower(h.Text)) < 2 {
			continue
		}
		out = append(out, Insight{
			Key:      "precedent:" + h.ID,
			Summary:  "there's an earlier attempt at something like this that didn't go well",
			Evidence: collapse(h.Text, 240),
			// Relevance from BM25, capped: a strong lexical match is suggestive,
			// never conclusive.
			Weight: minFloat(0.5+h.Score/20, 0.8),
		})
		break // one precedent is a useful reminder; three is a lecture
	}
	return out, nil
}

// --- pattern ----------------------------------------------------------------

// PatternLens notices repetition the user may not have registered.
type PatternLens struct {
	// Window is how far back to count. Zero means 30 days.
	Window time.Duration
	// MinOccurrences is the count worth mentioning.
	MinOccurrences int
}

func (PatternLens) Name() string { return "pattern" }

func (p PatternLens) Look(_ context.Context, in Input) ([]Insight, error) {
	window := p.Window
	if window == 0 {
		window = 30 * 24 * time.Hour
	}
	minCount := p.MinOccurrences
	if minCount == 0 {
		minCount = 4
	}

	terms := contentWords(in.Query)
	if len(terms) == 0 {
		return nil, nil
	}

	cutoff := in.Now.Add(-window)
	counts := map[string]int{}
	for _, t := range in.Store.Turns() {
		if t.Role != "user" || t.Timestamp.Before(cutoff) {
			continue
		}
		lower := strings.ToLower(t.Text)
		for _, term := range terms {
			if strings.Contains(lower, term) {
				counts[term]++
			}
		}
	}

	var out []Insight
	for term, n := range counts {
		if n < minCount {
			continue
		}
		out = append(out, Insight{
			Key:      "pattern:" + term,
			Summary:  fmt.Sprintf("%q has come up %d times in the last month — it might be worth solving properly rather than repeatedly", term, n),
			Evidence: fmt.Sprintf("%d mentions since %s", n, cutoff.Format("2 Jan")),
			// Confidence grows with count but saturates: twenty mentions is not
			// five times more meaningful than four.
			Weight: minFloat(0.45+float64(n)*0.05, 0.8),
		})
	}
	return out, nil
}

// --- staleness --------------------------------------------------------------

// StalenessLens flags remembered facts old enough to have quietly become false.
type StalenessLens struct {
	// StaleAfter is when a volatile fact should be re-checked.
	StaleAfter time.Duration
}

func (StalenessLens) Name() string { return "staleness" }

// volatileFacts describe things that change without anyone announcing it.
var volatileFacts = regexp.MustCompile(`(?i)\b(version|installed|running|currently|at the moment|this week|deadline|due|free space|percent|%|password|key|token|port|url|branch)\b`)

func (s StalenessLens) Look(_ context.Context, in Input) ([]Insight, error) {
	stale := s.StaleAfter
	if stale == 0 {
		stale = 60 * 24 * time.Hour
	}

	query := strings.ToLower(in.Query)
	var out []Insight
	for _, f := range in.Store.Facts() {
		if !volatileFacts.MatchString(f.Text) {
			continue
		}
		age := in.Now.Sub(f.Updated)
		if age < stale {
			continue
		}
		// Only worth raising if the conversation is actually touching it.
		if overlap(query, strings.ToLower(f.Text)) < 2 {
			continue
		}
		out = append(out, Insight{
			Key:      "stale:" + f.Key,
			Summary:  fmt.Sprintf("this relies on something recorded %d days ago that may have changed: %q", int(age.Hours()/24), f.Text),
			Evidence: fmt.Sprintf("fact [%s] last updated %s", f.Key, f.Updated.Format("2 Jan 2006")),
			Weight:   minFloat(0.5+age.Hours()/24/365, 0.8),
		})
	}
	return out, nil
}

// --- consequence ------------------------------------------------------------

// ConsequenceLens connects the current request to known commitments.
//
// Spending an afternoon refactoring is fine; spending it three days before a
// deadline is a decision worth making consciously.
type ConsequenceLens struct {
	// Deadlines supplies known commitments. Injected to avoid importing the
	// sentinel package, which already imports nothing.
	Deadlines func() ([]Deadline, error)
}

// Deadline is a known commitment with a date.
type Deadline struct {
	Text string
	When time.Time
}

func (ConsequenceLens) Name() string { return "consequence" }

// diversionWords mark work that is easy to justify and easy to regret.
var diversionWords = regexp.MustCompile(`(?i)\b(refactor|rewrite|redesign|reorganise|reorganize|clean up|tidy|migrate|experiment|try out|play with|explore|from scratch)\b`)

func (c ConsequenceLens) Look(_ context.Context, in Input) ([]Insight, error) {
	if c.Deadlines == nil || !diversionWords.MatchString(in.Query) {
		return nil, nil
	}
	deadlines, err := c.Deadlines()
	if err != nil {
		return nil, err
	}

	var out []Insight
	for _, d := range deadlines {
		remaining := d.When.Sub(in.Now)
		if remaining < 0 || remaining > 14*24*time.Hour {
			continue
		}
		days := int(remaining.Hours() / 24)
		weight := 0.6
		if days <= 3 {
			weight = 0.9
		} else if days <= 7 {
			weight = 0.75
		}
		out = append(out, Insight{
			Key:      "consequence:" + d.Text,
			Summary:  fmt.Sprintf("worth noting this is optional work with %d days left on: %s", days, d.Text),
			Evidence: "deadline " + d.When.Format("Mon 2 Jan"),
			Weight:   weight,
		})
	}
	return out, nil
}

// --- arc --------------------------------------------------------------------

// ArcLens tracks how the user has been feeling about a topic over time.
//
// Not sentiment analysis for its own sake: knowing that the last four mentions
// of a subject were frustrated changes what a useful reply looks like.
type ArcLens struct {
	// Window is how far back to read the mood. Zero means 14 days.
	Window time.Duration
}

func (ArcLens) Name() string { return "arc" }

var frustrationWords = regexp.MustCompile(`(?i)\b(stuck|frustrat\w+|annoy\w+|hate|sick of|fed up|why (won'?t|doesn'?t)|still (not|broken)|again|ugh|argh|stressed|overwhelm\w+|exhaust\w+|burn(ed|t) out)\b`)

func (a ArcLens) Look(_ context.Context, in Input) ([]Insight, error) {
	window := a.Window
	if window == 0 {
		window = 14 * 24 * time.Hour
	}

	terms := contentWords(in.Query)
	if len(terms) == 0 {
		return nil, nil
	}
	cutoff := in.Now.Add(-window)

	frustrated, total := 0, 0
	var latest time.Time
	for _, t := range in.Store.Turns() {
		if t.Role != "user" || t.Timestamp.Before(cutoff) {
			continue
		}
		lower := strings.ToLower(t.Text)
		relevant := false
		for _, term := range terms {
			if strings.Contains(lower, term) {
				relevant = true
				break
			}
		}
		if !relevant {
			continue
		}
		total++
		if frustrationWords.MatchString(lower) {
			frustrated++
			if t.Timestamp.After(latest) {
				latest = t.Timestamp
			}
		}
	}

	// A single bad day is not an arc. Three out of a handful is.
	if frustrated < 3 || total < 3 {
		return nil, nil
	}
	ratio := float64(frustrated) / float64(total)
	if ratio < 0.5 {
		return nil, nil
	}

	return []Insight{{
		Key: "arc:" + terms[0],
		Summary: fmt.Sprintf("this topic has been going badly for a while — %d of the last %d times it came up, they were frustrated. Might be worth addressing that rather than just the question",
			frustrated, total),
		Evidence: fmt.Sprintf("most recent %s", latest.Format("2 Jan")),
		Weight:   minFloat(0.5+ratio*0.35, 0.85),
	}}, nil
}

// --- helpers ----------------------------------------------------------------

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "you": true, "that": true, "this": true,
	"with": true, "was": true, "are": true, "but": true, "not": true, "have": true,
	"can": true, "will": true, "what": true, "how": true, "why": true, "should": true,
	"would": true, "could": true, "from": true, "into": true, "about": true,
	"just": true, "like": true, "get": true, "got": true, "make": true, "let": true,
	"need": true, "want": true, "please": true, "help": true, "does": true,
}

// contentWords extracts the meaningful terms of a message.
func contentWords(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
	var out []string
	seen := map[string]bool{}
	for _, f := range fields {
		if len(f) < 4 || stopWords[f] || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// overlap counts shared content words, the cheap proxy for "about the same
// thing" that keeps every lens from firing on every turn.
func overlap(a, b string) int {
	set := map[string]bool{}
	for _, w := range contentWords(a) {
		set[w] = true
	}
	n := 0
	for _, w := range contentWords(b) {
		if set[w] {
			n++
		}
	}
	return n
}

func collapse(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// compile-time assurance every lens satisfies the interface.
var (
	_ Lens = (*ContradictionLens)(nil)
	_ Lens = (*PrecedentLens)(nil)
	_ Lens = (*PatternLens)(nil)
	_ Lens = (*StalenessLens)(nil)
	_ Lens = (*ConsequenceLens)(nil)
	_ Lens = (*ArcLens)(nil)
)
