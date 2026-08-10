package voice

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/Akins20/FREYA/internal/llm"
)

// EspeakTTS speaks using espeak or espeak-ng.
//
// It is not a pretty voice, but it is already installed on most Linux systems,
// starts instantly, costs nothing, and works offline. Piper sounds far better
// and is preferred when present.
type EspeakTTS struct {
	// Binary is espeak-ng or espeak. Empty means auto-detect.
	Binary string
	// Voice is an espeak voice name such as "en-gb" or "en-us+f3".
	Voice string
	// Style supplies pace and pitch. espeak cannot act on tone descriptors —
	// it has no prosody model — so those are silently ignored here. Choose
	// Gemini TTS when delivery matters.
	Style Style

	mu      sync.Mutex
	current *exec.Cmd
}

func (e *EspeakTTS) Name() string {
	return e.binary()
}

func (e *EspeakTTS) binary() string {
	if e.Binary != "" {
		return e.Binary
	}
	if have("espeak-ng") {
		return "espeak-ng"
	}
	return "espeak"
}

// Say speaks the text, one sentence at a time.
//
// Splitting matters: espeak buffers a whole utterance before emitting audio, so
// handing it a long reply produces a silence and then a wall of speech. Feeding
// sentences means the first words land almost immediately, and it gives Stop a
// clean seam to interrupt on.
func (e *EspeakTTS) Say(ctx context.Context, text string) error {
	text = ForSpeech(text)
	if text == "" {
		return nil
	}
	bin := e.binary()
	if !have(bin) {
		return fmt.Errorf("%s not found; install espeak-ng", bin)
	}

	rate := e.Style.WPM()
	pitch := e.Style.PitchValue()
	voice := e.Voice
	if voice == "" {
		voice = e.Style.Voice
	}

	for _, sentence := range splitSentences(text) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		args := []string{"-s", strconv.Itoa(rate), "-p", strconv.Itoa(pitch)}
		if voice != "" {
			args = append(args, "-v", voice)
		}
		args = append(args, "--", sentence)

		cmd := exec.CommandContext(ctx, bin, args...)
		e.mu.Lock()
		e.current = cmd
		e.mu.Unlock()

		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// A single failed sentence should not silence the rest.
			continue
		}
	}

	e.mu.Lock()
	e.current = nil
	e.mu.Unlock()
	return nil
}

// Stop interrupts speech in progress.
func (e *EspeakTTS) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.current != nil && e.current.Process != nil {
		_ = e.current.Process.Kill()
	}
}

// PiperTTS speaks using piper, a neural synthesiser that sounds markedly better
// than espeak. It needs a downloaded .onnx voice model.
//
// Note that the "piper" package in Debian and Ubuntu archives is an unrelated
// mouse-configuration tool; the synthesiser must come from its own releases.
type PiperTTS struct {
	Binary string
	Model  string // path to a .onnx voice

	mu      sync.Mutex
	current *exec.Cmd
}

func (p *PiperTTS) Name() string { return "piper" }

// Say synthesises with piper and pipes the audio to the system player.
func (p *PiperTTS) Say(ctx context.Context, text string) error {
	text = ForSpeech(text)
	if text == "" {
		return nil
	}
	bin := p.Binary
	if bin == "" {
		bin = "piper"
	}
	if p.Model == "" {
		return fmt.Errorf("piper needs a voice model; set FREYA_PIPER_MODEL")
	}
	if _, err := os.Stat(p.Model); err != nil {
		return fmt.Errorf("piper model %q not readable: %w", p.Model, err)
	}

	player, playerArgs := audioPlayer()
	if player == "" {
		return fmt.Errorf("no audio player found; install pulseaudio-utils or alsa-utils")
	}

	for _, sentence := range splitSentences(text) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		synth := exec.CommandContext(ctx, bin, "--model", p.Model, "--output-raw")
		synth.Stdin = strings.NewReader(sentence)

		play := exec.CommandContext(ctx, player, playerArgs...)
		pipe, err := synth.StdoutPipe()
		if err != nil {
			return fmt.Errorf("piper pipe: %w", err)
		}
		play.Stdin = pipe

		p.mu.Lock()
		p.current = play
		p.mu.Unlock()

		if err := synth.Start(); err != nil {
			return fmt.Errorf("piper: %w", err)
		}
		if err := play.Start(); err != nil {
			_ = synth.Process.Kill()
			return fmt.Errorf("%s: %w", player, err)
		}
		_ = synth.Wait()
		_ = play.Wait()
	}
	return nil
}

// Stop interrupts playback.
func (p *PiperTTS) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current != nil && p.current.Process != nil {
		_ = p.current.Process.Kill()
	}
}

// audioPlayer returns a command that plays raw 22 kHz mono PCM from stdin,
// which is what piper emits with --output-raw.
func audioPlayer() (string, []string) {
	switch {
	case have("paplay"):
		return "paplay", []string{"--raw", "--rate=22050", "--channels=1", "--format=s16le"}
	case have("aplay"):
		return "aplay", []string{"-q", "-r", "22050", "-c", "1", "-f", "S16_LE", "-t", "raw"}
	default:
		return "", nil
	}
}

// piperLooksLikeTTS distinguishes the speech synthesiser from the identically
// named mouse-configuration utility packaged by Debian and Ubuntu.
func piperLooksLikeTTS() bool {
	out, err := exec.Command("piper", "--help").CombinedOutput()
	if err != nil && len(out) == 0 {
		return false
	}
	help := strings.ToLower(string(out))
	return strings.Contains(help, "model") &&
		(strings.Contains(help, "onnx") || strings.Contains(help, "espeak") ||
			strings.Contains(help, "output-raw"))
}

// SynthOptions configures synthesiser selection.
type SynthOptions struct {
	// Preference is "gemini", "piper", "espeak" or "none". Empty auto-selects.
	Preference string
	Style      Style
	PiperModel string
	// Speech is the provider used for Gemini synthesis, if it supports audio.
	Speech llm.SpeechSynthesizer
}

// NewSynthesizerWith picks a voice engine.
//
// Gemini leads when available: espeak cannot vary tone at all, and a monotone
// reading undercuts a personality built on delivery.
func NewSynthesizerWith(opts SynthOptions) (Synthesizer, error) {
	switch strings.ToLower(strings.TrimSpace(opts.Preference)) {
	case "gemini", "neural", "cloud":
		if opts.Speech == nil {
			return nil, fmt.Errorf("the active provider cannot synthesise speech; " +
				"use FREYA_TTS=espeak")
		}
		return &GeminiTTS{Synth: opts.Speech, Style: opts.Style}, nil

	case "piper":
		return &PiperTTS{Model: opts.PiperModel}, nil

	case "espeak", "espeak-ng":
		return &EspeakTTS{Style: opts.Style}, nil

	case "none", "off", "silent":
		return NoopTTS{}, nil

	case "":
		if opts.Speech != nil {
			return &GeminiTTS{Synth: opts.Speech, Style: opts.Style}, nil
		}
		if have("piper") && piperLooksLikeTTS() && opts.PiperModel != "" {
			return &PiperTTS{Model: opts.PiperModel}, nil
		}
		if have("espeak-ng") || have("espeak") {
			return &EspeakTTS{Style: opts.Style}, nil
		}
		return nil, fmt.Errorf("no speech synthesiser found; install espeak-ng")

	default:
		return nil, fmt.Errorf("unknown synthesiser %q (want gemini, piper, espeak or none)",
			opts.Preference)
	}
}

// Restyle updates the delivery settings of a synthesiser that supports them.
func Restyle(s Synthesizer, style Style) {
	switch t := s.(type) {
	case *GeminiTTS:
		t.Style = style
	case *EspeakTTS:
		t.Style = style
	}
}

// NoopTTS discards speech, for text-only sessions.
type NoopTTS struct{}

func (NoopTTS) Name() string                      { return "none" }
func (NoopTTS) Say(context.Context, string) error { return nil }
func (NoopTTS) Stop()                             {}

// --- text preparation -------------------------------------------------------

var (
	codeBlockPattern  = regexp.MustCompile("(?s)```.*?```")
	inlineCodePattern = regexp.MustCompile("`([^`]*)`")
	linkPattern       = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	bareURLPattern    = regexp.MustCompile(`https?://\S+`)
	emphasisPattern   = regexp.MustCompile(`[*_]{1,3}([^*_]+)[*_]{1,3}`)
	headingPattern    = regexp.MustCompile(`(?m)^#{1,6}\s*`)
	bulletPattern     = regexp.MustCompile(`(?m)^\s*[-*+]\s+`)
	whitespacePattern = regexp.MustCompile(`\s+`)
)

// ForSpeech strips markup that is meaningless when read aloud.
//
// Without this, espeak dutifully pronounces every asterisk, backtick and URL,
// which turns a tidy reply into noise.
func ForSpeech(s string) string {
	s = codeBlockPattern.ReplaceAllString(s, " (code omitted) ")
	s = inlineCodePattern.ReplaceAllString(s, "$1")
	s = linkPattern.ReplaceAllString(s, "$1")
	s = bareURLPattern.ReplaceAllString(s, " (link) ")
	s = emphasisPattern.ReplaceAllString(s, "$1")
	s = headingPattern.ReplaceAllString(s, "")
	s = bulletPattern.ReplaceAllString(s, ". ")
	s = whitespacePattern.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// maxSentenceRunes caps a single utterance so an unpunctuated wall of text
// still gets broken up for streaming and interruption.
const maxSentenceRunes = 240

// splitSentences breaks text at sentence boundaries for incremental speech.
func splitSentences(s string) []string {
	var out []string
	var cur strings.Builder

	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, t)
		}
		cur.Reset()
	}

	runes := []rune(s)
	for i, r := range runes {
		cur.WriteRune(r)
		switch r {
		case '.', '!', '?':
			// Only break when the next character is whitespace, so decimals
			// and abbreviations stay intact.
			if i+1 >= len(runes) || unicode.IsSpace(runes[i+1]) {
				flush()
			}
		case '\n':
			flush()
		default:
			if cur.Len() >= maxSentenceRunes && r == ' ' {
				flush()
			}
		}
	}
	flush()

	if len(out) == 0 && strings.TrimSpace(s) != "" {
		return []string{strings.TrimSpace(s)}
	}
	return out
}
