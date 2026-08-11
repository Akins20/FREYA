//go:build !unix

package browser

import "syscall"

// detachedAttr is a no-op where there is no POSIX session to leave.
//
// On Windows the equivalent is CREATE_NEW_PROCESS_GROUP plus DETACHED_PROCESS,
// which needs golang.org/x/sys/windows constants this project does not carry.
// Returning nil launches the browser attached instead, so it dies with the
// process rather than surviving the turn — worse, and not wrong. Whoever
// implements the Windows side should fix this alongside the terminal, since both
// are the same question about process lifetime.
func detachedAttr() *syscall.SysProcAttr { return nil }
