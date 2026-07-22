package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/akins/jarvis/internal/llm"
)

// Note is a captured thought or a reminder. Reminders are notes with a due time.
type Note struct {
	ID      string     `json:"id"`
	Text    string     `json:"text"`
	Created time.Time  `json:"created"`
	Due     *time.Time `json:"due,omitempty"`
	Done    bool       `json:"done"`
	Tags    []string   `json:"tags,omitempty"`
}

// noteBook is a small JSON-backed collection kept beside the memory store.
type noteBook struct {
	mu    sync.Mutex
	path  string
	Notes []Note
}

func openNoteBook(dir string) (*noteBook, error) {
	nb := &noteBook{path: filepath.Join(dir, "notes.json")}
	b, err := os.ReadFile(nb.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nb, nil
		}
		return nil, fmt.Errorf("notes: read: %w", err)
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &nb.Notes); err != nil {
			return nil, fmt.Errorf("notes: parse: %w", err)
		}
	}
	return nb, nil
}

// save writes atomically. Callers must hold the lock.
func (nb *noteBook) save() error {
	b, err := json.MarshalIndent(nb.Notes, "", "  ")
	if err != nil {
		return err
	}
	tmp := nb.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, nb.path)
}

// RegisterNotes adds note and reminder skills backed by dir.
func RegisterNotes(r *Registry, dir string) error {
	nb, err := openNoteBook(dir)
	if err != nil {
		return err
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "note_add",
			Description: "Save a note. Include a due time to make it a reminder.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"text": {Type: "string", Description: "The note content."},
				"due": {Type: "string", Description: "Optional due time as RFC3339 " +
					"(2026-07-23T09:00:00Z) or a relative offset like '2h', '30m', '3d'."},
				"tags": {Type: "string", Description: "Optional comma-separated tags."},
			}, "text"),
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			text := argString(args, "text")
			if text == "" {
				return "", fmt.Errorf("note text is required")
			}
			n := Note{ID: shortID(), Text: text, Created: time.Now()}
			if tags := argString(args, "tags"); tags != "" {
				for _, t := range strings.Split(tags, ",") {
					if t = strings.TrimSpace(t); t != "" {
						n.Tags = append(n.Tags, t)
					}
				}
			}
			if dueRaw := argString(args, "due"); dueRaw != "" {
				due, err := parseDue(dueRaw)
				if err != nil {
					return "", err
				}
				n.Due = &due
			}

			nb.mu.Lock()
			defer nb.mu.Unlock()
			nb.Notes = append(nb.Notes, n)
			if err := nb.save(); err != nil {
				return "", err
			}
			if n.Due != nil {
				return fmt.Sprintf("Reminder set for %s: %s",
					n.Due.Format("Mon 2 Jan 15:04"), n.Text), nil
			}
			return "Noted: " + n.Text, nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "note_list",
			Description: "List saved notes and reminders.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"filter": {Type: "string", Description: "Which notes to show.",
					Enum: []string{"all", "notes", "reminders", "due", "done"}},
				"search": {Type: "string", Description: "Optional text to match."},
			}),
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			filter := argString(args, "filter")
			if filter == "" {
				filter = "all"
			}
			search := strings.ToLower(argString(args, "search"))

			nb.mu.Lock()
			defer nb.mu.Unlock()

			now := time.Now()
			var shown []Note
			for _, n := range nb.Notes {
				switch filter {
				case "notes":
					if n.Due != nil {
						continue
					}
				case "reminders":
					if n.Due == nil || n.Done {
						continue
					}
				case "due":
					if n.Due == nil || n.Done || n.Due.After(now) {
						continue
					}
				case "done":
					if !n.Done {
						continue
					}
				default:
					if n.Done {
						continue
					}
				}
				if search != "" && !strings.Contains(strings.ToLower(n.Text), search) {
					continue
				}
				shown = append(shown, n)
			}
			if len(shown) == 0 {
				return "Nothing matches.", nil
			}

			// Due items first, soonest first; undated notes after, newest first.
			sort.Slice(shown, func(i, j int) bool {
				a, b := shown[i], shown[j]
				if (a.Due != nil) != (b.Due != nil) {
					return a.Due != nil
				}
				if a.Due != nil {
					return a.Due.Before(*b.Due)
				}
				return a.Created.After(b.Created)
			})

			var sb strings.Builder
			for _, n := range shown {
				sb.WriteString("- [" + n.ID + "] ")
				if n.Due != nil {
					marker := ""
					if n.Due.Before(now) {
						marker = " OVERDUE"
					}
					fmt.Fprintf(&sb, "(due %s%s) ", n.Due.Format("Mon 2 Jan 15:04"), marker)
				}
				sb.WriteString(n.Text)
				if len(n.Tags) > 0 {
					sb.WriteString(" #" + strings.Join(n.Tags, " #"))
				}
				sb.WriteString("\n")
			}
			return strings.TrimSpace(sb.String()), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "note_done",
			Description: "Mark a note or reminder complete by its id.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"id": {Type: "string", Description: "The note id shown in note_list."},
			}, "id"),
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			id := argString(args, "id")
			nb.mu.Lock()
			defer nb.mu.Unlock()
			for i := range nb.Notes {
				if nb.Notes[i].ID == id {
					nb.Notes[i].Done = true
					if err := nb.save(); err != nil {
						return "", err
					}
					return "Marked done: " + nb.Notes[i].Text, nil
				}
			}
			return "", fmt.Errorf("no note with id %q", id)
		},
	})

	return nil
}

// parseDue accepts RFC3339 timestamps, a date, or a relative offset such as
// "45m", "2h" or "3d" — the forms a model is most likely to produce.
func parseDue(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02 15:04", s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(d), nil
	}
	// time.ParseDuration has no day unit; support it explicitly.
	if strings.HasSuffix(s, "d") {
		if d, err := time.ParseDuration(strings.TrimSuffix(s, "d") + "h"); err == nil {
			return time.Now().Add(d * 24), nil
		}
	}
	return time.Time{}, fmt.Errorf("could not read %q as a time; use RFC3339 or an offset like '2h'", s)
}

func shortID() string {
	return fmt.Sprintf("%x", time.Now().UnixNano()%0xfffff)
}
