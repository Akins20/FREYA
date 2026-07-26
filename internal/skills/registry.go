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
//
// Handler is the original contract and still works untouched. The three optional
// fields below let a skill opt into being verified by the framework rather than
// being trusted: they exist because the playbooks were full of instructions to
// check your own work ("after ANY click, read again to confirm it did what you
// expected") which the substrate could do itself, in the same round, instead of
// spending a round asking the model to remember.
type Skill struct {
	Tool    llm.Tool
	Handler Handler
	// Act is Handler's richer form, reporting an Outcome. Preferred when set.
	Act Acting
	// Mutates marks a skill that changes the world, so the framework verifies it.
	Mutates bool
	// Observe returns a cheap fingerprint of whatever this skill touches. When it
	// is set on a mutating skill, Execute samples it before and after: identical
	// readings mean nothing happened, and the result says so.
	Observe func(ctx context.Context) string
	// Affordances lists what is genuinely available right now — the clickable
	// things on this page, the open terminal sessions, the files in this
	// directory. Attached automatically when the skill fails, so a miss hands back
	// the state needed to succeed instead of an invitation to guess again.
	Affordances func(ctx context.Context) []string
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

// Execute runs a skill by name, verifying it where the skill allows.
//
// Three things happen around the call that used to be left to the model's
// discretion, one round at a time: a mutating skill's effect on the world is
// sampled before and after, so "it ran and nothing moved" is stated rather than
// implied; a failure carries what IS available; and the result keeps the
// distinction between what was asked for and what actually happened.
func (r *Registry) Execute(ctx context.Context, name string, args map[string]any) (string, error) {
	r.mu.RLock()
	s, ok := r.skills[name]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("no such skill %q", name)
	}

	// Sample the world first. Only for skills that change it — a read has nothing
	// to verify, and the sample costs a round-trip to the page.
	var before string
	watching := s.Mutates && s.Observe != nil
	if watching {
		before = s.Observe(ctx)
	}

	var (
		out Outcome
		err error
	)
	if s.Act != nil {
		out, err = s.Act(ctx, args)
	} else {
		var text string
		text, err = s.Handler(ctx, args)
		out = Outcome{Text: text}
	}

	if err != nil {
		if len(out.Options) > 0 {
			return "", withOptions(err, out.Options)
		}
		if s.Affordances != nil {
			return "", withOptions(err, s.Affordances(ctx))
		}
		return "", err
	}

	// An action that reported success while the world stayed identical is the
	// failure the model cannot see. Only fill in what the skill did not already
	// determine for itself — a skill that knows better outranks the fingerprint.
	if watching && out.Changed == nil {
		if after := s.Observe(ctx); after != "" && before != "" {
			changed := after != before
			out.Changed = &changed
		}
	}
	return out.Render(), nil
}

// AffordancesFor reports what a named skill says is available right now, or nil.
// The agent uses it when refusing a call, so a refusal still points somewhere.
func (r *Registry) AffordancesFor(ctx context.Context, name string) []string {
	r.mu.RLock()
	s, ok := r.skills[name]
	r.mu.RUnlock()
	if !ok || s.Affordances == nil {
		return nil
	}
	return s.Affordances(ctx)
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

// argRaw returns a string argument with NO trimming.
//
// argString trims whitespace, which is right for paths and names and actively
// wrong for content. Trailing newlines are meaningful in a file, and leading
// indentation is meaningful in a code edit: trimming `old_text` would strip the
// indentation off a match anchor and either miss entirely or, worse, match a
// differently-indented line elsewhere. Anything that becomes file bytes must
// come through here.
func argRaw(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
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
