package clitest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aurorainfra/grev/internal/jevtest"
	"github.com/aurorainfra/grev/test/harness"
)

// homeOf is the temp HOME of a harness environment.
func homeOf(env []string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "HOME="); ok {
			return v
		}
	}
	return ""
}

func writeConfig(t *testing.T, env []string, content string) string {
	t.Helper()
	p := filepath.Join(homeOf(env), ".grevconfig")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfigToolDefaultsAndNegation(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	writeConfig(t, env, "[tool \"grev\"]\n\tscores = true\n\tline-number\n")
	expect(t, run(t, env, fruits, "grev", "q"), 0, "0.95\t1:apple yes\n0.95\t3:cherry yes\n")
	expect(t, run(t, env, fruits, "grev", "--no-scores", "--no-line-number", "q"), 0, "apple yes\ncherry yes\n")
	// A section for another tool doesn't apply.
	writeConfig(t, env, "[tool \"rank\"]\n\tscores = true\n")
	expect(t, run(t, env, fruits, "grev", "q"), 0, "apple yes\ncherry yes\n")
}

func TestConfigWarningsAndErrors(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	writeConfig(t, env, "[tool \"grev\"]\n\tno-such-option = 1\n[limits]\n\tdayly = 5\n")
	r := run(t, env, fruits, "grev", "q")
	if r.Code != 0 || !strings.Contains(r.Stderr, "~/.grevconfig:2: grev has no option --nosuchoption") ||
		!strings.Contains(r.Stderr, "~/.grevconfig:4: unknown config key limits.dayly") {
		t.Fatalf("warnings: %+v", r)
	}
	writeConfig(t, env, "[defaults]\n\tjobs = 0\n")
	if r := run(t, env, fruits, "grev", "q"); r.Code != 2 || !strings.Contains(r.Stderr, "-J") {
		t.Fatalf("bad jobs value: %+v", r)
	}
	writeConfig(t, env, "[api\n")
	if r := run(t, env, fruits, "grev", "q"); r.Code != 2 || !strings.Contains(r.Stderr, "~/.grevconfig:1: missing ]") {
		t.Fatalf("syntax error: %+v", r)
	}
	// jev config still works with a broken file, so it can be fixed.
	if r := run(t, env, "", "jev", "config", "path"); r.Code != 0 || !strings.HasSuffix(strings.TrimSpace(r.Stdout), ".grevconfig") {
		t.Fatalf("jev config path with a broken config: %+v", r)
	}
	writeConfig(t, env, "[tool \"grev\"]\n\tmodel = x\n")
	if r := run(t, env, fruits, "grev", "q"); r.Code != 0 || !strings.Contains(r.Stderr, "use [api]") {
		t.Fatalf("model in a tool section: %+v", r)
	}
}

func TestConfigAPIKeyEndpointModel(t *testing.T) {
	srv := jevtest.New(t, jevtest.Options{Key: testKey})
	env := harness.Env(t) // no TYPESAFE_* at all: everything from the config
	writeConfig(t, env, "[api]\n\tkey = "+testKey+"\n\tendpoint = "+srv.URL+"/\n\tmodel = jev-test-9\n[model \"jev-test-9\"]\n\tprice = 0.05\n")
	expect(t, run(t, env, fruits, "grev", "--max-cost", "1", "q"), 0, "apple yes\ncherry yes\n")
	if log := srv.Log(); len(log) == 0 || log[len(log)-1].Model != "jev-test-9" {
		t.Fatalf("model from config: %+v", log)
	}
	r := run(t, env, "", "jev", "key", "status", "--no-check")
	if r.Code != 0 || !strings.Contains(r.Stdout, "source: api.key (~/.grevconfig:2)") {
		t.Fatalf("key status: %+v", r)
	}
	noLeak(t, r, testKey)
	// A model without a price can't be budgeted.
	writeConfig(t, env, "[api]\n\tkey = "+testKey+"\n\tendpoint = "+srv.URL+"\n")
	if r := run(t, env, fruits, "grev", "-M", "jev-unknown-1", "--max-cost", "1", "q"); r.Code != 2 || !strings.Contains(r.Stderr, "need the price") {
		t.Fatalf("unknown price with a budget: %+v", r)
	}
	// Environment beats config.
	other := jevtest.New(t, jevtest.Options{Key: testKey})
	env2 := append(append([]string{}, env...), "TYPESAFE_BASE_URL="+other.URL)
	expect(t, run(t, env2, fruits, "grev", "q"), 0, "apple yes\ncherry yes\n")
	if other.Requests() != 1 {
		t.Fatalf("TYPESAFE_BASE_URL should override api.endpoint (requests %d)", other.Requests())
	}
}

func TestConfirmAbove(t *testing.T) {
	notty := filepath.Join(t.TempDir(), "no-such-tty")
	srv, env := fake(t, jevtest.Options{}, "GREV_TTY="+notty)
	// Below the built-in $1: no prompt.
	expect(t, run(t, env, fruits, "grev", "q"), 0, "apple yes\ncherry yes\n")
	// Above the threshold and no terminal: refused, nothing sent.
	before := srv.Requests()
	r := run(t, env, fruits, "grev", "--confirm-above", "0", "q")
	if r.Code != 4 || r.Stdout != "" || !strings.Contains(r.Stderr, "no terminal to ask") ||
		!strings.Contains(r.Stderr, "--max-cost=") || srv.Requests() != before {
		t.Fatalf("confirm-above without a terminal: %+v", r)
	}
	// From the config, the message says where the threshold came from.
	writeConfig(t, env, "[defaults]\n\tconfirmAbove = 0\n")
	if r := run(t, env, fruits, "grev", "q"); r.Code != 4 || !strings.Contains(r.Stderr, "(~/.grevconfig:2)") {
		t.Fatalf("confirm-above from config: %+v", r)
	}
	// An explicit budget covering the quote answers the question.
	expect(t, run(t, env, fruits, "grev", "--max-cost", "1", "q"), 0, "apple yes\ncherry yes\n")
	// --confirm-above=off overrides the config.
	expect(t, run(t, env, fruits, "grev", "--confirm-above=off", "q"), 0, "apple yes\ncherry yes\n")
	// With a terminal that says yes, it proceeds.
	yes := writeFile(t, t.TempDir(), "yes", "y\n")
	_, env2 := fake(t, jevtest.Options{}, "GREV_TTY="+yes)
	writeConfig(t, env2, "[defaults]\n\tconfirmAbove = 0\n")
	r = run(t, env2, fruits, "grev", "q")
	if r.Code != 0 || r.Stdout != "apple yes\ncherry yes\n" || !strings.Contains(r.Stderr, "grev quote:") {
		t.Fatalf("confirm-above with a yes: %+v", r)
	}
}

func TestSpendCapsAndLedger(t *testing.T) {
	srv, env := fake(t, jevtest.Options{})
	// Spend something, then look at the ledger.
	expect(t, run(t, env, fruits, "grev", "q"), 0, "apple yes\ncherry yes\n")
	expect(t, run(t, env, fruits, "rank", "q"), 0, "apple yes\ncherry yes\nbanana no\n")
	r := run(t, env, "", "jev", "spend", "--days", "2")
	if r.Code != 0 || !strings.Contains(r.Stdout, "today") || !strings.Contains(r.Stdout, "grev") ||
		!strings.Contains(r.Stdout, "rank") || !strings.Contains(r.Stdout, "no cap") {
		t.Fatalf("jev spend: %+v", r)
	}
	// A daily cap already used up refuses the next run up front.
	writeConfig(t, env, "[limits]\n\tdaily = 0.0000000001\n")
	before := srv.Requests()
	r = run(t, env, fruits, "grev", "q")
	if r.Code != 4 || !strings.Contains(r.Stderr, "spend caps") || srv.Requests() != before {
		t.Fatalf("daily cap: %+v (requests %d → %d)", r, before, srv.Requests())
	}
	// Streaming runs (no quote) stop at the cap too.
	r = run(t, env, fruits, "grev", "--line-buffered", "q")
	if r.Code != 4 || srv.Requests() != before || !strings.Contains(r.Stderr, "spend limit reached") {
		t.Fatalf("daily cap while streaming: %+v (requests %d → %d)", r, before, srv.Requests())
	}
	// GREV_LEDGER= turns the ledger (and the caps) off.
	expect(t, run(t, append(env, "GREV_LEDGER="), fruits, "grev", "q"), 0, "apple yes\ncherry yes\n")
}

func TestJevConfigCommands(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	p := filepath.Join(homeOf(env), ".grevconfig")
	for _, args := range [][]string{
		{"config", "set", "api.keyCommand", "pass show typesafe"},
		{"config", "set", "limits.daily", "5"},
		{"config", "set", "tool.grev.about", "app logs"},
		{"config", "set", "model.jev-1.14.0.price", "0.05"},
	} {
		if r := run(t, env, "", "jev", args...); r.Code != 0 {
			t.Fatalf("jev %v: %+v", args, r)
		}
	}
	if r := run(t, env, "", "jev", "config", "set", "limits.daily", "lots"); r.Code != 2 {
		t.Fatalf("bad value should be refused: %+v", r)
	}
	if fi, _ := os.Stat(p); !modeIs(fi, 0o600) {
		t.Fatalf("config mode %v", fi.Mode())
	}
	r := run(t, env, "", "jev", "config", "get", "tool.grev.about")
	if r.Code != 0 || r.Stdout != "app logs\n" {
		t.Fatalf("get: %+v", r)
	}
	if r := run(t, env, "", "jev", "config", "get", "limits.monthly"); r.Code != 1 {
		t.Fatalf("get missing: %+v", r)
	}
	r = run(t, env, "", "jev", "config", "list", "--show-origin")
	for _, want := range []string{"~/.grevconfig:2\tapi.keycommand=pass show typesafe", "limits.daily=5", "tool.grev.about=app logs", "model.jev-1.14.0.price=0.05"} {
		if !strings.Contains(r.Stdout, want) {
			t.Fatalf("list missing %q:\n%s", want, r.Stdout)
		}
	}
	if r := run(t, env, "", "jev", "config", "unset", "limits.daily"); r.Code != 0 {
		t.Fatalf("unset: %+v", r)
	}
	if r := run(t, env, "", "jev", "config", "unset", "limits.daily"); r.Code != 1 {
		t.Fatalf("unset missing: %+v", r)
	}
	// api.key is masked unless asked for.
	run(t, env, "", "jev", "config", "set", "api.key", testKey)
	r = run(t, env, "", "jev", "config", "list")
	noLeak(t, r, testKey)
	if r := run(t, env, "", "jev", "config", "get", "api.key", "--show-secret"); r.Stdout != testKey+"\n" {
		t.Fatalf("--show-secret: %+v", r)
	}
}
