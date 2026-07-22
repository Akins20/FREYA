package skills

import (
	"context"
	"fmt"
	"strings"

	"github.com/akins/jarvis/internal/llm"
	"github.com/akins/jarvis/internal/reflect"
	"github.com/akins/jarvis/internal/sentinel"
	"github.com/akins/jarvis/internal/voice"
)

// ObservationSource is the sentinel, narrowed to what the skill needs.
type ObservationSource interface {
	Pending() []sentinel.Observation
	Peek() []sentinel.Observation
}

// VoiceController is what the voice skill manipulates. The REPL supplies it,
// so the skills package stays unaware of how speech is actually produced.
type VoiceController interface {
	// Style returns the current delivery settings.
	Style() voice.Style
	// SetStyle applies and persists new delivery settings.
	SetStyle(voice.Style) error
}

// RegisterVoice lets Freya adjust her own delivery.
//
// This is the difference between a voice that is configured at you and one you
// can simply talk to: "slow down", "you sound too cheerful", "use a lower
// voice" all become ordinary requests she can act on.
func RegisterVoice(r *Registry, vc VoiceController) {
	if vc == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "voice_adjust",
			Description: "Change how you sound: speaking pace, pitch, tone and voice. " +
				"Use whenever the user comments on your delivery — too fast, too flat, " +
				"too loud, wrong mood — or asks you to sound a particular way. " +
				"Only set the fields you intend to change; omitted fields stay as they are.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"pace": {
					Type:        "string",
					Description: "Speaking speed.",
					Enum:        voice.PaceNames(),
				},
				"pitch": {
					Type:        "string",
					Description: "Vocal pitch.",
					Enum:        voice.PitchNames(),
				},
				"tone": {
					Type: "string",
					Description: "Comma-separated delivery descriptors, e.g. " +
						"'casual, dry, amused' or 'warm, calm'. Known values: " +
						strings.Join(voice.ToneNames(), ", ") +
						". Other plain-English descriptors also work.",
				},
				"voice": {
					Type: "string",
					Description: "Voice preset name, e.g. Leda, Kore, Aoede, Sulafat. " +
						"Only change this if the user asks for a different voice.",
				},
				"custom": {
					Type: "string",
					Description: "Freeform delivery direction appended to the rest, " +
						"e.g. 'with a slight British accent'. Empty string clears it.",
				},
			}),
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			style := vc.Style()
			var changes []string

			if pace := argString(args, "pace"); pace != "" {
				if _, ok := voice.Paces[strings.ToLower(pace)]; !ok {
					return "", fmt.Errorf("unknown pace %q; choose from %s",
						pace, strings.Join(voice.PaceNames(), ", "))
				}
				style.Pace = strings.ToLower(pace)
				changes = append(changes, "pace "+style.Pace)
			}

			if pitch := argString(args, "pitch"); pitch != "" {
				if _, ok := voice.Pitches[strings.ToLower(pitch)]; !ok {
					return "", fmt.Errorf("unknown pitch %q; choose from %s",
						pitch, strings.Join(voice.PitchNames(), ", "))
				}
				style.Pitch = strings.ToLower(pitch)
				changes = append(changes, "pitch "+style.Pitch)
			}

			if tone := argString(args, "tone"); tone != "" {
				style.SetTone(strings.Split(tone, ","))
				changes = append(changes, "tone "+strings.Join(style.Tone, ", "))
			}

			if v := argString(args, "voice"); v != "" {
				style.Voice = v
				changes = append(changes, "voice "+v)
			}

			// An explicitly empty string is a request to clear, which is
			// distinct from the key being absent.
			if raw, present := args["custom"]; present {
				style.Custom = strings.TrimSpace(fmt.Sprint(raw))
				if style.Custom == "" || style.Custom == "<nil>" {
					style.Custom = ""
					changes = append(changes, "cleared custom direction")
				} else {
					changes = append(changes, "custom: "+style.Custom)
				}
			}

			if len(changes) == 0 {
				return "Nothing changed. Current delivery: " + style.Describe(), nil
			}
			if err := vc.SetStyle(style); err != nil {
				return "", fmt.Errorf("save voice settings: %w", err)
			}
			return fmt.Sprintf("Adjusted %s. Now: %s",
				strings.Join(changes, ", "), style.Describe()), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "voice_status",
			Description: "Report how you currently sound: voice, pace, pitch and tone.",
			Params:      llm.ObjectSchema(nil),
		},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return vc.Style().Describe(), nil
		},
	})
}

// RegisterProactive lets Freya read her own backlog of observations.
//
// Without this the sentinel could only interrupt. With it, "anything I should
// know about?" becomes a question she can actually answer, which is what makes
// a quiet setting usable rather than merely silent.
func RegisterProactive(r *Registry, s ObservationSource) {
	if s == nil {
		return
	}
	r.Register(Skill{
		Tool: llm.Tool{
			Name: "pending_observations",
			Description: "Check what the background watchers have noticed but not " +
				"interrupted the user about — disk, battery, memory, uncommitted work, " +
				"due reminders. Use when asked what's going on, what needs attention, " +
				"or whether anything is wrong.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"clear": {Type: "boolean", Description: "Drain the queue after reading. " +
					"Default false, so nothing is silently lost."},
			}),
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			var items []string
			if argBool(args, "clear") {
				for _, o := range s.Pending() {
					items = append(items, o.Describe())
				}
			} else {
				for _, o := range s.Peek() {
					items = append(items, o.Describe())
				}
			}
			if len(items) == 0 {
				return "Nothing outstanding. Everything looks normal.", nil
			}
			return strings.Join(items, "\n"), nil
		},
	})
}

// InsightSource is the reflector, narrowed to what the skill needs.
type InsightSource interface {
	Peek() []reflect.Insight
	Lenses() []string
}

// RegisterReflection lets Freya consult her own background reading of memory.
func RegisterReflection(r *Registry, src InsightSource) {
	if src == nil {
		return
	}
	r.Register(Skill{
		Tool: llm.Tool{
			Name: "recall_perspectives",
			Description: "Consult the background lenses that re-read memory from " +
				"different angles — contradictions with past decisions, earlier " +
				"attempts that failed, recurring patterns, facts that may have gone " +
				"stale, deadline conflicts, and how the user has been feeling about " +
				"a topic. Use when a request seems familiar or when you suspect " +
				"there is context you are missing.",
			Params: llm.ObjectSchema(nil),
		},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			pending := src.Peek()
			if len(pending) == 0 {
				return "No additional angles surfaced. Lenses active: " +
					strings.Join(src.Lenses(), ", "), nil
			}
			var sb strings.Builder
			for _, i := range pending {
				sb.WriteString(i.Describe())
				sb.WriteString("\n")
			}
			return strings.TrimSpace(sb.String()), nil
		},
	})
}
