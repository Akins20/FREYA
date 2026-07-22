package voice

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Style is how Freya sounds, as opposed to what she says.
//
// It is deliberately expressed as words rather than numbers. Gemini's speech
// models take natural-language direction — "speak quickly, dry and amused" —
// so a descriptive style maps straight onto the engine's own controls instead
// of being flattened into a rate parameter. Engines that only understand
// numbers, like espeak, get the same settings converted for them.
type Style struct {
	// Voice is an engine preset: a Gemini voice name such as "Leda", or an
	// espeak voice such as "en-gb+f3".
	Voice string `json:"voice,omitempty"`
	// Pace is a key from Paces.
	Pace string `json:"pace,omitempty"`
	// Pitch is a key from Pitches.
	Pitch string `json:"pitch,omitempty"`
	// Tone holds delivery descriptors, e.g. "dry", "warm", "amused".
	Tone []string `json:"tone,omitempty"`
	// Custom is freeform direction appended verbatim.
	Custom string `json:"custom,omitempty"`
}

// DefaultStyle matches the default persona: quick, dry and casual.
func DefaultStyle() Style {
	return Style{
		Voice: "Leda",
		Pace:  "brisk",
		Pitch: "normal",
		Tone:  []string{"casual", "dry", "amused"},
	}
}

// Paces map a pace name to a spoken instruction and a words-per-minute figure
// for engines that need a number.
var Paces = map[string]struct {
	Instruction string
	WPM         int
}{
	"very slow": {"speaking very slowly and deliberately", 120},
	"slow":      {"speaking slowly and clearly", 140},
	"relaxed":   {"speaking at an unhurried, relaxed pace", 160},
	"normal":    {"speaking at a natural conversational pace", 175},
	"brisk":     {"speaking briskly, keeping things moving", 195},
	"fast":      {"speaking quickly", 215},
	"very fast": {"speaking very quickly, rattling it off", 240},
}

// Pitches map a pitch name to an instruction and an espeak pitch value (0-99).
var Pitches = map[string]struct {
	Instruction string
	Value       int
}{
	"very low":  {"with a low, deep voice", 15},
	"low":       {"with a slightly lower voice", 32},
	"normal":    {"", 50},
	"high":      {"with a slightly brighter, higher voice", 68},
	"very high": {"with a high, bright voice", 85},
}

// Tones are recognised delivery descriptors. Unknown words are still accepted
// and passed through — the speech model understands far more English than any
// list could enumerate — but these are the ones offered by name.
var Tones = map[string]string{
	"casual":         "casual and relaxed",
	"dry":            "dry and deadpan",
	"amused":         "faintly amused",
	"warm":           "warm and friendly",
	"sassy":          "with a bit of attitude and sass",
	"blunt":          "blunt and matter-of-fact",
	"excited":        "energetic and enthusiastic",
	"calm":           "calm and steady",
	"serious":        "serious and measured",
	"gentle":         "gently and softly",
	"urgent":         "with urgency",
	"playful":        "playful and teasing",
	"tired":          "sounding a little tired",
	"professional":   "polished and professional",
	"conspiratorial": "quietly, as if sharing a secret",
}

// ToneNames lists recognised tones, sorted.
func ToneNames() []string {
	out := make([]string, 0, len(Tones))
	for k := range Tones {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PaceNames lists pace settings slowest first.
func PaceNames() []string {
	return []string{"very slow", "slow", "relaxed", "normal", "brisk", "fast", "very fast"}
}

// PitchNames lists pitch settings lowest first.
func PitchNames() []string {
	return []string{"very low", "low", "normal", "high", "very high"}
}

// GeminiVoices are the prebuilt presets, grouped loosely by character so the
// list is navigable rather than thirty opaque names.
var GeminiVoices = []struct{ Name, Character string }{
	{"Leda", "youthful, bright"},
	{"Kore", "firm, confident"},
	{"Aoede", "breezy, light"},
	{"Callirrhoe", "easy-going"},
	{"Autonoe", "bright"},
	{"Despina", "smooth"},
	{"Erinome", "clear, precise"},
	{"Laomedeia", "upbeat"},
	{"Achernar", "soft"},
	{"Gacrux", "mature"},
	{"Sulafat", "warm"},
	{"Vindemiatrix", "gentle"},
	{"Zephyr", "bright, airy"},
	{"Puck", "upbeat, playful"},
	{"Charon", "informative"},
	{"Fenrir", "excitable"},
	{"Orus", "firm"},
	{"Enceladus", "breathy"},
	{"Iapetus", "clear"},
	{"Algieba", "smooth"},
	{"Rasalgethi", "informative"},
	{"Alnilam", "firm"},
	{"Schedar", "even"},
	{"Achird", "friendly"},
	{"Zubenelgenubi", "casual"},
	{"Sadaltager", "knowledgeable"},
}

// Prompt renders the style as a delivery instruction for a speech model.
//
// Returns an empty string when nothing has been set, so a neutral style costs
// no tokens and leaves the model's own defaults alone.
func (s Style) Prompt() string {
	var parts []string

	for _, t := range s.Tone {
		key := strings.ToLower(strings.TrimSpace(t))
		if desc, ok := Tones[key]; ok {
			parts = append(parts, desc)
		} else if key != "" {
			// Unrecognised descriptors still carry meaning to the model.
			parts = append(parts, key)
		}
	}
	if p, ok := Paces[strings.ToLower(s.Pace)]; ok && p.Instruction != "" {
		parts = append(parts, p.Instruction)
	}
	if p, ok := Pitches[strings.ToLower(s.Pitch)]; ok && p.Instruction != "" {
		parts = append(parts, p.Instruction)
	}
	if c := strings.TrimSpace(s.Custom); c != "" {
		parts = append(parts, c)
	}
	if len(parts) == 0 {
		return ""
	}
	return "Say the following " + joinNaturally(parts)
}

// WPM converts the pace to words per minute for engines that take a number.
func (s Style) WPM() int {
	if p, ok := Paces[strings.ToLower(s.Pace)]; ok {
		return p.WPM
	}
	return 175
}

// PitchValue converts the pitch to espeak's 0-99 scale.
func (s Style) PitchValue() int {
	if p, ok := Pitches[strings.ToLower(s.Pitch)]; ok {
		return p.Value
	}
	return 50
}

// Describe summarises the style for display.
func (s Style) Describe() string {
	voice := s.Voice
	if voice == "" {
		voice = "default"
	}
	tone := "neutral"
	if len(s.Tone) > 0 {
		tone = strings.Join(s.Tone, ", ")
	}
	out := fmt.Sprintf("voice %s · pace %s (%d wpm) · pitch %s · tone %s",
		voice, orDefault(s.Pace, "normal"), s.WPM(), orDefault(s.Pitch, "normal"), tone)
	if s.Custom != "" {
		out += " · " + s.Custom
	}
	return out
}

// SetTone replaces the tone list, reporting any names not in the catalogue.
// Unknown names are still kept — the speech model may well understand them.
func (s *Style) SetTone(tones []string) (unrecognised []string) {
	var cleaned []string
	for _, t := range tones {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if _, ok := Tones[t]; !ok {
			unrecognised = append(unrecognised, t)
		}
		cleaned = append(cleaned, t)
	}
	if len(cleaned) > 0 {
		s.Tone = cleaned
	}
	return unrecognised
}

const styleFile = "voicestyle.json"

// LoadStyle reads the stored style, falling back to the default.
func LoadStyle(dir string) Style {
	b, err := os.ReadFile(filepath.Join(dir, styleFile))
	if err != nil {
		return DefaultStyle()
	}
	var s Style
	if err := json.Unmarshal(b, &s); err != nil {
		return DefaultStyle()
	}
	return s
}

// SaveStyle persists the style atomically.
func SaveStyle(dir string, s Style) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	final := filepath.Join(dir, styleFile)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// joinNaturally renders a list as English prose rather than comma soup.
func joinNaturally(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1]
	}
}

func orDefault(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
