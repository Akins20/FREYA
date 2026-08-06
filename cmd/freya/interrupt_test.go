package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/akins/jarvis/internal/config"
)

// The failure this whole file exists for: she went down a wrong path, and the
// only interface she has — the talk key — did nothing, because the guard that
// keeps two recordings off one microphone also swallowed the interruption.
func TestPressingTheKeyWhileWorkingIsHeard(t *testing.T) {
	defer resetTurns()

	ctx, end := beginTurn(context.Background(), "opening the portal")
	defer end()

	// The microphone is free while she works — that is the whole point.
	if !takeMic() {
		t.Fatal("the microphone was held during a running task, so no interruption could be recorded")
	}
	releaseMic()

	what, ok := stopCurrentTurn()
	if !ok {
		t.Fatal("there was no running task to stop")
	}
	if what != "opening the portal" {
		t.Errorf("the stop did not name what it stopped: %q", what)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("the running task was not actually cancelled — the tools would keep going")
	}
}

// Two recordings on one device produce nothing usable, so that stays exclusive.
func TestRecordingStaysExclusive(t *testing.T) {
	defer resetTurns()

	if !takeMic() {
		t.Fatal("could not take a free microphone")
	}
	if takeMic() {
		t.Fatal("a second recording started on top of the first")
	}
	releaseMic()
	if !takeMic() {
		t.Fatal("the microphone was not released")
	}
	releaseMic()
}

// Speaking over her supersedes what she was doing, the way it does with a person
// — and the superseded exchange gets to finish writing its account first, or the
// transcript records her answering the new question with a report about the old.
func TestNewInstructionSupersedesTheRunningOne(t *testing.T) {
	defer resetTurns()

	first, endFirst := beginTurn(context.Background(), "reading the syllabus")
	// The real caller unwinds when cancelled; model that.
	unwound := make(chan struct{})
	go func() {
		<-first.Done()
		close(unwound)
		endFirst()
	}()

	started := time.Now()
	second, endSecond := beginTurn(context.Background(), "starting the quiz")
	defer endSecond()

	select {
	case <-unwound:
	default:
		t.Fatal("the new exchange started before the old one had finished unwinding, " +
			"so their turns will land in the archive out of order")
	}
	if time.Since(started) >= unwindGrace {
		t.Error("the wait fell through to the timeout instead of the unwind signal")
	}
	if second.Err() != nil {
		t.Fatal("the new instruction was cancelled by the one it replaced")
	}
	if h := currentTurn(); h == nil || h.what != "starting the quiz" {
		t.Fatalf("the newest task is not the one on record: %+v", h)
	}
}

// A wedged exchange must never make her deaf: the wait is a courtesy with a
// deadline, not a lock.
func TestAWedgedExchangeDoesNotBlockTheNewOne(t *testing.T) {
	defer resetTurns()

	_, endStuck := beginTurn(context.Background(), "wedged in something uncancellable")
	defer endStuck()

	done := make(chan struct{})
	go func() {
		_, end := beginTurn(context.Background(), "the new request")
		end()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(unwindGrace + 3*time.Second):
		t.Fatal("a wedged exchange blocked the next one indefinitely")
	}
}

// A finished turn must not clear a newer one, or a stop would silently hit nothing.
func TestFinishingAnOldTurnDoesNotClearTheNewOne(t *testing.T) {
	defer resetTurns()

	first, endFirst := beginTurn(context.Background(), "first")
	go func() { <-first.Done(); endFirst() }()
	beginTurn(context.Background(), "second")

	endFirst() // idempotent: the superseded goroutine also runs its defer

	if h := currentTurn(); h == nil {
		t.Fatal("the running task was forgotten when an older one unwound; a stop would hit nothing")
	} else if h.what != "second" {
		t.Errorf("the wrong task is on record: %q", h.what)
	}
}

// Stopping nothing is not an error, just nothing.
func TestStoppingWhenIdleReportsNothing(t *testing.T) {
	defer resetTurns()
	if _, ok := stopCurrentTurn(); ok {
		t.Fatal("something was stopped when nothing was running")
	}
}

// "Stop" is an instruction about the work. "Stop by the shop" is not.
func TestStopIsDistinguishedFromASentenceMentioningIt(t *testing.T) {
	interruptions := []string{
		"stop", "Stop.", "stop it", "STOP!", "cancel", "cancel that",
		"never mind", "forget it", "wait", "hold on", "that's enough",
		"abort", "freya stop", "okay stop", "drop it",
	}
	for _, s := range interruptions {
		if !isStopInstruction(s) {
			t.Errorf("an interruption was taken as a new request: %q", s)
		}
	}

	requests := []string{
		"stop by the shop on the way home and remind me about milk",
		"remind me to stop the subscription before the trial ends",
		"what time does the bus stop running tonight",
		"open the portal",
		"cancel my gym membership by emailing them tomorrow morning",
		"",
	}
	for _, s := range requests {
		if isStopInstruction(s) {
			t.Errorf("a real request was thrown away as an interruption: %q", s)
		}
	}
}

// What was stopped has to be said out loud, or "stopped" is indistinguishable
// from "crashed".
func TestTheStopSaysWhatItStopped(t *testing.T) {
	msg := describeStopped("opening the portal", time.Now().Add(-90*time.Second))
	if !strings.Contains(msg, "opening the portal") {
		t.Errorf("the acknowledgement does not name the task: %q", msg)
	}
	if !strings.Contains(msg, "1m30s") {
		t.Errorf("the acknowledgement does not say how long it ran: %q", msg)
	}
	if plain := describeStopped("", time.Now()); plain != "Stopped." {
		t.Errorf("an unnamed task should still acknowledge cleanly: %q", plain)
	}
}

// resetTurns clears the package state between tests.
func resetTurns() {
	stopCurrentTurn()
	mic.Release()
}

// Always-on listening must be something the user chose, not something that
// arrives when an unrelated setting is fixed.
//
// It had no off switch at all: wake listening started whenever voice was
// available, and the only reason it was ever quiet was that starting it had
// FAILED. Restoring an API key therefore turned on a microphone that records the
// room almost continuously.
func TestWakeListeningCanBeTurnedOff(t *testing.T) {
	for _, off := range []string{"off", "OFF", "no", "false", "0", "deaf", " off "} {
		if !wakeDisabled(&config.Config{Wake: off}) {
			t.Errorf("FREYA_WAKE=%q did not turn always-on listening off", off)
		}
	}
	for _, on := range []string{"", "on", "yes", "true"} {
		if wakeDisabled(&config.Config{Wake: on}) {
			t.Errorf("FREYA_WAKE=%q silently disabled listening", on)
		}
	}
}
