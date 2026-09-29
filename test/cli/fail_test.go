package clitest

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/aurorainfra/grev/internal/jev"
	"github.com/aurorainfra/grev/internal/jevtest"
)

// attempts is how many times the client sends one request before it fails
// for good.
const attempts = jev.MaxRetries + 1

// failedRun runs sortv over two records against a server whose first failN
// POSTs answer 500, with the terminal answering tty ("" for no terminal).
// --confirm-above=0 makes any run "big", so a failure asks; --max-cost
// answers the quote, so only the failure prompt reads the terminal.
func failedRun(t *testing.T, failN int, tty string, big bool) (*jevtest.Server, string, int) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "no-such-tty")
	if tty != "" {
		path = writeFile(t, dir, "tty", tty)
	}
	srv, env := fake(t, jevtest.Options{FailFirst: failN, FailWaitMS: 1}, "GREV_TTY="+path)
	args := []string{"--passes", "0", "by size"}
	if big {
		args = append([]string{"--confirm-above=0", "--max-cost=1"}, args...)
	}
	r := run(t, env, "mouse\nelephant\n", "sortv", args...)
	return srv, r.Stderr, r.Code
}

// TestFailedRequestAsks: on a big run with a terminal, a request that fails
// for good asks what to do, with the error and the spend so far.
func TestFailedRequestAsks(t *testing.T) {
	srv, stderr, code := failedRun(t, attempts, "r\n", true)
	if code != 0 || srv.Requests() != attempts+1 {
		t.Fatalf("retry: exit %d after %d requests\n%s", code, srv.Requests(), stderr)
	}
	for _, want := range []string{
		"sortv: a request failed: API 500 Internal Server Error: \"injected failure\" (request-id req_test_500); gave up after 7 attempts",
		"sortv: 0 of 2 questions answered so far, $0 spent; the request held 2",
		"Retry it [r], retry without asking again [a], or stop [q]? [R/a/q]", // sortv can't skip
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}

	// Stop: nothing more is sent, and the error says why the run ended.
	srv, stderr, code = failedRun(t, 1000, "q\n", true)
	if code != 2 || srv.Requests() != attempts || !strings.Contains(stderr, "sortv: run stopped: API 500") {
		t.Fatalf("stop: exit %d after %d requests\n%s", code, srv.Requests(), stderr)
	}

	// "a" retries this request and every later failure without asking again.
	srv, stderr, code = failedRun(t, 2*attempts, "a\n", true)
	if code != 0 || strings.Count(stderr, "Retry it") != 1 || srv.Requests() != 2*attempts+1 {
		t.Fatalf("always: exit %d after %d requests\n%s", code, srv.Requests(), stderr)
	}
}

// TestFailedRequestWithoutPrompt: small runs, and runs without a terminal,
// fail as before; sortv stops at the first failed request.
func TestFailedRequestWithoutPrompt(t *testing.T) {
	for name, c := range map[string]struct {
		tty string
		big bool
	}{"no terminal": {"", true}, "small run": {"r\n", false}} {
		srv, stderr, code := failedRun(t, 1000, c.tty, c.big)
		if code != 2 || strings.Contains(stderr, "Retry it") || srv.Requests() != attempts ||
			!strings.Contains(stderr, "gave up after 7 attempts") {
			t.Errorf("%s: exit %d after %d requests\n%s", name, code, srv.Requests(), stderr)
		}
	}
}
