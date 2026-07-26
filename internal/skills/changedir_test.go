package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// change_dir is the one lever Freya has to move her whole toolset — file and
// shell tools both follow the process CWD — so its non-obvious behaviours each
// get a named test: creating-and-entering a fresh subfolder in one call,
// reporting position without a path, and refusing to treat a file as a folder.

// withCWD runs fn from a scratch directory and restores the original CWD after,
// so a test that changes directory can't leak into the rest of the suite.
func withCWD(t *testing.T) string {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	base := t.TempDir()
	// macOS/Linux temp dirs can be symlinks (/tmp -> /private/tmp); resolve so
	// the assertion compares the same canonical form os.Getwd reports.
	if real, err := filepath.EvalSymlinks(base); err == nil {
		base = real
	}
	if err := os.Chdir(base); err != nil {
		t.Fatalf("chdir base: %v", err)
	}
	return base
}

func TestChangeDirCreatesAndEnters(t *testing.T) {
	base := withCWD(t)
	r := New()
	registerChangeDir(r)

	// Moving into a not-yet-existing nested path should make it and enter it in
	// one step — this is the "fan out into a new task folder" path, and it must
	// work even though nothing created the folder first.
	out, err := r.Execute(context.Background(), "change_dir", map[string]any{
		"path": "task-42/output",
	})
	if err != nil {
		t.Fatalf("change_dir create: %v", err)
	}
	want := filepath.Join(base, "task-42", "output")
	wd, _ := os.Getwd()
	if wd != want {
		t.Fatalf("cwd = %q, want %q", wd, want)
	}
	if !strings.Contains(out, want) {
		t.Fatalf("reply %q does not name the new directory %q", out, want)
	}
	// The reply must announce that it created the folder, so a mistyped path is
	// audible rather than a silent wrong turn.
	if !strings.Contains(strings.ToLower(out), "created") {
		t.Fatalf("reply %q should say it created the folder", out)
	}
}

func TestChangeDirWithoutPathReports(t *testing.T) {
	base := withCWD(t)
	r := New()
	registerChangeDir(r)

	out, err := r.Execute(context.Background(), "change_dir", map[string]any{})
	if err != nil {
		t.Fatalf("change_dir report: %v", err)
	}
	if !strings.Contains(out, base) {
		t.Fatalf("empty-path call should report current dir %q, got %q", base, out)
	}
	// It must not have moved.
	if wd, _ := os.Getwd(); wd != base {
		t.Fatalf("reporting position moved cwd to %q", wd)
	}
}

func TestChangeDirRejectsAFile(t *testing.T) {
	base := withCWD(t)
	r := New()
	registerChangeDir(r)

	if err := os.WriteFile(filepath.Join(base, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := r.Execute(context.Background(), "change_dir", map[string]any{
		"path": "notes.txt",
	}); err == nil {
		t.Fatal("moving into a regular file should error, not create over it")
	}
	// It must not have moved.
	if wd, _ := os.Getwd(); wd != base {
		t.Fatalf("a rejected move still changed cwd to %q", wd)
	}
}
