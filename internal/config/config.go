// Package config resolves Freya's runtime settings from the environment,
// with an optional .env file for convenience.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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

	// ToolRouting narrows the tools she is SHOWN to those a request calls for.
	// "off" disables it. Everything stays executable either way — a tool she names
	// that was not offered still runs, and the miss is counted. See
	// internal/skills/kits.go.
	ToolRouting string

	// SourceDir is her own repository, for the self-repair loop.
	//
	// Explicit because it cannot be inferred: the daemon starts from the home
	// directory and works in ~/freya-workspace, neither of which is anywhere near
	// the checkout. Empty disables the loop rather than guessing — a consult
	// pointed at the wrong tree would read someone else's code and change it.
	SourceDir string

	// WorkDir is her working directory — the one place file and shell tools both
	// anchor to, so a file she writes is where a command she runs looks for it.
	// She fans out into subfolders from here. Empty leaves the process where it
	// started (which is what the benchmark relies on); the daemon sets it to a
	// fixed workspace so voice-driven work always lands somewhere findable.
	WorkDir string

	// ThinkBudget controls how much she reasons before each step, and whether
	// that reasoning is surfaced. -1 (the default) lets the model decide; 0 turns
	// thinking off; a positive value caps thinking tokens. Deeper thinking buys
	// better decisions between tool calls at the cost of latency and output-priced
	// tokens, so it is tunable — lower it if voice latency bites.
	ThinkBudget int

	// FollowupAfter is how long the conversation must be quiet before she reviews
	// it and may re-engage with a loose end. Zero disables re-engagement entirely.
	// Default is a few minutes; tune it down to be more forward, up to be more
	// reserved, or off if unprompted follow-ups are unwanted.
	FollowupAfter time.Duration

	// Address is how Freya refers to the user ("sir", a first name, ...).
	// Empty means she uses no honorific at all, which is the default.
	Address string

	// Verbose echoes tool calls and their results to the terminal.
	Verbose bool
	// DryRun assesses actions and reports them without executing.
	DryRun bool
	// Chattiness controls proactive interruptions: quiet, balanced, companion.
	Chattiness string
	// SafetyThreshold sets Gemini's content filtering. Empty means the
	// built-in default of "OFF"; set BLOCK_ONLY_HIGH or BLOCK_MEDIUM_AND_ABOVE
	// to re-enable filtering.
	SafetyThreshold string

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
	// Wake controls always-on wake-word listening: "off" disables it, anything
	// else leaves it on.
	//
	// It exists because there was no way to say no. Wake listening started
	// whenever voice was available, and the only reason it was ever off was that
	// starting it had FAILED — so restoring an unrelated setting silently turned
	// on a microphone that records almost continuously, which is not a thing
	// software should do by accident. Push-to-talk is unaffected either way.
	Wake string

	// WakeAck is how a detected wake word is acknowledged: chime, speak,
	// both or silent.
	WakeAck string
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
		Provider:        os.Getenv("FREYA_PROVIDER"),
		Model:           os.Getenv("FREYA_MODEL"),
		GeminiKey:       firstNonEmpty(os.Getenv("GEMINI_API_KEY"), os.Getenv("GOOGLE_API_KEY")),
		AnthropicKey:    os.Getenv("ANTHROPIC_API_KEY"),
		DataDir:         os.Getenv("FREYA_DATA_DIR"),
		WorkDir:         os.Getenv("FREYA_WORK_DIR"),
		ProjectsDir:     os.Getenv("FREYA_PROJECTS_DIR"),
		SourceDir:       os.Getenv("FREYA_SOURCE_DIR"),
		ToolRouting:     os.Getenv("FREYA_TOOL_ROUTING"),
		Address:         os.Getenv("FREYA_ADDRESS"),
		Verbose:         isTruthy(os.Getenv("FREYA_VERBOSE")),
		DryRun:          isTruthy(os.Getenv("FREYA_DRY_RUN")),
		SafetyThreshold: os.Getenv("FREYA_SAFETY"),
		Chattiness:      os.Getenv("FREYA_CHATTINESS"),

		Voice:        isTruthy(os.Getenv("FREYA_VOICE")),
		STT:          os.Getenv("FREYA_STT"),
		TTS:          os.Getenv("FREYA_TTS"),
		VoiceName:    os.Getenv("FREYA_VOICE_NAME"),
		WhisperModel: os.Getenv("FREYA_WHISPER_MODEL"),
		PiperModel:   os.Getenv("FREYA_PIPER_MODEL"),
		VoicePolicy:  os.Getenv("FREYA_VOICE_POLICY"),
		Wake:         os.Getenv("FREYA_WAKE"),
		WakeAck:      os.Getenv("FREYA_WAKE_ACK"),
	}

	cfg.ThinkBudget = parseThinkBudget(os.Getenv("FREYA_THINKING"))
	cfg.FollowupAfter = parseFollowup(os.Getenv("FREYA_FOLLOWUP"))

	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Join(home, ".local", "share", "freya")
	}
	if cfg.ProjectsDir == "" {
		cfg.ProjectsDir = defaultProjectsDir(home)
	}

	// Every directory setting is resolved to a real absolute path here, once,
	// rather than wherever it happens to be used. See expandPath: a settings file
	// is not a shell, so ~ and $HOME arrive as literal characters, and a path that
	// is not absolute cannot be compared against another path by anyone.
	cfg.DataDir = expandPath(cfg.DataDir, home)
	cfg.WorkDir = expandPath(cfg.WorkDir, home)
	cfg.ProjectsDir = expandPath(cfg.ProjectsDir, home)
	cfg.SourceDir = expandPath(cfg.SourceDir, home)

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

// expandPath turns a configured directory into an absolute one: ~ becomes the
// home directory, $VARs are substituted, and anything still relative is anchored
// to where the process started.
//
// # Why this is not cosmetic
//
// FREYA_WORK_DIR is not only where she works — it is the directory the guard
// treats as her own, the one place a write needs no confirmation she has no way
// to obtain in the daemon. That carve-out compares the file's path against the
// workspace root and, quite deliberately, refuses to resolve either itself: a
// guard that expanded paths would be guessing about a directory some other
// process chose. So it demands both be absolute, and answers "not in the
// workspace" for anything else.
//
// Nothing expanded them. A settings file is not a shell: .env stores the bytes
// between = and the newline, so FREYA_WORK_DIR=~/freya-workspace — the form this
// project's own .env.example uses for FREYA_DATA_DIR — reaches the guard as a
// string starting with a tilde, matches no path on the machine, and every single
// write she makes in her own workspace is assessed as a medium-risk write
// somewhere else. In the daemon that means asking a confirmation nobody can
// answer, once per file, and the task dies on file one. The guard is right, the
// wiring is right, and the carve-out silently never fires.
//
// It also made the working directory itself a fiction: os.MkdirAll of that same
// string creates a folder literally named "~" next to wherever she was launched,
// and she then works there — findable by nobody, including her.
//
// Empty stays empty. It is a meaningful value everywhere it is read: no fixed
// working directory, no self-repair checkout.
func expandPath(p, home string) string {
	p = os.ExpandEnv(strings.TrimSpace(p))
	if p == "" {
		return ""
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		p = filepath.Join(home, p[2:])
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return filepath.Clean(p)
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

// parseThinkBudget reads FREYA_THINKING. Empty or "on"/"auto" means thinking is
// on and the model chooses its depth (-1); "off" disables it (0); a number is a
// hard token cap. Thinking is on by default because seeing her reason between
// steps is the point — and a model that plans guesses less.
func parseThinkBudget(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "on", "auto", "dynamic", "yes", "true":
		return -1
	case "off", "no", "false", "0":
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return -1
}

// parseFollowup reads FREYA_FOLLOWUP: empty means the default lull (6 minutes);
// "off"/"no"/"0" disables re-engagement; a duration ("20s", "10m") sets the lull.
func parseFollowup(s string) time.Duration {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return 6 * time.Minute
	case "off", "no", "false", "0", "never":
		return 0
	}
	if d, err := time.ParseDuration(strings.TrimSpace(s)); err == nil && d > 0 {
		return d
	}
	return 6 * time.Minute
}

func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
