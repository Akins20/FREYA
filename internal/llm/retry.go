package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// Retrying a request that failed for a reason that will pass.
//
// # Why this exists
//
// There was no retry anywhere. A single 429 — one burst of tool-driven calls
// against a rate limit — or one transient 502 from the provider ended the whole
// exchange, discarding every round of work already done and surfacing to the user
// as a bare "http 429". The model had done nothing wrong and neither had she.
//
// The costly part of an exchange is the context: by round twelve each call is
// carrying a large prompt, so losing the turn to a hiccup that would have cleared
// in half a second is the single worst trade available. Three attempts spread
// over a couple of seconds cost almost nothing and save the turn.
//
// Only failures that are genuinely transient are retried. A 400 is a bug in the
// request and will fail identically forever; a 401 is a bad key. Retrying either
// wastes time and hides the real problem.

const (
	maxAttempts = 3
	baseDelay   = 500 * time.Millisecond
	maxDelay    = 4 * time.Second
)

// retryable reports whether a status is worth another attempt.
func retryable(status int) bool {
	switch status {
	case http.StatusTooManyRequests, // 429 — rate limited, the common one
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	}
	return false
}

// backoff returns how long to wait before attempt n (1-based), honouring the
// server's own Retry-After when it sends one.
//
// Jitter matters more than it looks: tool calls inside a round run concurrently,
// so several requests hit the limit at the same instant. Backing off by an
// identical amount would send them back in lockstep to be limited together
// again.
func backoff(attempt int, retryAfter string) time.Duration {
	if retryAfter != "" {
		if secs, err := strconv.Atoi(retryAfter); err == nil && secs >= 0 {
			d := time.Duration(secs) * time.Second
			if d > maxDelay*4 {
				d = maxDelay * 4 // a server asking for a minute still gets a bound
			}
			return d
		}
	}
	d := baseDelay << (attempt - 1)
	if d > maxDelay {
		d = maxDelay
	}
	return d + time.Duration(rand.Int63n(int64(d/2+1)))
}

// postJSON sends a JSON body and returns the response payload, retrying the
// failures that are worth retrying.
//
// The body is passed as bytes rather than a reader because a retry needs to send
// it again, and a reader is consumed by the first attempt — the bug this shape
// exists to avoid.
func postJSON(ctx context.Context, hc *http.Client, provider, url string,
	headers map[string]string, body []byte) ([]byte, error) {

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff(attempt-1, "")):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("%s: build request: %w", provider, err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := hc.Do(req)
		if err != nil {
			// A cancelled context is a decision, not a hiccup; do not retry it.
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return nil, fmt.Errorf("%s: request failed: %w", provider, err)
			}
			lastErr = fmt.Errorf("%s: request failed: %w", provider, err)
			continue
		}

		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		retryAfter := resp.Header.Get("Retry-After")
		status := resp.StatusCode
		resp.Body.Close()

		if readErr != nil {
			lastErr = fmt.Errorf("%s: read response: %w", provider, readErr)
			continue
		}
		if status == http.StatusOK {
			return payload, nil
		}
		if !retryable(status) || attempt == maxAttempts {
			return nil, &APIError{Provider: provider, Status: status, Body: string(payload)}
		}

		lastErr = &APIError{Provider: provider, Status: status, Body: string(payload)}
		// Wait as the server asked, if it asked.
		if retryAfter != "" {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff(attempt, retryAfter)):
			}
		}
	}
	return nil, lastErr
}
