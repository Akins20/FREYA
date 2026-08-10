package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/Akins20/FREYA/internal/guard"
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

// Walking the whole disk for a file that is in the folder she is standing in.
//
// Measured. Asked to read sales.csv she ran `find / -name "sales.csv"` from a
// directory the file was in. On this machine that crosses a near-full NTFS mount
// and takes minutes, and in that run it needed a confirmation nobody could give,
// so it was cancelled and three more rounds went by before she read the file.
func TestAWholeDiskWalkIsRefused(t *testing.T) {
	r := New()
	RegisterShell(r, approveAll())

	refused := []map[string]any{
		{"script": `find / -name "sales.csv"`},
		{"script": `find / -name sales.csv 2>/dev/null`},
		{"script": "grep -rn needle /"},
		{"command": "find", "args": "/ -name sales.csv"},
		{"script": "ls -R /"},
	}

	for _, args := range refused {
		tool := "run_shell"
		if _, ok := args["command"]; ok {
			tool = "run_command"
		}
		_, err := r.Execute(context.Background(), tool, args)
		if err == nil {
			t.Errorf("%v was allowed to walk the whole disk", args)
			continue
		}
		if !strings.Contains(err.Error(), "entire filesystem") {
			t.Errorf("%v was refused for the wrong reason: %v", args, err)
		}
	}
}

// The half that matters more. This rule is wrong if it stops ordinary work, and
// what she does when a rule is wrong is obey it with something slower.
func TestOrdinarySearchesAreLeftAlone(t *testing.T) {
	r := New()
	RegisterShell(r, approveAll())

	allowed := []string{
		`find . -name "*.go"`,
		`find /etc -name "hosts"`,
		"find /home/me/project -name go.mod",
		// A pattern carrying a directory is looking for a location, and the
		// working folder cannot answer it.
		`find / -name "*/site-packages"`,
		"grep -rn needle ./src",
		"ls -la /",
		"ls /",
		"which chromium",
		"git status",
	}
	for _, script := range allowed {
		_, err := r.Execute(context.Background(), "run_shell", map[string]any{"script": script})
		if err != nil && strings.Contains(err.Error(), "entire filesystem") {
			t.Errorf("%q was refused as a disk walk", script)
		}
	}
}

// An empty or malformed call is the handler's to report, with its own better
// message. A guard must not turn one failure into a different one.
func TestTheWalkGuardStaysQuietOnNonsense(t *testing.T) {
	for _, args := range []map[string]any{
		{},
		{"script": ""},
		{"script": "find"},
		{"command": "find"},
	} {
		if err := refuseWholeDiskWalk(context.Background(), args); err != nil {
			t.Errorf("%v was refused by the walk guard: %v", args, err)
		}
	}
}
