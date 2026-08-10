package skills

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/llm"
)

// shellTimeout bounds any single command.
const shellTimeout = 120 * time.Second

// maxShellOutput caps what is fed back to the model. A command that produces
// megabytes of output would otherwise blow the context budget in one turn.
const maxShellOutput = 20000

// RegisterShell gives Freya the ability to run commands, every one of them
// passing through guard first.
//
// The skill deliberately exposes two entry points rather than one. `run_command`
// takes an argv array and executes it directly, with no shell involved, so
// arguments cannot chain further commands. `run_shell` accepts a pipeline and
// is assessed more harshly precisely because it can. Making the safe path also
// the convenient path is most of the battle.
func RegisterShell(r *Registry, g *guard.Guard) {
	// change_dir is registered unconditionally: moving her own process between
	// folders touches nothing outside it and reaches no external service, so it
	// never needs the guard and must stay available even in offline/mock setups
	// where no guard is wired.
	registerChangeDir(r)

	if g == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "run_command",
			Description: "Run a program directly, without a shell. Prefer this over " +
				"run_shell: arguments are passed literally, so nothing can be chained " +
				"or injected. Destructive commands will ask the user first.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"command": {Type: "string", Description: "Program to run, e.g. 'ls', 'git', 'go'."},
				"args": {Type: "string", Description: "Arguments, space separated. " +
					"Quote anything containing spaces."},
				"dir":    {Type: "string", Description: "Working directory. Defaults to home."},
				"reason": {Type: "string", Description: "Why this is needed — shown to the user when confirming."},
			}, "command"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			command := argString(args, "command")
			if command == "" {
				return "", fmt.Errorf("command is required")
			}
			argv := splitArgs(argString(args, "args"))
			// Shell syntax handed to a program that is not a shell.
			if why := shellOnly(argv); why != "" {
				return "", fmt.Errorf("%s", why)
			}
			dir := resolveDir(ctx, argString(args, "dir"))

			action := guard.Action{
				Kind:     guard.KindExec,
				Command:  command,
				Args:     argv,
				Paths:    pathLikeArgs(argv),
				Elevated: command == "sudo" || command == "pkexec" || command == "doas",
				Reason:   argString(args, "reason"),
			}

			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				return runProcess(ctx, dir, command, argv...)
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "run_shell",
			Description: "Run a shell pipeline, for when you genuinely need pipes, " +
				"redirection or globbing. Treated as higher risk than run_command " +
				"because a shell string can chain commands. Use run_command when you can.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"script": {Type: "string", Description: "The shell command line."},
				"dir":    {Type: "string", Description: "Working directory. Defaults to home."},
				"reason": {Type: "string", Description: "Why this is needed — shown to the user when confirming."},
			}, "script"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			script := argString(args, "script")
			if script == "" {
				return "", fmt.Errorf("script is required")
			}
			dir := resolveDir(ctx, argString(args, "dir"))

			action := guard.Action{
				Kind:     guard.KindExec,
				Shell:    script,
				Paths:    pathLikeArgs(strings.Fields(script)),
				Elevated: strings.Contains(script, "sudo ") || strings.HasPrefix(script, "sudo"),
				Reason:   argString(args, "reason"),
			}

			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				return runProcess(ctx, dir, "bash", "-c", script)
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "audit_recent",
			Description: "Show recent actions taken on the machine and their outcomes, " +
				"from the tamper-resistant audit log.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"limit": {Type: "number", Description: "How many entries, default 15."},
			}),
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			if g.Audit == nil {
				return "No audit log is configured.", nil
			}
			records, err := g.Audit.Recent(clamp(argInt(args, "limit", 15), 1, 100))
			if err != nil {
				return "", err
			}
			// A log that could not be written looks exactly like a quiet day, so
			// say when it is incomplete rather than letting an empty answer imply
			// nothing happened.
			gap := ""
			if n := g.Audit.Dropped(); n > 0 {
				gap = fmt.Sprintf("\n\n[%d action(s) could not be written to the audit log "+
					"this session — it is incomplete. Usually a full disk.]", n)
			}
			if len(records) == 0 {
				return "Nothing recorded yet." + gap, nil
			}
			var sb strings.Builder
			for _, rec := range records {
				sb.WriteString(rec.Summary())
				sb.WriteString("\n")
			}
			return strings.TrimSpace(sb.String()) + gap, nil
		},
	})
}

// registerChangeDir gives Freya an explicit way to move her working directory
// mid-task. Every file and shell tool resolves against the scope's directory, so
// moving the scope relocates her whole toolset at once — read, write and run all
// follow. She is told where she is at the top of every turn, so this is the only
// lever she needs to fan out from her base into a per-task subfolder and back.
//
// It moves the scope rather than the process. os.Chdir would drag every other
// thread of work along with it, which is precisely why the process directory
// could not survive her doing two things at once.
//
// It creates the target if it doesn't exist yet, on purpose. "Make a folder and
// move into it" is one thought for a task-driven agent, and models issue it as
// one thought — often batching change_dir and a separate create in a single
// turn, where the create hasn't run when the move does. Erroring there stalled
// the fan-out while the reply still claimed success. Creating-on-enter serves
// the real intent; the reply names whether the folder was made or merely
// entered, so a typo lands as a spoken "created …/reprots" rather than a
// silent wrong turn.
func registerChangeDir(r *Registry) {
	r.Register(Skill{
		Tool: llm.Tool{
			Name: "change_dir",
			Description: "Change your working directory — the folder your file and " +
				"shell tools act in. Everything you read, write or run happens there " +
				"until you move again, and your current directory is shown to you at " +
				"the start of every turn. If the folder doesn't exist yet it is created " +
				"for you (with any missing parents), so this alone is how you branch " +
				"into a fresh subfolder for a task — no separate create step. Call it " +
				"with no path to just report where you are.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path": {Type: "string", Description: "Where to move to. A relative path " +
					"resolves against where you are now; ~ is home. Created if missing. " +
					"Omit to report the current directory."},
			}),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			scope := ScopeFrom(ctx)
			raw := strings.TrimSpace(argString(args, "path"))
			if raw == "" {
				return "You're in " + scope.Dir(), nil
			}
			target := resolveDir(ctx, raw)
			// Distinguish making a folder from stepping into an existing one, so
			// the spoken reply surfaces a mistyped destination instead of hiding it.
			created := false
			if info, err := os.Stat(target); err != nil {
				if !os.IsNotExist(err) {
					return "", fmt.Errorf("couldn't reach %s: %w", target, err)
				}
				if err := os.MkdirAll(target, 0o755); err != nil {
					return "", fmt.Errorf("couldn't create %s: %w", target, err)
				}
				created = true
			} else if !info.IsDir() {
				return "", fmt.Errorf("%s is a file, not a folder", target)
			}
			// Move this thread of work, not the process. os.Chdir would drag every
			// other thread along with it — the reason the process directory could
			// not survive concurrency.
			scope.SetDir(target)
			if created {
				return "Created " + target + " and moved in", nil
			}
			return "Working directory is now " + target, nil
		},
	})
}

// runProcess executes a command and returns its combined output, truncated.
func runProcess(ctx context.Context, dir, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, shellTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()

	text := strings.TrimSpace(string(out))
	if len(text) > maxShellOutput {
		text = text[:maxShellOutput] + fmt.Sprintf("\n[output truncated at %d characters]", maxShellOutput)
	}

	if ctx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("timed out after %s", shellTimeout)
	}
	if err != nil {
		if text == "" {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		// Exit status plus output is more useful to the model than either alone.
		return text, fmt.Errorf("%s exited with error: %w", name, err)
	}
	if text == "" {
		return "(no output)", nil
	}
	return text, nil
}

// splitArgs performs minimal quote-aware splitting, so a single argument
// containing spaces survives intact.
func splitArgs(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	var quote rune

	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
		case r == ' ' || r == '\t':
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// pathLikeArgs picks out arguments that look like filesystem paths, so guard
// can assess what is actually being touched rather than only the verb.
func pathLikeArgs(args []string) []string {
	var out []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		// Require a path separator. A bare token like "*.go" is far more often
		// a pattern argument (find -name "*.go") than a filesystem target, and
		// treating it as one made ordinary searches look risky.
		if strings.Contains(a, "/") || strings.HasPrefix(a, "~") {
			out = append(out, a)
		}
	}
	return out
}

// resolveDir expands a working directory, defaulting to the caller's scope.
//
// The default is the scope's directory, not home, and that is a cure rather than
// a preference. File tools write relative to it and shell tools default to it, so
// a file written by file_write and a command run by run_shell land in the same
// place. When they disagreed — shell defaulting to home — any task that created a
// file and then acted on it broke silently: the file was here, the shell was over
// there. Reading it from the scope rather than the process is what lets two
// threads of work each have their own, without either moving the other.
func resolveDir(ctx context.Context, dir string) string {
	if dir == "" {
		return ScopeFrom(ctx).Dir()
	}
	if strings.HasPrefix(dir, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(dir, "~"))
		}
	}
	return expandIn(ctx, dir)
}

// shellOnly reports why these arguments cannot work without a shell.
//
// # The failure it makes loud
//
// run_command execs the program directly — deliberately, so nothing in an
// argument can chain a second command. The cost of that is invisible: a
// redirection, a pipe or a variable handed to it is not an instruction, it is
// just another argument. `run_command(command="echo", args="hi > out.txt")`
// runs echo with three arguments, prints "hi > out.txt", exits 0, and creates no
// file. Every signal available says it worked. The shell playbook has said
// "ALWAYS VERIFY THE RESULT, NOT THE EXIT" about exactly this for weeks, which is
// prose, and prose is not a control.
//
// # Why this and not a generic "did anything change" check
//
// The rest of the codebase answers "did that do anything?" with a before/after
// fingerprint. That does not transfer here: most commands legitimately change
// nothing observable — they print. A fingerprint would either be silent about
// the real bug or confidently wrong about every read-only command, and a
// verifier that answers confidently about the wrong thing is worse than none.
//
// This instead names the one case where the tool cannot possibly do what was
// asked, and says which tool can. Deterministic, and no false positive is
// possible: none of these tokens means anything to a program invoked directly.
func shellOnly(argv []string) string {
	// Standalone operators. A program receiving ">" as an argument is never what
	// anybody meant; quoting makes these part of a larger token, so a lone one is
	// unambiguous.
	operators := map[string]string{
		">": "redirect output", ">>": "append output", "<": "read input",
		"|": "pipe", "||": "chain on failure", "&&": "chain on success",
		";": "run several commands", "&": "run in the background",
		"2>": "redirect errors", "2>&1": "merge errors into output",
	}
	for _, a := range argv {
		if what, ok := operators[a]; ok {
			return fmt.Sprintf("%q is shell syntax, and run_command does not use a shell — "+
				"it would be passed to the program as a literal argument, so the command "+
				"would appear to succeed and simply not %s.\n\nUse run_shell for this, "+
				"with the whole line as the script.", a, what)
		}
		switch {
		case strings.HasPrefix(a, "$(") || strings.Contains(a, "`"):
			return fmt.Sprintf("%q substitutes a command, which needs a shell. Without one "+
				"it is passed through literally and the program sees the text rather than "+
				"the result. Use run_shell.", a)
		case strings.HasPrefix(a, "$") && len(a) > 1:
			return fmt.Sprintf("%q is a shell variable and nothing expands it here — the "+
				"program would receive the characters %q. Use run_shell, or pass the value "+
				"itself.", a, a)
		case strings.HasPrefix(a, "~"):
			return fmt.Sprintf("%q relies on the shell expanding ~, which does not happen "+
				"here — the program would look for a directory literally named %q. Write "+
				"the full path, or use run_shell.", a, a)
		}
	}
	return ""
}
