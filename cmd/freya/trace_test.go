package main

import (
	"sync"
	"testing"
)

// Two subscribers both hear a turn, and one leaving does not silence the other.
//
// This is the bug the old arrangement had. OnThought, OnInterim and OnTool were
// single fields on a shared agent, and four things wanted them: the terminal, the
// spoken narration, the window and the verbose flag. Each assigned, so the last
// one won — opening a window silently took the tool trace off the terminal — and
// the window swapped them per turn, writing fields another goroutine was reading.
func TestEverySubscriberHearsTheTurn(t *testing.T) {
	h := newTraceHub()

	var terminal, window []string
	stopTerminal := h.Add(func(kind, name, text string) {
		terminal = append(terminal, kind+":"+name+":"+text)
	})
	h.Add(func(kind, name, text string) {
		window = append(window, kind+":"+name+":"+text)
	})

	h.emit("thought", "", "working out what they meant")
	h.emit("tool-start", "browser_open", "url=x")
	h.emit("tool-ok", "browser_open", "")

	if len(terminal) != 3 || len(window) != 3 {
		t.Fatalf("terminal saw %d, window saw %d, want 3 each", len(terminal), len(window))
	}
	if terminal[1] != "tool-start:browser_open:url=x" {
		t.Errorf("terminal got %q", terminal[1])
	}

	// The window closes. The terminal must keep hearing everything.
	stopTerminal()
	h.emit("reply", "", "done")
	if len(window) != 4 {
		t.Errorf("the remaining subscriber stopped hearing: %d events", len(window))
	}
	if len(terminal) != 3 {
		t.Errorf("a removed subscriber was still called: %d events", len(terminal))
	}
}

// Removing twice must not panic or take a second subscriber with it, because the
// window's remover is called when a person closes a window and there is nobody
// to be careful on their behalf.
func TestRemovingTwiceIsSafe(t *testing.T) {
	h := newTraceHub()
	var n int
	remove := h.Add(func(string, string, string) { n++ })
	other := 0
	h.Add(func(string, string, string) { other++ })

	remove()
	remove()
	h.emit("thought", "", "x")
	if n != 0 {
		t.Errorf("a removed subscriber was called %d times", n)
	}
	if other != 1 {
		t.Errorf("removing one subscriber twice silenced another: %d", other)
	}
	// And a nil subscriber is a no-op rather than a panic on the next emit.
	h.Add(nil)()
	h.emit("thought", "", "y")
}

// The hub is written from HTTP handlers and read on the agent's goroutine, so
// subscribing while events are in flight has to be safe. Run with -race.
func TestSubscribingDuringATurnIsSafe(t *testing.T) {
	h := newTraceHub()
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				h.emit("thought", "", "thinking")
			}
		}
	}()

	for i := 0; i < 50; i++ {
		remove := h.Add(func(string, string, string) {})
		remove()
	}
	close(stop)
	wg.Wait()
}
