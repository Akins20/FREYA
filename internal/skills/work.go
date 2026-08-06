package skills

import (
	"context"
	"fmt"
	"strings"

	"github.com/akins/jarvis/internal/llm"
	"github.com/akins/jarvis/internal/work"
)

// Putting something in the background, and knowing what happened to it.
//
// The three tools are deliberately few. She does not need a job control system;
// she needs to be able to say "this will take a while, I'll get on with it" and
// then actually answer the next question — and the user needs to be able to ask
// what is running and stop it.
func RegisterWork(r *Registry, m *work.Manager) {
	if m == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "work_start",
			Description: "Begin a long task in the background and return immediately, so you " +
				"stay free to talk. Use when the user asks for something that will take minutes " +
				"— working through a set of quizzes, reading a long document, a multi-step " +
				"errand — AND they would rather keep talking than wait. Say what you have " +
				"started. Do NOT use it for anything quick, and do NOT use it to escape a task " +
				"you should simply do now.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"goal": {Type: "string", Description: "The whole task, written so it stands alone: " +
					"the background thread cannot ask you what you meant."},
			}, "goal"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			goal := strings.TrimSpace(argString(args, "goal"))
			if goal == "" {
				return "", fmt.Errorf("goal is required, written so it stands alone")
			}
			// A job may not start a job. One level is concurrency; recursion is a
			// fork bomb with a friendly name, and a job that spawns work nobody is
			// watching is exactly the failure the bounded pool exists to prevent.
			if scope := ScopeFrom(ctx); scope.JobID != "" {
				return "", fmt.Errorf("you are already running in the background (%s) — "+
					"do the work here rather than starting another thread", scope.JobID)
			}
			j, err := m.Start(goal, "you")
			if err != nil {
				return "", fmt.Errorf("%w — finish or cancel something first (work_list shows what)", err)
			}
			return fmt.Sprintf("Started %s in the background: %s\n"+
				"Tell the user you have started it and carry on. You will be given the result "+
				"when it finishes; do not poll for it.", j.ID, goal), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "work_list",
			Description: "List background tasks and how far they have got. Use when the user " +
				"asks what you are doing, or before saying you are free.",
			Params: llm.ObjectSchema(nil),
		},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			jobs := m.List()
			if len(jobs) == 0 {
				return "Nothing running in the background.", nil
			}
			var sb strings.Builder
			for _, j := range jobs {
				sb.WriteString(j.Describe())
				sb.WriteByte('\n')
				for _, p := range j.Progress() {
					fmt.Fprintf(&sb, "      · %s\n", p)
				}
			}
			return strings.TrimRight(sb.String(), "\n"), nil
		},
		Affordances: func(_ context.Context) []string { return jobIDs(m) },
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "work_cancel",
			Description: "Stop a background task. Use when the user says to stop something, or " +
				"when it has plainly gone wrong.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"id":  {Type: "string", Description: "The job id from work_list, e.g. 'job2'."},
				"all": {Type: "boolean", Description: "Stop everything running in the background."},
			}),
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			if argBool(args, "all") {
				if n := m.CancelAll(); n > 0 {
					return fmt.Sprintf("Stopped %d background task(s).", n), nil
				}
				return "Nothing was running.", nil
			}
			id := strings.TrimSpace(argString(args, "id"))
			if id == "" {
				return "", withOptions(fmt.Errorf("which one? give an id, or set all"), jobIDs(m))
			}
			if !m.Cancel(id) {
				return "", withOptions(fmt.Errorf("%s is not running", id), jobIDs(m))
			}
			return fmt.Sprintf("Stopped %s.", id), nil
		},
		Affordances: func(_ context.Context) []string { return jobIDs(m) },
	})
}

// jobIDs lists what is actually there, so a wrong id comes back with the right
// ones rather than a bare refusal.
func jobIDs(m *work.Manager) []string {
	var out []string
	for _, j := range m.List() {
		if !j.State().Finished() {
			out = append(out, fmt.Sprintf("%s — %s", j.ID, j.Goal))
		}
	}
	return out
}
