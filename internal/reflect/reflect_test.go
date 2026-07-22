package reflect

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/akins/jarvis/internal/memory"
)

func newStore(t *testing.T) *memory.Store {
	t.Helper()
	s, err := memory.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestContradictionLensCatchesReversal(t *testing.T) {
	store := newStore(t)
	if err := store.PutFact(memory.Fact{
		Key: "database", Text: "Always use PostgreSQL for persistence, never MongoDB",
	}); err != nil {
		t.Fatal(err)
	}

	in := Input{
		Query: "let's switch the persistence layer to MongoDB",
		Store: store, Index: memory.BuildIndex(store), Now: time.Now(),
	}
	got, err := (&ContradictionLens{}).Look(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("did not notice a request reversing a settled decision")
	}
	t.Logf("found: %s (weight %.2f)", got[0].Summary, got[0].Weight)
}

func TestContradictionIgnoresUnrelatedFacts(t *testing.T) {
	store := newStore(t)
	if err := store.PutFact(memory.Fact{
		Key: "editor", Text: "Always use vim, never emacs",
	}); err != nil {
		t.Fatal(err)
	}
	in := Input{
		Query: "add a new endpoint to the payment service",
		Store: store, Index: memory.BuildIndex(store), Now: time.Now(),
	}
	got, _ := (&ContradictionLens{}).Look(context.Background(), in)
	if len(got) != 0 {
		t.Errorf("fired on an unrelated fact: %s", got[0].Summary)
	}
}

func TestPatternLensNoticesRepetition(t *testing.T) {
	store := newStore(t)
	for range 5 {
		if _, err := store.AppendTurn(memory.Turn{
			Role: "user", Text: "the deployment pipeline is broken again",
		}); err != nil {
			t.Fatal(err)
		}
	}
	in := Input{
		Query: "deployment pipeline broken",
		Store: store, Index: memory.BuildIndex(store), Now: time.Now(),
	}
	got, err := (&PatternLens{}).Look(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("five mentions in a month produced no pattern insight")
	}
	t.Logf("found: %s", got[0].Summary)
}

func TestArcLensDetectsSustainedFrustration(t *testing.T) {
	store := newStore(t)
	for _, text := range []string{
		"I'm so stuck on this webpack config",
		"webpack is still broken, ugh",
		"why won't webpack just work, I'm fed up",
		"webpack config again, frustrating",
	} {
		if _, err := store.AppendTurn(memory.Turn{Role: "user", Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	in := Input{
		Query: "help me with the webpack config",
		Store: store, Index: memory.BuildIndex(store), Now: time.Now(),
	}
	got, err := (&ArcLens{}).Look(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("sustained frustration went unnoticed")
	}
	t.Logf("found: %s", got[0].Summary)
}

func TestArcLensIgnoresOneBadDay(t *testing.T) {
	store := newStore(t)
	if _, err := store.AppendTurn(memory.Turn{
		Role: "user", Text: "ugh this webpack thing is annoying",
	}); err != nil {
		t.Fatal(err)
	}
	in := Input{
		Query: "webpack config help",
		Store: store, Index: memory.BuildIndex(store), Now: time.Now(),
	}
	got, _ := (&ArcLens{}).Look(context.Background(), in)
	if len(got) != 0 {
		t.Error("a single frustrated message was treated as an arc")
	}
}

func TestStalenessLensFlagsOldVolatileFacts(t *testing.T) {
	store := newStore(t)
	if err := store.PutFact(memory.Fact{
		Key: "disk", Text: "The external drive has 14 GB free space remaining",
	}); err != nil {
		t.Fatal(err)
	}
	in := Input{
		Query: "how much free space is on the external drive?",
		Store: store, Index: memory.BuildIndex(store),
		// Look from far in the future so the fact is stale.
		Now: time.Now().Add(200 * 24 * time.Hour),
	}
	got, err := (&StalenessLens{}).Look(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("a 200-day-old volatile fact was not flagged")
	}
	t.Logf("found: %s", got[0].Summary)
}

func TestConsequenceLensWarnsAboutDiversions(t *testing.T) {
	store := newStore(t)
	deadline := time.Now().Add(48 * time.Hour)
	lens := &ConsequenceLens{
		Deadlines: func() ([]Deadline, error) {
			return []Deadline{{Text: "dissertation submission", When: deadline}}, nil
		},
	}
	in := Input{
		Query: "let's refactor the whole storage layer from scratch",
		Store: store, Index: memory.BuildIndex(store), Now: time.Now(),
	}
	got, err := lens.Look(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("optional work two days before a deadline was not flagged")
	}
	if got[0].Weight < 0.85 {
		t.Errorf("weight %.2f too low for a two-day deadline", got[0].Weight)
	}
	t.Logf("found: %s (weight %.2f)", got[0].Summary, got[0].Weight)
}

func TestConsequenceIgnoresNormalWork(t *testing.T) {
	lens := &ConsequenceLens{
		Deadlines: func() ([]Deadline, error) {
			return []Deadline{{Text: "thesis", When: time.Now().Add(48 * time.Hour)}}, nil
		},
	}
	in := Input{Query: "fix the null pointer in the parser", Now: time.Now()}
	got, _ := lens.Look(context.Background(), in)
	if len(got) != 0 {
		t.Error("fired on ordinary necessary work")
	}
}

// TestOnlyOneInsightPerExchange is the anti-mess guarantee. Several lenses
// firing at once must not produce a pile of interjections.
func TestOnlyOneInsightPerExchange(t *testing.T) {
	r := New()
	many := make([]Insight, 0, 10)
	for i := range 10 {
		many = append(many, Insight{
			Key:     "k" + string(rune('a'+i)),
			Summary: "insight",
			Weight:  0.9,
		})
	}
	kept := r.arbitrate(many, surfacingThreshold, time.Now())
	if len(kept) != 1 {
		t.Fatalf("surfaced %d insights in one exchange, want at most 1", len(kept))
	}
}

func TestWeakInsightsAreDropped(t *testing.T) {
	r := New()
	kept := r.arbitrate([]Insight{
		{Key: "weak", Summary: "mildly interesting", Weight: 0.3},
	}, surfacingThreshold, time.Now())
	if len(kept) != 0 {
		t.Error("a low-weight insight was surfaced")
	}
}

func TestSameInsightNotRepeatedWithinADay(t *testing.T) {
	r := New()
	now := time.Now()
	in := []Insight{{Key: "repeat", Summary: "the same point", Weight: 0.9}}

	if got := r.arbitrate(in, surfacingThreshold, now); len(got) != 1 {
		t.Fatal("first surfacing was suppressed")
	}
	if got := r.arbitrate(in, surfacingThreshold, now.Add(time.Hour)); len(got) != 0 {
		t.Error("the same point was made twice within an hour")
	}
	if got := r.arbitrate(in, surfacingThreshold, now.Add(25*time.Hour)); len(got) != 1 {
		t.Error("the point never resurfaced after a full day")
	}
}

func TestStrongestFindingWinsAKeyCollision(t *testing.T) {
	r := New()
	kept := r.arbitrate([]Insight{
		{Key: "same", Summary: "weaker", Weight: 0.6},
		{Key: "same", Summary: "stronger", Weight: 0.95},
	}, surfacingThreshold, time.Now())
	if len(kept) != 1 || kept[0].Summary != "stronger" {
		t.Fatalf("kept %+v, want the stronger finding", kept)
	}
}

func TestReflectRunsEveryLensWithoutBlocking(t *testing.T) {
	store := newStore(t)
	r := New()

	done := make(chan []Insight, 1)
	go func() {
		done <- r.Reflect(context.Background(), Input{
			Query: "anything", Store: store, Index: memory.BuildIndex(store), Now: time.Now(),
		})
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("reflection did not complete promptly; it must never delay a reply")
	}

	if len(r.Lenses()) != 6 {
		t.Errorf("registered %d lenses, want 6: %v", len(r.Lenses()), r.Lenses())
	}
}

func TestFailingLensIsSilent(t *testing.T) {
	r := &Reflector{surfaced: map[string]time.Time{}}
	r.Add(brokenLens{})
	got := r.Reflect(context.Background(), Input{Query: "x", Now: time.Now()})
	if len(got) != 0 {
		t.Error("a failing lens produced output")
	}
}

type brokenLens struct{}

func (brokenLens) Name() string { return "broken" }
func (brokenLens) Look(context.Context, Input) ([]Insight, error) {
	return nil, context.DeadlineExceeded
}

func TestContentWordsFiltersNoise(t *testing.T) {
	got := contentWords("Can you please help me with the deployment pipeline?")
	joined := strings.Join(got, ",")
	for _, unwanted := range []string{"can", "you", "please", "help", "with", "the"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("stopword %q survived: %v", unwanted, got)
		}
	}
	if !strings.Contains(joined, "deployment") || !strings.Contains(joined, "pipeline") {
		t.Errorf("content words lost: %v", got)
	}
}

// TestAddReplacesSameName guards against a lens running twice. New() installs
// defaults; a caller supplying a properly-configured replacement must not end
// up with both, which showed up live as "consequence" listed twice.
func TestAddReplacesSameName(t *testing.T) {
	r := New()
	before := len(r.Lenses())

	r.Add(&ConsequenceLens{Deadlines: func() ([]Deadline, error) { return nil, nil }})

	if after := len(r.Lenses()); after != before {
		t.Fatalf("lens count went %d -> %d; the replacement was appended", before, after)
	}
	seen := map[string]int{}
	for _, n := range r.Lenses() {
		seen[n]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("lens %q registered %d times", name, n)
		}
	}
}
