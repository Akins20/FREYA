package voice

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// Recording defaults. 16 kHz mono is the standard speech sampling rate and is
// what every recogniser expects; anything higher is wasted bytes on the wire.
const (
	sampleRate = 16000
	channels   = 1
	bitDepth   = 16

	// silenceThreshold is the amplitude below which audio counts as silence.
	// 3% tolerates typical room noise without clipping soft speech.
	silenceThreshold = "3%"
	// silenceDuration is how long the speaker must pause before recording
	// stops. Below about a second it truncates people mid-sentence.
	silenceDuration = 1.5
	// maxRecording is a hard stop, so a stuck gate cannot record forever.
	maxRecording = 60 * time.Second
)

// SoxRecorder captures from the default input device using sox, which stops
// automatically once the speaker falls silent.
//
// Encoding to Ogg happens during capture rather than as a second pass: it costs
// nothing extra here and removes ninety percent of the bytes before upload,
// which is where voice latency actually lives.
type SoxRecorder struct{}

func (SoxRecorder) Name() string { return "sox" }

// Record captures speech to path, returning when the speaker stops.
func (SoxRecorder) Record(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, maxRecording)
	defer cancel()

	// The silence effect takes two clauses:
	//   above 1 0.1 3%  — begin once 0.1s rises above the threshold, which
	//                     trims dead air before the first word
	//   below 1 1.5 3%  — stop after 1.5s below it, i.e. the speaker finished
	cmd := exec.CommandContext(ctx, "sox",
		"-q",
		"-d", // default input device
		"-r", strconv.Itoa(sampleRate),
		"-c", strconv.Itoa(channels),
		"-b", strconv.Itoa(bitDepth),
		path,
		"silence",
		"1", "0.1", silenceThreshold,
		"1", strconv.FormatFloat(silenceDuration, 'f', 1, 64), silenceThreshold,
	)

	if err := cmd.Run(); err != nil {
		// Hitting the ceiling is a successful long recording, not a failure.
		if ctx.Err() == context.DeadlineExceeded {
			if fileHasAudio(path) {
				return nil
			}
			return fmt.Errorf("recording timed out after %s with no audio", maxRecording)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("sox: %w", err)
	}
	return nil
}

// ArecordRecorder is the fallback when sox is absent. It records for a fixed
// duration because arecord alone cannot detect silence.
type ArecordRecorder struct {
	// Duration to capture. Zero means five seconds.
	Duration time.Duration
}

func (ArecordRecorder) Name() string { return "arecord" }

// Record captures a fixed-length clip to path.
func (a ArecordRecorder) Record(ctx context.Context, path string) error {
	d := a.Duration
	if d <= 0 {
		d = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, d+5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "arecord",
		"-q",
		"-f", "S16_LE",
		"-r", strconv.Itoa(sampleRate),
		"-c", strconv.Itoa(channels),
		"-d", strconv.Itoa(int(d.Seconds())),
		path,
	)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("arecord: %w", err)
	}
	return nil
}

// NewRecorder returns the best recorder available.
func NewRecorder() (Recorder, error) {
	if have("sox") {
		return SoxRecorder{}, nil
	}
	if have("arecord") {
		return ArecordRecorder{}, nil
	}
	return nil, fmt.Errorf("no recorder found; install sox or alsa-utils")
}

// minAudioBytes is roughly a tenth of a second of Ogg. Anything smaller is an
// empty container from a recording that captured nothing.
const minAudioBytes = 1024

func fileHasAudio(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > minAudioBytes
}
