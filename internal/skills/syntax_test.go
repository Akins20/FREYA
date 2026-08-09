package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func checkFile(t *testing.T, name, body string) (string, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New()
	RegisterSyntax(r)
	return r.Execute(context.Background(), "code_check", map[string]any{"path": p})
}

// Writing a file and reporting it done is half the job. A missing brace costs
// nothing to find now and is found by the user later otherwise.
func TestBrokenCodeIsCaught(t *testing.T) {
	cases := []struct{ name, body string }{
		{"broken.json", `{"a": 1,}`},
		{"broken.sh", "if [ -f x ]; then\n  echo hi\n"},
		{"broken.py", "def f(:\n  pass\n"},
	}
	for _, c := range cases {
		out, err := checkFile(t, c.name, c.body)
		if err == nil && !strings.Contains(out, "none of the checkers") {
			t.Errorf("%s: broken file reported as fine: %q", c.name, out)
		}
	}
}

// And valid code must pass, naming the checker — a pass that does not say what
// checked it is an assurance nobody can weigh.
func TestValidCodePassesAndNamesTheChecker(t *testing.T) {
	out, err := checkFile(t, "ok.json", `{"a": 1, "b": [2,3]}`)
	if err != nil {
		t.Fatalf("valid JSON was rejected: %v", err)
	}
	if !strings.Contains(out, "parses cleanly") || !strings.Contains(out, "checked with") {
		t.Errorf("the pass does not name its checker: %q", out)
	}
}

// An unknown extension must say it is unverified rather than returning a green
// tick it has not earned.
func TestAnUncheckableFileSaysSo(t *testing.T) {
	out, err := checkFile(t, "notes.xyz", "whatever")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "unverified") {
		t.Errorf("an uncheckable file did not say so: %q", out)
	}
	if strings.Contains(out, "parses cleanly") {
		t.Errorf("an unchecked file was reported as passing: %q", out)
	}
}
