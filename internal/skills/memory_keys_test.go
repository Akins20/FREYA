package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/Akins20/FREYA/internal/memory"
)

// Reusing a key REPLACES the fact under it, and slugify collapses case and
// punctuation — "CS 401 deadline", "cs-401-deadline" and "CS-401-Deadline" are
// all one key. So a near-miss destroys a fact she never saw. Silently, until now.
func TestOverwritingAFactIsDisclosed(t *testing.T) {
	dir := t.TempDir()
	store, err := memory.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	index := memory.BuildIndex(store)
	r := New()
	RegisterMemory(r, store, index)
	ctx := context.Background()

	if _, err := r.Execute(ctx, "memory_remember", map[string]any{
		"key": "cs-401-deadline", "text": "CS 401 final is on the 12th",
	}); err != nil {
		t.Fatal(err)
	}

	// A different spelling of the same key, carrying a different fact.
	out, err := r.Execute(ctx, "memory_remember", map[string]any{
		"key": "CS-401-Deadline", "text": "CS 401 essay is due Friday",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "replaced") {
		t.Fatalf("a fact was destroyed without saying so: %q", out)
	}
	if !strings.Contains(out, "final is on the 12th") {
		t.Fatalf("the displaced fact was not quoted back, so it is unrecoverable: %q", out)
	}
}

// A key she was never shown is a key she had to invent. Recall must name them.
func TestRecallNamesFactKeys(t *testing.T) {
	dir := t.TempDir()
	store, err := memory.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutFact(memory.Fact{Key: "main-drive-full", Text: "the external drive sits at 99 percent"}); err != nil {
		t.Fatal(err)
	}
	// Rebuild the index the way a fresh session does.
	index := memory.BuildIndex(store)
	r := New()
	RegisterMemory(r, store, index)

	out, err := r.Execute(context.Background(), "memory_recall", map[string]any{"query": "drive"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[main-drive-full]") {
		t.Fatalf("recall did not name the key, so memory_remember and memory_forget have no source: %q", out)
	}
}
