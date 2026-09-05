//go:build !unix

package daemon

import "syscall"

// detachedAttr is a no-op where there is no POSIX session to leave. The daemon
// then dies with the shell that spawned it, which is worse and not wrong; see
// internal/browser/detach_other.go for the same note.
func detachedAttr() *syscall.SysProcAttr { return nil }
