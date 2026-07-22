package claude

import (
	"regexp"
	"strings"
)

// Choosing a model for a task.
//
// # Why a heuristic rather than asking the model
//
// The obvious approach is to let the delegating model pick. It does not work
// well: models are poor judges of how hard a task will turn out to be, and when
// uncertain they reach for the most capable option. Claude Code already
// defaults to its strongest model, so "let it decide" collapses to "always
// Opus" — and on a subscription that is quota spent on work a smaller model
// would have done identically.
//
// So a cheap classifier proposes, and an explicit choice always wins. The
// signals below are shallow on purpose. A deep estimate of difficulty is itself
// a hard problem; what is wanted is only enough separation to keep lookups off
// the expensive model.

// Complexity is how demanding a task looks.
type Complexity int

const (
	// Simple: a lookup, a reading, a single obvious change.
	Simple Complexity = iota
	// Moderate: ordinary engineering within a file or two.
	Moderate
	// Hard: reasoning across a codebase, debugging, design.
	Hard
)

func (c Complexity) String() string {
	switch c {
	case Simple:
		return "simple"
	case Hard:
		return "hard"
	default:
		return "moderate"
	}
}

// Model returns the model alias suited to this complexity.
func (c Complexity) Model() string {
	switch c {
	case Simple:
		return "haiku"
	case Hard:
		return "opus"
	default:
		return "sonnet"
	}
}

// Effort returns a matching reasoning effort.
func (c Complexity) Effort() string {
	switch c {
	case Simple:
		return "low"
	case Hard:
		return "high"
	default:
		return "medium"
	}
}

// Budget returns a runaway ceiling proportional to the work expected.
func (c Complexity) Budget() float64 {
	switch c {
	case Simple:
		return 0.50
	case Hard:
		return 5.00
	default:
		return 2.00
	}
}

var (
	// simpleVerbs describe work that reads rather than changes.
	simpleVerbs = regexp.MustCompile(`(?i)\b(what|where|which|list|show|find|read|print|summari[sz]e|describe|explain briefly|check|look at|tell me)\b`)

	// hardVerbs describe work that requires holding a system in mind.
	//
	// "port" is deliberately absent as a bare word: it is far more often the
	// noun. "the default port" was scoring as porting a codebase, sending a
	// one-line lookup to the most expensive model.
	hardVerbs = regexp.MustCompile(`(?i)\b(refactor|redesign|re-?architect|migrat\w+|port(ing|ed) (it |this |the )?to|rewrite|debug|diagnose|investigate|optimi[sz]e|profile|audit|review|design|restructure|untangle|root cause)\b`)

	// modifyVerbs describe real but contained work: something is being built or
	// changed, in a known place.
	modifyVerbs = regexp.MustCompile(`(?i)\b(add|write|create|implement|update|change|modify|fix|extend|rename|remove|delete|wire up|hook up|support)\b`)

	// breadthWords indicate the task spans more than one place.
	breadthWords = regexp.MustCompile(`(?i)\b(across|throughout|every|all (the )?(files?|packages?|modules?|tests?)|entire|whole|codebase|repository|repo|end.to.end|system.wide)\b`)

	// depthWords indicate subtlety rather than volume.
	depthWords = regexp.MustCompile(`(?i)\b(race condition|deadlock|memory leak|performance|concurren\w+|architecture|trade.?off|why does|why is|intermittent|flaky|corrupt\w*|security|vulnerab\w+)\b`)
)

// Classify estimates how demanding a task is.
func Classify(task string) Complexity {
	t := strings.TrimSpace(task)
	if t == "" {
		return Simple
	}

	// A Claude Code skill invocation carries its own weight: /review and
	// /security-review read an entire diff and reason about it.
	if strings.HasPrefix(t, "/") {
		return Hard
	}

	score := 0

	// Precedence matters: a task can read as both "explain" and "refactor", and
	// the heavier intent is the one that governs.
	switch {
	case hardVerbs.MatchString(t):
		score += 2
	case modifyVerbs.MatchString(t):
		score++
	case simpleVerbs.MatchString(t):
		score--
	}
	if breadthWords.MatchString(t) {
		score += 2
	}
	if depthWords.MatchString(t) {
		score += 2
	}

	// Length is a weak signal on its own but a decent tiebreaker: a task
	// someone bothered to describe in detail is usually not a lookup.
	words := len(strings.Fields(t))
	switch {
	case words > 120:
		score += 2
	case words > 50:
		score++
	case words < 8:
		// Only genuinely terse instructions get the discount. A twelve-word
		// threshold was penalising ordinary one-line engineering tasks into
		// the cheapest model.
		score--
	}

	// Several explicit file paths mean several places to hold at once.
	if paths := strings.Count(t, "/"); paths >= 3 {
		score++
	}

	// A bare question is usually a lookup, whatever else it contains.
	if strings.HasSuffix(t, "?") && words < 25 && !hardVerbs.MatchString(t) {
		score--
	}

	switch {
	case score >= 3:
		return Hard
	case score <= 0:
		return Simple
	default:
		return Moderate
	}
}

// Plan is a recommended configuration for a task.
type Plan struct {
	Complexity Complexity
	Model      string
	Effort     string
	BudgetUSD  float64
	// Reason explains the choice, so it can be shown rather than guessed at.
	Reason string
}

// PlanFor recommends settings for a task, honouring anything already chosen.
//
// An explicit model always wins: the classifier is a default, not a policy.
func PlanFor(task, model, effort string, budget float64) Plan {
	c := Classify(task)
	p := Plan{
		Complexity: c,
		Model:      c.Model(),
		Effort:     c.Effort(),
		BudgetUSD:  c.Budget(),
	}

	var overrides []string
	if model != "" {
		p.Model = model
		overrides = append(overrides, "model set explicitly")
	}
	if effort != "" {
		p.Effort = effort
		overrides = append(overrides, "effort set explicitly")
	}
	if budget > 0 {
		p.BudgetUSD = budget
		overrides = append(overrides, "budget set explicitly")
	}

	p.Reason = "classified " + c.String()
	if len(overrides) > 0 {
		p.Reason += "; " + strings.Join(overrides, ", ")
	}
	return p
}

// PlanForResume keeps a thread on the model it began with.
//
// Switching models mid-conversation discards the advantage of resuming: the
// reasoning in the session was produced by a particular model, and handing it
// to a weaker one to continue tends to produce confident nonsense built on
// premises it cannot actually reconstruct.
func PlanForResume(previous string, task, model, effort string, budget float64) Plan {
	p := PlanFor(task, model, effort, budget)
	if model == "" && previous != "" {
		p.Model = previous
		p.Reason = "continuing on the session's original model"
	}
	return p
}

// aliasFor reduces a full model name to the alias used on the command line.
func aliasFor(fullName string) string {
	n := strings.ToLower(fullName)
	switch {
	case strings.Contains(n, "haiku"):
		return "haiku"
	case strings.Contains(n, "sonnet"):
		return "sonnet"
	case strings.Contains(n, "opus"):
		return "opus"
	case strings.Contains(n, "fable"):
		return "fable"
	}
	return ""
}
