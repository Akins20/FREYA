package daemon

import (
	"os"
	"strings"
	"testing"
)

// TestSocketPathSurvivesLongDataDirs is a regression test. A Unix socket path
// over 108 bytes fails to bind with a bare "invalid argument" that mentions
// nothing about length, and a data directory nested a few levels deep reaches
// that easily.
func TestSocketPathSurvivesLongDataDirs(t *testing.T) {
	long := "/tmp/" + strings.Repeat("deeply-nested-directory/", 8) + "freya-data"
	t.Setenv("XDG_RUNTIME_DIR", "")

	p := SocketPath(long)
	if len(p) > maxSocketPath {
		t.Errorf("socket path is %d bytes, over the %d limit: %s", len(p), maxSocketPath, p)
	}
	if !strings.HasSuffix(p, ".sock") {
		t.Errorf("not a socket path: %s", p)
	}
}

func TestSocketPathPrefersRuntimeDir(t *testing.T) {
	// A socket is runtime state, not user data, and belongs where the session
	// clears it rather than beside the memory archive.
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	p := SocketPath("/home/someone/.local/share/freya")
	if !strings.HasPrefix(p, "/run/user/1000") {
		t.Errorf("did not use the runtime directory: %s", p)
	}
}

func TestSocketPathIsStableAndDistinct(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	a1 := SocketPath("/home/a/.local/share/freya")
	a2 := SocketPath("/home/a/.local/share/freya")
	b := SocketPath("/home/b/.local/share/freya")

	if a1 != a2 {
		t.Error("socket path is not stable for the same data directory")
	}
	if a1 == b {
		t.Error("separate data directories share a socket; two instances would collide")
	}
}

func TestRunningIsFalseWithoutADaemon(t *testing.T) {
	// Tested by connecting, not by looking for the file: a crash leaves the
	// socket behind and a stale one is indistinguishable on disk.
	dir := t.TempDir()
	if Running(dir) {
		t.Error("reported a daemon that is not there")
	}
	stale := SocketPath(dir)
	if err := os.WriteFile(stale, nil, 0o600); err == nil {
		if Running(dir) {
			t.Error("a leftover socket file was mistaken for a running daemon")
		}
	}
}
