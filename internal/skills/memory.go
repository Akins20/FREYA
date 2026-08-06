package skills

import (
	"context"
	"fmt"
	"strings"

	"github.com/akins/jarvis/internal/llm"
	"github.com/akins/jarvis/internal/memory"
	"github.com/akins/jarvis/internal/sentinel"
)

// RegisterMemory exposes Freya's long-term memory to the model itself.
//
// This is what separates a memory architecture from a transcript: the model
// decides what is worth keeping, writes it as a durable fact, and searches the
// archive when the working set does not already hold the answer.
func RegisterMemory(r *Registry, store *memory.Store, index *memory.Index) {
	r.Register(Skill{
		Tool: llm.Tool{
			Name: "memory_remember",
			Description: "Store a durable fact worth recalling in future sessions. " +
				"Use for stable preferences, decisions and context — not for passing chatter. " +
				"Reusing an existing key updates that fact rather than duplicating it.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"key": {Type: "string", Description: "Short stable handle, e.g. 'prefers-go' or 'main-drive-full'."},
				"text": {Type: "string", Description: "The fact, written so it stands alone " +
					"without the surrounding conversation."},
				"pinned": {Type: "boolean", Description: "True for standing directives that must be " +
					"present in every future prompt. Use sparingly."},
			}, "key", "text"),
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			key := slugify(argString(args, "key"))
			text := argString(args, "text")
			if key == "" || text == "" {
				return "", fmt.Errorf("both key and text are required")
			}
			// Reusing a key REPLACES the fact stored under it, and slugify collapses
			// punctuation and spacing — so "CS 401 deadline", "cs-401-deadline" and
			// "CS401: deadline" are all one key. A near-miss therefore destroys a fact
			// she never saw, silently. Say what was displaced.
			var replaced string
			for _, existing := range store.Facts() {
				if existing.Key == key && existing.Text != text {
					replaced = existing.Text
					break
				}
			}
			f := memory.Fact{
				Key:    key,
				Text:   text,
				Source: "conversation",
				Pinned: argBool(args, "pinned"),
			}
			if err := store.PutFact(f); err != nil {
				return "", err
			}
			// Index immediately so it is retrievable within this same session.
			index.Add(f.Key, "fact", f.Key+" "+f.Text)
			if replaced != "" {
				return fmt.Sprintf("Remembered [%s].\n(That key already held something else, now replaced: %q. "+
					"If they were different facts, store the new one under its own key and put the old one back.)",
					key, clip(replaced, 200)), nil
			}
			if f.Pinned {
				return fmt.Sprintf("Remembered [%s] as a standing directive.", key), nil
			}
			return fmt.Sprintf("Remembered [%s].", key), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "memory_recall",
			Description: "Search the full memory archive — every past conversation, episode " +
				"and fact — for material not already in the current context.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"query": {Type: "string", Description: "What to look for, in the user's own words."},
				"limit": {Type: "number", Description: "Maximum results, default 8."},
			}, "query"),
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			query := argString(args, "query")
			if query == "" {
				return "", fmt.Errorf("query is required")
			}
			hits := index.Search(query, argInt(args, "limit", 8))
			if len(hits) == 0 {
				return "Nothing in memory matches that.", nil
			}
			// Show the key for facts. Without it, memory_remember's key argument had
			// no legitimate source anywhere in the toolset — every key she supplied
			// was necessarily invented, and an invented key that happens to collide
			// overwrites a real fact.
			var sb strings.Builder
			for _, h := range hits {
				if h.Kind == "fact" && h.ID != "" {
					fmt.Fprintf(&sb, "- (fact) [%s] %s\n", h.ID, clip(h.Text, 500))
					continue
				}
				fmt.Fprintf(&sb, "- (%s) %s\n", h.Kind, clip(h.Text, 500))
			}
			return strings.TrimSpace(sb.String()), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "memory_forget",
			Description: "Delete a stored fact by its key, for when something is wrong or obsolete.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"key": {Type: "string", Description: "The key of the fact to remove."},
			}, "key"),
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			key := slugify(argString(args, "key"))
			removed, err := store.ForgetFact(key)
			if err != nil {
				return "", err
			}
			if !removed {
				return fmt.Sprintf("No fact stored under [%s].", key), nil
			}
			return fmt.Sprintf("Forgotten [%s].", key), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "memory_stats",
			Description: "Report what is currently held in memory: turns archived, facts and episodes.",
			Params:      llm.ObjectSchema(nil),
		},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			facts := store.Facts()
			pinned := 0
			for _, f := range facts {
				if f.Pinned {
					pinned++
				}
			}
			return fmt.Sprintf(
				"Archive: %d turns. Facts: %d (%d pinned). Episodes: %d. Search index: %d documents. Stored in %s.",
				store.TurnCount(), len(facts), pinned, len(store.Episodes()), index.Size(), store.Dir()), nil
		},
	})
}

// slugify normalises a fact key so the same concept does not accumulate
// near-duplicate entries under different spellings.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var sb strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			sb.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && sb.Len() > 0 {
				sb.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(sb.String(), "-")
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// Commitments scans stored facts for deadlines the user has mentioned.
//
// This is what turns memory from a transcript into something that works: the
// user said once, weeks ago, that a dissertation was due — nobody should have
// to ask for that to resurface at the right moment. Only facts phrased as
// obligations qualify, so remembered dates in passing do not become nags.
func Commitments(store *memory.Store) func() ([]sentinel.Commitment, error) {
	return func() ([]sentinel.Commitment, error) {
		var out []sentinel.Commitment
		for _, f := range store.Facts() {
			if deadline, ok := sentinel.ExtractDeadline(f.Text); ok {
				out = append(out, sentinel.Commitment{
					Key:      f.Key,
					Text:     f.Text,
					Deadline: deadline,
				})
			}
		}
		return out, nil
	}
}

// Deadlines is the goal-aware feed for the CommitmentWatcher: it merges the two
// places the user's deadlines actually live. The first is obligations she
// mentioned in passing, extracted from facts (that's Commitments). The second —
// the one people rely on — is reminders they explicitly set with a due time via
// note_add: "quiz due tonight". Those carry a structured Due, but until now they
// were only ever fired at the instant they came due, never surfaced with the
// lead time that lets her offer to act *before* the deadline. Feeding them here
// runs them through the same escalating-urgency machinery as everything else.
func Deadlines(store *memory.Store, dir string) func() ([]sentinel.Commitment, error) {
	factFeed := Commitments(store)
	return func() ([]sentinel.Commitment, error) {
		out, _ := factFeed() // never errors; ignore so notes still surface regardless

		nb, err := openNoteBook(dir)
		if err != nil {
			return out, nil
		}
		nb.mu.Lock()
		defer nb.mu.Unlock()
		for _, n := range nb.Notes {
			if n.Done || n.Due == nil {
				continue
			}
			out = append(out, sentinel.Commitment{
				// Keyed by note id so the same reminder is one commitment, distinct
				// from any fact-derived one.
				Key:      "note:" + n.ID,
				Text:     n.Text,
				Deadline: *n.Due,
			})
		}
		return out, nil
	}
}
