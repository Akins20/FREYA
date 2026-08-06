package voice

import (
	"context"
	"sync"
)

// One ear.
//
// Three things want the microphone: the wake-word listener, which records almost
// continuously; push-to-talk, which records when the key is pressed; and the
// spoken confirmation prompt, which records an answer to a question she just
// asked. Nothing coordinated them. Two recorders on one device produce a clip
// with half an utterance in it, or none at all, and the failure is silent — she
// simply mishears.
//
// # Why a plain gate was not enough
//
// The first version was one flag: whoever asked first got the device, everyone
// else was refused. That is exactly backwards for the case that matters. The wake
// listener holds the microphone almost all the time — record, transcribe, repeat
// — so a press of the talk key nearly always arrived while it was recording, was
// refused, and did NOTHING AT ALL. The user pressed the key and nothing happened,
// which is indistinguishable from the software being broken.
//
// A deliberate act outranks an ambient one. Pressing a key is someone asking to
// be heard; the wake listener is a guess that they might. So the listener yields:
// its recording is cancelled, it loses the clip, and the key press gets the
// device. Losing an ambient clip costs nothing — another one starts a moment
// later — and it is the whole difference between a key that works and a key that
// silently does not.
type Mic struct {
	mu     sync.Mutex
	free   *sync.Cond
	holder string
	// yield interrupts the current holder's recording. Only ambient claimants
	// provide one; a deliberate recording is never cut short, because the person
	// speaking into it is the point.
	yield context.CancelFunc
}

// Claim describes how badly the caller needs the device.
type Claim int

const (
	// Ambient is background listening: valuable, but never at the cost of
	// someone deliberately trying to speak.
	Ambient Claim = iota
	// Deliberate is a person asking to be heard — a key press, or an answer to a
	// question she just asked aloud.
	Deliberate
)

// NewMic creates a free microphone gate.
func NewMic() *Mic {
	m := &Mic{}
	m.free = sync.NewCond(&m.mu)
	return m
}

// Take claims the device for a named user, at a given urgency.
//
// It reports whether the claim succeeded. A nil Mic always succeeds, so code
// paths without arbitration — tests, the one-shot CLI — behave exactly as they
// did before it existed.
//
// interrupt, when non-nil, is how this holder can be cut short by a more urgent
// claim. Ambient callers should always supply one; deliberate callers should not.
func (m *Mic) Take(who string, claim Claim, interrupt context.CancelFunc) bool {
	if m == nil {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.holder == "" {
		m.holder, m.yield = who, interrupt
		return true
	}
	if claim != Deliberate {
		return false // ambient never displaces anything
	}

	// Ask the holder to stop, then wait for it to actually let go. Waiting is
	// what makes this safe: returning as soon as the cancel is sent would start a
	// second recorder while the first was still winding down, which is the very
	// thing this gate exists to prevent.
	if m.yield == nil {
		return false // a deliberate recording in progress; do not cut a person off
	}
	m.yield()
	for m.holder != "" {
		m.free.Wait()
	}
	m.holder, m.yield = who, interrupt
	return true
}

// Release hands the device back.
func (m *Mic) Release() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.holder, m.yield = "", nil
	m.free.Broadcast()
	m.mu.Unlock()
}

// Holder reports who has the device, empty when it is free.
func (m *Mic) Holder() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.holder
}
