// Package voice gives Freya ears and a mouth.
//
// # Why speech recognition is not local
//
// The original plan was whisper.cpp on-device. Measurement changed it. On this
// hardware — a 2013 dual-core ULV part with no GPU — local Whisper is the
// slowest link in the chain, and its accuracy on a laptop microphone is worse
// than Gemini's. Sending compressed audio to a model that already holds the
// conversation is both faster and better:
//
//	format   size    round trip   transcript
//	wav      190 KB  8.34 s       perfect
//	mp3       18 KB  2.89 s       perfect
//	ogg       31 KB  1.83 s       perfect
//
// Ogg Vorbis wins: a tenth the bytes of WAV, and the upload dominates latency.
// Whisper remains available as WhisperSTT for offline use, chosen with
// FREYA_STT=whisper.
//
// # Shape of the pipeline
//
//	record (sox, auto-stops on silence) -> Recognizer -> agent -> Synthesizer
//
// Every stage is an interface, so a better engine can be swapped in without the
// REPL or the agent knowing.
package voice

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Recognizer turns recorded audio into text.
type Recognizer interface {
	Name() string
	// Transcribe reads the audio file at path and returns what was said.
	// An empty string with a nil error means no speech was detected.
	Transcribe(ctx context.Context, path string) (string, error)
}

// Synthesizer speaks text aloud.
type Synthesizer interface {
	Name() string
	Say(ctx context.Context, text string) error
	// Stop interrupts any speech in progress.
	Stop()
}

// Recorder captures microphone audio to a file.
type Recorder interface {
	Name() string
	// Record blocks until the speaker stops talking, the context is cancelled,
	// or the maximum duration elapses.
	Record(ctx context.Context, path string) error
}

// have reports whether a binary is on PATH.
func have(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// Available describes which voice components this machine can actually run,
// so the REPL can explain precisely what is missing rather than failing late.
type Available struct {
	Recorder    string
	Recognizers []string
	Synths      []string
	Problems    []string
}

// Detect probes the system for usable voice tooling.
func Detect(hasGeminiKey bool) Available {
	var a Available

	switch {
	case have("sox"):
		a.Recorder = "sox"
	case have("arecord"):
		a.Recorder = "arecord"
	default:
		a.Problems = append(a.Problems,
			"no recorder: install sox (preferred) or alsa-utils for arecord")
	}

	if hasGeminiKey {
		a.Recognizers = append(a.Recognizers, "gemini")
	}
	if have(whisperBinary()) {
		a.Recognizers = append(a.Recognizers, "whisper")
	}
	if len(a.Recognizers) == 0 {
		a.Problems = append(a.Problems,
			"no speech recognition: set GEMINI_API_KEY, or install whisper.cpp for offline use")
	}

	if have("piper") && piperLooksLikeTTS() {
		a.Synths = append(a.Synths, "piper")
	}
	for _, bin := range []string{"espeak-ng", "espeak"} {
		if have(bin) {
			a.Synths = append(a.Synths, bin)
			break
		}
	}
	if len(a.Synths) == 0 {
		a.Problems = append(a.Problems, "no speech synthesis: install espeak-ng")
	}

	if !have("paplay") && !have("aplay") {
		a.Problems = append(a.Problems, "no audio playback: install pulseaudio-utils or alsa-utils")
	}
	return a
}

// Summary renders Detect's findings for display.
func (a Available) Summary() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "recorder: %s · recognisers: %s · voices: %s",
		orNone(a.Recorder), orNone(strings.Join(a.Recognizers, ", ")),
		orNone(strings.Join(a.Synths, ", ")))
	for _, p := range a.Problems {
		fmt.Fprintf(&sb, "\n  ! %s", p)
	}
	return sb.String()
}

// OK reports whether a full voice loop is possible.
func (a Available) OK() bool {
	return a.Recorder != "" && len(a.Recognizers) > 0 && len(a.Synths) > 0
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
