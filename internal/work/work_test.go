package work

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The point of the whole package: asking her to do something slow must not mean
// waiting for it.
func TestStartReturnsBeforeTheWorkIsDone(t *testing.T) {
	release := make(chan struct{})
	m := New(context.Background(), 2, func(ctx context.Context, j *Job) (string, error) {
		<-release
		return "finished " + j.Goal, nil
	})
	defer m.Shutdown(time.Second)

	started := time.Now()
	j, err := m.Start("work through the quizzes", "you")
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took > 200*time.Millisecond {
		t.Fatalf("Start blocked for %s — the caller is waiting for the work", took)
	}
	if s := j.State(); s.Finished() {
		t.Fatalf("the job was already %s before the runner released", s)
	}

	close(release)
	waitFor(t, j, Done)
	if result, _ := j.Result(); result != "finished work through the quizzes" {
		t.Errorf("result = %q", result)
	}
}

// The pool is bounded because her attention and the provider's rate limit are,
// not because the machine is.
func TestOnlyTwoJobsRunAtOnce(t *testing.T) {
	var live, peak atomic.Int32
	release := make(chan struct{})
	m := New(context.Background(), 2, func(ctx context.Context, j *Job) (string, error) {
		n := live.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		live.Add(-1)
		return "", nil
	})
	defer m.Shutdown(2 * time.Second)

	for i := 0; i < 4; i++ {
		if _, err := m.Start(fmt.Sprintf("task %d", i), "you"); err != nil {
			t.Fatal(err)
		}
	}
	// Give the pool time to over-admit if it is going to.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && live.Load() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := peak.Load(); got > 2 {
		t.Fatalf("%d jobs ran at once; the pool is meant to hold 2", got)
	}
	close(release)
}

// A queue that grows without limit turns "I'll do it in the background" into a
// promise she will not keep.
func TestTheQueueIsBounded(t *testing.T) {
	block := make(chan struct{})
	m := New(context.Background(), 1, func(ctx context.Context, j *Job) (string, error) {
		<-block
		return "", nil
	})
	defer m.Shutdown(2 * time.Second)

	var lastErr error
	for i := 0; i < maxQueued+5; i++ {
		if _, err := m.Start(fmt.Sprintf("task %d", i), "you"); err != nil {
			lastErr = err
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !errors.Is(lastErr, ErrBusy) {
		t.Fatalf("the queue accepted work without limit: %v", lastErr)
	}
	close(block)
}

// "Stop" has to actually stop it, including a job that has not started yet.
func TestCancellingAQueuedJobNeverRunsIt(t *testing.T) {
	block := make(chan struct{})
	var ran atomic.Bool
	m := New(context.Background(), 1, func(ctx context.Context, j *Job) (string, error) {
		if j.Goal == "second" {
			ran.Store(true)
		}
		<-block
		return "", nil
	})
	defer m.Shutdown(2 * time.Second)

	first, _ := m.Start("first", "you")
	waitFor(t, first, Running)
	second, _ := m.Start("second", "you")

	if !m.Cancel(second.ID) {
		t.Fatal("a queued job could not be cancelled")
	}
	waitFor(t, second, Cancelled)
	close(block)

	time.Sleep(100 * time.Millisecond)
	if ran.Load() {
		t.Fatal("a cancelled job ran anyway once a slot came free")
	}
}

// Cancelling something the user asked to stop is not a failure, and reporting it
// as one puts a false alarm in front of them.
func TestCancellationIsNotReportedAsFailure(t *testing.T) {
	m := New(context.Background(), 1, func(ctx context.Context, j *Job) (string, error) {
		<-ctx.Done()
		// A runner unwinding from a cancelled context reports an error that is a
		// consequence, not a cause.
		return "got two of three done", ctx.Err()
	})
	defer m.Shutdown(2 * time.Second)

	j, _ := m.Start("the quizzes", "you")
	waitFor(t, j, Running)
	m.Cancel(j.ID)
	waitFor(t, j, Cancelled)

	result, err := j.Result()
	if err != nil {
		t.Errorf("a deliberate stop was recorded as an error: %v", err)
	}
	if result != "got two of three done" {
		t.Errorf("how far it got was discarded: %q", result)
	}
}

// A finished job must be reported exactly once, whatever it did.
func TestEveryJobIsReportedOnce(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}

	m := New(context.Background(), 2, func(ctx context.Context, j *Job) (string, error) {
		switch j.Goal {
		case "boom":
			return "", errors.New("disk on fire")
		case "stop me":
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "done", nil
	})
	m.OnFinish = func(j *Job) {
		mu.Lock()
		seen[j.ID]++
		mu.Unlock()
	}
	defer m.Shutdown(2 * time.Second)

	ok, _ := m.Start("fine", "you")
	bad, _ := m.Start("boom", "you")
	waitFor(t, ok, Done)
	waitFor(t, bad, Failed)

	stopped, _ := m.Start("stop me", "you")
	waitFor(t, stopped, Running)
	m.Cancel(stopped.ID)
	waitFor(t, stopped, Cancelled)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 {
		t.Fatalf("reported %d jobs, want 3: %v", len(seen), seen)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("%s was reported %d times, want exactly once", id, n)
		}
	}
	if _, err := bad.Result(); err == nil {
		t.Error("a failure was not recorded as one")
	}
}

// Progress has to be answerable while it happens, and bounded — a forty-round job
// must not accumulate a second transcript here.
func TestProgressIsKeptButBounded(t *testing.T) {
	j := &Job{ID: "job1", Goal: "g", state: Running}
	for i := 0; i < 60; i++ {
		j.Note(fmt.Sprintf("step %d", i))
	}
	p := j.Progress()
	if len(p) > maxNotes+1 {
		t.Fatalf("kept %d notes; this is a glance, not a transcript", len(p))
	}
	if !strings.Contains(strings.Join(p, "\n"), "more") {
		t.Error("notes were dropped without saying so")
	}
	if !strings.Contains(p[0], "step 0") {
		t.Error("the beginning was thrown away; the arc needs both ends")
	}
	if !strings.Contains(p[len(p)-1], "step 59") {
		t.Error("the most recent note — what she is doing NOW — was dropped")
	}
}

// Shutting the process down must not leave a job half-written.
func TestShutdownStopsEverything(t *testing.T) {
	var finished atomic.Int32
	m := New(context.Background(), 2, func(ctx context.Context, j *Job) (string, error) {
		<-ctx.Done()
		finished.Add(1)
		return "", ctx.Err()
	})
	m.Start("a", "you")
	m.Start("b", "you")
	time.Sleep(50 * time.Millisecond)

	m.Shutdown(2 * time.Second)
	if got := finished.Load(); got != 2 {
		t.Fatalf("%d of 2 jobs unwound on shutdown", got)
	}
	for _, j := range m.List() {
		if !j.State().Finished() {
			t.Errorf("%s is still %s after shutdown", j.ID, j.State())
		}
	}
}

func waitFor(t *testing.T, j *Job, want State) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if j.State() == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s stayed %s, waiting for %s", j.ID, j.State(), want)
}

// A job stopped before it ever ran has nothing to report and no failure to
// confess.
func TestAJobCancelledBeforeRunningIsSilentAndClean(t *testing.T) {
	block := make(chan struct{})
	m := New(context.Background(), 1, func(ctx context.Context, j *Job) (string, error) {
		<-block
		return "", nil
	})
	defer m.Shutdown(2 * time.Second)

	first, _ := m.Start("first", "you")
	waitFor(t, first, Running)
	queued, _ := m.Start("second", "you")
	m.Cancel(queued.ID)
	waitFor(t, queued, Cancelled)

	result, err := queued.Result()
	if err != nil {
		t.Errorf("a job that never ran reported a failure: %v", err)
	}
	if result != "" {
		t.Errorf("a job that never ran reported work: %q", result)
	}
	if !queued.Started.IsZero() {
		t.Error("a job that never ran claims a start time")
	}
	close(block)
}
