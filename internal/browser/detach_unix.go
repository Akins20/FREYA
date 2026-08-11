//go:build unix

package browser

import "syscall"

// detachedAttr puts a launched browser in its own session, so it survives the
// process that started it. Without this the browser dies with the turn that
// opened it, which defeats the point of a persistent browsing context.
//
// Setsid is POSIX, so this covers Linux and macOS alike. It used to be tagged
// linux only, which was the single thing standing between this package and a
// macOS build.
func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
