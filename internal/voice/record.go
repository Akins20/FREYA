package voice

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Recording defaults. 16 kHz mono is the standard speech sampling rate and is
// what every recogniser expects; anything higher is wasted bytes on the wire.
const (
	sampleRate = 16000
	channels   = 1
	bitDepth   = 16

	// startThreshold is how loud the room must get before recording begins.
	//
	// On a healthy microphone silence sits near zero and speech is a few percent
	// of full scale, so 3% cleanly separates them. This briefly lived at 6% to
	// fight a mic whose gain was set so high that its own noise floor clipped at
	// full scale — but that was a hardware misconfiguration to fix at the source,
	// not a threshold to chase, and once the capture gain was sane 3% was right
	// again. The energy gate below is the real backstop against silence.
	startThreshold = "3%"
	// startDuration is how long the sound must be sustained to count as speech
	// beginning. A tenth of a second let a single click start a recording; three
	// tenths requires something with the shape of a spoken syllable.
	startDuration = 0.3
	// stopThreshold is the level below which the room counts as quiet again.
	// Under the start level so a voice tailing off does not flicker the gate.
	stopThreshold = "2%"
	// silenceDuration is how long a pause ends a recording. Two seconds rather
	// than the old 1.5 so a natural mid-sentence breath — "set a timer for…
	// ten minutes" — does not cut the speaker off.
	silenceDuration = 2.0
	// maxRecording is a hard stop, so a stuck gate cannot record forever.
	maxRecording = 60 * time.Second
)

// SoxRecorder captures from the default input device using sox, which stops
// automatically once the speaker falls silent.
//
// Encoding to Ogg happens during capture rather than as a second pass: it costs
// nothing extra here and removes ninety percent of the bytes before upload,
// which is where voice latency actually lives.
//
// The two timing fields are zero by default and matter for one specific job.
// Silence detection only stops a recording when the room falls quiet; in a room
// that never does — a fan, an air conditioner, a nearby conversation above the
// threshold — it runs to the hard ceiling instead. For a command that is fine,
// the ceiling is generous on purpose. For wake-word listening it is a disaster:
// every cycle records the full ceiling, uploads a minute of audio, and the wake
// word is found a minute after it was said. WakeTuned trades length for latency.
type SoxRecorder struct {
	// Max caps a single recording. Zero uses the default ceiling, which is
	// generous because a spoken request can legitimately be long.
	Max time.Duration
	// SilenceSeconds is how long a pause ends the recording. Zero uses the
	// default, tuned not to clip someone mid-sentence.
	SilenceSeconds float64
}

// WakeTuned returns a recorder sized for wake-word capture: a short ceiling so
// background noise cannot stretch a cycle to a minute, and a short trailing
// pause because a wake word is one or two words, not a sentence.
func WakeTuned() SoxRecorder {
	return SoxRecorder{Max: 6 * time.Second, SilenceSeconds: 0.8}
}

func (SoxRecorder) Name() string { return "sox" }

// Record captures speech to path, returning when the speaker stops.
func (s SoxRecorder) Record(ctx context.Context, path string) error {
	limit := maxRecording
	if s.Max > 0 {
		limit = s.Max
	}
	silence := silenceDuration
	if s.SilenceSeconds > 0 {
		silence = s.SilenceSeconds
	}

	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	// The silence effect takes two clauses:
	//   above 1 0.3 6%   — begin only once 0.3s sustains above 6%, which is
	//                      speech onset and not the room's noise floor
	//   below 1 <n> 5%   — stop after n seconds below 5%, i.e. speaker finished
	cmd := exec.CommandContext(ctx, "sox",
		"-q",
		"-d", // default input device
		"-r", strconv.Itoa(sampleRate),
		"-c", strconv.Itoa(channels),
		"-b", strconv.Itoa(bitDepth),
		path,
		"silence",
		"1", strconv.FormatFloat(startDuration, 'f', 1, 64), startThreshold,
		"1", strconv.FormatFloat(silence, 'f', 1, 64), stopThreshold,
	)

	if err := cmd.Run(); err != nil {
		// Hitting the ceiling is a successful long recording, not a failure.
		if ctx.Err() == context.DeadlineExceeded {
			if fileHasAudio(path) {
				return nil
			}
			// Report the limit that actually fired, not the default ceiling: a
			// wake-tuned recorder times out at six seconds, and a message that
			// always says a minute sends the next debugger looking in the wrong
			// place — as it briefly did here.
			return fmt.Errorf("recording timed out after %s with no audio", limit)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("sox: %w", err)
	}
	return nil
}

// pttMaxRecording bounds a single push-to-talk capture. Long, because a spoken
// request after a deliberate key press can be a whole paragraph of instructions
// — "sign in, open this, then do that" — and cutting it at fifteen seconds sent
// half-sentences to be acted on. Finite still, so a stuck gate cannot run away.
const pttMaxRecording = 45 * time.Second

// pttTrailingSilence is how long a pause must last before the capture ends.
//
// This was 1.5s and cut people off mid-thought: a natural pause while deciding
// what to say next reads as the end of speech at that setting, and the recorder
// stopped and sent a fragment. 2.5s tolerates a real thinking pause while still
// ending promptly once someone has genuinely finished.
const pttTrailingSilence = "2.5"

// PushToTalkRecorder captures a command after an explicit trigger.
//
// It differs from the wake recorder in one decisive way: it does not wait for a
// loud onset. The key press already declared that speech is coming, so there is
// nothing to detect — and on some microphones the onset detector never fires
// reliably anyway, which is the failure that made wake-word listening unusable
// on this hardware. Setting the start threshold *below* the room's noise floor
// means recording begins the instant the device opens; the trailing-silence
// clause then ends it when the speaker pauses. Start at once, stop on the pause.
type PushToTalkRecorder struct{}

func (PushToTalkRecorder) Name() string { return "sox-ptt" }

// Record captures until the speaker pauses, or the ceiling is reached.
func (PushToTalkRecorder) Record(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, pttMaxRecording)
	defer cancel()

	// silence 1 0.1 0.1%   — begin almost immediately: 0.1% is below any real
	//                        room, so the first clause is satisfied at once
	// silence 1 <n> 2%     — stop after n seconds below 2%, i.e. a genuine pause
	//                        rather than a mid-sentence breath
	cmd := exec.CommandContext(ctx, "sox",
		"-q", "-d",
		"-r", strconv.Itoa(sampleRate),
		"-c", strconv.Itoa(channels),
		"-b", strconv.Itoa(bitDepth),
		path,
		"silence", "1", "0.1", "0.1%", "1", pttTrailingSilence, "2%",
	)
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			if fileHasAudio(path) {
				return nil // a long command that ran to the ceiling is fine
			}
			return fmt.Errorf("push-to-talk recording captured nothing in %s", pttMaxRecording)
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

// speechFloor is the RMS amplitude, as a fraction of full scale, below which a
// clip is treated as containing no real speech.
//
// It is the backstop behind the recorder's onset threshold. The onset gate can
// still be tripped by a single loud transient — a door, a cough, a knock —
// which produces a clip that is one spike in a second of quiet. Averaged over
// the whole clip that reads as near-silence, and near-silence is exactly what a
// transcriber turns into a confident hallucination. Measuring the average
// rather than the peak is the point: speech sustains energy, a bang does not.
const speechFloor = 0.012

// HasSpeechEnergy reports whether a recording carries real speech rather than
// near-silence, for callers outside this package. See hasSpeechEnergy.
func HasSpeechEnergy(ctx context.Context, path string) bool {
	return hasSpeechEnergy(ctx, path)
}

// hasSpeechEnergy reports whether a recording carries enough sustained energy to
// be worth transcribing.
//
// It shells out to sox for the RMS, which means one cheap local process instead
// of a network round trip that comes back with fiction. When sox is missing or
// the measurement cannot be read, it returns true: the gate must fail open, or
// a parsing quirk would make her deaf.
func hasSpeechEnergy(ctx context.Context, path string) bool {
	if !have("sox") {
		return true
	}
	// `sox <file> -n stat` writes its statistics to stderr; -n discards the
	// audio itself, so nothing is decoded twice.
	cmd := exec.CommandContext(ctx, "sox", path, "-n", "stat")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return true // fail open
	}

	for _, line := range strings.Split(stderr.String(), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "RMS     amplitude") {
			continue
		}
		fields := strings.Fields(line)
		rms, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			return true
		}
		return rms >= speechFloor
	}
	return true // no RMS line found: do not silently swallow speech
}
