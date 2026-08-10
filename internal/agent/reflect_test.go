package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Akins20/FREYA/internal/reflect"
)

// countingLens records that it was asked to look.
type countingLens struct {
	mu    sync.Mutex
	looks int
	query string
	reply string
}

func (l *countingLens) Name() string { return "counting" }

func (l *countingLens) Look(_ context.Context, in reflect.Input) ([]reflect.Insight, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.looks++
	l.query, l.reply = in.Query, in.Reply
	return nil, nil
}

func (l *countingLens) seen() (int, string, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.looks, l.query, l.reply
}

// The failure this pins is invisible by construction.
//
// The reflection lenses had FOUR readers — the insights tier in the context
// builder, /reflect, the recall_perspectives tool, and the proactive loop — and
// nothing whatsoever producing for them, because reflectAfter was written and
// never called. Every one of those readers answered "no additional angles
// surfaced", which is indistinguishable from "the lenses ran and found nothing".
// So the system reported working while an entire memory tier had never once
// executed, and sixteen passing tests in internal/reflect said it was fine.
//
// A test that a producer exists is the only thing that can tell those two apart.
func TestAnExchangeActuallyFeedsTheLenses(t *testing.T) {
	a, _ := newTestAgent(t, &concurrentProvider{gate: closedGate()})

	lens := &countingLens{}
	r := &reflect.Reflector{}
	r.Add(lens)
	a.Reflector = r

	if _, err := a.Ask(context.Background(), "what did we decide about the drive"); err != nil {
		t.Fatal(err)
	}

	// Reflection is deliberately detached and asynchronous — the exchange is
	// finished, and the user's reply must not wait on it.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n, _, _ := lens.seen(); n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	n, query, reply := lens.seen()
	if n == 0 {
		t.Fatal("the exchange finished and no lens was ever asked to look — the " +
			"reflection tier has no producer, so every reader of it reports " +
			"'nothing surfaced' forever")
	}
	if query != "what did we decide about the drive" {
		t.Errorf("the lens was given query %q, not the user's message", query)
	}
	if reply == "" {
		t.Error("the lens was given an empty reply, so it cannot reason about the answer")
	}
}

// Reflection must not be able to fail a turn or slow it down. It runs after the
// answer is already out, on a context of its own, so a slow or panicking lens is
// the reflection's problem and never the user's.
func TestReflectionCannotDelayTheAnswer(t *testing.T) {
	a, _ := newTestAgent(t, &concurrentProvider{gate: closedGate()})

	blocked := make(chan struct{})
	r := &reflect.Reflector{}
	r.Add(&blockingLens{until: blocked})
	a.Reflector = r

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := a.Ask(context.Background(), "hello"); err != nil {
			t.Error(err)
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the answer waited on a blocked lens — reflection is supposed to be " +
			"detached from the turn that triggered it")
	}
	close(blocked)
}

type blockingLens struct{ until chan struct{} }

func (l *blockingLens) Name() string { return "blocking" }

func (l *blockingLens) Look(ctx context.Context, _ reflect.Input) ([]reflect.Insight, error) {
	select {
	case <-l.until:
	case <-ctx.Done():
	}
	return nil, nil
}

func closedGate() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}
