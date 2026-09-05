package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/Akins20/FREYA/internal/agent"
)

// The window's hand on the microphone.
//
// # Why the browser's microphone is not used
//
// getUserMedia would have been less code than this. It would also have been a
// second audio stack living beside the one she already has: a second recorder
// competing for one device with the wake listener, a second silence detector, a
// second encoder — and no speaker verification, because internal/voice/verify.go
// gates who she obeys on a voiceprint, and audio arriving as a blob from a web
// page has walked around that gate.
//
// So the window presses the button and everything downstream is unchanged.
// pushToTalk is the same function the Ctrl+Space hotkey and the daemon socket
// call, so a spoken request through the window records, transcribes, verifies,
// supersedes whatever was running and speaks the answer exactly as it does
// everywhere else. There is one pipeline and three doors to it.
type guiControls struct {
	ctx context.Context
	a   *agent.Agent
	vs  *voiceState
}

// Talk starts one exchange and returns immediately.
//
// It is tap-to-talk, not push-to-talk, whatever the function is called: the
// recorder stops on silence rather than on a key release, so the window's button
// is pressed once and then spoken at. Calling it "hold to talk" in the interface
// would be a lie about how it behaves.
func (g *guiControls) Talk() error {
	if g.vs == nil || g.vs.session == nil {
		return errors.New("voice is not set up in this session")
	}
	// takeMic inside pushToTalk is the real gate and it returns silently. Saying
	// so here is what turns a button that does nothing into a button that says
	// why — the usual cause is a second press while the first is still recording.
	if who := mic.Holder(); who != "" {
		return fmt.Errorf("the microphone is in use by the %s", who)
	}
	// Started, not awaited. Recording, a model call and synthesis together are far
	// longer than an HTTP request should be held open; the window follows the rest
	// on the event stream, the same way it follows a typed turn.
	go pushToTalk(g.ctx, g.a, g.vs)
	return nil
}

// Voice turns spoken replies on or off, and reports what it actually is.
//
// Reporting the state afterwards rather than echoing the request matters when
// voice is unavailable: asking for it on and being told it is off is the honest
// answer, and it keeps the button from lying about a microphone that is not
// there.
func (g *guiControls) Voice(on bool) bool {
	if g.vs == nil {
		return false
	}
	g.vs.setVoice(on)
	return g.vs.voiceOn()
}

// Stop is the same interrupt as Ctrl-C and the spoken "stop": it cancels the
// turn in flight and cuts off anything mid-sentence.
func (g *guiControls) Stop() string { return stopEverything() }
