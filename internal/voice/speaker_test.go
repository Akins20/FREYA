package voice

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSynth records what it was asked to say and how much of it overlapped.
type fakeSynth struct {
	dur time.Duration

	mu     sync.Mutex
	live   int
	peak   int
	spoken []string
	cutOff []string
}

func (f *fakeSynth) Name() string { return "fake" }
func (f *fakeSynth) Stop()        {}

func (f *fakeSynth) Say(ctx context.Context, text string) error {
	f.mu.Lock()
	f.live++
	if f.live > f.peak {
		f.peak = f.live
	}
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.live--
		f.mu.Unlock()
	}()

	select {
	case <-time.After(f.dur):
		f.mu.Lock()
		f.spoken = append(f.spoken, text)
		f.mu.Unlock()
		return nil
	case <-ctx.Done():
		f.mu.Lock()
		f.cutOff = append(f.cutOff, text)
		f.mu.Unlock()
		return ctx.Err()
	}
}

func (f *fakeSynth) result() (peak int, spoken, cut []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peak, append([]string(nil), f.spoken...), append([]string(nil), f.cutOff...)
}

// The measured bug: concurrent Say calls each launched a player, so the audio
// overlapped and Stop killed only the last one.
func TestOnlyOneThingIsEverSpokenAtOnce(t *testing.T) {
	f := &fakeSynth{dur: 40 * time.Millisecond}
	s := NewSpeaker(f)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.Say(context.Background(), Reply, "utterance")
		}(i)
	}
	wg.Wait()

	peak, spoken, _ := f.result()
	if peak > 1 {
		t.Fatalf("%d utterances played at once — that is the overlapping audio bug", peak)
	}
	if len(spoken) != 5 {
		t.Errorf("spoke %d of 5 replies", len(spoken))
	}
}

// A report nobody is waiting for must never interrupt a conversation, and must
// tell its caller it was not spoken so the report can be held back.
func TestBackgroundNeverInterruptsAndSaysSo(t *testing.T) {
	f := &fakeSynth{dur: 80 * time.Millisecond}
	s := NewSpeaker(f)

	go s.Say(context.Background(), Reply, "her answer")
	waitUntil(t, s.Speaking)

	said, err := s.Say(context.Background(), Background, "by the way, that job finished")
	if err != nil {
		t.Fatal(err)
	}
	if said {
		t.Fatal("a background report talked over a live reply")
	}

	_, spoken, _ := f.result()
	for _, s := range spoken {
		if strings.Contains(s, "by the way") {
			t.Fatal("the background report was spoken anyway")
		}
	}
}

// When she is quiet, a report should just be said.
func TestBackgroundSpeaksWhenSheIsIdle(t *testing.T) {
	f := &fakeSynth{dur: time.Millisecond}
	s := NewSpeaker(f)

	said, err := s.Say(context.Background(), Background, "the quizzes are done")
	if err != nil || !said {
		t.Fatalf("a report was dropped even though nothing was speaking: said=%v err=%v", said, err)
	}
}

// The user just pressed the key. Whatever she was saying stops, immediately.
func TestUrgentCutsIn(t *testing.T) {
	f := &fakeSynth{dur: 2 * time.Second}
	s := NewSpeaker(f)

	replyReturned := make(chan struct{})
	go func() {
		s.Say(context.Background(), Reply, "a long rambling answer")
		close(replyReturned)
	}()
	waitUntil(t, s.Speaking)

	// The acknowledgement takes the fake's full duration to "speak", so timing the
	// urgent call would measure the wrong thing. What matters is that the reply
	// stops as soon as the urgent one arrives.
	start := time.Now()
	acked := make(chan bool, 1)
	go func() {
		said, _ := s.Say(context.Background(), Urgent, "stopped")
		acked <- said
	}()

	select {
	case <-replyReturned:
		if took := time.Since(start); took > time.Second {
			t.Fatalf("the reply played on for %s after the acknowledgement arrived", took)
		}
	case <-time.After(time.Second):
		t.Fatal("the reply was not cut off; the acknowledgement is queued behind it")
	}

	if !<-acked {
		t.Fatal("the acknowledgement was not spoken")
	}

	_, spoken, cut := f.result()
	if len(cut) != 1 || !strings.Contains(cut[0], "rambling") {
		t.Errorf("the interrupted reply was not cut off: cut=%v", cut)
	}
	if len(spoken) != 1 || spoken[0] != "stopped" {
		t.Errorf("spoken = %v, want just the acknowledgement", spoken)
	}
}

// Being cut off is not a failure and must not be reported as one — otherwise
// every interruption logs an error the user caused on purpose.
func TestPreemptionIsNotAnError(t *testing.T) {
	f := &fakeSynth{dur: 2 * time.Second}
	s := NewSpeaker(f)

	done := make(chan error, 1)
	go func() {
		_, err := s.Say(context.Background(), Reply, "long answer")
		done <- err
	}()
	waitUntil(t, s.Speaking)
	s.Say(context.Background(), Urgent, "stopped")

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("an interruption the user asked for was reported as an error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the interrupted utterance never returned")
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition never became true")
}
