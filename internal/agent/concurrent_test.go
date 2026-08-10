package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Akins20/FREYA/internal/llm"
	"github.com/Akins20/FREYA/internal/memory"
	"github.com/Akins20/FREYA/internal/skills"
)

// concurrentProvider answers both threads at once, and records what each was
// sent, so the test can check that neither could see the other's conversation.
type concurrentProvider struct {
	mu       sync.Mutex
	requests []llm.Request
	// gate holds the background thread mid-exchange until the foreground has
	// finished, which is the interleaving the branch exists to prevent.
	gate chan struct{}
}

func (p *concurrentProvider) Name() string { return "concurrent" }

func (p *concurrentProvider) Chat(_ context.Context, req llm.Request) (*llm.Response, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()

	last := ""
	for _, m := range req.Messages {
		if strings.TrimSpace(m.Text) != "" {
			last = m.Text
		}
	}
	switch {
	case strings.Contains(last, "background goal"):
		<-p.gate // hold here while the foreground talks
		return &llm.Response{Text: "background answer"}, nil
	case strings.Contains(last, "background answer"):
		return &llm.Response{Text: "background answer"}, nil
	}
	return &llm.Response{Text: "foreground answer"}, nil
}

// The load-bearing invariant of the whole phase: two threads of work, one
// archive, and no interleaving.
func TestForegroundAndBackgroundDoNotInterleave(t *testing.T) {
	p := &concurrentProvider{gate: make(chan struct{})}
	a, store := newTestAgent(t, p)

	// The background thread is an isolated conversation over the same memory.
	branch := memory.NewBranch(store, "job1")
	worker := a.ForJob(branch, skills.NewScope(skills.NewWorkspace(t.TempDir()), "job1-", "job1"))

	done := make(chan string, 1)
	go func() {
		res, err := worker.Ask(context.Background(), "background goal: work through the quizzes")
		if err != nil {
			t.Error(err)
			done <- ""
			return
		}
		done <- res.Reply
	}()

	// While that is held mid-exchange, the user says something else.
	deadline := time.Now().Add(3 * time.Second)
	for branch.TurnCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if _, err := a.Ask(context.Background(), "and what's the time"); err != nil {
		t.Fatal(err)
	}

	close(p.gate)
	if reply := <-done; reply != "background answer" {
		t.Fatalf("the background thread did not finish cleanly: %q", reply)
	}

	// The archive holds the foreground exchange and nothing else.
	var texts []string
	for _, turn := range store.Turns() {
		texts = append(texts, turn.Text)
	}
	joined := strings.Join(texts, "\n")
	if strings.Contains(joined, "background goal") || strings.Contains(joined, "background answer") {
		t.Fatalf("a background job's turns landed in the shared archive:\n%s", joined)
	}
	if len(texts) != 2 {
		t.Fatalf("the archive holds %d turns, want 2 (the foreground exchange):\n%s", len(texts), joined)
	}

	// And the job kept its own.
	if n := len(branch.Own()); n != 2 {
		t.Errorf("the job kept %d of its own turns, want 2", n)
	}
}

// Tool declarations lead the request and the persona opens the system prompt, so
// varying either per worker gives every thread a different cacheable prefix — and
// with prefix caching that means each pays full price for the whole history
// instead of sharing one cached block.
func TestABackgroundJobSharesTheCachedPrefix(t *testing.T) {
	p := &concurrentProvider{gate: make(chan struct{})}
	close(p.gate)
	a, store := newTestAgent(t, p)
	// The catalogue sits in the stable prefix too, so it is one more thing that
	// must not differ per worker. Set here so the comparison below actually
	// exercises it rather than passing on an empty string.
	a.Builder.Catalogue = "CATALOGUE-MARKER — every tool she has"
	a.Skills.Register(skills.Skill{
		Tool:    llm.Tool{Name: "ping", Params: llm.ObjectSchema(nil)},
		Handler: func(context.Context, map[string]any) (string, error) { return "pong", nil },
	})

	if _, err := a.Ask(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	foreground := p.requests[len(p.requests)-1]
	p.mu.Unlock()

	branch := memory.NewBranch(store, "job1")
	worker := a.ForJob(branch, skills.NewScope(skills.NewWorkspace(t.TempDir()), "job1-", "job1"))
	if _, err := worker.Ask(context.Background(), "background goal"); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	background := p.requests[len(p.requests)-1]
	p.mu.Unlock()

	if len(foreground.Tools) != len(background.Tools) {
		t.Fatalf("the job was offered %d tools, the foreground %d — different declarations "+
			"mean a different cached prefix for every worker",
			len(background.Tools), len(foreground.Tools))
	}
	for i := range foreground.Tools {
		if foreground.Tools[i].Name != background.Tools[i].Name {
			t.Fatalf("tool order differs at %d: %q vs %q", i,
				foreground.Tools[i].Name, background.Tools[i].Name)
		}
	}

	// The persona and the stable tiers open the system prompt identically. Only
	// the trailing clock block may differ, so compare what precedes it.
	fg, _, _ := strings.Cut(foreground.System, "# Right now")
	bg, _, _ := strings.Cut(background.System, "# Right now")
	if fg != bg {
		t.Errorf("the job's stable prompt prefix differs from the foreground's, so neither "+
			"can reuse the other's cache:\n--- foreground ---\n%s\n--- background ---\n%s", fg, bg)
	}
}

// A job that moves directory must not move the foreground underneath the user.
func TestAJobsScopeIsItsOwn(t *testing.T) {
	p := &concurrentProvider{gate: make(chan struct{})}
	close(p.gate)
	a, store := newTestAgent(t, p)

	foregroundDir, jobDir := t.TempDir(), t.TempDir()
	a.Scope = skills.NewScope(skills.NewWorkspace(foregroundDir), "", "")

	branch := memory.NewBranch(store, "job1")
	worker := a.ForJob(branch, skills.NewScope(skills.NewWorkspace(jobDir), "job1-", "job1"))

	if worker.Scope.Dir() != jobDir {
		t.Errorf("the job runs in %q, want its own %q", worker.Scope.Dir(), jobDir)
	}
	if a.Scope.Dir() != foregroundDir {
		t.Errorf("creating a job moved the foreground to %q", a.Scope.Dir())
	}
	if worker.Scope.JobID != "job1" {
		t.Errorf("the job is not marked as background work: %q", worker.Scope.JobID)
	}
	if a.Scope.JobID != "" {
		t.Error("the foreground was marked as background work")
	}
}

// Work that finished in the background must reach her as context for her next
// turn, in the volatile tail — never ahead of identity, or a block that changes
// whenever a job ends would rewrite the cached prefix for every turn after it.
func TestPendingWorkRidesInTheVolatileTail(t *testing.T) {
	p := &concurrentProvider{gate: make(chan struct{})}
	close(p.gate)
	a, _ := newTestAgent(t, p)

	delivered := 0
	a.PendingWork = func() string {
		delivered++
		if delivered > 1 {
			return "" // drained: offered exactly once
		}
		return "[Background work finished] job2 — Quizzes 1 and 2 submitted, 8/10 and 9/10."
	}

	if _, err := a.Ask(context.Background(), "what's next"); err != nil {
		t.Fatal(err)
	}

	msgs := p.requests[len(p.requests)-1].Messages
	var at = -1
	for i, m := range msgs {
		if strings.Contains(m.Text, "Quizzes 1 and 2 submitted") {
			at = i
		}
	}
	if at < 0 {
		t.Fatal("the finished job never reached her; she cannot mention what she was not told")
	}
	if at != len(msgs)-2 {
		t.Errorf("the report is at position %d of %d — it must sit just before the user's "+
			"turn, in the volatile tail", at, len(msgs))
	}
	if strings.Contains(p.requests[len(p.requests)-1].System, "Quizzes 1 and 2") {
		t.Error("the report landed in the system prompt, which is the cached prefix")
	}

	// Offered once. A report raised on every turn until the end of time is worse
	// than one never raised at all.
	if _, err := a.Ask(context.Background(), "and now"); err != nil {
		t.Fatal(err)
	}
	for _, m := range p.requests[len(p.requests)-1].Messages {
		if strings.Contains(m.Text, "[Background work finished]") {
			t.Error("the same report was offered a second time")
		}
	}
}
