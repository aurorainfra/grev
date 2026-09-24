//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package cli

import (
	"os"
	"syscall"
	"unsafe"
)

func ioctl(fd, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

// ttyPath is the controlling terminal, used by the -Q prompt when stdin
// carries data.
const ttyPath = "/dev/tty"

// EnableVT reports whether ANSI escapes work on f; they always do on unix
// terminals.
func EnableVT(f *os.File) bool { return true }

// IsTerminal reports whether f is a terminal.
func IsTerminal(f *os.File) bool {
	var t syscall.Termios
	return ioctl(f.Fd(), ioctlGetTermios, unsafe.Pointer(&t)) == nil
}

// TermWidth is the column count of terminal f, or 0 if unknown.
func TermWidth(f *os.File) int {
	var ws struct{ Row, Col, X, Y uint16 }
	if ioctl(f.Fd(), syscall.TIOCGWINSZ, unsafe.Pointer(&ws)) != nil {
		return 0
	}
	return int(ws.Col)
}

// ReadSecret reads one line from terminal f with echo turned off.
func ReadSecret(f *os.File) (string, error) {
	var old syscall.Termios
	if err := ioctl(f.Fd(), ioctlGetTermios, unsafe.Pointer(&old)); err != nil {
		return "", err
	}
	quiet := old
	quiet.Lflag &^= syscall.ECHO
	quiet.Lflag |= syscall.ICANON | syscall.ISIG
	if err := ioctl(f.Fd(), ioctlSetTermios, unsafe.Pointer(&quiet)); err != nil {
		return "", err
	}
	defer ioctl(f.Fd(), ioctlSetTermios, unsafe.Pointer(&old))
	return readLine(f)
}
