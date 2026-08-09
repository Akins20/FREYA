package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/akins/jarvis/internal/llm"
)

// The list of what this piece of work actually consists of, written before the
// work starts and checked before it is called finished.
//
// # Why a list, and why the framework holds it
//
// Long tasks push their own instructions to the edge of the window. A documented
// case in Claude Code: an eighteen-step workflow silently dropped two steps every
// run; the cure was making "write the checklist first" step zero, after which
// steps skipped went to zero. Execution tracks the list rather than a memory of
// the request that is getting older every round.
//
// She already had the instruction — "work out the whole set BEFORE you start" —
// and nowhere to put it. Reasoning is not durable and nothing can check it. This
// gives it somewhere to live, and gives the finish gate something to read.
//
// # Why she does not tick her own boxes
//
// Because the measured failure mode is exactly that. "Characterizing False
// Success in LLM Agents" (arXiv 2606.09863) finds agents asserting completion
// against an environment that says otherwise in 75.8% of self-assessing coding
// trajectories that made an explicit status claim, and finds that LLM judges
// cannot detect it — AUROC 0.65 and 0.54, close enough to chance to be useless —
// because they key on confident closing language and on how much the agent did.
// Detectors that check state instead reach 0.83 to 0.95.
//
// Her own worst run today is the paper's abstract in miniature: she wrote three
// files, ran code_check three times, served the site, opened it on the user's
// screen and said it was done, with a dead link she had been told about still in
// it. Maximum activity, maximum confidence, wrong.
//
// So marking a step done is a claim the framework checks where it can. Today
// that is one check — a step that names a file it will produce cannot be
// completed while that file is absent — and the answer comes from the disk. It
// is deliberately narrow. A checker that is wrong does not get ignored; she
// obeys it, and the work gets worse. That happened today too.
//
// # Why the list is not re-sent on every update
//
// Claude Code's TodoWrite takes the whole array each call, which is fine for a
// model that will reproduce it faithfully. This runs on a small model, where
// re-emitting a nine-item list to tick item four is a real chance of returning
// eight. The list lives here; plan_step moves one item. Losing work to a
// transcription slip is the failure this whole file exists to prevent.

// StepState is where one item stands.
type StepState string

const (
	StepTodo    StepState = "todo"
	StepDoing   StepState = "doing"
	StepDone    StepState = "done"
	StepDropped StepState = "dropped"
)

// Step is one item of work.
type Step struct {
	Text  string
	State StepState
	// Produces is the file this step is expected to leave behind, when it named
	// one. Empty when the step is not about making a file.
	Produces string
	// Note is why it was dropped, or what was done. Shown back to the user.
	Note string
}

// Plan is the per-thread list. A pointer inside Scope, like found — a step
// created in round two must be markable in round nine, and the scope is copied
// into every call.
type Plan struct {
	mu    sync.Mutex
	steps []Step
}

// NewPlan makes an empty plan.
func NewPlan() *Plan { return &Plan{} }

// Set replaces the list. Revising it mid-task is expected and encouraged: work
// discovered halfway is work, and the alternative is doing it without recording
// it or, worse, not doing it.
func (p *Plan) Set(items []string) int {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Anything already settled keeps its state, matched on text, so revising the
	// list does not resurrect finished work.
	was := map[string]Step{}
	for _, s := range p.steps {
		was[normalise(s.Text)] = s
	}
	p.steps = p.steps[:0]
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it == "" {
			continue
		}
		step := Step{Text: it, State: StepTodo, Produces: fileNamedIn(it)}
		if old, ok := was[normalise(it)]; ok {
			step.State, step.Note = old.State, old.Note
		}
		p.steps = append(p.steps, step)
	}
	return len(p.steps)
}

// Mark moves one step, one-based. dir is where relative filenames resolve.
func (p *Plan) Mark(n int, state StepState, note, dir string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if n < 1 || n > len(p.steps) {
		return fmt.Errorf("there is no step %d — the plan has %d", n, len(p.steps))
	}
	step := &p.steps[n-1]

	if state == StepDoing {
		// Exactly one at a time. Five things started and none finished is the
		// shape this prevents; Claude Code enforces the same rule and words it
		// "not less, not more".
		for i := range p.steps {
			if p.steps[i].State == StepDoing && i != n-1 {
				p.steps[i].State = StepTodo
			}
		}
	}

	if state == StepDone && step.Produces != "" {
		path := step.Produces
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("step %d says it produces %s and that file is not there. "+
				"Either it was never written, or it went somewhere else — check before "+
				"marking this done, because the only thing they will have is the file",
				n, step.Produces)
		}
	}

	step.State = state
	if note = strings.TrimSpace(note); note != "" {
		step.Note = note
	}
	return nil
}

// Outstanding returns the steps not yet settled, rendered for reading.
func (p *Plan) Outstanding() []string {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	var out []string
	for i, s := range p.steps {
		if s.State == StepTodo || s.State == StepDoing {
			word := "not started"
			if s.State == StepDoing {
				word = "started, not finished"
			}
			out = append(out, fmt.Sprintf("step %d, %s: %s", i+1, word, s.Text))
		}
	}
	return out
}

// Render is the list as she should see it.
func (p *Plan) Render() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.steps) == 0 {
		return "The plan is empty."
	}
	var sb strings.Builder
	done := 0
	for i, s := range p.steps {
		box := map[StepState]string{
			StepTodo: "[ ]", StepDoing: "[~]", StepDone: "[x]", StepDropped: "[-]",
		}[s.State]
		if s.State == StepDone {
			done++
		}
		fmt.Fprintf(&sb, "%s %d. %s", box, i+1, s.Text)
		if s.Note != "" {
			fmt.Fprintf(&sb, "  (%s)", s.Note)
		}
		sb.WriteString("\n")
	}
	fmt.Fprintf(&sb, "\n%d of %d done.", done, len(p.steps))
	return sb.String()
}

// normalise makes step text comparable across a revision that changed only
// spacing or case.
func normalise(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// looksLikeFile matches a token that is unambiguously a filename: a name and a
// known extension. Deliberately a closed list — "e.g." and "3.5" and "vs." all
// match a general name-dot-suffix pattern, and a check that fires on those would
// refuse to let her finish steps that never involved a file.
var looksLikeFile = regexp.MustCompile(`(?i)\b([\w./-]+\.(?:html?|css|js|ts|tsx|jsx|go|py|rb|sh|json|ya?ml|toml|md|txt|csv|pdf|docx?|xlsx?|pptx?|png|jpe?g|svg|sql))\b`)

// fileNamedIn pulls the artefact out of a step that promises one.
//
// Only when the step reads like making it. "Fix the header in index.html" names
// a file that already exists and proves nothing about whether the fix landed;
// "write index.html" does. Getting this wrong in the permissive direction blocks
// real work, so it stays narrow and silent when unsure.
func fileNamedIn(text string) string {
	m := looksLikeFile.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	lower := strings.ToLower(text)
	for _, verb := range []string{"write", "create", "build", "make", "add", "generate",
		"produce", "save", "export", "render"} {
		if strings.Contains(lower, verb) {
			return m[1]
		}
	}
	return ""
}

// lines splits a written-out list into steps, tolerating the several ways a
// model writes one: bare lines, "1." numbering, or a leading dash.
func lines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		ln = strings.TrimLeft(ln, "-*•\t ")
		ln = regexp.MustCompile(`^\d+[.)]\s*`).ReplaceAllString(ln, "")
		if ln = strings.TrimSpace(ln); ln != "" {
			out = append(out, ln)
		}
	}
	return out
}

// RegisterPlan adds the plan tools.
func RegisterPlan(r *Registry) {
	r.Register(Skill{
		Tool: llm.Tool{
			Name: "plan_set",
			Description: "Write down everything this piece of work consists of, before " +
				"starting it.\n\n" +
				"Do this FIRST for anything that is more than a couple of steps — a site, " +
				"a report, research with several questions in it, a request with two " +
				"halves. List the WHOLE set, including the parts you thought of yourself " +
				"rather than only the ones they said out loud: if the nav is going to have " +
				"four pages, that is four steps.\n\n" +
				"Why it matters: on a long task the request slides to the back of your " +
				"attention and steps get dropped silently, with nothing failing. The list " +
				"is what execution follows once that happens.\n\n" +
				"Revise it whenever you learn something — work you discover halfway is " +
				"work. Calling this again keeps the state of steps you have already " +
				"settled.\n\n" +
				"You will not be able to give a final answer while steps are still open, " +
				"so put real items on it, not a summary of your intentions.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"steps": {Type: "string", Description: "The steps, one per line, in order. " +
					"Say what each one produces where it produces something — 'write " +
					"contact.html' rather than 'contact page'."},
			}, "steps"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			items := lines(argRaw(args, "steps"))
			if len(items) == 0 {
				return "", fmt.Errorf("a plan with no steps in it is not a plan")
			}
			plan := ScopeFrom(ctx).Plan()
			if plan == nil {
				return "", fmt.Errorf("no plan is being kept for this piece of work")
			}
			n := plan.Set(items)
			return fmt.Sprintf("Plan set, %d step(s). Mark each one with plan_step as you "+
				"go — one in progress at a time, and done only when it is really done.\n\n%s",
				n, plan.Render()), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "plan_step",
			Description: "Move one step of the plan: starting it, finishing it, or " +
				"dropping it.\n\n" +
				"Mark it the moment it happens rather than all at the end — a list updated " +
				"afterwards is a list that was not used.\n\n" +
				"Only one step is in progress at a time. Mark done only when it is FULLY " +
				"done: not when the file is written but unchecked, not when three of four " +
				"pages exist, not when it works but you have not looked at it. If a step " +
				"turns out to be unnecessary, drop it and say why — that is honest, and " +
				"leaving it open is not.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"step":  {Type: "number", Description: "Which step, counting from 1."},
				"state": {Type: "string", Description: "doing, done, dropped, or todo to reopen it."},
				"note":  {Type: "string", Description: "Optional. What happened, or why it was dropped."},
			}, "step", "state"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			scope := ScopeFrom(ctx)
			plan := scope.Plan()
			if plan == nil {
				return "", fmt.Errorf("no plan is being kept for this piece of work")
			}
			state := StepState(strings.ToLower(strings.TrimSpace(argString(args, "state"))))
			switch state {
			case StepTodo, StepDoing, StepDone, StepDropped:
			default:
				return "", fmt.Errorf("state is one of doing, done, dropped, todo — got %q", state)
			}
			if err := plan.Mark(argInt(args, "step", 0), state, argString(args, "note"), scope.Dir()); err != nil {
				return "", err
			}
			return plan.Render(), nil
		},
	})
}
