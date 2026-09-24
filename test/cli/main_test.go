// Package clitest runs the grev tools as subprocesses against the fake API
// server (internal/jevtest). To cover a new tool, write a TestXxx that calls
// fake(t, opts) for a server and its environment, then run(t, env, stdin,
// "tool", args...) and check the Result; the tool is built on first use.
package clitest

import (
	"os"
	"strings"
	"testing"

	"github.com/aurorainfra/grev/internal/jevtest"
	"github.com/aurorainfra/grev/test/harness"
)

func TestMain(m *testing.M) {
	code := m.Run()
	harness.Cleanup()
	os.Exit(code)
}

// testKey is the API key the fake server expects in CLI tests.
const testKey = "tsk-test-key-0000"

// fake starts a server and returns it with an environment pointing at it.
func fake(t *testing.T, o jevtest.Options, extra ...string) (*jevtest.Server, []string) {
	t.Helper()
	if o.Key == "" {
		o.Key = testKey
	}
	srv := jevtest.New(t, o)
	env := harness.Env(t, append([]string{"TYPESAFE_BASE_URL=" + srv.URL, "TYPESAFE_API_KEY=" + o.Key}, extra...)...)
	return srv, env
}

func run(t *testing.T, env []string, stdin, tool string, args ...string) harness.Result {
	t.Helper()
	return harness.Run(t, env, stdin, tool, args...)
}

// expect checks exit code and exact stdout, and that no key leaked anywhere.
func expect(t *testing.T, r harness.Result, code int, stdout string) {
	t.Helper()
	if r.Code != code || r.Stdout != stdout {
		t.Fatalf("exit %d (want %d)\nstdout: %q\nwant:   %q\nstderr: %s", r.Code, code, r.Stdout, stdout, r.Stderr)
	}
	noLeak(t, r, testKey)
}

func noLeak(t *testing.T, r harness.Result, key string) {
	t.Helper()
	if strings.Contains(r.Stdout, key) || strings.Contains(r.Stderr, key) {
		t.Fatalf("the API key leaked into output:\nstdout: %s\nstderr: %s", r.Stdout, r.Stderr)
	}
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := dir + "/" + name
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// harnessEnvNoKey points at url without any API key configured.
func harnessEnvNoKey(t *testing.T, url string) []string {
	t.Helper()
	return harness.Env(t, "TYPESAFE_BASE_URL="+url)
}
