package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Spawn starts a daemon for this data directory and waits for it to answer.
//
// # Why the launcher starts one rather than becoming one
//
// The window is served by whoever owns the archive. If `freya -gui` built its
// own agent it would trip the handover at startup and take the store off a
// daemon that was already running — two writers on one append-only file, which
// internal/memory/journal.go spends a package comment explaining corrupts both
// the transcript and the cached prefix. So the launcher never becomes her; when
// nobody is home it starts the real thing and waits.
//
// Racing two spawns is harmless: Daemon.Run refuses to start when the socket is
// already answering, so the loser exits and the winner is found by the poll.
func Spawn(ctx context.Context, dataDir string, wait time.Duration) error {
	if Running(dataDir) {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find this program to start it again: %w", err)
	}

	// Her journal is the daemon's only trace when systemd did not start her —
	// the watcher lines, the job reports, what she said unprompted. Sending it
	// to a file rather than discarding it is the difference between a daemon you
	// can debug and one that is simply silent.
	logPath := filepath.Join(dataDir, "daemon.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", logPath, err)
	}
	defer log.Close()

	cmd := exec.Command(exe, "-daemon")
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Stdin = nil
	cmd.SysProcAttr = detachedAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the daemon: %w", err)
	}
	// Released rather than waited on: it is meant to outlive this process.
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release the daemon: %w", err)
	}

	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if Running(dataDir) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return fmt.Errorf("started a daemon but it did not answer within %s — see %s",
		wait, logPath)
}
