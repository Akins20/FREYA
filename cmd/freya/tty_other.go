//go:build !linux

package main

import "os"

// isTerminal falls back to the character-device test where TCGETS is not the
// spelling. It is wrong for /dev/null in the same way the Linux version used to
// be — but the daemon no longer installs the terminal channel at all, which is
// the belt to this brace. See tty_unix.go for what that cost.
func isTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
