//go:build live

// Package live runs the tools against the real API with your key: from
// ~/.grevconfig (as set by `jev key set`), or TYPESAFE_API_KEY /
// TYPESAFE_API_KEY_FILE in CI. It skips when there is none. Every call is
// capped with --max-cost, spend goes to a throwaway ledger, and the key is
// only ever read by the tools.
package live

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aurorainfra/grev/internal/config"
	"github.com/aurorainfra/grev/test/harness"
)

const maxCost = "0.01"

var (
	keyEnv  string // GREV_CONFIG=…, TYPESAFE_API_KEY_FILE=… or TYPESAFE_API_KEY=…, passed to the tools
	spentMu sync.Mutex
	spent   float64
)

func TestMain(m *testing.M) {
	if f := os.Getenv("TYPESAFE_API_KEY_FILE"); f != "" {
		if strings.HasPrefix(f, "~/") {
			home, _ := os.UserHomeDir()
			f = filepath.Join(home, f[2:])
		}
		keyEnv = "TYPESAFE_API_KEY_FILE=" + f
	} else if k := os.Getenv("TYPESAFE_API_KEY"); k != "" {
		keyEnv = "TYPESAFE_API_KEY=" + k
	} else if cfg, err := config.Load(); err == nil {
		// Your own config: the tools see it as GREV_CONFIG (their HOME is a
		// temp dir), so its key, endpoint and defaults apply.
		if v, ok := cfg.Get("api", "", "key"); ok {
			keyEnv = "GREV_CONFIG=" + v.File
		} else if v, ok := cfg.Get("api", "", "keyCommand"); ok {
			keyEnv = "GREV_CONFIG=" + v.File
		}
	}
	if keyEnv == "" {
		fmt.Println("live tests skipped: no API key (run `jev key set`, or set TYPESAFE_API_KEY)")
		os.Exit(0)
	}
	code := m.Run()
	fmt.Printf("live tests spent $%.6f\n", spent)
	harness.Cleanup()
	os.Exit(code)
}

var costRe = regexp.MustCompile(`· \$([0-9.]+) ·`)

// run invokes a tool against the live API with -p (for the spend summary)
// and --max-cost.
func run(t *testing.T, stdin, tool string, args ...string) harness.Result {
	t.Helper()
	env := harness.Env(t, keyEnv)
	if base := os.Getenv("TYPESAFE_BASE_URL"); base != "" {
		env = append(env, "TYPESAFE_BASE_URL="+base)
	}
	r := harness.Run(t, env, stdin, tool, append([]string{"-p", "--max-cost", maxCost}, args...)...)
	for _, line := range strings.Split(r.Stderr, "\n") {
		if m := costRe.FindStringSubmatch(line); m != nil && strings.HasPrefix(line, tool+": ") {
			c, _ := strconv.ParseFloat(m[1], 64)
			spentMu.Lock()
			spent += c
			spentMu.Unlock()
			t.Log(line)
		}
	}
	return r
}

func TestLiveGrevVeganMenu(t *testing.T) {
	menu := "grilled ribeye steak with butter\nboiled carrots\nlentil soup with vegetable broth\nchicken tikka masala\nfresh fruit salad\n"
	r := run(t, menu, "grev", "is vegan meal")
	if r.Code != 0 {
		t.Fatalf("exit %d\n%s", r.Code, r.Stderr)
	}
	if !strings.Contains(r.Stdout, "boiled carrots") || strings.Contains(r.Stdout, "steak") || strings.Contains(r.Stdout, "chicken") {
		t.Fatalf("vegan selection:\n%s", r.Stdout)
	}
	r = run(t, menu, "grev", "-v", "-c", "is vegan meal")
	if r.Code != 0 || strings.TrimSpace(r.Stdout) < "2" {
		t.Fatalf("non-vegan count: %q (exit %d)", r.Stdout, r.Code)
	}
}

func TestLiveIsSecret(t *testing.T) {
	q := "Does this code contain a hard-coded secret or credential?"
	if r := run(t, `const apiKey = "sk-live-9f8a7b6c5d4e3f2a1b0c"`+"\n", "isv", q); r.Code != 0 {
		t.Fatalf("obvious secret: exit %d\n%s", r.Code, r.Stderr)
	}
	if r := run(t, "fix typo in README\n", "isv", q); r.Code != 1 {
		t.Fatalf("no secret: exit %d\n%s", r.Code, r.Stderr)
	}
}

func TestLiveJev(t *testing.T) {
	r := run(t, "", "jev", "key", "status")
	if r.Code != 0 || !strings.Contains(r.Stdout, "check:  ok") {
		t.Fatalf("key status: exit %d\n%s\n%s", r.Code, r.Stdout, r.Stderr)
	}
	r = run(t, "I was charged twice for my order, please refund me!", "jev", "ask",
		"-q", "refund: The customer asks for a refund",
		"-q", "team: Which team should handle this? [billing|technical|sales]")
	if r.Code != 0 || !strings.Contains(r.Stdout, "refund  noul") || !strings.Contains(r.Stdout, "team    choice  billing") {
		t.Fatalf("ask: exit %d\n%s\n%s", r.Code, r.Stdout, r.Stderr)
	}
}

func TestLiveQuoteDeclineSpendsNothing(t *testing.T) {
	no := filepath.Join(t.TempDir(), "no")
	os.WriteFile(no, []byte("n\n"), 0o600)
	env := append(harness.Env(t, keyEnv), "GREV_TTY="+no)
	r := harness.Run(t, env, "a\nb\n", "grev", "-Q", "q")
	if r.Code != 4 || !strings.Contains(r.Stderr, "grev quote:") {
		t.Fatalf("declined quote: exit %d\n%s", r.Code, r.Stderr)
	}
}
