package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/Akins20/FREYA/internal/memory"
)

// The gap goal-aware watching closes: a reminder the user set with a due time
// (a note) must appear in the deadline feed with its real deadline, so it gets
// lead-time nudges — not only the at-due ping it used to get.
func TestDeadlinesIncludesNoteReminders(t *testing.T) {
	dir := t.TempDir()
	store, err := memory.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	r := New()
	if err := RegisterNotes(r, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Execute(context.Background(), "note_add", map[string]any{
		"text": "attempt the accounting quiz",
		"due":  "2h",
	}); err != nil {
		t.Fatalf("note_add: %v", err)
	}
	// A note WITHOUT a due time must not become a phantom deadline.
	if _, err := r.Execute(context.Background(), "note_add", map[string]any{
		"text": "no deadline here",
	}); err != nil {
		t.Fatalf("note_add: %v", err)
	}

	items, err := Deadlines(store, dir)()
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, it := range items {
		if strings.Contains(it.Text, "no deadline here") {
			t.Errorf("a note with no due time leaked into the deadline feed: %q", it.Text)
		}
		if strings.Contains(it.Text, "accounting quiz") {
			found = true
			if it.Deadline.IsZero() {
				t.Error("the note reminder has a zero deadline")
			}
		}
	}
	if !found {
		t.Fatalf("the due note reminder was not in the deadline feed: %+v", items)
	}
}
