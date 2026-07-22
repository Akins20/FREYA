// Package config resolves Freya's runtime settings from the environment,
// with an optional .env file for convenience.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config holds every tunable Freya reads at startup.
type Config struct {
	// Provider selects the reasoning backend: "gemini", "anthropic", or "mock".
	Provider string
	// Model overrides the provider's default model. Empty means provider default.
	Model string

	GeminiKey    string
	AnthropicKey string

	// DataDir stores notes, reminders and history. Kept on the internal disk
	// rather than the external drive, which is near capacity.
	DataDir string

	// ProjectsDir is what the dev skills scan for repositories.
	ProjectsDir string

	// Address is how Freya refers to the user ("sir", a first name, ...).
	// Empty means she uses no honorific at all, which is the default.
	Address string

	// Verbose echoes tool calls and their results to the terminal.
	Verbose bool

	// --- voice ---

	// Voice starts the session in spoken mode.
	Voice bool
	// STT selects the speech recogniser: "gemini" (default) or "whisper".
	STT string
	// TTS selects the synthesiser: "espeak", "piper" or "none".
	TTS string
	// VoiceName is an engine-specific voice, e.g. espeak's "en-gb".
	VoiceName string
	// WhisperModel is the ggml model path for offline recognition.
	WhisperModel string
	// PiperModel is the .onnx voice path for neural synthesis.
	PiperModel string
	// VoicePolicy governs unrecognised speakers: "off", "warn" or "enforce".
	// Defaults to warn, because speaker verification is not accurate enough
	// here to lock anyone out — see internal/voice/verify.go.
	VoicePolicy string
}

// Load reads configuration from .env (if present) and the environment.
// Environment variables always win over the file.
func Load() (*Config, error) {
	loadDotEnv(".env")

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("config: resolve home: %w", err)
	}

	cfg := &Config{
		Provider:     os.Getenv("FREYA_PROVIDER"),
		Model:        os.Getenv("FREYA_MODEL"),
		GeminiKey:    firstNonEmpty(os.Getenv("GEMINI_API_KEY"), os.Getenv("GOOGLE_API_KEY")),
		AnthropicKey: os.Getenv("ANTHROPIC_API_KEY"),
		DataDir:      os.Getenv("FREYA_DATA_DIR"),
		ProjectsDir:  os.Getenv("FREYA_PROJECTS_DIR"),
		Address:      os.Getenv("FREYA_ADDRESS"),
		Verbose:      isTruthy(os.Getenv("FREYA_VERBOSE")),

		Voice:        isTruthy(os.Getenv("FREYA_VOICE")),
		STT:          os.Getenv("FREYA_STT"),
		TTS:          os.Getenv("FREYA_TTS"),
		VoiceName:    os.Getenv("FREYA_VOICE_NAME"),
		WhisperModel: os.Getenv("FREYA_WHISPER_MODEL"),
		PiperModel:   os.Getenv("FREYA_PIPER_MODEL"),
		VoicePolicy:  os.Getenv("FREYA_VOICE_POLICY"),
	}

	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Join(home, ".local", "share", "freya")
	}
	if cfg.ProjectsDir == "" {
		cfg.ProjectsDir = defaultProjectsDir(home)
	}

	// With no explicit choice, prefer whichever key is present, else offline.
	if cfg.Provider == "" {
		switch {
		case cfg.GeminiKey != "":
			cfg.Provider = "gemini"
		case cfg.AnthropicKey != "":
			cfg.Provider = "anthropic"
		default:
			cfg.Provider = "mock"
		}
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("config: create data dir: %w", err)
	}
	return cfg, nil
}

// defaultProjectsDir walks up from the working directory to find the folder
// holding this project, which is where the user's other repositories live.
func defaultProjectsDir(home string) string {
	if wd, err := os.Getwd(); err == nil {
		if parent := filepath.Dir(wd); parent != "" && parent != "." && parent != "/" {
			return parent
		}
	}
	return home
}

// loadDotEnv applies KEY=VALUE lines from path without overwriting real
// environment variables. Missing files are not an error.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
