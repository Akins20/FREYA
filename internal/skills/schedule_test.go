package skills

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Akins20/FREYA/internal/schedule"
)

func TestParseWhen(t *testing.T) {
	ok := []string{"10m", "2h", "1d", "30s", "in 15m", "in 2h", "2026-07-24 17:00", "2026-07-24"}
	for _, s := range ok {
		if _, err := parseWhen(s); err != nil {
			t.Errorf("parseWhen(%q) should parse, got %v", s, err)
		}
	}
	bad := []string{"", "soonish", "banana", "next tuesday"}
	for _, s := range bad {
		if _, err := parseWhen(s); err == nil {
			t.Errorf("parseWhen(%q) should have failed", s)
		}
	}
	// An offset lands roughly the right distance in the future.
	got, err := parseWhen("in 10m")
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(got); d < 9*time.Minute || d > 11*time.Minute {
		t.Fatalf("'in 10m' landed %v away, want ~10m", d)
	}
}

func TestScheduleSelfToolRoundTrip(t *testing.T) {
	store, err := schedule.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := New()
	RegisterSchedule(r, store)

	out, err := r.Execute(context.Background(), "schedule_self", map[string]any{
		"prompt": "check the download and unzip it",
		"when":   "10m",
		"note":   "download",
	})
	if err != nil {
		t.Fatalf("schedule_self: %v", err)
	}
	if !strings.Contains(out, "Scheduled") {
		t.Fatalf("unexpected confirmation: %q", out)
	}

	pending, _ := store.Pending()
	if len(pending) != 1 || pending[0].Prompt != "check the download and unzip it" {
		t.Fatalf("task not stored: %v", pending)
	}

	list, _ := r.Execute(context.Background(), "scheduled_list", map[string]any{})
	if !strings.Contains(list, "check the download") {
		t.Fatalf("scheduled_list did not show it: %q", list)
	}

	if _, err := r.Execute(context.Background(), "scheduled_cancel", map[string]any{"id": pending[0].ID}); err != nil {
		t.Fatalf("scheduled_cancel: %v", err)
	}
	if p, _ := store.Pending(); len(p) != 0 {
		t.Fatalf("task not cancelled: %v", p)
	}

	// A missing prompt must be rejected, not silently scheduled as a no-op.
	if _, err := r.Execute(context.Background(), "schedule_self", map[string]any{"when": "5m"}); err == nil {
		t.Fatal("schedule_self with no prompt should error")
	}
}
