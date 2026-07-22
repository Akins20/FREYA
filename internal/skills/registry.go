// Package skills is Freya's capability layer: every action she can take is a
// Skill registered here and described to the model as a tool.
//
// Adding a capability means writing one Handler and registering it. Nothing in
// the agent loop or the providers needs to change.
package skills

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/akins/jarvis/internal/llm"
)

// Handler executes a skill and returns text for the model to read. Returning an
// error is fine and expected — the message is fed back so the model can adapt.
type Handler func(ctx context.Context, args map[string]any) (string, error)

// Skill couples a tool declaration with its implementation.
type Skill struct {
	Tool    llm.Tool
	Handler Handler
}

// Registry holds the registered skills.
type Registry struct {
	mu     sync.RWMutex
	skills map[string]Skill
}

// New creates an empty registry.
func New() *Registry {
	return &Registry{skills: map[string]Skill{}}
}

// Register adds a skill, replacing any earlier one with the same name.
func (r *Registry) Register(s Skill) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.skills[s.Tool.Name] = s
}

// Tools returns every tool declaration, sorted for a stable prompt prefix —
// reordering them each run would defeat the model's prompt cache.
func (r *Registry) Tools() []llm.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]llm.Tool, 0, len(r.skills))
	for _, s := range r.skills {
		out = append(out, s.Tool)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Names lists registered skill names, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.skills))
	for name := range r.skills {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Execute runs a skill by name.
func (r *Registry) Execute(ctx context.Context, name string, args map[string]any) (string, error) {
	r.mu.RLock()
	s, ok := r.skills[name]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("no such skill %q", name)
	}
	return s.Handler(ctx, args)
}

// --- argument helpers -------------------------------------------------------
//
// Providers hand back JSON, so numbers arrive as float64 and everything may be
// absent. These coerce leniently rather than failing on a type the model chose.

func argString(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func argInt(args map[string]any, key string, fallback int) int {
	v, ok := args[key]
	if !ok || v == nil {
		return fallback
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return n
		}
	}
	return fallback
}

func argBool(args map[string]any, key string) bool {
	v, ok := args[key]
	if !ok || v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		b, _ := strconv.ParseBool(strings.TrimSpace(t))
		return b
	}
	return false
}

// --- process helper ---------------------------------------------------------

// run executes a command with a timeout and returns its combined output.
// Commands are invoked directly, never through a shell, so skill arguments
// cannot inject extra commands.
func run(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("%s timed out after %s", name, timeout)
	}
	if err != nil {
		if text == "" {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		return text, fmt.Errorf("%s: %w: %s", name, err, text)
	}
	return text, nil
}

// have reports whether a binary exists on PATH, so skills can degrade with a
// useful message instead of an exec error.
func have(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}
