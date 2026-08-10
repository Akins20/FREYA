package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/claude"
	"github.com/Akins20/FREYA/internal/playbook"
)

// Letting Claude tidy up what she has taught herself.
//
// # Why anything reviews this at all
//
// Learned playbooks duplicate by construction. She works out the portal sign-in
// on Monday and writes it down; the flow shifts and on Thursday she writes it
// down again under a different name; neither entry knows about the other. The
// index rides the volatile tail, so every duplicate is paid for on every turn,
// and worse, two entries for one job is a choice she has to make every time she
// looks — the same trap two selector-click tools were.
//
// # Why Claude and not the user
//
// The obvious design was a review surface: print what she has learned, flag the
// overlaps, let a human merge them. That was wrong for this house — the user
// does not drive her CLI, they talk to her. A review step nobody performs is a
// queue that grows forever.
//
// So the reviewer is Claude, on the most capable model available, which is also
// the honest allocation: detecting that two playbooks are ABOUT the same thing
// is cheap and deterministic and stays in Go (internal/playbook/consolidate.go),
// while deciding what the merged one should SAY is judgement — keep the step
// that only exists in the older one, notice where the newer one contradicts it,
// work out which is now true. That is worth a good model and it runs rarely.
//
// # Nothing is destroyed
//
// Anthropic's own consolidation pass never modifies the input store, so a bad
// merge can be discarded. Here the originals are SUPERSEDED rather than deleted
// — moved aside with a note saying what replaced them. This store is the only
// place she keeps what she works out, so a wrong merge has to cost a lookup
// rather than the only copy.

const (
	// consolidateEvery is how often the pass wakes up. Rare on purpose: this is
	// housekeeping, it competes with the user for the same machine, and there is
	// nothing urgent about a duplicate.
	consolidateEvery = 6 * time.Hour
	// consolidateModel is the reviewer.
	//
	// Fable, the strongest available, and the cost argument runs the other way
	// from usual here. This fires at most once every six hours, on two short
	// playbooks, and only when they already look like duplicates — so the whole
	// job is a few thousand tokens of the most careful reading available. What it
	// is deciding is which of two accounts of the same work is still true, over
	// text she cannot reconstruct if the merge throws the wrong half away. Paying
	// twice the rate a handful of times a day to not lose that is not a close
	// call.
	consolidateModel = "fable"
	// mergesPerPass keeps a wrong idea small. One merge per wake-up, highest
	// overlap first, so a bad call affects one pair rather than the whole store.
	mergesPerPass = 1
)

// tidier merges the playbooks she has learned twice.
type tidier struct {
	learned *playbook.Learned
	claude  *claude.Client
}

// runConsolidation starts the housekeeping loop. It is silent when there is
// nothing overlapping, which is most of the time.
func (t *tidier) run(ctx context.Context) {
	if t == nil || t.learned == nil || t.claude == nil {
		return
	}
	ticker := time.NewTicker(consolidateEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Never while she is working. This is a long call competing for the
			// same machine and the same rate-limit window as whatever the user is
			// actually waiting on — the same rule the engineer consult follows.
			if currentTurn() != nil || (jobs != nil && jobs.Active() > 0) {
				continue
			}
			t.once(ctx)
		}
	}
}

// once does at most one merge.
func (t *tidier) once(ctx context.Context) {
	overlaps := t.learned.Overlaps()
	if len(overlaps) == 0 {
		return
	}
	for i, o := range overlaps {
		if i >= mergesPerPass {
			return
		}
		t.merge(ctx, o)
	}
}

// merge asks Claude to fold one overlapping pair into a single playbook.
func (t *tidier) merge(ctx context.Context, o playbook.Overlap) {
	bodies := t.learned.Bodies(o.Names)
	if len(bodies) < 2 {
		return // one was evicted between detection and now
	}
	fmt.Printf("%s  ⧉ tidying %s%s\n", cDim, o.Describe(), cReset)

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	res, err := t.claude.Run(ctx, claude.Options{
		Prompt:  mergeBrief(bodies),
		Model:   consolidateModel,
		Effort:  "high",
		Timeout: 10 * time.Minute,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "consolidate: %v\n", err)
		return
	}

	merged, ok := parseMerge(res.Text)
	if !ok {
		// A reviewer that declined, or answered in prose. Leaving the duplicate
		// alone is the correct outcome of "these are actually different".
		fmt.Printf("%s  ⧉ left alone: %s%s\n", cDim, clipLine(firstLine(res.Text), 120), cReset)
		return
	}
	if err := t.learned.Supersede(o.Names, merged); err != nil {
		fmt.Fprintf(os.Stderr, "consolidate: %v\n", err)
		return
	}
	fmt.Printf("%s  ⧉ %s now covers %s — the originals are kept, superseded%s\n",
		cDim, merged.Name, strings.Join(o.Names, " and "), cReset)
}

// mergeBrief is the whole interface between her memory and its reviewer.
//
// It states the asymmetry plainly, because that is the judgement being asked
// for: keeping a duplicate is cheap and losing a hard-won step is not, so
// declining is a real and expected answer rather than a failure to comply.
func mergeBrief(bodies []playbook.Skill) string {
	var b strings.Builder
	b.WriteString("Freya is a personal assistant who records procedures she works out " +
		"for herself — the door that turned out to be the right one, the control that " +
		"is not where it looks, the order of steps that finally worked. She keeps them " +
		"as short playbooks. She has no other copy of any of this.\n\n" +
		"Two of them look like they describe the same job. Read them and decide.\n\n")

	for _, s := range bodies {
		fmt.Fprintf(&b, "--- %s ---\nsummary: %s\n%s\n\n", s.Name, s.Summary, s.Body)
	}

	b.WriteString("If they really are one procedure, merge them into a single playbook " +
		"that keeps EVERY useful step from both. Where they disagree, prefer the more " +
		"specific claim and say what does not work as well as what does — a wrong turn " +
		"she can name is worth as much as the right one.\n\n" +
		"If they are actually different jobs that happen to share vocabulary, say so " +
		"and change nothing. That is a real answer, not a failure: a duplicate costs " +
		"her a line of context, and a bad merge costs her something she cannot work " +
		"out again.\n\n" +
		"To merge, reply with ONLY a JSON object and nothing else:\n" +
		`{"name": "short-handle", "summary": "one line saying when to reach for this", ` +
		`"body": "the ordered steps"}` + "\n\n" +
		"To decline, reply with one sentence of prose explaining why they are different.")
	return b.String()
}

// parseMerge reads a merged playbook out of the reply, tolerating the fences a
// model wraps JSON in. Anything else is a decline, which is a valid answer.
func parseMerge(out string) (playbook.Skill, bool) {
	text := strings.TrimSpace(out)
	if i := strings.Index(text, "{"); i >= 0 {
		if j := strings.LastIndex(text, "}"); j > i {
			text = text[i : j+1]
		}
	}
	var m struct {
		Name    string `json:"name"`
		Summary string `json:"summary"`
		Body    string `json:"body"`
	}
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		return playbook.Skill{}, false
	}
	if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Summary) == "" ||
		strings.TrimSpace(m.Body) == "" {
		return playbook.Skill{}, false
	}
	return playbook.Skill{Name: m.Name, Summary: m.Summary, Body: m.Body}, true
}
