package llm

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Mock is an offline, rule-based Provider. It exists so the full agent loop —
// prompt, tool dispatch, result summarisation — can be exercised and tested
// with no API key and no network.
//
// It is deliberately dumb: it keyword-matches the user's text against the
// registered tools. Anything it cannot match gets a canned reply. Real
// reasoning arrives by setting FREYA_PROVIDER=gemini.
type Mock struct{}

// NewMock builds the offline provider.
func NewMock() *Mock { return &Mock{} }

func (m *Mock) Name() string { return "mock/offline" }

var numberPattern = regexp.MustCompile(`-?\d+`)

// mockInspectLimit caps how much of an input the matcher reads.
//
// Real commands are short. Scanning an unbounded blob all but guarantees a
// chance substring collision — a 500KB paste happening to contain "mute"
// silently set the machine's volume to zero during testing. Bounding the
// window plus whole-word matching removes that entire class of accident.
const mockInspectLimit = 400

// normalizeForMatch reduces text to space-separated lowercase words, padded so
// that whole-word containment checks work with a plain strings.Contains.
func normalizeForMatch(s string) string {
	if len(s) > mockInspectLimit {
		s = s[:mockInspectLimit]
	}
	var sb strings.Builder
	sb.WriteByte(' ')
	prevSpace := true
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
			prevSpace = false
		} else if !prevSpace {
			sb.WriteByte(' ')
			prevSpace = true
		}
	}
	if !prevSpace {
		sb.WriteByte(' ')
	}
	return sb.String()
}

// matchesTrigger reports whether normalized contains trigger as whole words.
func matchesTrigger(normalized, trigger string) bool {
	return strings.Contains(normalized, " "+trigger+" ")
}

// rule maps a set of trigger words to a tool and an argument extractor.
type rule struct {
	tool     string
	triggers []string
	args     func(input string) map[string]any
}

// mockRules is ordered: the first matching rule wins, so more specific
// triggers ("git status", "search for") must precede broader ones ("git",
// "search"), and reminders must precede plain notes.
func mockRules() []rule {
	return []rule{
		{
			// Reminders are notes carrying a due time; there is no separate
			// reminder skill, so this must route to note_add.
			tool:     "note_add",
			triggers: []string{"remind me", "reminder to", "set a reminder"},
			args: func(in string) map[string]any {
				args := map[string]any{"text": stripPrefixes(in,
					"remind me to", "remind me", "set a reminder to", "set a reminder", "reminder to")}
				if due := findDuration(in); due != "" {
					args["due"] = due
				}
				return args
			},
		},
		{
			tool:     "note_list",
			triggers: []string{"my reminders", "list reminders", "what reminders"},
			args:     func(string) map[string]any { return map[string]any{"filter": "reminders"} },
		},
		{
			tool:     "note_add",
			triggers: []string{"note that", "take a note", "make a note", "remember that"},
			args: func(in string) map[string]any {
				return map[string]any{"text": stripPrefixes(in,
					"note that", "take a note", "make a note", "remember that")}
			},
		},
		{
			tool:     "note_list",
			triggers: []string{"list notes", "my notes", "show notes", "what notes"},
			args:     func(string) map[string]any { return map[string]any{} },
		},
		{
			tool:     "system_volume",
			triggers: []string{"volume", "louder", "quieter", "mute"},
			args: func(in string) map[string]any {
				if n := numberPattern.FindString(in); n != "" {
					v, _ := strconv.Atoi(n)
					return map[string]any{"level": float64(v)}
				}
				if strings.Contains(in, "mute") {
					return map[string]any{"level": float64(0)}
				}
				if strings.Contains(in, "louder") {
					return map[string]any{"level": float64(80)}
				}
				return map[string]any{"level": float64(30)}
			},
		},
		{
			tool:     "system_status",
			triggers: []string{"status", "how are you", "system", "battery", "disk", "memory", "uptime"},
			args:     func(string) map[string]any { return map[string]any{} },
		},
		{
			tool:     "system_open",
			triggers: []string{"open", "launch"},
			args: func(in string) map[string]any {
				return map[string]any{"app": stripPrefixes(in, "open", "launch")}
			},
		},
		{
			tool:     "dev_projects",
			triggers: []string{"projects", "what am i working on"},
			args:     func(string) map[string]any { return map[string]any{} },
		},
		{
			tool:     "dev_git_status",
			triggers: []string{"git status", "git"},
			args: func(in string) map[string]any {
				return map[string]any{"project": stripPrefixes(in, "git status for", "git status", "git")}
			},
		},
		{
			tool:     "web_search",
			triggers: []string{"search for", "look up", "google", "search"},
			args: func(in string) map[string]any {
				return map[string]any{"query": stripPrefixes(in, "search for", "search", "look up", "google")}
			},
		},
	}
}

// durationPattern spots offsets like "2h" or "30m" so "remind me in 2h" keeps
// its due time instead of silently becoming an undated note.
var durationPattern = regexp.MustCompile(`\b(\d+)\s*(m|min|mins|minutes?|h|hr|hours?|d|days?)\b`)

func findDuration(s string) string {
	m := durationPattern.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	unit := m[2]
	switch {
	case strings.HasPrefix(unit, "m"):
		unit = "m"
	case strings.HasPrefix(unit, "h"):
		unit = "h"
	default:
		unit = "d"
	}
	return m[1] + unit
}

// Chat matches the latest user turn against the registered tools.
func (m *Mock) Chat(_ context.Context, req Request) (*Response, error) {
	// A tool has just run: summarise its output instead of calling again.
	if n := len(req.Messages); n > 0 && req.Messages[n-1].Role == RoleTool {
		var b strings.Builder
		for i := n - 1; i >= 0 && req.Messages[i].Role == RoleTool; i-- {
			b.WriteString(req.Messages[i].Text)
			b.WriteString("\n")
		}
		return &Response{Text: strings.TrimSpace(b.String())}, nil
	}

	last := lastUserText(req.Messages)
	// Matching runs on a bounded, whole-word view; extraction still uses the
	// readable text so note bodies and queries survive intact.
	norm := normalizeForMatch(last)
	lower := strings.ToLower(last)
	if len(lower) > mockInspectLimit {
		lower = lower[:mockInspectLimit]
	}

	available := make(map[string]bool, len(req.Tools))
	for _, t := range req.Tools {
		available[t.Name] = true
	}

	for _, r := range mockRules() {
		if !available[r.tool] {
			continue
		}
		for _, trigger := range r.triggers {
			if matchesTrigger(norm, trigger) {
				return &Response{ToolCalls: []ToolCall{{
					ID:   r.tool + "-mock",
					Name: r.tool,
					Args: r.args(lower),
				}}}, nil
			}
		}
	}

	if strings.TrimSpace(last) == "" {
		return &Response{Text: "I'm listening."}, nil
	}
	return &Response{Text: fmt.Sprintf(
		"I'm running on the offline stand-in model, so I can only route commands I "+
			"recognise — notes, reminders, system status, volume, projects, git, search. "+
			"%q isn't one of them.\n\nSet FREYA_PROVIDER=gemini with a key to enable real reasoning.",
		strings.TrimSpace(last))}, nil
}

func lastUserText(msgs []Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == RoleUser {
			return msgs[i].Text
		}
	}
	return ""
}

// stripPrefixes removes the longest matching lead-in phrase and tidies the rest.
func stripPrefixes(s string, prefixes ...string) string {
	s = strings.TrimSpace(s)
	for _, p := range prefixes {
		if idx := strings.Index(strings.ToLower(s), p); idx >= 0 {
			s = s[idx+len(p):]
			break
		}
	}
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(s), ".,:!?\"'"))
}
