package voice

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/akins/jarvis/internal/llm"
)

// GeminiSTT transcribes by handing audio to the reasoning provider itself.
//
// This is the default: it is faster than local Whisper on this hardware, more
// accurate, needs no model download, and reuses the API key already configured.
// It does require the network, and the audio does leave the machine — use
// WhisperSTT when either matters.
type GeminiSTT struct {
	Transcriber llm.AudioTranscriber
}

func (g *GeminiSTT) Name() string { return "gemini" }

// Transcribe uploads the recording and returns what was said.
func (g *GeminiSTT) Transcribe(ctx context.Context, path string) (string, error) {
	if !fileHasAudio(path) {
		return "", nil // silence; not an error
	}
	audio, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read recording: %w", err)
	}

	text, err := g.Transcriber.TranscribeAudio(ctx, audio, mimeForPath(path))
	if err != nil {
		return "", err
	}
	return cleanTranscript(text), nil
}

// WhisperSTT runs whisper.cpp locally. Offline and private, but slower on
// modest hardware. Requires the whisper.cpp binary and a ggml model.
type WhisperSTT struct {
	// Binary is the whisper.cpp executable. Empty means auto-detect.
	Binary string
	// Model is the path to a ggml model file.
	Model string
	// Threads to use. Zero means all available cores.
	Threads int
}

func (w *WhisperSTT) Name() string { return "whisper" }

// whisperBinary returns the whisper.cpp executable name, which differs across
// distributions — Debian and Ubuntu ship it as whisper-cli.
func whisperBinary() string {
	for _, candidate := range []string{"whisper-cli", "whisper.cpp", "whisper", "main"} {
		if have(candidate) {
			return candidate
		}
	}
	return "whisper-cli"
}

// Transcribe runs whisper.cpp over the recording.
func (w *WhisperSTT) Transcribe(ctx context.Context, path string) (string, error) {
	if !fileHasAudio(path) {
		return "", nil
	}
	bin := w.Binary
	if bin == "" {
		bin = whisperBinary()
	}
	if !have(bin) {
		return "", fmt.Errorf("whisper binary %q not found; install whisper.cpp "+
			"or use FREYA_STT=gemini", bin)
	}
	if w.Model == "" {
		return "", fmt.Errorf("no whisper model configured; set FREYA_WHISPER_MODEL " +
			"to a ggml model file")
	}
	if _, err := os.Stat(w.Model); err != nil {
		return "", fmt.Errorf("whisper model %q not readable: %w", w.Model, err)
	}

	// whisper.cpp wants 16 kHz mono PCM; our recordings are Ogg, so convert.
	wav := strings.TrimSuffix(path, filepath.Ext(path)) + ".whisper.wav"
	defer os.Remove(wav)
	if err := convertToWAV(ctx, path, wav); err != nil {
		return "", err
	}

	args := []string{"-m", w.Model, "-f", wav, "--no-timestamps", "--output-txt", "-of", "-"}
	if w.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(w.Threads))
	}

	out, err := exec.CommandContext(ctx, bin, args...).Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w", bin, err)
	}
	return cleanTranscript(string(out)), nil
}

// convertToWAV re-encodes any recording to the PCM format whisper.cpp requires.
func convertToWAV(ctx context.Context, src, dst string) error {
	if !have("sox") {
		return fmt.Errorf("sox is required to convert audio for whisper")
	}
	cmd := exec.CommandContext(ctx, "sox", src,
		"-r", strconv.Itoa(sampleRate), "-c", strconv.Itoa(channels),
		"-b", strconv.Itoa(bitDepth), dst)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("convert audio for whisper: %w", err)
	}
	return nil
}

func mimeForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ogg", ".oga":
		return "audio/ogg"
	case ".mp3":
		return "audio/mp3"
	case ".flac":
		return "audio/flac"
	case ".m4a", ".aac":
		return "audio/aac"
	default:
		return "audio/wav"
	}
}

// nonSpeechMarkers are the bracketed annotations recognisers emit for silence,
// music and background noise. They are not speech and must not reach the agent
// as if the user had said them.
var nonSpeechMarkers = []string{
	"[blank_audio]", "[silence]", "[music]", "[sound]", "[noise]",
	"(silence)", "(music)", "[inaudible]", "[ blank_audio ]",
}

// cleanTranscript normalises recogniser output and drops non-speech artefacts.
func cleanTranscript(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	lower := strings.ToLower(s)
	for _, marker := range nonSpeechMarkers {
		if lower == marker {
			return ""
		}
		lower = strings.ReplaceAll(lower, marker, "")
	}
	if strings.TrimSpace(lower) == "" {
		return ""
	}

	// Models occasionally wrap a transcript in quotes despite being told not to.
	s = strings.TrimSpace(s)
	if len(s) > 1 {
		if (strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`)) ||
			(strings.HasPrefix(s, "'") && strings.HasSuffix(s, "'")) {
			s = strings.TrimSpace(s[1 : len(s)-1])
		}
	}
	return s
}

// NewRecognizer picks a speech recogniser. Preference is honoured when the
// requested engine is usable; otherwise it falls back with an explanation.
func NewRecognizer(preference string, provider llm.Provider, whisperModel string) (Recognizer, error) {
	transcriber, providerDoesAudio := provider.(llm.AudioTranscriber)

	switch strings.ToLower(strings.TrimSpace(preference)) {
	case "whisper", "local", "offline":
		return &WhisperSTT{Model: whisperModel}, nil

	case "gemini", "cloud", "":
		if providerDoesAudio {
			return &GeminiSTT{Transcriber: transcriber}, nil
		}
		if have(whisperBinary()) && whisperModel != "" {
			return &WhisperSTT{Model: whisperModel}, nil
		}
		return nil, fmt.Errorf("provider %s cannot transcribe audio and no local "+
			"whisper model is configured", provider.Name())

	default:
		return nil, fmt.Errorf("unknown speech recogniser %q (want gemini or whisper)", preference)
	}
}
