package main

import "sync"

// One place her thinking goes, and everyone who wants it subscribes.
//
// # What this replaces
//
// OnThought, OnInterim and OnTool are single fields on a shared *agent.Agent,
// and three different things wanted them at once. The REPL set them to print.
// The daemon wrapped OnInterim to speak. The window swapped all three at the
// start of every turn and put back what it found.
//
// That last one is a data race by construction: the swap writes fields another
// goroutine is reading, and if a REPL turn and a window turn overlap, one of
// them has its trace delivered to the other's surface. It also cannot compose —
// whoever assigns last wins, so opening a window silently took the tool trace
// off the terminal.
//
// A hub costs one indirection and removes the whole class. The agent's hooks are
// set once, at startup, and never touched again; the terminal, the daemon's
// speaker and the window are all subscribers, each free to arrive and leave
// without knowing about the others.
type traceHub struct {
	mu   sync.RWMutex
	next int
	subs map[int]TraceFunc
}

// TraceFunc receives one thing that happened during a turn.
//
// Deliberately three strings rather than three methods: every subscriber wants a
// different subset, and an interface would make each of them implement the parts
// they ignore.
//
// kind is one of:
//
//	thought      her reasoning before a step
//	interim      text the model produced alongside its tool calls
//	tool-start   a tool was called; name is set, text is the arguments
//	tool-ok      it succeeded
//	tool-error   it failed; text is why
//
// A spoken exchange publishes the same way, because from the window's side a
// voice turn and a typed one are the same turn arriving through a different
// door — and if they did not share a channel, pressing the microphone would
// leave the thread blank while she worked:
//
//	listening    the microphone is open
//	heard        what she heard; this is the user's turn
//	speaking     she has started saying this out loud
//	spoken       the reply that ended the exchange
//	turn-done    the exchange finished, however it finished
//
// The terminal subscriber ignores the voice kinds — it prints them itself, on
// the path that produced them, and printing twice is worse than not at all.
type TraceFunc func(kind, name, text string)

func newTraceHub() *traceHub { return &traceHub{subs: map[int]TraceFunc{}} }

// Add subscribes, and returns the way to stop.
//
// The remover is idempotent and safe from any goroutine, because the window
// unsubscribing is a thing that happens when a person closes it and there is
// nobody to be careful on their behalf.
func (h *traceHub) Add(fn TraceFunc) (remove func()) {
	if fn == nil {
		return func() {}
	}
	h.mu.Lock()
	id := h.next
	h.next++
	h.subs[id] = fn
	h.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, id)
			h.mu.Unlock()
		})
	}
}

// emit delivers to everyone listening.
//
// Called synchronously, in the agent's own goroutine, because the terminal
// printing has to stay in order with the rest of the line and a subscriber that
// blocks is a subscriber that is wrong. The window's subscriber hands off to a
// non-blocking channel of its own; that is where the decoupling belongs.
func (h *traceHub) emit(kind, name, text string) {
	// Nil-safe: voiceState carries a hub that is only set once one exists, and a
	// spoken exchange in a session that never built one must still work rather
	// than take the process down.
	if h == nil {
		return
	}
	h.mu.RLock()
	subs := make([]TraceFunc, 0, len(h.subs))
	for _, fn := range h.subs {
		subs = append(subs, fn)
	}
	h.mu.RUnlock()
	for _, fn := range subs {
		fn(kind, name, text)
	}
}
