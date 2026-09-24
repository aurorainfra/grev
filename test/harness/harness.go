// Package harness builds the grev tools and runs them as subprocesses, for
// the CLI tests (fake server) and the live tests (real API).
package harness

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	mu    sync.Mutex
	dir   string
	built = map[string]string{}
)

// Root is the module root: the nearest parent directory holding go.mod.
func Root() string {
	d, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			panic("harness: go.mod not found")
		}
		d = parent
	}
}

// Bin builds cmd/<tool> once per test binary and returns its path.
func Bin(tb testing.TB, tool string) string {
	tb.Helper()
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	// GREV_TEST_BIN points at prebuilt tools, for running the suite where
	// there is no Go toolchain (e.g. a cross-compiled test binary under wine).
	if d := os.Getenv("GREV_TEST_BIN"); d != "" {
		return filepath.Join(d, tool+exe)
	}
	mu.Lock()
	defer mu.Unlock()
	if p, ok := built[tool]; ok {
		return p
	}
	if dir == "" {
		d, err := os.MkdirTemp("", "grev-bin-")
		if err != nil {
			tb.Fatal(err)
		}
		dir = d
	}
	out := filepath.Join(dir, tool+exe)
	args := []string{"build", "-o", out}
	if raceEnabled {
		args = append(args, "-race")
	}
	cmd := exec.Command("go", append(args, "./cmd/"+tool)...)
	cmd.Dir = Root()
	if b, err := cmd.CombinedOutput(); err != nil {
		tb.Fatalf("building %s: %v\n%s", tool, err, b)
	}
	built[tool] = out
	return out
}

// Cleanup removes the built binaries; call it from TestMain after m.Run.
func Cleanup() {
	mu.Lock()
	defer mu.Unlock()
	if dir != "" {
		os.RemoveAll(dir)
	}
}

// Env is a clean environment for a tool: the host's variables minus every
// TYPESAFE_*, GREV_* and credential variable, with HOME, XDG_CONFIG_HOME and
// XDG_STATE_HOME in a fresh temp dir, so no real key, ~/.grevconfig or spend
// ledger can be picked up or written; then extra KEY=VALUE pairs.
func Env(tb testing.TB, extra ...string) []string {
	tb.Helper()
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "TYPESAFE_") || strings.HasPrefix(k, "GREV_") ||
			k == "CREDENTIALS_DIRECTORY" || k == "HOME" || k == "XDG_CONFIG_HOME" ||
			k == "XDG_STATE_HOME" || k == "USERPROFILE" || k == "APPDATA" || k == "LOCALAPPDATA" {
			continue
		}
		env = append(env, kv)
	}
	home := tb.TempDir()
	env = append(env, "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"USERPROFILE="+home, "APPDATA="+filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA="+filepath.Join(home, "AppData", "Local"))
	return append(env, extra...)
}

// Result is the outcome of one run.
type Result struct {
	Stdout, Stderr string
	Code           int
}

// Run runs tool with args, stdin and env (see Env), failing the test if it
// cannot be started or takes longer than a minute.
func Run(tb testing.TB, env []string, stdin, tool string, args ...string) Result {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, Bin(tb, tool), args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := Result{Stdout: out.String(), Stderr: errb.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.Code = ee.ExitCode()
	default:
		tb.Fatalf("running %s %q: %v\nstderr: %s", tool, args, err, res.Stderr)
	}
	if ctx.Err() != nil {
		tb.Fatalf("%s %q timed out\nstderr: %s", tool, args, res.Stderr)
	}
	return res
}
