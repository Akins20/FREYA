package sentinel

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The sub-day escalation is the point of goal-aware deadline watching: a quiz
// due tonight should climb through its stages, not sit at one coarse "within a
// day". Each stage must carry the right urgency and its own bucket key so the
// sentinel announces it once.
func TestCommitmentWatcherSubDayEscalation(t *testing.T) {
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	deadline := clock.Add(30 * time.Minute) // fixed; the clock moves toward it
	feed := func() ([]Commitment, error) {
		return []Commitment{{Key: "q", Text: "quiz", Deadline: deadline}}, nil
	}
	w := CommitmentWatcher{Commitments: feed, Now: func() time.Time { return clock }}

	check := func() Observation {
		t.Helper()
		obs, err := w.Check(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(obs) != 1 {
			t.Fatalf("want exactly 1 observation, got %d", len(obs))
		}
		return obs[0]
	}

	stages := []struct {
		remaining time.Duration
		urgency   Urgency
		bucket    string
	}{
		{30 * time.Minute, UrgencyCritical, ":1h"},  // within the hour
		{10 * time.Minute, UrgencyCritical, ":15m"}, // final quarter hour
		{2 * time.Hour, UrgencyImportant, ":3h"},    // a few hours out
		{8 * time.Hour, UrgencyImportant, ":12h"},   // due tonight
		{20 * time.Hour, UrgencyNotable, ":1d"},     // within a day
		{-5 * time.Minute, UrgencyImportant, ":overdue"},
	}
	for _, s := range stages {
		clock = deadline.Add(-s.remaining)
		o := check()
		if o.Urgency != s.urgency || !strings.Contains(o.Key, s.bucket) {
			t.Errorf("at %v remaining: urgency=%v key=%q; want %v containing %q",
				s.remaining, o.Urgency, o.Key, s.urgency, s.bucket)
		}
	}
}
