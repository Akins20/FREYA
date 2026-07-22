// Package guard decides what Freya is allowed to do to the machine.
//
// # Threat model
//
// The dangerous actor here is not a malicious user — it is a capable model
// acting on an ambiguous instruction. "Clean up the old project files" is a
// reasonable request that becomes catastrophic if the model resolves "old" to
// the wrong directory. Guard exists to make that class of mistake visible
// before it happens, not to defend against an adversary who already has shell
// access.
//
// # Three tiers, not two
//
// A yes/no prompt trains people to say yes. Guard sorts actions into:
//
//   - allowed outright: reads and trivially reversible writes, executed silently
//   - confirmed: mutating or destructive, shown with a concrete preview of what
//     will actually change, and executed only on explicit approval
//   - forbidden: refused regardless of approval, because no human confirmation
//     makes `dd` onto a mounted system disk survivable
//
// The forbidden tier matters most. Confirmation dialogs are answered by tired
// people at midnight; some commands must simply not be reachable.
//
// # Previews are the actual safety feature
//
// "Delete 4,312 files totalling 8.2 GB, including Development/JARVIS" is a
// decision. "Are you sure?" is a reflex. Guard resolves globs, counts files and
// measures bytes before asking, so approval means something.
package guard

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Risk classifies how much damage an action could do.
type Risk int

const (
	// RiskNone reads state without changing it.
	RiskNone Risk = iota
	// RiskLow mutates something trivially reversible: volume, brightness, a
	// window position.
	RiskLow
	// RiskMedium writes or modifies files that could be recreated with effort.
	RiskMedium
	// RiskHigh destroys data, escalates privilege, or reaches the network in a
	// way that could execute code.
	RiskHigh
	// RiskForbidden is never permitted, with or without confirmation.
	RiskForbidden
)

func (r Risk) String() string {
	switch r {
	case RiskNone:
		return "read"
	case RiskLow:
		return "low"
	case RiskMedium:
		return "medium"
	case RiskHigh:
		return "destructive"
	case RiskForbidden:
		return "forbidden"
	default:
		return "unknown"
	}
}

// Kind categorises what an action does, which selects the rules applied.
type Kind string

const (
	KindExec    Kind = "exec"    // run a program
	KindDelete  Kind = "delete"  // remove files
	KindWrite   Kind = "write"   // create or modify files
	KindMove    Kind = "move"    // rename or relocate
	KindBrowser Kind = "browser" // drive the browser
	KindInput   Kind = "input"   // synthetic keyboard or mouse
	KindSystem  Kind = "system"  // settings, services, power
)

// Action is a single thing Freya proposes to do.
type Action struct {
	Kind Kind
	// Command and Args describe an exec action. Args are passed directly to
	// the process, never through a shell, unless Shell is set.
	Command string
	Args    []string
	// Shell is a raw shell string. It is treated as inherently riskier than
	// argv form because metacharacters can chain unrelated commands.
	Shell string
	// Paths are the filesystem targets the action will affect.
	Paths []string
	// Elevated means the action runs as root.
	Elevated bool
	// Reason is Freya's stated justification, shown in the confirmation.
	Reason string
}

// Assessment is guard's verdict on an action.
type Assessment struct {
	Risk    Risk
	Rule    string   // the rule that set the risk
	Reasons []string // everything noteworthy found
	Preview string   // concrete description of the effect
	// Confirm is true when the user must approve before execution.
	Confirm bool
	// Reversible indicates the effect can be undone without a backup.
	Reversible bool
}

// Blocked reports whether the action may not run at all.
func (a Assessment) Blocked() bool { return a.Risk == RiskForbidden }

// ConfirmFunc asks the user to approve an action. It returns true to proceed.
// Implementations must default to false on any error or ambiguity.
type ConfirmFunc func(ctx context.Context, action Action, assessment Assessment) bool

// Guard evaluates and gates actions.
type Guard struct {
	// Confirm is called for actions needing approval. A nil Confirm denies
	// every action that requires one, which is the safe default: a guard with
	// no way to ask must not assume yes.
	Confirm ConfirmFunc
	// Audit records every decision. Optional but strongly recommended.
	Audit *Log
	// DryRun assesses and reports without executing anything.
	DryRun bool
	// AutoApprove skips confirmation up to and including this risk level.
	// Defaults to RiskLow, so reads and trivial changes are silent while
	// anything that writes files gets asked about.
	AutoApprove Risk
	// ProtectedPaths are additional paths to treat as high risk.
	ProtectedPaths []string
}

// New builds a guard with safe defaults.
func New(confirm ConfirmFunc, audit *Log) *Guard {
	return &Guard{
		Confirm:     confirm,
		Audit:       audit,
		AutoApprove: RiskLow,
	}
}

// Assess evaluates an action without running it.
func (g *Guard) Assess(action Action) Assessment {
	a := assess(action, g.ProtectedPaths)
	if a.Risk > g.AutoApprove && a.Risk != RiskForbidden {
		a.Confirm = true
	}
	return a
}

// ErrForbidden is returned when an action is refused outright.
type ErrForbidden struct {
	Rule   string
	Detail string
}

func (e *ErrForbidden) Error() string {
	return fmt.Sprintf("refused: %s (%s)", e.Detail, e.Rule)
}

// ErrDenied is returned when the user declines an action.
var ErrDenied = fmt.Errorf("declined by user")

// Run assesses an action and, if permitted, executes it via exec.
//
// The caller supplies the execution function so guard stays independent of how
// actions are actually performed — the same gate covers shell commands,
// browser navigation and synthetic input.
func (g *Guard) Run(ctx context.Context, action Action, exec func(context.Context) (string, error)) (string, error) {
	assessment := g.Assess(action)
	start := time.Now()

	record := Record{
		Time:    start,
		Action:  action,
		Risk:    assessment.Risk.String(),
		Rule:    assessment.Rule,
		Preview: assessment.Preview,
	}

	if assessment.Blocked() {
		record.Outcome = "forbidden"
		g.record(record)
		return "", &ErrForbidden{
			Rule:   assessment.Rule,
			Detail: strings.Join(assessment.Reasons, "; "),
		}
	}

	if assessment.Confirm {
		if g.Confirm == nil {
			// No means of asking is not a licence to proceed.
			record.Outcome = "denied-no-prompt"
			g.record(record)
			return "", fmt.Errorf("%w: this action needs confirmation but no prompt is available", ErrDenied)
		}
		if !g.Confirm(ctx, action, assessment) {
			record.Outcome = "denied"
			g.record(record)
			return "", ErrDenied
		}
		record.Confirmed = true
	}

	if g.DryRun {
		record.Outcome = "dry-run"
		g.record(record)
		return "[dry run] would have: " + assessment.Preview, nil
	}

	out, err := exec(ctx)
	record.Duration = time.Since(start)
	if err != nil {
		record.Outcome = "error"
		record.Error = err.Error()
	} else {
		record.Outcome = "ok"
	}
	g.record(record)
	return out, err
}

func (g *Guard) record(r Record) {
	if g.Audit != nil {
		_ = g.Audit.Append(r)
	}
}

// Describe renders an assessment for display.
func (a Assessment) Describe() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "risk: %s", a.Risk)
	if a.Rule != "" {
		fmt.Fprintf(&sb, " (%s)", a.Rule)
	}
	if !a.Reversible && a.Risk >= RiskMedium {
		sb.WriteString(" · NOT reversible")
	}
	for _, r := range a.Reasons {
		fmt.Fprintf(&sb, "\n  - %s", r)
	}
	if a.Preview != "" {
		fmt.Fprintf(&sb, "\n  effect: %s", a.Preview)
	}
	return sb.String()
}
