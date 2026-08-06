package voice

import "sync"

// One ear.
//
// Three things want the microphone: the wake-word listener, which records almost
// continuously; push-to-talk, which records when the key is pressed; and the
// spoken confirmation prompt, which records an answer to a question she just
// asked. Nothing coordinated them. Two recorders on one device produce a clip
// with half an utterance in it, or none at all, and the failure is silent — she
// simply mishears.
//
// The gate is deliberately not a queue. A recording that has to wait its turn has
// already missed the words it was meant to capture, so a claimant that cannot
// have the device now should give up and let the one holding it finish.
type Mic struct {
	mu     sync.Mutex
	holder string
}

// NewMic creates a free microphone gate.
func NewMic() *Mic { return &Mic{} }

// Take claims the device for a named user. It reports whether the claim
// succeeded; a nil Mic always succeeds, so code paths without arbitration (tests,
// the one-shot CLI) behave exactly as they did.
func (m *Mic) Take(who string) bool {
	if m == nil {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.holder != "" {
		return false
	}
	m.holder = who
	return true
}

// Release hands the device back.
func (m *Mic) Release() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.holder = ""
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
