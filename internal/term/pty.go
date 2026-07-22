// Package term gives Freya persistent, interactive terminal sessions.
//
// # Why a pseudo-terminal rather than pipes
//
// Pipes are simpler and wrong for this. A program connected to a pipe knows it
// is not talking to a person: it switches to block buffering so output arrives
// in 4KB lumps rather than lines, drops colour, and — the case that matters —
// interactive prompts never appear. A Python REPL over pipes produces no `>>>`,
// `sudo` refuses to ask for a password, and `git` will not prompt. An assistant
// that cannot see a prompt cannot answer one.
//
// So sessions run on a real pseudo-terminal. Linux exposes this through
// /dev/ptmx and three ioctls, which is a couple of dozen lines of syscall — far
// less than the behaviour it buys.
//
// # Why sessions persist
//
// A command that finishes inside one exchange never needed a session. The point
// is the opposite case: start a build, keep talking, check on it later. Sessions
// outlive the turn that created them, which is what makes long-running work
// possible at all.
package term

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// Linux ioctl numbers for pseudo-terminal control.
const (
	tiocGPTN   = 0x80045430 // get the pty number
	tiocSPTLCK = 0x40045431 // lock or unlock the slave side
	tiocSWINSZ = 0x5414     // set window size
)

// winsize mirrors struct winsize from termios.h.
type winsize struct {
	rows, cols, x, y uint16
}

// openPTY allocates a pseudo-terminal pair.
//
// Returns the master, which is read and written like a file, and the slave
// path, which the child process opens as its controlling terminal.
func openPTY() (master *os.File, slaveName string, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, "", fmt.Errorf("term: open /dev/ptmx: %w", err)
	}

	// Unlock the slave. Without this, opening it fails with EIO.
	var unlock int32
	if err := ioctl(master.Fd(), tiocSPTLCK, uintptr(unsafe.Pointer(&unlock))); err != nil {
		master.Close()
		return nil, "", fmt.Errorf("term: unlock pty: %w", err)
	}

	var ptyNumber uint32
	if err := ioctl(master.Fd(), tiocGPTN, uintptr(unsafe.Pointer(&ptyNumber))); err != nil {
		master.Close()
		return nil, "", fmt.Errorf("term: get pty number: %w", err)
	}

	return master, fmt.Sprintf("/dev/pts/%d", ptyNumber), nil
}

// setWindowSize tells the terminal how big it is.
//
// Programs lay out their output to fit: without this they assume 80x24, and
// anything wider is wrapped mid-word, which makes output far harder to read
// than the arbitrary default suggests.
func setWindowSize(f *os.File, rows, cols int) error {
	ws := winsize{rows: uint16(rows), cols: uint16(cols)}
	return ioctl(f.Fd(), tiocSWINSZ, uintptr(unsafe.Pointer(&ws)))
}

func ioctl(fd, request, arg uintptr) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, arg); errno != 0 {
		return errno
	}
	return nil
}

// termios mirrors struct termios from termios.h on Linux.
type termios struct {
	iflag, oflag, cflag, lflag uint32
	line                       byte
	cc                         [32]byte
	ispeed, ospeed             uint32
}

const (
	tcGETS = 0x5401
	tcSETS = 0x5402

	// Terminal local-mode flags.
	echoFlag   = 0x0008 // ECHO — echo input back to the terminal
	echoCtl    = 0x0200 // ECHOCTL — render control characters as ^C
	echoKE     = 0x0800 // ECHOKE
	icanonFlag = 0x0002 // ICANON — line-at-a-time input
)

// disableEcho turns off input echo on a terminal.
//
// A pty echoes whatever is written to it back out, exactly as a terminal shows
// you what you type. For a person that is essential; for programmatic capture
// it is contamination — the captured output contains the command that produced
// it, so searching the result for a word finds the word in the command itself.
// A test looking for "FINISHED" matched `echo FINISHED` and concluded a
// one-and-a-half second loop had finished instantly.
//
// Canonical mode is left on, so line editing and signal characters still work.
func disableEcho(f *os.File) error {
	var t termios
	if err := ioctl(f.Fd(), tcGETS, uintptr(unsafe.Pointer(&t))); err != nil {
		return fmt.Errorf("term: read terminal settings: %w", err)
	}
	t.lflag &^= echoFlag | echoCtl | echoKE
	if err := ioctl(f.Fd(), tcSETS, uintptr(unsafe.Pointer(&t))); err != nil {
		return fmt.Errorf("term: apply terminal settings: %w", err)
	}
	return nil
}
