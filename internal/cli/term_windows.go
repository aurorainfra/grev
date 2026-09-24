//go:build windows

package cli

import (
	"os"
	"syscall"
	"unsafe"
)

// ttyPath is the console input, used by the -Q prompt when stdin carries
// data.
const ttyPath = "CONIN$"

// Console mode bits (wincon.h).
const (
	enableEchoInput                 = 0x0004
	enableLineInput                 = 0x0002
	enableProcessedInput            = 0x0001
	enableProcessedOutput           = 0x0001
	enableVirtualTerminalProcessing = 0x0004
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleMode             = kernel32.NewProc("SetConsoleMode")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
)

func setConsoleMode(h syscall.Handle, mode uint32) error {
	r, _, err := procSetConsoleMode.Call(uintptr(h), uintptr(mode))
	if r == 0 {
		return err
	}
	return nil
}

// IsTerminal reports whether f is a console.
func IsTerminal(f *os.File) bool {
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(f.Fd()), &mode) == nil
}

type coord struct{ X, Y int16 }

type smallRect struct{ Left, Top, Right, Bottom int16 }

type consoleScreenBufferInfo struct {
	Size              coord
	CursorPosition    coord
	Attributes        uint16
	Window            smallRect
	MaximumWindowSize coord
}

// TermWidth is the column count of console f, or 0 if unknown.
func TermWidth(f *os.File) int {
	var info consoleScreenBufferInfo
	r, _, _ := procGetConsoleScreenBufferInfo.Call(f.Fd(), uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return 0
	}
	return int(info.Window.Right-info.Window.Left) + 1
}

// EnableVT turns on ANSI escape processing for console f (Windows 10+). It
// reports false when escapes can't be used, and the overlay falls back to
// plain status lines.
func EnableVT(f *os.File) bool {
	h := syscall.Handle(f.Fd())
	var mode uint32
	if syscall.GetConsoleMode(h, &mode) != nil {
		return false
	}
	if mode&enableVirtualTerminalProcessing != 0 {
		return true
	}
	return setConsoleMode(h, mode|enableVirtualTerminalProcessing|enableProcessedOutput) == nil
}

// ReadSecret reads one line from console f with echo turned off.
func ReadSecret(f *os.File) (string, error) {
	h := syscall.Handle(f.Fd())
	var old uint32
	if err := syscall.GetConsoleMode(h, &old); err != nil {
		return "", err
	}
	quiet := (old &^ enableEchoInput) | enableLineInput | enableProcessedInput
	if err := setConsoleMode(h, quiet); err != nil {
		return "", err
	}
	defer setConsoleMode(h, old)
	return readLine(f)
}
