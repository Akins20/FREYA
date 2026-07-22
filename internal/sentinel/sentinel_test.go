package sentinel

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeWatcher emits a fixed set of observations on demand.
type fakeWatcher struct {
	name string
	obs  []Observation
	mu   sync.Mutex
	runs int
}

func (f *fakeWatcher) Name() string            { return f.name }
func (f *fakeWatcher) Interval() time.Duration { return time.Hour }
func (f *fakeWatcher) Check(context.Context) ([]Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs++
	return f.obs, nil
}

func collector() (func(Observation), *[]Observation, *sync.Mutex) {
	var mu sync.Mutex
	var got []Observation
	return func(o Observation) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, o)
	}, &got, &mu
}

func TestChattinessGatesByUrgency(t *testing.T) {
	cases := []struct {
		chattiness Chattiness
		urgency    Urgency
		wantSpoken bool
	}{
		{ChattyQuiet, UrgencyAmbient, false},
		{ChattyQuiet, UrgencyNotable, false},
		{ChattyQuiet, UrgencyImportant, true},
		{ChattyQuiet, UrgencyCritical, true},

		{ChattyBalanced, UrgencyAmbient, false},
		{ChattyBalanced, UrgencyNotable, true},
		{ChattyBalanced, UrgencyCritical, true},

		{ChattyCompanion, UrgencyAmbient, true},
		{ChattyCompanion, UrgencyNotable, true},
	}

	for _, tc := range cases {
		notify, got, mu := collector()
		s := New(tc.chattiness, notify)
		s.consider(Observation{Key: "k", Summary: "x", Urgency: tc.urgency})

		mu.Lock()
		spoken := len(*got) > 0
		mu.Unlock()

		if spoken != tc.wantSpoken {
			t.Errorf("%s + %s: spoken=%v, want %v",
				tc.chattiness, tc.urgency, spoken, tc.wantSpoken)
		}
	}
}

// TestCriticalAlwaysPassesTheGate is the promise that matters: however quiet
// the setting, a critical condition still reaches the user.
func TestCriticalAlwaysPassesTheGate(t *testing.T) {
	for _, c := range []Chattiness{ChattyQuiet, ChattyBalanced, ChattyCompanion} {
		notify, got, mu := collector()
		s := New(c, notify)
		s.consider(Observation{Key: "fire", Summary: "battery at 3%", Urgency: UrgencyCritical})

		mu.Lock()
		n := len(*got)
		mu.Unlock()
		if n != 1 {
			t.Errorf("chattiness %s swallowed a critical observation", c)
		}
	}
}

// TestRepeatsDecay is the anti-nagging guarantee.
func TestRepeatsDecay(t *testing.T) {
	notify, got, mu := collector()
	s := New(ChattyBalanced, notify)

	obs := Observation{Key: "disk:/", Summary: "disk 86% full", Urgency: UrgencyNotable}

	// Ten identical observations in quick succession.
	for range 10 {
		s.consider(obs)
	}

	mu.Lock()
	n := len(*got)
	mu.Unlock()

	if n != 1 {
		t.Errorf("the same condition was raised %d times in a row, want 1", n)
	}

	// It must still be retrievable on request, just not shouted.
	if len(s.Peek()) == 0 {
		t.Error("suppressed observation was discarded rather than queued")
	}
}

func TestRepeatsResurfaceAfterTheInterval(t *testing.T) {
	notify, got, mu := collector()
	s := New(ChattyBalanced, notify)
	obs := Observation{Key: "disk:/", Summary: "disk full", Urgency: UrgencyNotable}

	s.consider(obs)

	// Backdate the record past the second-mention interval.
	s.mu.Lock()
	s.said[obs.Key].Last = time.Now().Add(-3 * time.Hour)
	s.mu.Unlock()

	s.consider(obs)

	mu.Lock()
	n := len(*got)
	mu.Unlock()
	if n != 2 {
		t.Errorf("raised %d times, want 2 after the interval elapsed", n)
	}
}

func TestCriticalRepeatsSooner(t *testing.T) {
	// A critical condition should nag more than a notable one, but still not
	// continuously.
	notify, got, mu := collector()
	s := New(ChattyBalanced, notify)
	obs := Observation{Key: "batt", Summary: "battery 3%", Urgency: UrgencyCritical}

	s.consider(obs)
	s.mu.Lock()
	s.said[obs.Key].Last = time.Now().Add(-40 * time.Minute)
	s.mu.Unlock()
	s.consider(obs)

	mu.Lock()
	n := len(*got)
	mu.Unlock()
	if n != 2 {
		t.Errorf("critical raised %d times after 40 min, want 2 (quarter of the 2h base)", n)
	}
}

func TestPendingIsSortedAndDrains(t *testing.T) {
	s := New(ChattyQuiet, func(Observation) {})

	s.consider(Observation{Key: "a", Summary: "ambient", Urgency: UrgencyAmbient})
	s.consider(Observation{Key: "b", Summary: "notable", Urgency: UrgencyNotable})

	pending := s.Pending()
	if len(pending) != 2 {
		t.Fatalf("pending = %d, want 2", len(pending))
	}
	if pending[0].Urgency < pending[1].Urgency {
		t.Error("pending not sorted most urgent first")
	}
	if len(s.Peek()) != 0 {
		t.Error("Pending did not drain the queue")
	}
}

func TestPendingDeduplicatesByKey(t *testing.T) {
	s := New(ChattyQuiet, func(Observation) {})
	for i := range 5 {
		s.consider(Observation{
			Key:     "disk:/",
			Summary: "reading " + string(rune('a'+i)),
			Urgency: UrgencyAmbient,
		})
	}
	pending := s.Peek()
	if len(pending) != 1 {
		t.Fatalf("queue holds %d entries for one key, want 1", len(pending))
	}
	// The newest reading should win, not the stalest.
	if pending[0].Summary != "reading e" {
		t.Errorf("queue kept %q, want the most recent", pending[0].Summary)
	}
}

func TestMarkSaidSuppressesLaterInterruption(t *testing.T) {
	// If Freya already mentioned the disk while answering a direct question,
	// the sentinel must not then interrupt with the same news.
	notify, got, mu := collector()
	s := New(ChattyBalanced, notify)

	s.MarkSaid("disk:/")
	s.consider(Observation{Key: "disk:/", Summary: "disk 86% full", Urgency: UrgencyNotable})

	mu.Lock()
	n := len(*got)
	mu.Unlock()
	if n != 0 {
		t.Error("interrupted with something already mentioned in conversation")
	}
}

func TestStartRunsWatchersImmediately(t *testing.T) {
	notify, got, mu := collector()
	s := New(ChattyBalanced, notify)
	w := &fakeWatcher{
		name: "test",
		obs:  []Observation{{Key: "k", Summary: "found", Urgency: UrgencyImportant}},
	}
	s.Add(w)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	// A session must not wait a full interval to learn the disk is already full.
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(*got)
		mu.Unlock()
		if n > 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("watcher did not run on start")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestFailingWatcherIsSilentNotNoisy(t *testing.T) {
	notify, got, mu := collector()
	s := New(ChattyCompanion, notify)
	s.Add(&errorWatcher{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	n := len(*got)
	mu.Unlock()
	if n != 0 {
		t.Error("a failing watcher produced user-facing noise")
	}
}

type errorWatcher struct{}

func (errorWatcher) Name() string            { return "broken" }
func (errorWatcher) Interval() time.Duration { return time.Hour }
func (errorWatcher) Check(context.Context) ([]Observation, error) {
	return nil, context.DeadlineExceeded
}

func TestParseChattiness(t *testing.T) {
	cases := map[string]Chattiness{
		"quiet": ChattyQuiet, "minimal": ChattyQuiet,
		"companion": ChattyCompanion, "chatty": ChattyCompanion,
		"balanced": ChattyBalanced, "": ChattyBalanced, "nonsense": ChattyBalanced,
	}
	for in, want := range cases {
		if got := ParseChattiness(in); got != want {
			t.Errorf("ParseChattiness(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDiskWatcherStaysSilentBelowThreshold(t *testing.T) {
	// The real filesystem is used here; the assertion is only that a watcher
	// with an impossible threshold says nothing.
	w := DiskWatcher{WarnPercent: 100, CriticalPercent: 100}
	obs, err := w.Check(context.Background())
	if err != nil {
		t.Skipf("df unavailable: %v", err)
	}
	for _, o := range obs {
		t.Errorf("reported %q despite a 100%% threshold", o.Summary)
	}
}

func TestMemoryWatcherReadsRealMeminfo(t *testing.T) {
	w := MemoryWatcher{WarnPercent: 1} // certain to trigger
	obs, err := w.Check(context.Background())
	if err != nil {
		t.Skipf("/proc/meminfo unavailable: %v", err)
	}
	if len(obs) == 0 {
		t.Fatal("no observation at a 1% threshold")
	}
	if obs[0].Key != "memory" {
		t.Errorf("key = %q", obs[0].Key)
	}
}

// TestAnnouncedObservationsStayRetrievable is a regression test for a
// self-contradiction: the sentinel announced "your disk is 86% full", then
// answered "nothing, you're running clean" when asked moments later, because
// announcing an observation removed it from the queue.
func TestAnnouncedObservationsStayRetrievable(t *testing.T) {
	notify, got, mu := collector()
	s := New(ChattyBalanced, notify)

	s.consider(Observation{
		Key: "disk:/", Summary: "disk 86% full", Urgency: UrgencyNotable,
	})

	mu.Lock()
	announced := len(*got)
	mu.Unlock()
	if announced != 1 {
		t.Fatalf("announced %d times, want 1", announced)
	}

	// Having said it aloud must not erase it from what she knows.
	pending := s.Peek()
	if len(pending) != 1 {
		t.Fatalf("announced observation is not retrievable: queue holds %d", len(pending))
	}
	if pending[0].Summary != "disk 86% full" {
		t.Errorf("queue holds %q", pending[0].Summary)
	}
}

func TestSuppressedAndAnnouncedCoexist(t *testing.T) {
	s := New(ChattyBalanced, func(Observation) {})

	s.consider(Observation{Key: "loud", Summary: "announced", Urgency: UrgencyImportant})
	s.consider(Observation{Key: "quiet", Summary: "suppressed", Urgency: UrgencyAmbient})

	pending := s.Peek()
	if len(pending) != 2 {
		t.Fatalf("queue holds %d, want both announced and suppressed", len(pending))
	}
}
