//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package cli

import "syscall"

const (
	ioctlGetTermios = syscall.TIOCGETA
	ioctlSetTermios = syscall.TIOCSETA
)
