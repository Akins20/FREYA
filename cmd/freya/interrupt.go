package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Akins20/FREYA/internal/voice"
	"github.com/Akins20/FREYA/internal/work"
)

// Being able to stop her.
//
// # Why this was missing, and what it cost
//
// A press of the talk key while she was working did nothing at all: the guard
// that stops two recordings fighting over one microphone also swallowed every
// attempt to interrupt. So once she started down a wrong path — and she can spend
// forty rounds on one — there was no way to say "stop" short of killing the
// process. The only interface she has is voice, and voice was deaf exactly when
// it mattered most.
//
// # The distinction that makes it safe
//
// The microphone is busy only while she is RECORDING. While she is thinking and
// running tools — which is nearly all of a long task — the microphone is idle and
// a second press can safely record. So the two states are tracked separately:
// recording is exclusive, working is interruptible. That is the whole trick.

// turnHandle is the in-flight exchange, and the means to stop it.
type turnHandle struct {
	cancel context.CancelFunc
	what   string
	since  time.Time
	// done closes when the exchange has finished unwinding. A replacement waits
	// on it, because the archive is append-only in call order and a cancelled
	// exchange writes its closing account as it unwinds — if the replacement gets
	// there first, the transcript reads as though she answered the new question
	// with a report about the old one.
	done chan struct{}
}

var (
	// mic is the one input device, shared with the wake listener and the spoken
	// confirmation prompt. It lives in the voice package so that every claimant
	// arbitrates against the same gate rather than each keeping its own flag —
	// which is what let two recorders run at once and produce half an utterance.
	mic = voice.NewMic()

	turnMu     sync.Mutex
	activeTurn *turnHandle
)

// takeMic reports whether the caller got the microphone.
func takeMic() bool { return mic.Take("push-to-talk", voice.Deliberate, nil) }

func releaseMic() { mic.Release() }

// beginTurn registers an exchange as interruptible and returns the context it
// should run under, plus a function to call when it finishes.
func beginTurn(parent context.Context, what string) (context.Context, func()) {
	turnMu.Lock()
	previous := activeTurn
	turnMu.Unlock()

	// Starting a new exchange while one is running supersedes it. That is what a
	// person means by talking over you — but the old one must be allowed to finish
	// unwinding first, because it writes a closing account of where it got to and
	// the archive appends in call order. Without the wait the transcript ends up
	// reading: "check my email" / "Your inbox is empty." / "Stopped there — you'd
	// asked for the quizzes…", and the next turn reasons from that.
	if previous != nil {
		previous.cancel()
		select {
		case <-previous.done:
		case <-time.After(unwindGrace):
			// It is wedged in something that ignores cancellation. Losing the
			// ordering is better than refusing to listen to the user.
		}
	}

	ctx, cancel := context.WithCancel(parent)
	h := &turnHandle{cancel: cancel, what: what, since: time.Now(), done: make(chan struct{})}

	turnMu.Lock()
	activeTurn = h
	turnMu.Unlock()

	var once sync.Once
	return ctx, func() {
		turnMu.Lock()
		if activeTurn == h {
			activeTurn = nil
		}
		turnMu.Unlock()
		cancel()
		once.Do(func() { close(h.done) })
	}
}

// unwindGrace is how long a superseding request waits for the one it replaced to
// finish writing its account. Long enough for an archive append, short enough
// that a wedged exchange never makes her look deaf.
const unwindGrace = 3 * time.Second

// currentTurn returns what she is working on, or nil.
func currentTurn() *turnHandle {
	turnMu.Lock()
	defer turnMu.Unlock()
	return activeTurn
}

// stopEverything works out what "stop" means right now and does it.
//
// She has more than one thing she could be doing since background jobs landed,
// so "stop" is no longer a single switch. The order is the order a person means
// it in: the thing she is doing WITH you first, then the thing she is doing FOR
// you. When several background tasks are running, naming them and asking is the
// only honest answer — picking one would be a guess with consequences.
func stopEverything() string {
	if h := currentTurn(); h != nil {
		what, _ := stopCurrentTurn()
		return describeStopped(what, h.since)
	}
	if jobs == nil {
		return "Nothing was running."
	}
	running := jobs.List()
	var live []*work.Job
	for _, j := range running {
		if !j.State().Finished() {
			live = append(live, j)
		}
	}
	switch len(live) {
	case 0:
		return "Nothing was running."
	case 1:
		jobs.Cancel(live[0].ID)
		return "Stopped the background task: " + clipLine(live[0].Goal, 70) + "."
	default:
		var names []string
		for _, j := range live {
			names = append(names, clipLine(j.Goal, 40))
		}
		return fmt.Sprintf("There are %d running: %s. Which one?",
			len(live), strings.Join(names, "; "))
	}
}

// stopCurrentTurn cancels the in-flight exchange and describes what was stopped.
func stopCurrentTurn() (string, bool) {
	turnMu.Lock()
	h := activeTurn
	activeTurn = nil
	turnMu.Unlock()
	if h == nil {
		return "", false
	}
	h.cancel()
	// Not waited on here: a plain stop starts nothing afterwards, so there is no
	// ordering to protect, and the user should hear "stopped" immediately rather
	// than after the exchange has finished tidying up.
	return h.what, true
}

// stopPhrases are the ways a person calls something off mid-flight.
//
// Matched on the whole utterance rather than as substrings, because "stop by the
// shop on the way" is not an instruction to abandon the task. A short utterance
// that is mostly one of these is an interruption; a long sentence that happens to
// contain the word is a new request.
var stopPhrases = []string{
	"stop", "stop it", "stop that", "stop stop", "cancel", "cancel that",
	"abort", "never mind", "nevermind", "forget it", "leave it",
	"quit", "halt", "wait", "wait wait", "hold on", "that's enough",
	"thats enough", "enough", "give up", "drop it", "stop please",
	"please stop", "okay stop", "ok stop", "no stop",
}

// isStopInstruction reports whether an utterance means "abandon what you are
// doing" rather than "here is a new thing to do".
func isStopInstruction(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	t = strings.Trim(t, ".!?,; ")
	if t == "" {
		return false
	}
	for _, p := range stopPhrases {
		if t == p {
			return true
		}
	}
	// Allow a little politeness around it — "freya, stop" — without matching a
	// sentence that merely mentions stopping.
	words := strings.Fields(t)
	if len(words) <= 3 {
		for _, p := range stopPhrases {
			if strings.Contains(t, p) {
				return true
			}
		}
	}
	return false
}

// describeStopped renders what was interrupted, for speaking back.
func describeStopped(what string, since time.Time) string {
	ran := time.Since(since).Round(time.Second)
	if strings.TrimSpace(what) == "" {
		return "Stopped."
	}
	return fmt.Sprintf("Stopped — I was %s (%s in).", strings.TrimSpace(what), ran)
}
