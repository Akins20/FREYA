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
