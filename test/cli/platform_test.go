package clitest

import (
	"os"
	"runtime"
)

// modeIs checks unix permission bits; Windows has none to check.
func modeIs(fi os.FileInfo, want os.FileMode) bool {
	return runtime.GOOS == "windows" || (fi != nil && fi.Mode().Perm() == want)
}

// unixPerms reports whether the platform has unix permission bits.
var unixPerms = runtime.GOOS != "windows"
