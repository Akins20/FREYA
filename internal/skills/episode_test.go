package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/Akins20/FREYA/internal/memory"
)

// An episode summary is a door or a dead end, and it was a dead end.
//
// The prompt carries "12 turns. Topics raised: …" and nothing that opens it, so
// the only route to the detail was BM25 over the archive — which is lexical, and
// the requests that need an episode are exactly the ones lexical search cannot
// serve. "How did I work this site last night" has no distinctive words in it.
func TestAnEpisodeCanBeOpened(t *testing.T) {
	store, err := memory.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.AppendTurn(memory.Turn{Role: "user", Text: "which door do I sign in at"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.AppendTurn(memory.Turn{Role: "assistant", Text: "my.uopeople.edu, not the d2l one"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddEpisode(memory.Episode{
		Summary: "2 turns. Topics raised: signing in",
		TurnIDs: []string{a.ID, b.ID},
	}); err != nil {
		t.Fatal(err)
	}

	r := New()
	RegisterMemory(r, store, memory.BuildIndex(store))

	id := store.View().Episodes[0].ID
	out, err := r.Execute(context.Background(), "recall_episode", map[string]any{"id": id})
	if err != nil {
		t.Fatalf("could not open the episode: %v", err)
	}
	if !strings.Contains(out, "my.uopeople.edu") {
		t.Errorf("the turns behind the summary did not come back:\n%s", out)
	}

	// The id is printed wrapped in brackets, so accepting them back costs nothing
	// and saves a wasted round.
	if _, err := r.Execute(context.Background(), "recall_episode",
		map[string]any{"id": "[" + id + "]"}); err != nil {
		t.Errorf("the id copied verbatim from the prompt was rejected: %v", err)
	}
}

// A wrong id must hand back the real ones. A refusal that only says "no" costs a
// round and teaches nothing.
func TestAnUnknownEpisodeSaysWhichOnesExist(t *testing.T) {
	store, err := memory.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	turn, _ := store.AppendTurn(memory.Turn{Role: "user", Text: "hello"})
	if err := store.AddEpisode(memory.Episode{
		Summary: "1 turn", TurnIDs: []string{turn.ID},
	}); err != nil {
		t.Fatal(err)
	}
	r := New()
	RegisterMemory(r, store, memory.BuildIndex(store))

	_, err = r.Execute(context.Background(), "recall_episode", map[string]any{"id": "nope"})
	if err == nil {
		t.Fatal("an unknown episode id was accepted")
	}
	real := store.View().Episodes[0].ID
	if !strings.Contains(err.Error(), real) {
		t.Errorf("the refusal does not name an episode she could actually open: %v", err)
	}
}
