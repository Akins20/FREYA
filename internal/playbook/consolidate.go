package playbook

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Noticing that she has learned the same thing twice.
//
// # Why this is needed at all
//
// Learned playbooks accumulate junk by construction. She signs into the portal
// on Monday and records it; the flow changes slightly and on Thursday she
// records it again under a different name; neither entry knows about the other.
// Twenty near-identical "signed into the portal" entries are worse than one, and
// they are worse in the expensive place: the index rides the volatile tail, so
// it is paid for on every single turn.
//
// Forgetting is the genuinely unsolved part of agent memory. The cap in
// learned.go is the crude version — least recently used goes — and it stops the
// store growing without bound while doing nothing at all about duplication.
//
// # Detection here, judgement elsewhere
//
// Deciding two playbooks are ABOUT the same thing is cheap and deterministic:
// they share vocabulary. Deciding what the merged one should SAY is not — it
// needs to keep the step that only appears in the older one, notice that the
// newer one contradicts it, and work out which is now true. That is judgement,
// and it is handed to Claude rather than guessed at with string operations.
//
// So this file only ever proposes. It finds candidates and it applies a merge
// somebody else decided on; it never rewrites a body itself.
//
// # Nothing is destroyed
//
// Anthropic's own memory-consolidation pass never modifies the input store — the
// merged result is written alongside so it can be discarded if it is wrong. The
// same instinct runs through this codebase: the archive is append-only, turns
// degrade rather than vanish, and the self-repair loop lands on a branch and is
// never deployed.
//
// A merge here therefore SUPERSEDES. The originals move aside with a note saying
// what replaced them, so a bad consolidation costs a lookup rather than the only
// copy of something she worked out.

// overlapThreshold is how much of the SHORTER description the two share.
//
// Measured as the overlap coefficient — shared over the smaller set — rather
// than Jaccard, which was tried first and failed on the case this exists for.
// "signing in to the UoPeople portal" against "signing in to the UoPeople portal
// each morning" scored 0.43 by Jaccard and was missed, because Jaccard divides
// by the union and so punishes one summary simply for being wordier than the
// other. Two descriptions of one job are routinely different lengths; that is
// not evidence they are different jobs.
//
// The threshold is still set high. A false positive proposes merging two
// genuinely different procedures, which — if the reviewer agrees — loses a
// distinction she earned. A false negative leaves a duplicate in the index for
// another day. Those costs are not symmetric, so this errs towards leaving
// things alone.
const overlapThreshold = 0.6

// minSubjectWords guards the shortest descriptions. The overlap coefficient
// divides by the smaller set, so a two-word summary sharing both its words
// scores a perfect 1.0 against almost anything — which says more about the
// summary being useless than about the two being the same.
const minSubjectWords = 3

// Overlap is a set of learned playbooks that look like the same subject.
type Overlap struct {
	Names []string
	Score float64
}

// Describe renders an overlap for a brief or a log line.
func (o Overlap) Describe() string {
	return fmt.Sprintf("%s (%.0f%% shared vocabulary)", strings.Join(o.Names, " + "), 100*o.Score)
}

// Overlaps finds pairs of learned playbooks that appear to cover one subject.
//
// Pairs rather than clusters, because a pair is what a merge brief can actually
// be written about: three-way overlaps show up as several pairs and are dealt
// with one merge at a time, which also means a wrong merge is smaller.
func (l *Learned) Overlaps() []Overlap {
	l.mu.Lock()
	type entry struct {
		name string
		bag  map[string]bool
	}
	var items []entry
	for name, s := range l.byID {
		items = append(items, entry{name, subjectWords(name + " " + s.Summary)})
	}
	l.mu.Unlock()

	sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })

	var out []Overlap
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			score := overlap(items[i].bag, items[j].bag)
			if score >= overlapThreshold {
				out = append(out, Overlap{
					Names: []string{items[i].name, items[j].name},
					Score: score,
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// Bodies returns the full text of named playbooks, for handing to a reviewer.
func (l *Learned) Bodies(names []string) []Skill {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Skill, 0, len(names))
	for _, n := range names {
		if s, ok := l.byID[normaliseName(n)]; ok {
			out = append(out, s)
		}
	}
	return out
}

// Supersede replaces several playbooks with one merged one.
//
// The originals are kept, marked with what replaced them. A consolidation that
// turns out to have thrown away the one useful step is then a lookup rather than
// a loss — which matters because the whole point of this store is that she has
// nowhere else to keep what she works out.
func (l *Learned) Supersede(replaced []string, merged Skill) error {
	merged.Name = normaliseName(merged.Name)
	if merged.Name == "" || strings.TrimSpace(merged.Summary) == "" ||
		strings.TrimSpace(merged.Body) == "" {
		return fmt.Errorf("a merged playbook needs a name, a summary and a body")
	}
	if _, clash := Get(merged.Name); clash {
		return fmt.Errorf("%q is a built-in skill; a merge may not shadow one", merged.Name)
	}

	now := time.Now()
	l.mu.Lock()
	// Refuse a merge that would leave nothing behind — replacing two entries with
	// one that has neither's name is how a consolidation quietly deletes both.
	var kept int
	for _, n := range replaced {
		if _, ok := l.byID[normaliseName(n)]; ok {
			kept++
		}
	}
	if kept == 0 {
		l.mu.Unlock()
		return fmt.Errorf("none of %v is a learned playbook", replaced)
	}

	for _, n := range replaced {
		n = normaliseName(n)
		s, ok := l.byID[n]
		if !ok || n == merged.Name {
			continue
		}
		l.gone = append(l.gone, supersededSkill{
			Name: n, Summary: s.Summary, Body: s.Body,
			ReplacedBy: merged.Name, ReplacedAt: now,
		})
		delete(l.byID, n)
		delete(l.used, n)
		delete(l.first, n)
	}
	if _, had := l.first[merged.Name]; !had {
		l.first[merged.Name] = now
	}
	l.byID[merged.Name] = merged
	l.used[merged.Name] = now
	l.evictLocked()
	snapshot := l.snapshotLocked()
	l.mu.Unlock()

	return l.save(snapshot)
}

// Superseded lists what has been merged away, newest first, so a consolidation
// that went wrong can be found and undone.
func (l *Learned) Superseded() []Skill {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Skill, 0, len(l.gone))
	for i := len(l.gone) - 1; i >= 0; i-- {
		g := l.gone[i]
		out = append(out, Skill{
			Name:    g.Name,
			Summary: fmt.Sprintf("%s [superseded by %s]", g.Summary, g.ReplacedBy),
			Body:    g.Body,
		})
	}
	return out
}

// subjectWords reduces text to the words that say what it is ABOUT.
func subjectWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(f) < 3 || subjectStop[f] {
			continue
		}
		out[strings.TrimSuffix(strings.TrimSuffix(f, "ing"), "s")] = true
	}
	return out
}

// overlap is the overlap coefficient: how much of the shorter description the
// two have in common. See overlapThreshold for why not Jaccard.
func overlap(a, b map[string]bool) float64 {
	smaller := len(a)
	if len(b) < smaller {
		smaller = len(b)
	}
	if smaller < minSubjectWords {
		return 0
	}
	var shared int
	for w := range a {
		if b[w] {
			shared++
		}
	}
	return float64(shared) / float64(smaller)
}

// subjectStop are words that say nothing about the subject. Smaller than a
// general stop list on purpose: this is comparing two short summaries, so
// dropping too much would make everything look alike.
var subjectStop = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "from": true,
	"how": true, "when": true, "into": true, "not": true, "this": true,
	"that": true, "you": true, "your": true, "use": true, "using": true,
}
