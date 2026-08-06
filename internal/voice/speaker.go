package voice

import (
	"context"
	"sync"
)

// One mouth.
//
// # The bug this closes
//
// Every synthesiser here keeps a single `current *exec.Cmd` so that Stop has
// something to kill. Nothing serialised the calls, so two concurrent Say calls
// each launched a player and each overwrote that field: the audio overlapped, and
// Stop killed only whichever had written it last, leaving the other talking. With
// one thread of work that was hard to hit. Background jobs made it ordinary — a
// job finishing while she is answering you is the normal case, not the edge one.
//
// # Priority, and why background work never queues
//
// There is one speaker, so something has to decide what happens when two things
// want it. The rule is about whose time is being spent:
//
//   - Urgent — an acknowledgement of something the user just did ("stopped").
//     It cuts in, because they are waiting for it and it lasts a second.
//   - Reply — her answer to a question. It waits for the current utterance and
//     then goes ahead.
//   - Background — a report on work nobody is waiting for. It is DROPPED rather
//     than queued: waiting for a gap and then announcing "by the way, that
//     finished" is exactly the interruption this phase exists to stop. Say
//     returns false, and the caller holds the report back for her next reply.
type Priority int

const (
	Background Priority = iota
	Reply
	Urgent
)

func (p Priority) String() string {
	switch p {
	case Urgent:
		return "urgent"
	case Reply:
		return "reply"
	}
	return "background"
}

// Speaker serialises access to the audio device.
type Speaker struct {
	synth Synthesizer

	mu   sync.Mutex
	free *sync.Cond
	// speaking is what is being said right now, and how to cut it off.
	speaking bool
	priority Priority
	stop     context.CancelFunc
}

// NewSpeaker wraps a synthesiser so exactly one thing is ever spoken at a time.
func NewSpeaker(s Synthesizer) *Speaker {
	sp := &Speaker{synth: s}
	sp.free = sync.NewCond(&sp.mu)
	return sp
}

// Name reports the underlying synthesiser.
func (s *Speaker) Name() string { return s.synth.Name() }

// Speaking reports whether audio is playing.
func (s *Speaker) Speaking() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.speaking
}

// Say speaks text, waiting for the device if something else holds it.
//
// It reports whether the words were actually spoken. Background returns false
// without speaking whenever anything else is talking; a preempted utterance
// returns false too, because being cut off by something more urgent is not a
// failure and must not be reported as one.
func (s *Speaker) Say(ctx context.Context, pri Priority, text string) (bool, error) {
	if text == "" {
		return false, nil
	}

	s.mu.Lock()
	if s.speaking {
		switch pri {
		case Background:
			// Never queue behind a conversation.
			s.mu.Unlock()
			return false, nil
		case Urgent:
			// Cut in: the user just acted and is waiting to hear that it landed.
			if s.stop != nil {
				s.stop()
			}
		}
	}
	// Only Reply and Urgent reach here, and each waits at most one utterance —
	// which is why a plain condition variable is enough and no timeout is needed.
	for s.speaking {
		s.free.Wait()
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return false, err
	}

	sayCtx, cancel := context.WithCancel(ctx)
	s.speaking, s.priority, s.stop = true, pri, cancel
	s.mu.Unlock()

	err := s.synth.Say(sayCtx, text)
	preempted := sayCtx.Err() != nil && ctx.Err() == nil
	cancel()

	s.mu.Lock()
	s.speaking, s.stop = false, nil
	s.free.Broadcast()
	s.mu.Unlock()

	if preempted {
		return false, nil
	}
	return err == nil, err
}

// Stop interrupts whatever is being said.
func (s *Speaker) Stop() {
	s.mu.Lock()
	stop := s.stop
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	s.synth.Stop()
}

// AtPriority adapts a Speaker back to the Synthesizer interface at a fixed
// priority.
//
// This is what makes the arbitration unavoidable rather than merely available.
// Session.Speak takes a Synthesizer, and twenty call sites hold a Session; if the
// speaker were only reachable through a new method, any one of them could still
// go straight to the device and the overlap would come back. Wrapping it means
// the old path IS the new path.
func AtPriority(s *Speaker, pri Priority) Synthesizer {
	return fixedPriority{speaker: s, pri: pri}
}

type fixedPriority struct {
	speaker *Speaker
	pri     Priority
}

func (f fixedPriority) Name() string { return f.speaker.Name() }
func (f fixedPriority) Stop()        { f.speaker.Stop() }

func (f fixedPriority) Say(ctx context.Context, text string) error {
	_, err := f.speaker.Say(ctx, f.pri, text)
	return err
}
