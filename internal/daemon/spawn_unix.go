//go:build unix

package daemon

import "syscall"

// detachedAttr puts the daemon in its own session, so it outlives the shell that
// started it. Without this, closing the terminal you typed `freya -gui` into
// takes her with it — which is the opposite of what a daemon is for.
//
// Setsid is POSIX, so this covers Linux and macOS alike. The same split as
// internal/browser/detach_unix.go, for the same reason.
func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
