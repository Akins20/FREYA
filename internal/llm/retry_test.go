package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A rate limit used to end the whole exchange, discarding every round of work
// already done. By round twelve each call carries a large prompt, so losing the
// turn to a hiccup that would have cleared in half a second is the worst trade
// available.
func TestPostJSONRetriesRateLimitAndSucceeds(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"slow down"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	got, err := postJSON(context.Background(), srv.Client(), "test", srv.URL, nil, []byte(`{}`))
	if err != nil {
		t.Fatalf("a transient rate limit ended the request: %v", err)
	}
	if !strings.Contains(string(got), "ok") {
		t.Fatalf("unexpected payload %q", got)
	}
	if n := hits.Load(); n != 3 {
		t.Fatalf("server saw %d attempts, want 3", n)
	}
}

// The body must survive a retry. Passing a reader would have it consumed by the
// first attempt and every retry would send an empty request — the exact bug this
// shape exists to avoid.
func TestPostJSONResendsTheBodyOnRetry(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 64)
		n, _ := r.Body.Read(buf)
		bodies = append(bodies, string(buf[:n]))
		if len(bodies) < 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	if _, err := postJSON(context.Background(), srv.Client(), "test", srv.URL, nil,
		[]byte(`{"prompt":"hello"}`)); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(bodies))
	}
	if bodies[0] != bodies[1] {
		t.Fatalf("retry sent a different body:\n first: %q\nsecond: %q", bodies[0], bodies[1])
	}
}

// A 400 is a bug in the request and will fail identically forever. Retrying it
// wastes time and hides the real problem.
func TestPostJSONDoesNotRetryClientErrors(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"malformed"}`))
	}))
	defer srv.Close()

	_, err := postJSON(context.Background(), srv.Client(), "test", srv.URL, nil, []byte(`{}`))
	if err == nil {
		t.Fatal("a 400 should surface, not be swallowed")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("wrong error shape: %v", err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("a client error was retried %d times", n)
	}
}

// After the last attempt the real failure must surface, not a generic one.
func TestPostJSONGivesUpWithTheRealError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"down for maintenance"}`))
	}))
	defer srv.Close()

	_, err := postJSON(context.Background(), srv.Client(), "test", srv.URL, nil, []byte(`{}`))
	if err == nil {
		t.Fatal("expected the failure to surface after the retries")
	}
	if !strings.Contains(err.Error(), "maintenance") {
		t.Fatalf("the provider's own message was lost: %v", err)
	}
}

// A cancelled context is a decision, not a hiccup.
func TestPostJSONHonoursCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := postJSON(ctx, srv.Client(), "test", srv.URL, nil, []byte(`{}`)); err == nil {
		t.Fatal("expected cancellation to end the attempt")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("kept retrying for %v after the context was done", d)
	}
}

// Backoff must not be identical across concurrent callers: tool calls in one
// round fire together, hit the limit together, and would otherwise return in
// lockstep to be limited again.
func TestBackoffIsJittered(t *testing.T) {
	seen := map[time.Duration]bool{}
	for i := 0; i < 40; i++ {
		seen[backoff(2, "")] = true
	}
	if len(seen) < 5 {
		t.Fatalf("backoff produced only %d distinct delays across 40 calls — not jittered", len(seen))
	}
	// And a server's own Retry-After is respected, within a bound.
	if d := backoff(1, "2"); d != 2*time.Second {
		t.Fatalf("Retry-After: 2 produced %v", d)
	}
	if d := backoff(1, "3600"); d > maxDelay*4 {
		t.Fatalf("an absurd Retry-After was honoured unbounded: %v", d)
	}
}

// The measured failure: two calls in her telemetry took 272 seconds and returned
// no tokens at all — a 90s client timeout taken three times. Retrying is worth
// doing; retrying past the point where anyone is still listening is not.
func TestRetriesCannotStackIntoMinutes(t *testing.T) {
	if totalAttemptBudget > 2*time.Minute {
		t.Fatalf("the total budget is %s; a person asked a question and is waiting",
			totalAttemptBudget)
	}

	// A server that never answers must not hold the caller for the full
	// per-attempt timeout, three times over. The handler also releases on the
	// test's own signal, so Close never waits on a connection the client has
	// abandoned but not torn down.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() { close(release); srv.Close() }()

	// A short budget stands in for the real one, so the test is quick; the
	// mechanism under test is that the deadline covers ALL attempts.
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := postJSON(ctx, &http.Client{}, "test", srv.URL, nil, []byte(`{}`))
	took := time.Since(start)

	if err == nil {
		t.Fatal("a hanging server eventually returned success")
	}
	if took > 2*time.Second {
		t.Errorf("gave up after %s — the attempts stacked past the deadline", took)
	}
}

// A caller with its own deadline keeps it: the default must not extend a
// cancellation the caller already decided on.
func TestAnExistingDeadlineIsNotExtended(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() { close(release); srv.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	postJSON(ctx, &http.Client{}, "test", srv.URL, nil, []byte(`{}`))
	if took := time.Since(start); took > time.Second {
		t.Errorf("the caller's 150ms deadline was overridden; took %s", took)
	}
}
