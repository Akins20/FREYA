package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/akins/jarvis/internal/guard"
)

// The silent one. run_command execs directly, so a redirection is not an
// instruction — it is another argument. `echo hi > out.txt` prints "hi > out.txt",
// exits 0, and writes no file. Every signal available says it worked, which is
// why the shell playbook has carried "VERIFY THE RESULT, NOT THE EXIT" for weeks:
// prose, doing a control's job.
func TestShellSyntaxWithoutAShellIsRefused(t *testing.T) {
	cases := []struct {
		args string
		says string
	}{
		{"hi > out.txt", "redirect output"},
		{"log.txt >> all.txt", "append output"},
		{"a | wc -l", "pipe"},
		{"one && two", "chain on success"},
		{"one ; two", "run several commands"},
		{"out 2>&1", "merge errors"},
		{"$HOME/notes", "nothing expands it"},
		{"~/Documents", "expanding ~"},
		{"$(date)", "substitutes a command"},
	}
	for _, c := range cases {
		why := shellOnly(splitArgs(c.args))
		if why == "" {
			t.Errorf("%q was accepted — it would exit 0 and quietly do nothing", c.args)
			continue
		}
		if !strings.Contains(why, c.says) {
			t.Errorf("%q: the refusal does not explain the failure (%q): %s", c.args, c.says, why)
		}
		if !strings.Contains(why, "run_shell") {
			t.Errorf("%q: the refusal does not say which tool to use instead: %s", c.args, why)
		}
	}
}

// And ordinary arguments must be untouched. A check that fires on real work is
// worse than the bug: this tool is the preferred one, and making it flaky would
// push her onto the shell for everything.
func TestOrdinaryArgumentsAreNotRefused(t *testing.T) {
	for _, args := range []string{
		"-la /tmp", "status --short", "commit -m 'fixed the thing'",
		"-name *.go", "--flag=value", "run ./cmd/freya",
		"grep a>b file", // part of a larger token, not a standalone operator
		"", "install -y package",
	} {
		if why := shellOnly(splitArgs(args)); why != "" {
			t.Errorf("ordinary arguments %q were refused: %s", args, why)
		}
	}
}

// End to end, through the registry, because a helper nobody calls is not a fix.
func TestRunCommandRefusesARedirection(t *testing.T) {
	r := New()
	g := guard.New(func(context.Context, guard.Action, guard.Assessment) bool { return true }, nil)
	RegisterShell(r, g)

	_, err := r.Execute(context.Background(), "run_command",
		map[string]any{"command": "echo", "args": "hello > /tmp/should-not-exist"})
	if err == nil {
		t.Fatal("run_command accepted a redirection and would have reported success")
	}
	if !strings.Contains(err.Error(), "run_shell") {
		t.Errorf("the refusal does not point at the tool that works: %v", err)
	}
}

// run_shell genuinely runs a shell, so the same line must be fine there.
func TestRunShellStillTakesShellSyntax(t *testing.T) {
	if why := shellOnly(splitArgs("hi > out.txt")); why == "" {
		t.Skip("nothing to contrast")
	}
	// run_shell takes a single `script` string and never goes through shellOnly;
	// this pins that the check is scoped to the tool that cannot handle it.
	r := New()
	g := guard.New(func(context.Context, guard.Action, guard.Assessment) bool { return true }, nil)
	RegisterShell(r, g)
	out, err := r.Execute(context.Background(), "run_shell",
		map[string]any{"script": "echo piped | wc -l"})
	if err != nil {
		t.Fatalf("run_shell refused a pipeline, which is its whole purpose: %v", err)
	}
	if strings.TrimSpace(out) != "1" {
		t.Errorf("the pipeline did not actually run: %q", out)
	}
}
