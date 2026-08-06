package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// The failure the branch exists to prevent: two threads of work appending to one
// archive in wall-clock order, producing a transcript that reads as one deranged
// conversation and is then fed back as context.
func TestABackgroundJobDoesNotInterleaveTheConversation(t *testing.T) {
	store := testStore(t)
	if _, err := store.AppendTurn(Turn{Role: "user", Text: "open my portal"}); err != nil {
		t.Fatal(err)
	}

	job := NewBranch(store, "job1")

	// Both threads work at once.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			job.AppendTurn(Turn{Role: "tool", Text: fmt.Sprintf("quiz step %d", i)})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			if _, err := store.AppendTurn(Turn{Role: "user", Text: fmt.Sprintf("foreground %d", i)}); err != nil {
				t.Error(err)
			}
		}
	}()
	wg.Wait()

	for _, turn := range store.Turns() {
		if strings.HasPrefix(turn.Text, "quiz step") {
			t.Fatalf("a background job's turn landed in the shared archive: %q", turn.Text)
		}
	}
	if got := store.TurnCount(); got != 21 {
		t.Errorf("the archive holds %d turns, want 21 — only the foreground writes to it", got)
	}
	if got := len(job.Own()); got != 20 {
		t.Errorf("the job kept %d of its own turns, want 20", got)
	}
}

// Freezing is what buys the cache: the job's prompt opens with the bytes the
// foreground had at spawn, so both threads share one cached prefix. A branch that
// tracked the live archive would have its prefix rewritten by every foreground
// turn.
func TestTheJobsHistoryIsFrozenAtSpawn(t *testing.T) {
	store := testStore(t)
	store.AppendTurn(Turn{Role: "user", Text: "before the job"})

	job := NewBranch(store, "job1")
	store.AppendTurn(Turn{Role: "user", Text: "said after the job started"})

	for _, turn := range job.Turns() {
		if turn.Text == "said after the job started" {
			t.Fatal("the foreground rewrote the job's prompt underneath it")
		}
	}
	// And what it started from is still there.
	if turns := job.Turns(); len(turns) != 1 || turns[0].Text != "before the job" {
		t.Fatalf("the job lost the conversation it was spawned from: %+v", turns)
	}
}

// Knowledge is shared even though the conversation is not — a fact learned in the
// background belongs to everybody, and identity/facts/episodes are also the
// cached prefix both threads depend on.
func TestTheBranchSharesKnowledgeButNotTheTranscript(t *testing.T) {
	store := testStore(t)
	store.PutFact(Fact{Key: "drive-full", Text: "the main drive is at 99%"})
	store.AddEpisode(Episode{Summary: "earlier session about the portal"})

	job := NewBranch(store, "job1")
	view := job.View()

	if len(view.Facts) != 1 || view.Facts[0].Key != "drive-full" {
		t.Errorf("the job cannot see what she knows: %+v", view.Facts)
	}
	if len(view.Episodes) != 1 {
		t.Errorf("the job cannot see earlier sessions: %+v", view.Episodes)
	}

	// A fact written from a job is written for real: it is knowledge, not chatter.
	if err := store.PutFact(Fact{Key: "from-job", Text: "quiz 1 is graded 8/10"}); err != nil {
		t.Fatal(err)
	}
	if len(job.Facts()) != 2 {
		t.Error("facts do not flow between the threads")
	}
}

// A job's internal chatter must not become long-term memory for every future
// prompt.
func TestAJobDoesNotWriteEpisodes(t *testing.T) {
	store := testStore(t)
	job := NewBranch(store, "job1")
	if err := job.AddEpisode(Episode{Summary: "forty rounds of clicking"}); err != nil {
		t.Fatal(err)
	}
	if len(store.Episodes()) != 0 {
		t.Error("a background job distilled its own chatter into shared memory")
	}
}

// A branch must survive a session taking the write handle. Its work is held and
// reconciled later; dying because someone opened a terminal would be absurd.
func TestABranchKeepsWorkingWhileTheStoreIsSuspended(t *testing.T) {
	store := testStore(t)
	job := NewBranch(store, "job1")

	if err := store.Suspend(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendTurn(Turn{Role: "user", Text: "x"}); err == nil {
		t.Fatal("precondition: a suspended store must refuse writes")
	}
	if _, err := job.AppendTurn(Turn{Role: "tool", Text: "still working"}); err != nil {
		t.Fatalf("a background job died because a session took the archive: %v", err)
	}
}

// Nothing is destroyed: the job's transcript is written whole, separately from
// the archive it must stay out of.
func TestAJobsTranscriptIsRecorded(t *testing.T) {
	dir := t.TempDir()
	turns := []Turn{
		{Role: "user", Text: "do the quizzes"},
		{Role: "tool", Text: "Quiz 1 submitted. 8/10."},
	}
	if err := RecordJob(dir, "job1", "do the quizzes", turns); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, jobsFile))
	if err != nil {
		t.Fatal(err)
	}
	data := string(raw)
	for _, want := range []string{"job1", "do the quizzes", "Quiz 1 submitted. 8/10."} {
		if !strings.Contains(data, want) {
			t.Errorf("the job record omits %q:\n%s", want, data)
		}
	}
}

// The prompt must describe one moment. Build used to read the store six separate
// times, so a concurrent writer could land between two of them.
func TestViewIsOneConsistentRead(t *testing.T) {
	store := testStore(t)
	store.PutFact(Fact{Key: "k", Text: "v"})
	for i := 0; i < 50; i++ {
		store.AppendTurn(Turn{Role: "user", Text: fmt.Sprintf("turn %d", i)})
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			store.AppendTurn(Turn{Role: "user", Text: fmt.Sprintf("concurrent %d", i)})
		}
	}()

	for i := 0; i < 200; i++ {
		v := store.View()
		if v.Anchor > len(v.Turns) {
			t.Fatalf("view is internally inconsistent: anchor %d beyond %d turns",
				v.Anchor, len(v.Turns))
		}
		if len(v.Window()) > len(v.Turns) {
			t.Fatal("the window escapes the turns it was cut from")
		}
	}
	close(stop)
	wg.Wait()
}

// The anchor moves forward only. Resume reloads the store from disk, including an
// anchor a session moved; a builder holding an older view would otherwise push it
// back and resurrect turns already distilled into an episode.
func TestTheWindowAnchorNeverMovesBackwards(t *testing.T) {
	store := testStore(t)
	for i := 0; i < 10; i++ {
		store.AppendTurn(Turn{Role: "user", Text: fmt.Sprintf("turn %d", i)})
	}
	if evicted := store.Advance(6); len(evicted) != 6 {
		t.Fatalf("advance evicted %d turns, want 6", len(evicted))
	}
	if evicted := store.Advance(3); evicted != nil {
		t.Fatal("the anchor was dragged backwards, resurrecting evicted turns")
	}
	if got := store.View().Anchor; got != 6 {
		t.Errorf("anchor = %d, want 6", got)
	}
	if evicted := store.Advance(99); evicted != nil {
		t.Error("the anchor advanced past the end of the archive")
	}
}

// The measured cause of "it takes minutes to get a simple response": the working
// allowance was five times the size of the whole archive, so it could never fill,
// so eviction never ran and every request replayed all 962 turns verbatim.
//
// Her own telemetry priced it — 2.0s median under 20k tokens, 6.9s over 150k —
// and she was sending 155k as a median, for any question at all.
func TestTheWorkingBudgetActuallyEvicts(t *testing.T) {
	b := DefaultBudget()

	// The bound is against the original bug — tiers sized as a slice of the 1M
	// window, so the allowance exceeded the entire archive and eviction could
	// never fire — not against any particular latency target. How much verbatim
	// recency is worth its seconds is a judgement call, and it has been made
	// deliberately more than once; what must not come back is an allowance no
	// realistic archive can fill.
	if b.Working > 256_000 {
		t.Errorf("working budget is %d tokens — large enough that eviction never "+
			"fires and every turn replays the whole archive", b.Working)
	}
	ceiling := b.Identity + b.Facts + b.Episodes + b.Working + b.Retrieved
	if ceiling > 320_000 {
		t.Errorf("tier ceilings total %d tokens; at that size the prompt is the "+
			"latency, on every round of every task", ceiling)
	}

	// And it must actually evict: an archive larger than the allowance moves the
	// anchor forward.
	store := testStore(t)
	big := strings.Repeat("a conversation turn with some substance to it. ", 40)
	for i := 0; i < 400; i++ {
		if _, err := store.AppendTurn(Turn{Role: "user", Text: big}); err != nil {
			t.Fatal(err)
		}
	}
	before := store.View().Anchor
	from := WindowFrom(store.View().Turns, before, b.Working, b.EvictionChunk)
	if from <= before {
		t.Fatalf("an archive of %d large turns did not evict anything; anchor stayed at %d",
			store.TurnCount(), before)
	}
	if evicted := store.Advance(from); len(evicted) == 0 {
		t.Error("eviction reported turns to drop but Advance kept them")
	}
}

// A budget change evicts hundreds of turns at once. Collapsing those into one
// 1,200-character summary would lose the shape of weeks.
func TestALargeEvictionIsChunkedIntoUsableEpisodes(t *testing.T) {
	store := testStore(t)
	index := BuildIndex(store)
	b := NewContextBuilder(store, index, "persona")

	var evicted []Turn
	for i := 0; i < 300; i++ {
		evicted = append(evicted, Turn{
			Role: "user", Text: fmt.Sprintf("turn %d about something", i),
			Timestamp: time.Now(),
		})
	}
	b.archiveEvicted(evicted)

	eps := store.Episodes()
	if len(eps) < 4 {
		t.Fatalf("300 evicted turns became %d episode(s); the detail of weeks is gone", len(eps))
	}
	total := 0
	for _, e := range eps {
		total += len(e.TurnIDs)
	}
	if total != 300 {
		t.Errorf("episodes cover %d turns, want all 300", total)
	}
}

// The regression the budget change exposed on its very first real request.
//
// The archive stores a tool's output but never the assistant turn that asked for
// it. Replayed from turn zero that is harmless — turn zero is always something
// the user said. Replayed from a moving anchor it is a function response with no
// function call in front of it, and Gemini rejects the entire request.
func TestTheWindowNeverOpensOnAToolResult(t *testing.T) {
	store := testStore(t)
	big := strings.Repeat("substantial conversation content here. ", 60)

	// A realistic shape: user asks, several tools run, she answers.
	for i := 0; i < 60; i++ {
		store.AppendTurn(Turn{Role: "user", Text: big})
		store.AppendTurn(Turn{Role: "tool", Text: big, ToolName: "browser_read"})
		store.AppendTurn(Turn{Role: "tool", Text: big, ToolName: "browser_click_text"})
		store.AppendTurn(Turn{Role: "assistant", Text: big})
	}

	turns := store.View().Turns
	// Every budget must land somewhere safe to start — including one that evicts
	// nothing, and including an anchor already parked on a tool turn by an
	// earlier build or read back from disk.
	for _, budget := range []int{2_000, 8_000, 16_000, 32_000, 64_000, 10_000_000} {
		from := WindowFrom(turns, 0, budget, 0.25)
		if from >= len(turns) {
			t.Fatalf("budget %d evicted the entire archive", budget)
		}
		if role := turns[from].Role; role == "tool" {
			t.Errorf("budget %d opened the window on a %q turn — the request is rejected "+
				"before the model ever sees it", budget, role)
		}
		// And starting from an anchor that is ALREADY on a tool turn must recover,
		// which is the state a previous build leaves behind in state.json.
		stuck := WindowFrom(turns, 1, budget, 0.25)
		if role := turns[stuck].Role; role == "tool" {
			t.Errorf("budget %d left the anchor stuck on a %q turn", budget, role)
		}
	}
}

// Skipping forward must not walk off the end and lose the newest turn, which is
// the exchange currently in progress.
func TestSafeStartKeepsTheNewestTurn(t *testing.T) {
	turns := []Turn{
		{Role: "user", Text: "a"},
		{Role: "tool", Text: "b"},
		{Role: "tool", Text: "c"},
	}
	if got := safeStart(turns, 1, len(turns)-1); got != 2 {
		t.Errorf("safeStart = %d; it must stop at the limit rather than discard the "+
			"newest turn", got)
	}
}
