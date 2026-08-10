package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/Akins20/FREYA/internal/glow"
)

// The screen-top status light.
//
// # Why a wrapper rather than using the package directly
//
// Two reasons. The indicator needs X11, and there are perfectly ordinary ways
// to run Freya without it — over SSH, from a service before a session exists,
// on Wayland. None of those should be an error, or even a warning: the light is
// a convenience, and a convenience that refuses to start is worse than one that
// quietly does not appear.
//
// The second reason is that "busy" nests. A voice turn is busy; a tool call
// inside it is also busy; the turn should not go back to idle when the inner
// one finishes. So busy is a counter, not a flag, and the resting state is
// remembered separately from it.

// indicator tracks Freya's visible state, if a screen is available.
type indicator struct {
	mu sync.Mutex
	in *glow.Indicator
	// resting is the state to return to when nothing is in progress: idle when
	// she is merely running, listening when the wake word is armed.
	resting glow.State
	// busy counts overlapping pieces of work.
	busy int
	ctx  context.Context
}

// newIndicator prepares the light, returning a usable no-op if there is no
// display.
func newIndicator(ctx context.Context) *indicator {
	ind := &indicator{resting: glow.StateIdle, ctx: ctx}

	in, err := glow.New()
	if err != nil {
		// No X11 is a normal way to run — over SSH, before login — and not worth
		// a warning in most cases. But the daemon runs where a display is
		// expected, and a light that silently never appears is precisely the bug
		// this just cost an evening to find, so say so once. FREYA_GLOW=off
		// silences it for the genuinely headless.
		if os.Getenv("FREYA_GLOW") != "off" {
			fmt.Fprintf(os.Stderr, "screen glow unavailable: %v\n", err)
		}
		return ind
	}
	ind.in = in
	in.ShowState(ctx, glow.StateIdle)
	return ind
}

// available reports whether there is a light to drive.
func (i *indicator) available() bool { return i != nil && i.in != nil }

// Every method below tolerates a nil receiver, so a caller that has no
// indicator — a headless daemon, a test — need not guard each call.

// setResting changes what she returns to when not working.
func (i *indicator) setResting(s glow.State) {
	if !i.available() {
		return
	}
	i.mu.Lock()
	i.resting = s
	busy := i.busy
	i.mu.Unlock()

	if busy == 0 {
		i.in.SetState(s)
	}
}

// listening arms the light for the wake word.
func (i *indicator) listening() { i.setResting(glow.StateListening) }

// idle returns the light to standby.
func (i *indicator) idle() { i.setResting(glow.StateIdle) }

// working marks the start of a piece of work and returns the function that
// marks its end.
//
// Returning the closer rather than exposing start/stop separately means the
// counter cannot be left unbalanced by an early return — the caller defers it.
func (i *indicator) working() func() {
	if !i.available() {
		return func() {}
	}
	i.mu.Lock()
	i.busy++
	first := i.busy == 1
	i.mu.Unlock()

	if first {
		i.in.SetState(glow.StateBusy)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			i.mu.Lock()
			i.busy--
			done := i.busy == 0
			resting := i.resting
			i.mu.Unlock()

			if done {
				i.in.SetState(resting)
			}
		})
	}
}

// close puts the light out.
func (i *indicator) close() {
	if i.available() {
		_ = i.in.Close()
	}
}

// describe reports the current state for /status.
func (i *indicator) describe() string {
	if !i.available() {
		return "screen glow: unavailable (no X11 display)"
	}
	i.mu.Lock()
	busy := i.busy
	resting := i.resting
	i.mu.Unlock()

	state := resting.String()
	if busy > 0 {
		state = "busy"
	}
	colour := map[string]string{"idle": "red", "listening": "blue", "busy": "amber"}[state]
	return "screen glow: " + state + " (" + colour + ")"
}
