package voice

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Session drives one full spoken exchange: capture, transcribe, hand the text
// to a thinker, then speak the answer.
type Session struct {
	Recorder   Recorder
	Recognizer Recognizer
	Synth      Synthesizer

	// TempDir holds recordings. Empty means the OS temp directory.
	TempDir string
	// KeepRecordings retains audio files instead of deleting them, for debugging.
	KeepRecordings bool

	// Hooks report progress so the UI can show what is happening. All optional.
	OnListening   func()
	OnTranscribed func(text string, took time.Duration)
	OnSpeaking    func(text string)
}

// Turn is the outcome of one spoken exchange.
type Turn struct {
	Transcript string
	Reply      string
	RecordTime time.Duration
	STTTime    time.Duration
}

// Listen records one utterance and returns its transcript. An empty string
// means nothing intelligible was said, which is not an error — the caller
// should simply prompt again.
func (s *Session) Listen(ctx context.Context) (string, error) {
	dir := s.TempDir
	if dir == "" {
		dir = os.TempDir()
	}
	// Ogg keeps the upload small, which dominates recognition latency.
	path := filepath.Join(dir, fmt.Sprintf("freya-%d.ogg", time.Now().UnixNano()))
	if !s.KeepRecordings {
		defer os.Remove(path)
	}

	if s.OnListening != nil {
		s.OnListening()
	}
	if err := s.Recorder.Record(ctx, path); err != nil {
		return "", fmt.Errorf("record: %w", err)
	}
	if !fileHasAudio(path) {
		return "", nil
	}

	start := time.Now()
	text, err := s.Recognizer.Transcribe(ctx, path)
	if err != nil {
		return "", fmt.Errorf("transcribe: %w", err)
	}
	if s.OnTranscribed != nil {
		s.OnTranscribed(text, time.Since(start))
	}
	return text, nil
}

// Speak reads text aloud.
func (s *Session) Speak(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	if s.OnSpeaking != nil {
		s.OnSpeaking(text)
	}
	return s.Synth.Say(ctx, text)
}

// Interrupt stops speech immediately.
func (s *Session) Interrupt() { s.Synth.Stop() }

// Thinker turns a transcript into a reply. The agent satisfies this without
// the voice package needing to know anything about it.
type Thinker func(ctx context.Context, input string) (string, error)

// Exchange runs one complete listen-think-speak cycle.
func (s *Session) Exchange(ctx context.Context, think Thinker) (*Turn, error) {
	recStart := time.Now()
	transcript, err := s.Listen(ctx)
	if err != nil {
		return nil, err
	}
	turn := &Turn{Transcript: transcript, RecordTime: time.Since(recStart)}
	if transcript == "" {
		return turn, nil
	}

	reply, err := think(ctx, transcript)
	if err != nil {
		// Speak the failure rather than dying silently; in a voice session the
		// terminal may not even be in view.
		_ = s.Speak(ctx, "Something went wrong handling that.")
		return turn, err
	}
	turn.Reply = reply

	if err := s.Speak(ctx, reply); err != nil {
		return turn, fmt.Errorf("speak: %w", err)
	}
	return turn, nil
}
