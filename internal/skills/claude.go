package skills

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/claude"
	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/llm"
)

// Claude Code as a subordinate.
//
// The skill descriptions carry the judgement, not just the mechanics. A model
// deciding whether to delegate needs to know what the cost actually is on this
// machine — rate-limit quota, not money, since auth is a subscription — and
// that resuming a thread beats starting a new one. Both are stated where the
// decision gets made.

// RegisterClaude gives Freya the ability to delegate work.
func RegisterClaude(r *Registry, g *guard.Guard, c *claude.Client) {
	if g == nil || c == nil || !c.Available() {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "claude_delegate",
			Description: "Hand a substantial task to Claude Code — a far stronger " +
				"model with full tool access to the filesystem, able to read and edit " +
				"many files, run commands and reason for a long time.\n\n" +
				"Worth delegating: writing or refactoring real code, debugging across " +
				"files, reviewing a codebase, explaining an unfamiliar project, " +
				"multi-step engineering work.\n\n" +
				"Not worth delegating: anything you can already do. Every call consumes " +
				"the user's Claude usage allowance — they are on a subscription with " +
				"rate limits, so heavy delegation can exhaust the window and lock them " +
				"out of Claude for hours — and takes tens of seconds. Do not delegate a " +
				"question you could answer, a file you could read, or a command you " +
				"could run yourself.\n\n" +
				"Returns a session id. For ANY follow-up about the same work — a further " +
				"question, a fix, a refinement — use claude_continue with that id rather " +
				"than delegating again or doing it yourself. The session already holds " +
				"the files read and the reasoning done, so continuing is cheaper, better " +
				"informed, and keeps the thread coherent.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"task": {Type: "string", Description: "What to do, in full. Claude cannot " +
					"see this conversation, so include the context it needs."},
				"dir": {Type: "string", Description: "Project directory to work in."},
				"mode": {Type: "string", Description: "How much freedom to allow.",
					Enum: []string{"plan", "read-only", "edit", "full"}},
				"model": {Type: "string", Description: "Override the automatic choice. " +
					"Leave unset unless you have a reason: the model is picked from the " +
					"shape of the task — haiku for lookups, sonnet for ordinary work, " +
					"opus for reasoning across a codebase — and a resumed thread stays " +
					"on whatever it began with.",
					Enum: []string{"haiku", "sonnet", "opus"}},
				"effort": {Type: "string", Description: "How hard to think. Higher costs more.",
					Enum: []string{"low", "medium", "high", "xhigh", "max"}},
				"budget": {Type: "number", Description: "Ceiling in API-equivalent dollars, " +
					"which bounds runaway loops rather than billing anything. Default 2."},
			}, "task"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return delegate(ctx, g, c, args, "")
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "claude_continue",
			Description: "Carry on an existing Claude thread. Strongly preferred over " +
				"a fresh delegation for follow-up work: the session already holds the " +
				"files read, the decisions made and the reasoning so far, so it is " +
				"cheaper and better informed than starting again. Refer to a session " +
				"by its id or by the label shown in claude_sessions.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"session": {Type: "string", Description: "Session id or label."},
				"task":    {Type: "string", Description: "The follow-up."},
				"budget":  {Type: "number", Description: "Runaway ceiling, API-equivalent. Default 2."},
			}, "session", "task"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			ref := argString(args, "session")
			s, ok := c.Find(ref)
			if !ok {
				return "", fmt.Errorf("no Claude session matching %q — use claude_sessions to see them", ref)
			}
			// Find matches on an id prefix OR a label substring, and breaks ties by
			// most-recently-used — so a half-remembered reference can resume an
			// entirely different thread, which then answers fluently about the wrong
			// codebase. Say which one actually woke up.
			out, err := delegate(ctx, g, c, args, s.ID)
			if err != nil || strings.EqualFold(strings.TrimSpace(ref), s.ID) {
				return out, err
			}
			return fmt.Sprintf("%s\n(Resumed session %s%s — the closest match to %q, not an exact one.)",
				out, s.ID, labelSuffix(s.Label), ref), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "claude_sessions",
			Description: "List Claude threads already started, with what they were about " +
				"and what they have cost. Check here before delegating something similar.",
			Params: llm.ObjectSchema(nil),
		},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			sessions := c.Sessions()
			if len(sessions) == 0 {
				return "No Claude sessions yet.", nil
			}
			var sb strings.Builder
			for _, s := range sessions {
				sb.WriteString("- " + s.Describe() + "\n")
			}
			fmt.Fprintf(&sb, "\nUsage this run: ~$%.4f equivalent "+
				"(subscription — counts against rate limits, not billed)", c.Usage())
			return strings.TrimSpace(sb.String()), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "claude_skill",
			Description: "Run one of Claude Code's own built-in skills, invoked as a " +
				"slash command. Useful ones: '/review' reviews the working diff, " +
				"'/security-review' audits pending changes, '/init' writes a CLAUDE.md " +
				"describing a codebase. Pass the command exactly as typed, including " +
				"the leading slash.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"command": {Type: "string", Description: "e.g. '/review' or '/security-review'."},
				"dir":     {Type: "string", Description: "Project directory."},
				"budget":  {Type: "number", Description: "Runaway ceiling, API-equivalent. Default 2."},
			}, "command"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			cmd := strings.TrimSpace(argString(args, "command"))
			if !strings.HasPrefix(cmd, "/") {
				cmd = "/" + cmd
			}
			merged := map[string]any{
				"task":   cmd,
				"dir":    argString(args, "dir"),
				"budget": args["budget"],
				"mode":   "read-only",
			}
			return delegate(ctx, g, c, merged, "")
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "claude_label",
			Description: "Name a Claude session so it can be referred to conversationally later.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"session": {Type: "string", Description: "Session id or current label."},
				"label":   {Type: "string", Description: "The new name."},
			}, "session", "label"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			s, ok := c.Find(argString(args, "session"))
			if !ok {
				return "", fmt.Errorf("no session matching %q", argString(args, "session"))
			}
			label := argString(args, "label")
			if err := c.Label(s.ID, label); err != nil {
				return "", err
			}
			return fmt.Sprintf("Session %s is now %q.", s.ID[:8], label), nil
		},
	})
}

// permissionMode maps a plain-English freedom level onto Claude's flag.
//
// The names matter: "read-only" and "edit" are decisions a person can make,
// while "acceptEdits" and "bypassPermissions" require knowing the tool.
func permissionMode(mode string) (string, []string) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "plan":
		return "plan", nil
	case "read-only", "readonly", "":
		// Planning mode plus an explicit deny is belt and braces: the model
		// should not be able to change anything it was not asked to.
		return "plan", []string{"Edit", "Write", "NotebookEdit"}
	case "edit":
		return "acceptEdits", nil
	case "full":
		return "bypassPermissions", nil
	default:
		return "plan", []string{"Edit", "Write", "NotebookEdit"}
	}
}

func delegate(ctx context.Context, g *guard.Guard, c *claude.Client,
	args map[string]any, resume string) (string, error) {

	task := argRaw(args, "task")
	if strings.TrimSpace(task) == "" {
		return "", fmt.Errorf("task is required")
	}

	mode := argString(args, "mode")
	permMode, denied := permissionMode(mode)

	var budget float64
	if b, ok := args["budget"].(float64); ok && b > 0 {
		budget = b
	}

	// Choose a model from the shape of the task unless one was named. Claude
	// defaults to its most capable model, so without this every lookup runs on
	// Opus and spends quota a smaller model would have matched.
	var plan claude.Plan
	if resume != "" {
		prior := ""
		if s, ok := c.Find(resume); ok {
			prior = s.Model
		}
		plan = claude.PlanForResume(prior, task, argString(args, "model"),
			argString(args, "effort"), budget)
	} else {
		plan = claude.PlanFor(task, argString(args, "model"),
			argString(args, "effort"), budget)
	}

	opts := claude.Options{
		Prompt:          task,
		Resume:          resume,
		Dir:             expandIn(ctx, argString(args, "dir")),
		Model:           plan.Model,
		Effort:          plan.Effort,
		PermissionMode:  permMode,
		DisallowedTools: denied,
		BudgetUSD:       plan.BudgetUSD,
		Timeout:         12 * time.Minute,
	}

	// Delegation consumes the user's Claude allowance and, in edit or full mode,
	// changes files. Both deserve the same gate as running a command directly.
	risk := guard.KindExec
	if permMode == "acceptEdits" || permMode == "bypassPermissions" {
		risk = guard.KindWrite
	}
	action := guard.Action{
		Kind:    risk,
		Command: "claude",
		Args:    []string{clip(task, 100)},
		Reason: fmt.Sprintf("delegate to Claude Code — %s, %s mode, %s, ceiling ~$%.2f equiv",
			plan.Model, orDefault(mode, "read-only"), plan.Reason, plan.BudgetUSD),
	}
	if opts.Dir != "" {
		action.Paths = []string{opts.Dir}
	}

	return g.Run(ctx, action, func(ctx context.Context) (string, error) {
		res, err := c.Run(ctx, opts)
		if err != nil && res == nil {
			return "", err
		}
		out := res.Text
		if strings.TrimSpace(out) == "" {
			out = "(Claude returned no text)"
		}
		return fmt.Sprintf("%s\n\n[%s · %s]%s", out, res.Describe(), plan.Reason,
			couldHaveDoneItHerself(task)), err
	})
}

// RegisterClaudeAdvice adds a skill for the case that motivates all of this:
// Freya knows what needs doing and has no tool that does it.
func RegisterClaudeAdvice(r *Registry, g *guard.Guard, c *claude.Client) {
	if g == nil || c == nil || !c.Available() {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "claude_advise",
			Description: "Ask Claude how to accomplish something you have no tool for, " +
				"and get back a concrete plan you then carry out yourself. Claude reads " +
				"the relevant files and reasons about them, but changes nothing.\n\n" +
				"This is the preferred way to handle work beyond your own skills: you " +
				"stay the one making the change, so the user still sees exactly what is " +
				"being done before approving it. Use claude_delegate with edit mode only " +
				"when the change is genuinely too large to apply by hand.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"goal": {Type: "string", Description: "What the user actually wants — the " +
					"outcome, not the mechanics."},
				"context": {Type: "string", Description: "What you already know: files " +
					"involved, what you have tried, what is failing."},
				"dir": {Type: "string", Description: "Project directory."},
			}, "goal"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			goal := argRaw(args, "goal")
			if strings.TrimSpace(goal) == "" {
				return "", fmt.Errorf("goal is required")
			}

			// The framing shapes the answer: asking for steps a caller can carry
			// out produces something actionable, where "how would you do this"
			// produces an essay.
			task := "A local assistant needs to accomplish the following but lacks the " +
				"capability to do it directly. Read whatever files are relevant and reply " +
				"with a concrete, ordered plan it can execute using ordinary file edits " +
				"and shell commands. Be specific: exact paths, exact changes. Do not " +
				"modify anything yourself.\n\nGOAL: " + goal
			if extra := argRaw(args, "context"); strings.TrimSpace(extra) != "" {
				task += "\n\nWHAT THE ASSISTANT ALREADY KNOWS: " + extra
			}

			return delegate(ctx, g, c, map[string]any{
				"task": task,
				"dir":  argString(args, "dir"),
				"mode": "read-only",
			}, "")
		},
	})
}

// labelSuffix renders a session's label for disclosure, or nothing when it has
// none. Used when a fuzzy reference resumed a different thread than the one
// named, so the reply says which one actually woke up.
func labelSuffix(label string) string {
	if strings.TrimSpace(label) == "" {
		return ""
	}
	return " (" + label + ")"
}

// couldHaveDoneItHerself names a delegation that did not need delegating.
//
// # Why this reports rather than refuses
//
// "Do NOT delegate what you can already do with your own tools" is the last of
// the prose rules, and it is the one where a hard gate is the wrong instrument.
// Every other rule audited today had the same asymmetry — a missed guard destroys
// something, a spurious guard costs a round — so refusing was right. This one
// runs the other way. A wrongly refused delegation blocks real work; a wrongly
// allowed one costs quota, which recovers on its own at the next window. Refusing
// on a keyword would eventually block "read every Go file and find the race",
// which looks like a read and is not.
//
// So the cost is made visible at the moment it is paid, and left as her call.
// Repeated often enough it is also a number in the telemetry, which is what turns
// a habit into something anyone can see.
func couldHaveDoneItHerself(task string) string {
	if claude.Classify(task) != claude.Simple {
		return ""
	}
	low := strings.ToLower(task)
	local := map[string]string{
		"read the file": "file_read", "read this file": "file_read",
		"what does the file": "file_read", "list the": "folder_list",
		"read the page": "browser_read", "what does the page": "browser_read",
		"search the web": "web_search", "look up": "web_search",
		"grep": "run_shell with grep", "find the file": "run_shell with find",
	}
	for phrase, tool := range local {
		if strings.Contains(low, phrase) {
			return fmt.Sprintf("\n(That looked like something %s does directly. Delegating "+
				"spends the user's Claude allowance on work you already had a tool for — "+
				"worth noticing if it becomes a habit.)", tool)
		}
	}
	return ""
}
