package daemon

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Akins20/FREYA/internal/sentinel"
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

// A delivery counter that counts attempts rather than deliveries.
//
// Measured on a box with no notify-send: three observations raised, none
// delivered anywhere, and the status line said "3 notifications sent". The
// counter incremented at the top of deliver, before anything was tried, and
// notify() returns silently when notify-send is absent. Nothing was journalled
// either, so there was nothing to check the count against.
func TestUndeliveredObservationsAreNotCountedAsSent(t *testing.T) {
	d := New(t.TempDir(), sentinel.New(sentinel.ChattyBalanced, nil))
	// Quiet stands in for a machine with no desktop notifier, and Speak is nil,
	// so this observation has nowhere at all to go.
	d.Quiet = true

	d.deliver(sentinel.Observation{Summary: "disk is nearly full", Urgency: sentinel.UrgencyImportant})

	if d.notified != 0 {
		t.Errorf("counted %d delivered with no channel to deliver on", d.notified)
	}
	if d.undelivered != 1 {
		t.Errorf("undelivered is %d, want 1", d.undelivered)
	}
}

// And the status has to say so, because an engine that notices things and cannot
// tell anyone looks exactly like one with nothing to report.
func TestStatusSaysWhenNothingCouldBeDelivered(t *testing.T) {
	s := &Status{PID: 1, Started: time.Now(), Chatty: "balanced", Notified: 0, Undelivered: 3}
	got := s.Describe()
	if !strings.Contains(got, "NOT DELIVERED") {
		t.Errorf("status hides that nothing arrived: %s", got)
	}

	quiet := &Status{PID: 1, Started: time.Now(), Chatty: "balanced", Notified: 2}
	if strings.Contains(quiet.Describe(), "NOT DELIVERED") {
		t.Errorf("a healthy daemon reported a delivery problem: %s", quiet.Describe())
	}
}

// Speech stays reserved for what is worth interrupting for. Observations are
// meant to arrive as toasts; her talking unprompted is a separate path.
func TestOnlyCriticalObservationsAreSpoken(t *testing.T) {
	var said []string
	d := New(t.TempDir(), sentinel.New(sentinel.ChattyCompanion, nil))
	d.Quiet = true
	d.Speak = func(s string) { said = append(said, s) }

	d.deliver(sentinel.Observation{Summary: "a repo has uncommitted work", Urgency: sentinel.UrgencyNotable})
	d.deliver(sentinel.Observation{Summary: "something is due in 10 minutes", Urgency: sentinel.UrgencyCritical})

	if len(said) != 1 || said[0] != "something is due in 10 minutes" {
		t.Errorf("spoke %v, want only the critical one", said)
	}
}
