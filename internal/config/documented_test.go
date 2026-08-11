package config

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// A setting nothing describes is a setting nobody can use.
//
// Six of these existed at once, found by cross-checking os.Getenv against the
// configuration table. One of them, FREYA_SAFETY, decides whether Gemini's
// content filter runs at all and defaults to OFF — so the single most consequential
// switch in the process was invisible to the person who owns the machine.
//
// This is the configuration form of the bug this codebase keeps hitting: the
// capability exists, works, and is never surfaced, so nothing fails and nobody
// can tell. It has now cost a browser sign-in tool, a chart engine, a page-state
// flag, three core-kit entries, and this.
func TestEverySettingTheCodeReadsIsDocumented(t *testing.T) {
	root := repoRoot(t)

	read := map[string]bool{}
	for _, dir := range []string{"internal", "cmd"} {
		walk(t, filepath.Join(root, dir), func(path, src string) {
			if strings.HasSuffix(path, "_test.go") {
				return
			}
			for _, m := range regexp.MustCompile(`os\.Getenv\("(\w+)"\)`).FindAllStringSubmatch(src, -1) {
				if strings.HasPrefix(m[1], "FREYA_") {
					read[m[1]] = true
				}
			}
		})
	}
	if len(read) == 0 {
		t.Fatal("found no FREYA_ settings at all — this test has stopped looking")
	}

	docs, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}

	var missing []string
	for name := range read {
		if !strings.Contains(string(docs), name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d setting(s) the code reads are described nowhere: %s\n"+
			"Nothing fails when a setting is undocumented — it simply cannot be used, "+
			"and the person who owns the machine cannot find out it exists.",
			len(missing), strings.Join(missing, ", "))
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this file")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

func walk(t *testing.T, dir string, fn func(path, src string)) {
	t.Helper()
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		fn(p, string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A real environment variable beats .env, which is the rule the whole file is
// documented on and which nothing exercised.
//
// The direction matters. .env is a convenience checked into nobody's repo and
// edited rarely; an exported variable is what someone typed on purpose a second
// ago, often to override exactly this. Getting it backwards means a stale line in
// a file silently wins over a deliberate instruction, which is unfalsifiable from
// the outside because both produce a running assistant.
func TestARealEnvironmentVariableBeatsDotEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"),
		[]byte("FREYA_TEST_PRECEDENCE=from-the-file\nFREYA_TEST_ONLYFILE=from-the-file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("FREYA_TEST_PRECEDENCE", "from-the-environment")

	loadDotEnv(".env")

	if got := os.Getenv("FREYA_TEST_PRECEDENCE"); got != "from-the-environment" {
		t.Errorf("the file overwrote a real variable: %q", got)
	}
	if got := os.Getenv("FREYA_TEST_ONLYFILE"); got != "from-the-file" {
		t.Errorf("a variable set only in the file did not arrive: %q", got)
	}
}

// The shapes a .env actually contains. Each of these has a way of going wrong
// that produces a working program with the wrong setting, which is the only kind
// of config bug that survives.
func TestDotEnvReadsTheShapesPeopleWrite(t *testing.T) {
	dir := t.TempDir()
	body := "# a comment\n" +
		"\n" +
		"  FREYA_TEST_SPACED  =  padded  \n" +
		"FREYA_TEST_QUOTED=\"quoted value\"\n" +
		"FREYA_TEST_SINGLE='single quoted'\n" +
		"FREYA_TEST_EQUALS=a=b=c\n" +
		"FREYA_TEST_EMPTY=\n" +
		"not a pair at all\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	for _, k := range []string{"FREYA_TEST_SPACED", "FREYA_TEST_QUOTED",
		"FREYA_TEST_SINGLE", "FREYA_TEST_EQUALS", "FREYA_TEST_EMPTY"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}

	loadDotEnv(".env")

	for _, c := range []struct{ key, want string }{
		{"FREYA_TEST_SPACED", "padded"},
		{"FREYA_TEST_QUOTED", "quoted value"},
		{"FREYA_TEST_SINGLE", "single quoted"},
		// Only the first separator splits, or a value containing one is truncated.
		{"FREYA_TEST_EQUALS", "a=b=c"},
		{"FREYA_TEST_EMPTY", ""},
	} {
		if got := os.Getenv(c.key); got != c.want {
			t.Errorf("%s is %q, want %q", c.key, got, c.want)
		}
	}
}

// A missing .env is the normal case, not an error.
func TestNoDotEnvIsFine(t *testing.T) {
	t.Chdir(t.TempDir())
	loadDotEnv(".env") // must not panic
}
