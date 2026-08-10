package skills

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/llm"
	"github.com/Akins20/FREYA/internal/term"
)

// Terminal sessions: persistent, interactive, and able to outlive a turn.
//
// run_shell already exists and is the right tool for "what does df say". These
// skills exist for the cases it cannot serve: work that keeps running while the
// conversation continues, programs that prompt, and anything where the shell
// must remember what happened before — a virtualenv, a directory, an ssh
// connection, a REPL holding state.

const defaultSessionTimeout = 30 * time.Second

// RegisterTerminal adds session skills.
func RegisterTerminal(r *Registry, g *guard.Guard, m *term.Manager) {
	if g == nil || m == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "terminal_open",
			Description: "Open a persistent terminal session that survives between " +
				"messages. Use for work that takes a while, anything interactive, or " +
				"when the shell must remember state — a directory, a virtualenv, a REPL. " +
				"For a single quick command use run_shell instead.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":    {Type: "string", Description: "A name to refer to this session by."},
				"program": {Type: "string", Description: "Program to run. Defaults to the login shell. Try 'python3', 'node'."},
				"dir":     {Type: "string", Description: "Starting directory."},
			}, "name"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			name := argString(args, "name")
			program := argString(args, "program")
			dir := expandIn(ctx, argString(args, "dir"))

			action := guard.Action{
				Kind:    guard.KindExec,
				Command: orDefault(program, "shell"),
				Reason:  "open a persistent terminal session named " + name,
			}
			return g.Run(ctx, action, func(context.Context) (string, error) {
				s, err := m.Start(name, program, dir)
				if err != nil {
					return "", err
				}
				// Let the shell print its banner before reporting.
				time.Sleep(400 * time.Millisecond)
				banner := term.Clean(s.Drain())
				out := fmt.Sprintf("Session %q open.", s.Name)
				if banner != "" {
					out += "\n" + clip(banner, 500)
				}
				return out, nil
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "terminal_run",
			Description: "Run a command in an open session and wait briefly for output. " +
				"State persists, so a cd or an export affects later commands. If the " +
				"command is still working when this returns, use terminal_read to " +
				"collect the rest.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":    {Type: "string", Description: "Session name."},
				"command": {Type: "string", Description: "What to run."},
				"timeout": {Type: "number", Description: "Seconds to wait for output, default 30."},
				"dir":     {Type: "string", Description: "Directory, used only if the session must be opened."},
				"reason":  {Type: "string", Description: "Why, shown when confirming."},
			}, "name", "command"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			name := argString(args, "name")
			command := argRaw(args, "command")
			// Open on demand. Requiring terminal_open first is ceremony that
			// buys nothing: "run this in a terminal called build" is a complete
			// instruction, and failing it on a technicality just wastes a turn.
			s, ok := m.Get(name)
			// Whether this session already existed changes what the output means.
			// Opening on demand is right — "run this in a terminal called build" is a
			// complete instruction — but a MISTYPED name silently opens a pristine
			// login shell instead, so the venv, the cd and everything else set up in
			// the real session are gone, and the command's output looks perfectly
			// normal. Saying which happened is the difference between a fresh start
			// and a mystery.
			fresh := !ok
			if !ok || s.Done() {
				if ok && s.Done() {
					_ = m.Close(name)
				}
				opened, err := m.Start(name, "", expandIn(ctx, argString(args, "dir")))
				if err != nil {
					return "", fmt.Errorf("could not open session %q: %w", name, err)
				}
				time.Sleep(600 * time.Millisecond)
				opened.Drain()
				s = opened
			}
			timeout := time.Duration(clamp(argInt(args, "timeout", 30), 1, 600)) * time.Second

			// Assessed exactly like any other command: a persistent session is
			// not a way around the guard.
			action := guard.Action{
				Kind:     guard.KindExec,
				Shell:    command,
				Paths:    pathLikeArgs(strings.Fields(command)),
				Elevated: strings.Contains(command, "sudo "),
				Reason:   argString(args, "reason"),
			}
			return g.Run(ctx, action, func(context.Context) (string, error) {
				out, err := s.Run(command, timeout)
				if err != nil {
					return "", err
				}
				cleaned := term.Clean(out)
				if cleaned == "" {
					cleaned = "(no output yet — still working? use terminal_read)"
				}
				if fresh {
					cleaned += fmt.Sprintf("\n(Session %q did not exist, so this ran in a brand-new shell — "+
						"nothing another session had set up applies here. Use terminal_list to see the open ones.)", name)
				}
				return clipText(cleaned, 20000), nil
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "terminal_read",
			Description: "Collect output a session has produced since it was last read. " +
				"Use to check on long-running work without interrupting it.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":  {Type: "string", Description: "Session name."},
				"clear": {Type: "boolean", Description: "Clear the buffer after reading. Default true."},
			}, "name"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			s, ok := m.Get(argString(args, "name"))
			if !ok {
				return "", fmt.Errorf("no session named %q", argString(args, "name"))
			}

			var out string
			if _, present := args["clear"]; present && !argBool(args, "clear") {
				out = s.Read()
			} else {
				out = s.Drain()
			}

			cleaned := term.Clean(out)
			status := fmt.Sprintf("[%s, idle %s]",
				map[bool]string{true: "ended", false: "running"}[s.Done()],
				s.Idle().Round(time.Second))
			if cleaned == "" {
				return status + " no new output.", nil
			}
			return status + "\n" + clipText(cleaned, 20000), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "terminal_send",
			Description: "Send raw input to a session without waiting — answering a " +
				"prompt, or a control character. Use '\\x03' for Ctrl-C, '\\x04' for Ctrl-D.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":    {Type: "string", Description: "Session name."},
				"input":   {Type: "string", Description: "Text to send."},
				"newline": {Type: "boolean", Description: "Append a newline. Default true; set false for control characters."},
			}, "name", "input"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			s, ok := m.Get(argString(args, "name"))
			if !ok {
				return "", fmt.Errorf("no session named %q", argString(args, "name"))
			}
			input := argRaw(args, "input")
			input = strings.NewReplacer(
				`\x03`, "\x03", `\x04`, "\x04", `\x1b`, "\x1b", `\t`, "\t", `\n`, "\n",
			).Replace(input)

			// Sending input is synthetic typing into a live process: it can
			// answer a destructive prompt, so it is never silent.
			action := guard.Action{
				Kind:    guard.KindInput,
				Command: "terminal input",
				Args:    []string{clip(input, 80)},
				Reason:  fmt.Sprintf("send input to session %q", s.Name),
			}
			return g.Run(ctx, action, func(context.Context) (string, error) {
				var err error
				if _, present := args["newline"]; present && !argBool(args, "newline") {
					err = s.SendRaw(input)
				} else {
					err = s.Send(input)
				}
				if err != nil {
					return "", err
				}
				time.Sleep(500 * time.Millisecond)
				if out := term.Clean(s.Drain()); out != "" {
					return clipText(out, 8000), nil
				}
				return "Sent.", nil
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "terminal_list",
			Description: "List open terminal sessions and whether they are still running.",
			Params:      llm.ObjectSchema(nil),
		},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			sessions := m.List()
			if len(sessions) == 0 {
				return "No terminal sessions open.", nil
			}
			var sb strings.Builder
			for _, s := range sessions {
				sb.WriteString("- " + s.Describe() + "\n")
			}
			return strings.TrimSpace(sb.String()), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "terminal_close",
			Description: "End a terminal session and everything running in it.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "Session name."},
			}, "name"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			name := argString(args, "name")
			action := guard.Action{
				Kind:    guard.KindSystem,
				Command: "close session " + name,
				Reason:  "terminate the session and its child processes",
			}
			return g.Run(ctx, action, func(context.Context) (string, error) {
				if err := m.Close(name); err != nil {
					return "", err
				}
				return fmt.Sprintf("Session %q closed.", name), nil
			})
		},
	})
}

func orDefault(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
