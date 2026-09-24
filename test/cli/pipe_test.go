package clitest

import (
	"bufio"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aurorainfra/grev/internal/jevtest"
	"github.com/aurorainfra/grev/test/harness"
)

// TestClosedStdoutStopsSpending: when the reader of stdout goes away (like
// `grev … | head -1`), the tool stops sending requests. On unix that is
// SIGPIPE; on Windows, where there is none, the failed write cancels the run.
func TestClosedStdoutStopsSpending(t *testing.T) {
	srv, env := fake(t, jevtest.Options{Latency: func() time.Duration { return 20 * time.Millisecond }},
		"GREV_MAX_Q=5")
	var in strings.Builder
	for i := 0; i < 1000; i++ {
		in.WriteString("record yes\n")
	}
	cmd := exec.Command(harness.Bin(t, "grev"), "-J1", "q")
	cmd.Env = env
	cmd.Stdin = strings.NewReader(in.String())
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(out).ReadString('\n'); err != nil {
		t.Fatalf("no first line: %v", err)
	}
	out.Close() // the reader is gone
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("grev kept running after stdout closed (%d requests)", srv.Requests())
	}
	if n := srv.Requests(); n >= 200 {
		t.Fatalf("all %d requests were sent; closing stdout should stop the run", n)
	}
}
