package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Akins20/FREYA/internal/guard"
)

// Asked to write hello.html in her working directory, she came back with one
// tool call and nothing written. The workspace carve-out — a write inside the
// directory she was given as her own needs no confirmation, because in the
// daemon there is nobody to give one — was in place and correctly wired. It just
// never matched: FREYA_WORK_DIR was configured as ~/freya-workspace, .env stores
// that tilde as a literal character, and the guard compares absolute paths and
// refuses (rightly) to resolve anything itself. So every write in her own
// workspace assessed as a medium-risk write somewhere else, and needed approval
// that could not be obtained.
func TestATildeWorkDirStillCountsAsHerOwnWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FREYA_DATA_DIR", filepath.Join(home, "data"))
	t.Setenv("FREYA_WORK_DIR", "~/freya-workspace")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := filepath.Join(home, "freya-workspace")
	if cfg.WorkDir != want {
		t.Fatalf("WorkDir = %q, want %q", cfg.WorkDir, want)
	}

	// The part that actually failed: what the guard makes of a write there.
	g := guard.New(nil, nil)
	g.Workspace = cfg.WorkDir
	a := g.Assess(guard.Action{Kind: guard.KindWrite,
		Paths: []string{filepath.Join(cfg.WorkDir, "hello.html")}})
	if a.Confirm {
		t.Errorf("writing hello.html into her own workspace needs approval: %s", a.Describe())
	}
	if a.Risk != guard.RiskLow {
		t.Errorf("risk = %s, want low", a.Risk)
	}
}

// $HOME and a bare relative path are the same failure written differently, and
// a relative one is worse than useless: it moves with the process.
func TestEveryDirectorySettingArrivesAbsolute(t *testing.T) {
	home := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("FREYA_DATA_DIR", "$HOME/data")
	t.Setenv("FREYA_WORK_DIR", "workspace")
	t.Setenv("FREYA_PROJECTS_DIR", "~/code")
	t.Setenv("FREYA_SOURCE_DIR", "~")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, c := range []struct{ name, got, want string }{
		{"DataDir", cfg.DataDir, filepath.Join(home, "data")},
		{"WorkDir", cfg.WorkDir, filepath.Join(cwd, "workspace")},
		{"ProjectsDir", cfg.ProjectsDir, filepath.Join(home, "code")},
		{"SourceDir", cfg.SourceDir, home},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// Empty is a value, not a missing one: no fixed working directory (which is what
// the benchmark relies on) and no self-repair checkout. Expanding it into the
// process directory would silently invent both.
func TestUnsetDirectoriesStayUnset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FREYA_DATA_DIR", filepath.Join(home, "data"))
	t.Setenv("FREYA_WORK_DIR", "")
	t.Setenv("FREYA_SOURCE_DIR", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.WorkDir != "" || cfg.SourceDir != "" {
		t.Errorf("unset directories were invented: work %q, source %q", cfg.WorkDir, cfg.SourceDir)
	}
}
