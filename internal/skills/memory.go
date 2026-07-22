package skills

import (
	"context"
	"fmt"
	"strings"

	"github.com/akins/jarvis/internal/llm"
	"github.com/akins/jarvis/internal/memory"
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
			var sb strings.Builder
			for _, h := range hits {
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
