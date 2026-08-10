package voice

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/Akins20/FREYA/internal/llm"
)

// GeminiTTS speaks using Gemini's neural speech models.
//
// Quality is far beyond espeak: real prosody, punctuation that lands, a choice
// of voices, and delivery steerable in plain English. The cost is network
// latency — roughly 3.5 to 4 seconds for a short reply.
//
// That cost is mostly hidden by pipelining. Sentences are synthesised one
// ahead of playback, so only the first sentence is actually waited on; by the
// time it finishes playing the next is usually ready. A three-sentence reply
// therefore feels like one wait, not three.
type GeminiTTS struct {
	Synth llm.SpeechSynthesizer
	// Style controls voice, pace, pitch and tone.
	Style Style
	// Lookahead is how many sentences to synthesise ahead of playback.
	// One is enough to cover playback of the sentence before it.
	Lookahead int

	mu      sync.Mutex
	current *exec.Cmd
	stopped bool
}

func (g *GeminiTTS) Name() string {
	voice := g.Style.Voice
	if voice == "" {
		voice = llm.DefaultVoice
	}
	return "gemini/" + voice
}

// clip is one synthesised sentence awaiting playback.
type clip struct {
	audio []byte
	mime  string
	err   error
}

// Say speaks the text, synthesising ahead of playback to hide latency.
func (g *GeminiTTS) Say(ctx context.Context, text string) error {
	text = ForSpeech(text)
	if text == "" {
		return nil
	}

	g.mu.Lock()
	g.stopped = false
	g.mu.Unlock()

	sentences := splitSentences(text)
	lookahead := g.Lookahead
	if lookahead < 1 {
		lookahead = 1
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// A buffered channel is the pipeline: the producer runs ahead by
	// `lookahead` clips while the consumer plays.
	clips := make(chan clip, lookahead)
	style := g.Style.Prompt()

	go func() {
		defer close(clips)
		for _, sentence := range sentences {
			if ctx.Err() != nil {
				return
			}
			audio, mime, err := g.Synth.SynthesizeSpeech(ctx, sentence, g.Style.Voice, style)
			select {
			case clips <- clip{audio: audio, mime: mime, err: err}:
			case <-ctx.Done():
				return
			}
		}
	}()

	for c := range clips {
		if g.isStopped() || ctx.Err() != nil {
			return ctx.Err()
		}
		if c.err != nil {
			// Losing one sentence should not silence the whole reply.
			continue
		}
		if len(c.audio) == 0 {
			continue
		}
		if err := g.play(ctx, c.audio, c.mime); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
	}
	return nil
}

// play pipes raw PCM to the system audio player.
func (g *GeminiTTS) play(ctx context.Context, audio []byte, mime string) error {
	rate, channels := parsePCMMime(mime)

	var name string
	var args []string
	switch {
	case have("paplay"):
		name = "paplay"
		args = []string{"--raw",
			"--rate=" + strconv.Itoa(rate),
			"--channels=" + strconv.Itoa(channels),
			"--format=s16le"}
	case have("aplay"):
		name = "aplay"
		args = []string{"-q", "-t", "raw", "-f", "S16_LE",
			"-r", strconv.Itoa(rate), "-c", strconv.Itoa(channels)}
	default:
		return fmt.Errorf("no audio player found; install pulseaudio-utils or alsa-utils")
	}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(string(audio))

	g.mu.Lock()
	g.current = cmd
	g.mu.Unlock()

	err := cmd.Run()

	g.mu.Lock()
	g.current = nil
	g.mu.Unlock()

	if err != nil && ctx.Err() == nil && !g.isStopped() {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// Stop interrupts playback immediately.
func (g *GeminiTTS) Stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stopped = true
	if g.current != nil && g.current.Process != nil {
		_ = g.current.Process.Kill()
	}
}

func (g *GeminiTTS) isStopped() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stopped
}

// pcmRatePattern extracts the sample rate from MIME types of the form
// "audio/l16; rate=24000; channels=1".
var pcmRatePattern = regexp.MustCompile(`rate=(\d+)`)
var pcmChannelPattern = regexp.MustCompile(`channels=(\d+)`)

// parsePCMMime reads the sample rate and channel count, defaulting to Gemini's
// documented 24 kHz mono output when the header is absent.
func parsePCMMime(mime string) (rate, channels int) {
	rate, channels = 24000, 1
	if m := pcmRatePattern.FindStringSubmatch(mime); m != nil {
		if v, err := strconv.Atoi(m[1]); err == nil && v > 0 {
			rate = v
		}
	}
	if m := pcmChannelPattern.FindStringSubmatch(mime); m != nil {
		if v, err := strconv.Atoi(m[1]); err == nil && v > 0 {
			channels = v
		}
	}
	return rate, channels
}
