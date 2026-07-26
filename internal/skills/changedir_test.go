package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// change_dir is the one lever Freya has to move her whole toolset — file and
// shell tools both resolve against the scope — so its behaviours each get a
// named test: creating-and-entering a fresh subfolder in one call, reporting
// position without a path, refusing to treat a file as a folder, and, the reason
// scopes exist at all, moving one thread of work without moving another.

// scopedIn builds a registry and a scope rooted at a fresh temp directory.
func scopedIn(t *testing.T) (*Registry, Scope, string) {
	t.Helper()
	base := t.TempDir()
	// Temp dirs can be symlinks (/tmp -> /private/tmp); resolve so comparisons
	// are against the same canonical form the code will produce.
	if real, err := filepath.EvalSymlinks(base); err == nil {
		base = real
	}
	r := New()
	registerChangeDir(r)
	return r, NewScope(NewWorkspace(base), "", ""), base
}

func TestChangeDirCreatesAndEnters(t *testing.T) {
	r, scope, base := scopedIn(t)
	ctx := WithScope(context.Background(), scope)

	// Moving into a not-yet-existing nested path should make it and enter it in
	// one step — the "fan out into a new task folder" path.
	out, err := r.Execute(ctx, "change_dir", map[string]any{"path": "task-42/output"})
	if err != nil {
		t.Fatalf("change_dir: %v", err)
	}
	want := filepath.Join(base, "task-42", "output")
	if got := scope.Dir(); got != want {
		t.Fatalf("scope dir = %q, want %q", got, want)
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
	r, scope, base := scopedIn(t)
	ctx := WithScope(context.Background(), scope)

	out, err := r.Execute(ctx, "change_dir", map[string]any{})
	if err != nil {
		t.Fatalf("change_dir report: %v", err)
	}
	if !strings.Contains(out, base) {
		t.Fatalf("empty-path call should report %q, got %q", base, out)
	}
	if got := scope.Dir(); got != base {
		t.Fatalf("reporting position moved the scope to %q", got)
	}
}

func TestChangeDirRejectsAFile(t *testing.T) {
	r, scope, base := scopedIn(t)
	ctx := WithScope(context.Background(), scope)

	if err := os.WriteFile(filepath.Join(base, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := r.Execute(ctx, "change_dir", map[string]any{"path": "notes.txt"}); err == nil {
		t.Fatal("moving into a regular file should error, not create over it")
	}
	if got := scope.Dir(); got != base {
		t.Fatalf("a rejected move still changed the scope to %q", got)
	}
}

// The reason the process working directory had to go: two threads of work must
// be able to sit in two directories, and neither may move the other.
func TestChangeDirMovesOnlyItsOwnScope(t *testing.T) {
	r := New()
	registerChangeDir(r)

	baseA, baseB := t.TempDir(), t.TempDir()
	if real, err := filepath.EvalSymlinks(baseA); err == nil {
		baseA = real
	}
	if real, err := filepath.EvalSymlinks(baseB); err == nil {
		baseB = real
	}
	scopeA := NewScope(NewWorkspace(baseA), "", "job-a")
	scopeB := NewScope(NewWorkspace(baseB), "", "job-b")

	procBefore, _ := os.Getwd()

	if _, err := r.Execute(WithScope(context.Background(), scopeA), "change_dir",
		map[string]any{"path": "only-a"}); err != nil {
		t.Fatal(err)
	}

	if got, want := scopeA.Dir(), filepath.Join(baseA, "only-a"); got != want {
		t.Fatalf("scope A did not move: %q, want %q", got, want)
	}
	if got := scopeB.Dir(); got != baseB {
		t.Fatalf("moving scope A dragged scope B to %q — the whole point of scopes is that it cannot", got)
	}
	// And the process itself stays put, so nothing outside either thread of work
	// is affected by one of them fanning out into a subfolder.
	if procAfter, _ := os.Getwd(); procAfter != procBefore {
		t.Fatalf("change_dir moved the process from %q to %q", procBefore, procAfter)
	}
}

// Relative paths in file and shell tools must resolve against the scope, which
// is what keeps a file she writes and a command she runs in the same place.
func TestExpandResolvesAgainstScope(t *testing.T) {
	base := t.TempDir()
	if real, err := filepath.EvalSymlinks(base); err == nil {
		base = real
	}
	ctx := WithScope(context.Background(), NewScope(NewWorkspace(base), "", ""))

	if got, want := expandIn(ctx, "notes.txt"), filepath.Join(base, "notes.txt"); got != want {
		t.Errorf("relative path resolved to %q, want %q", got, want)
	}
	// Absolute paths and ~ are left alone.
	if got := expandIn(ctx, "/etc/hosts"); got != "/etc/hosts" {
		t.Errorf("absolute path was rewritten to %q", got)
	}
	if got := expandIn(ctx, ""); got != "" {
		t.Errorf("empty path became %q", got)
	}
	home, _ := os.UserHomeDir()
	if got := expandIn(ctx, "~/x"); got != filepath.Join(home, "x") {
		t.Errorf("~ expanded to %q", got)
	}
}
