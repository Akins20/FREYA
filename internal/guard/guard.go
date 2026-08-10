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
	"sync"
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
	// Attended reports whether there is a human who can actually answer a
	// confirmation right now. Nil means yes, which is what a REPL with somebody
	// sitting at it gets.
	//
	// It exists because "no" and "nobody was asked" are different facts and were
	// being reported as the same one. A headless session installed a Confirm that
	// printed to stderr and returned false, so the guard took the ordinary
	// declined path and handed back ErrDenied — "declined by user" — for an
	// action no user ever saw. She then told the user they had refused something
	// they were never shown, and stopped, because a refusal is a decision and
	// there is nothing to adapt to. The honest branch below existed all along and
	// was simply unreachable, because the thing standing in for the prompt was
	// not nil.
	//
	// Separate from Confirm rather than folded into it: a ConfirmFunc returns one
	// bool and has no room to carry why, and every caller and test that supplies
	// a plain yes/no keeps working untouched.
	Attended func() bool
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
	// Workspace is the directory Freya was given as her own. A write confined to
	// it is low risk; everything outside is judged as before.
	//
	// Measured, not assumed. Asked to build a copy of a website — write the
	// files, open them, refine until they match — she was refused on the first
	// file, then on the folder, then on the shell fallback, and ended up pasting
	// the whole page into the reply for the user to save by hand. Writing
	// hello.html into her OWN workspace assessed as medium, which needs a
	// confirmation, and the daemon has no terminal: it asks aloud, once per file.
	// For a task that is dozens of writes that is not a safeguard, it is a wall.
	//
	// The same argument as the automation Chrome profile: her workspace holds
	// nothing of the user's, a bad write there destroys only her own scratch
	// work, and a confirmation she cannot obtain is not protection — it is a
	// capability that silently does not exist. Anything outside this directory is
	// untouched by this and still asks.
	Workspace string
	// Telemetry counts decisions, if anything is listening.
	//
	// Declared as a local interface rather than importing the telemetry package
	// so that the guard — the component with the least business depending on
	// anything — keeps its import list to the standard library.
	Telemetry DecisionRecorder

	// confirmMu serialises confirmation prompts. Tool calls now run
	// concurrently, and two prompts competing for the same terminal would
	// interleave their questions and read each other's answers.
	confirmMu sync.Mutex
}

// DecisionRecorder is told about every guard decision.
//
// Implementations must be non-blocking and must tolerate a nil receiver: a
// refusal is on the critical path of an action the user is waiting for, and
// metrics have no business slowing it down or failing it.
type DecisionRecorder interface {
	Guard(risk, outcome, detail string)
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
	a := assess(action, g.ProtectedPaths, g.Workspace)
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

// ErrDenied is returned when the user declines an action. A person saw it and
// said no.
var ErrDenied = fmt.Errorf("declined by user")

// ErrUnattended is returned when an action needed approval and there was nobody
// to ask. Deliberately not wrapping ErrDenied: anything matching on the text
// would then still read "declined by user", which is the false statement this
// exists to stop being made.
var ErrUnattended = fmt.Errorf("nobody could be asked")

// unattended reports that no human can answer a confirmation right now.
func (g *Guard) unattended() bool { return g.Confirm == nil || (g.Attended != nil && !g.Attended()) }

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
		if g.unattended() {
			// No means of asking is not a licence to proceed — but it is also not
			// a refusal, and the difference has to survive all the way back to
			// the model, because it is the difference between "the user said no"
			// and "the user was never shown this". The first ends the task; the
			// second is something she can say out loud and offer a way round.
			record.Outcome = "denied-no-prompt"
			g.record(record)
			return "", fmt.Errorf("%w: this needs confirmation (%s risk: %s) and this session has no way to "+
				"reach anyone — no prompt was shown, so nobody saw it and nobody refused it. Nothing was done. "+
				"Do not report this as the user declining. Say that it needs their approval and you had no way "+
				"to ask, and offer the alternatives: they can approve it themselves, or you can do it somewhere "+
				"that needs no approval",
				ErrUnattended, assessment.Risk, firstReason(assessment))
		}
		g.confirmMu.Lock()
		approved := g.Confirm(ctx, action, assessment)
		g.confirmMu.Unlock()
		if !approved {
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

// firstReason names what made the action need asking about, so the refusal says
// which rule fired rather than only that one did.
func firstReason(a Assessment) string {
	if len(a.Reasons) > 0 {
		return a.Reasons[0]
	}
	if a.Rule != "" {
		return a.Rule
	}
	return "unstated"
}

func (g *Guard) record(r Record) {
	if g.Audit != nil {
		_ = g.Audit.Append(r)
	}
	// Every outcome — allowed, confirmed, denied, forbidden — passes through
	// here, which is why the counter hangs off this one call rather than being
	// sprinkled through Run. A refusal that is never counted is a refusal
	// nobody can review later.
	if g.Telemetry != nil {
		detail := r.Rule
		if detail == "" {
			detail = r.Action.Command
		}
		g.Telemetry.Guard(r.Risk, r.Outcome, detail)
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

// Note records an action in the audit log without gating it.
//
// # Why this exists rather than routing everything through Run
//
// Three tools are marked Mutates and never reach Run, so nothing they do appears
// in the audit log at all — and the audit log is how the user sees what she did.
// Marked as changing the world, invisible in the record of the world changing.
//
// Sending them through Run instead would be worse than the gap. serve_stop as a
// KindExec assesses medium and would start asking permission to stop a server it
// started, in a session where nothing asked before. skill_learn writes into the
// data directory, which is on ProtectedPaths, so as a KindWrite it would be
// REFUSED and the tool would simply stop working. A guard call that breaks the
// thing it guards is not a guard.
//
// So: the record without the gate, for actions whose risk was already decided by
// their existence. It is deliberately not a way to skip Run — anything that could
// harm anything belongs in Run, and the two callers here stop a subprocess she
// started and append to her own notebook.
func (g *Guard) Note(action Action, outcome string, err error) {
	r := Record{Time: time.Now(), Action: action, Risk: RiskLow.String(), Outcome: outcome}
	if err != nil {
		r.Outcome, r.Error = "error", err.Error()
	}
	g.record(r)
}
