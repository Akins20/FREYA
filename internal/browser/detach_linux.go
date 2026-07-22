package browser

import "syscall"

// detachedAttr puts a launched browser in its own session, so it survives the
// process that started it. Without this the browser dies with the turn that
// opened it, which defeats the point of a persistent browsing context.
func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
