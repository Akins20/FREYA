//go:build linux

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// isTerminal reports whether stdin is a terminal somebody could type into.
//
// # Why the obvious test is wrong
//
// This used to be `info.Mode()&os.ModeCharDevice != 0`, and /dev/null is a
// character device. So it answered "yes, there is a person there" for every
// process started with its input closed — which is every daemon: daemon.Spawn
// sets Stdin to nil and systemd's default is StandardInput=null.
//
// The consequence was not cosmetic. The confirmation router installed the
// terminal prompt, the prompt read /dev/null, got EOF immediately, and returned
// false — and false is how a person says no, so the guard reported "declined by
// user" for every destructive action in the daemon, with the microphone that
// could have asked never reached. That is precisely the failure the (asked, ok)
// split was built to prevent, reintroduced one layer up.
//
// The real test is the one isatty(3) makes: ask for the terminal attributes and
// see whether the kernel objects. Pure syscall, no cgo, no dependency.
func isTerminal() bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, os.Stdin.Fd(),
		syscall.TCGETS, uintptr(unsafe.Pointer(&t)), 0, 0, 0)
	return errno == 0
}
