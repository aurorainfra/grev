//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly || windows)

package cli

import (
	"errors"
	"os"
)

const ttyPath = "/dev/tty"

func IsTerminal(f *os.File) bool { return false }

func EnableVT(f *os.File) bool { return false }

func TermWidth(f *os.File) int { return 0 }

func ReadSecret(f *os.File) (string, error) {
	return "", errors.New("reading a secret needs a unix terminal")
}
