package clitest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aurorainfra/grev/internal/jevtest"
	"github.com/aurorainfra/grev/test/harness"
)

func TestIs(t *testing.T) {
	srv, env := fake(t, jevtest.Options{})
	cases := []struct {
		name  string
		stdin string
		args  []string
		code  int
		out   string
	}{
		{"yes", "yes it is", []string{"is it?"}, 0, ""},
		{"no", "nope", []string{"is it?"}, 1, ""},
		{"score", "yes", []string{"-s", "is it?"}, 0, "0.95\n"},
		{"invert yes", "yes", []string{"-v", "is it?"}, 1, ""},
		{"invert no", "nope", []string{"-v", "is it?"}, 0, ""},
		{"threshold", "p=0.6", []string{"-t", "0.7", "q"}, 1, ""},
		{"band", "maybe", []string{"--band", "0.3:0.7", "q"}, 3, ""},
		{"band invert stays uncertain", "maybe", []string{"-v", "--band", "0.3:0.7", "q"}, 3, ""},
		{"band yes", "yes", []string{"--band", "0.3:0.7", "q"}, 0, ""},
		{"about", "yes", []string{"--about", "a diff", "q"}, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expect(t, run(t, env, tc.stdin, "isv", tc.args...), tc.code, tc.out)
		})
	}
	file := writeFile(t, t.TempDir(), "in.txt", "yes from a file")
	expect(t, run(t, env, "", "isv", "q", file), 0, "")

	for _, args := range [][]string{{}, {"a", "b", "c"}, {"--chunks", "some", "q"}, {"--band", "x", "q"}, {"--band", "0.2:1.5", "q"}} {
		if r := run(t, env, "yes", "isv", args...); r.Code != 2 {
			t.Errorf("is %q: want usage error, got %+v", args, r)
		}
	}
	if r := run(t, env, "  \n", "isv", "q"); r.Code != 2 || !strings.Contains(r.Stderr, "empty input") {
		t.Errorf("empty input: %+v", r)
	}

	// Input too large for one request: refused without --chunks, split with it.
	big := strings.Repeat("filler line without the word\n", 5000) + "yes at the very end\n"
	before := srv.Requests()
	if r := run(t, env, big, "isv", "q"); r.Code != 2 || !strings.Contains(r.Stderr, "too large") {
		t.Fatalf("oversize input: %+v", r)
	}
	if srv.Requests() != before {
		t.Fatal("an oversize input must not be sent")
	}
	expect(t, run(t, env, big, "isv", "--chunks", "any", "q"), 0, "")
	expect(t, run(t, env, big, "isv", "--chunks", "all", "q"), 1, "")
	if srv.Requests()-before < 4 {
		t.Fatalf("chunked runs should send several requests, sent %d", srv.Requests()-before)
	}
}

func TestJevAskRawModels(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	r := run(t, env, "", "jev", "ask", "--state", "p=0.42 about billing",
		"-q", "x: is it", "-q", "dept: which team [sales|billing]", "-q", "lvl: how much <lo|mid|hi>")
	if r.Code != 0 {
		t.Fatalf("ask: %+v", r)
	}
	lines := strings.Split(strings.TrimSpace(r.Stdout), "\n")
	if len(lines) != 3 ||
		!strings.HasPrefix(lines[0], "x     noul    0.42") ||
		!strings.HasPrefix(lines[1], "dept  choice  billing (conf 0.80)  billing 0.90 · sales 0.10") ||
		!strings.HasPrefix(lines[2], "lvl   score   1.00 (conf 1.00)  0:lo 0.00 · 1:mid 1.00 · 2:hi 0.00") {
		t.Fatalf("ask output:\n%s", r.Stdout)
	}
	noLeak(t, r, testKey)

	r = run(t, env, "yes from stdin", "jev", "ask", "--json", "-q", "a: q?")
	var got map[string]map[string]any
	if r.Code != 0 || json.Unmarshal([]byte(r.Stdout), &got) != nil || got["a"]["noul"] != 0.95 {
		t.Fatalf("ask --json: %+v", r)
	}
	if r := run(t, env, "x", "jev", "ask"); r.Code != 2 {
		t.Fatalf("ask without -q: %+v", r)
	}
	if r := run(t, env, "x", "jev", "ask", "-q", "a: q", "-q", "a: again"); r.Code != 2 {
		t.Fatalf("duplicate names: %+v", r)
	}

	r = run(t, env, `{"state":"yes","questions":{"a":{"type":"noul","instructions":"q?"}}}`, "jev", "raw")
	var resp struct {
		Model   string                    `json:"model"`
		Answers map[string]map[string]any `json:"answers"`
		Usage   map[string]int            `json:"usage"`
	}
	if r.Code != 0 || json.Unmarshal([]byte(r.Stdout), &resp) != nil || resp.Answers["a"]["noul"] != 0.95 || resp.Model == "" {
		t.Fatalf("raw: %+v", r)
	}
	if r := run(t, env, "not json", "jev", "raw"); r.Code != 2 {
		t.Fatalf("raw with bad JSON: %+v", r)
	}
	r = run(t, env, `{"state":"x","questions":{"a":{"type":"bogus"}}}`, "jev", "raw")
	if r.Code != 0 && r.Code != 2 {
		t.Fatalf("raw bogus: %+v", r)
	}

	r = run(t, env, "", "jev", "models")
	if r.Code != 0 || !strings.Contains(r.Stdout, "jev-latest") || !strings.Contains(r.Stdout, "2026-09-10") {
		t.Fatalf("models: %+v", r)
	}
	for _, args := range [][]string{{}, {"nope"}, {"key"}, {"key", "frob"}} {
		if r := run(t, env, "", "jev", args...); r.Code != 2 {
			t.Errorf("jev %q: want usage error, got %+v", args, r)
		}
	}
}

func TestJevKey(t *testing.T) {
	const key = "tsk-live-looking-KEY-9f8e7d6c5b4a"
	srv := jevtest.New(t, jevtest.Options{Key: key})
	env := harness.Env(t, "TYPESAFE_BASE_URL="+srv.URL)
	home := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "HOME="); ok {
			home = v
		}
	}
	keyPath := filepath.Join(home, ".grevconfig") // key set stores api.key in the config

	// No key anywhere yet.
	r := run(t, env, "", "jev", "key", "status", "--no-check")
	if r.Code != 2 || !strings.Contains(r.Stderr, "no API key") {
		t.Fatalf("status without key: %+v", r)
	}
	r = run(t, env, "", "jev", "key", "path")
	if r.Code != 0 || strings.TrimSpace(r.Stdout) != keyPath {
		t.Fatalf("key path = %q, want %q", r.Stdout, keyPath)
	}

	// key set from a pipe: verified against the server, stored 0600.
	r = run(t, env, "  "+key+"\n", "jev", "key", "set")
	if r.Code != 0 || !strings.Contains(r.Stderr, "…7d6c5b4a"[len("…7d6c"):]) {
		t.Fatalf("key set: %+v", r)
	}
	noLeak(t, r, key)
	fi, err := os.Stat(keyPath)
	if err != nil || !modeIs(fi, 0o600) {
		t.Fatalf("stored key file: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(keyPath); !strings.Contains(string(b), "key = "+key) {
		t.Fatal("stored key differs")
	}
	if r := run(t, env, "wrong-key-xyz\n", "jev", "key", "set"); r.Code != 2 || !strings.Contains(r.Stderr, "check failed") {
		t.Fatalf("a key the server rejects should not be stored: %+v", r)
	}
	if b, _ := os.ReadFile(keyPath); !strings.Contains(string(b), "key = "+key) {
		t.Fatal("a rejected key overwrote the stored one")
	}

	r = run(t, env, "", "jev", "key", "status")
	if r.Code != 0 || !strings.Contains(r.Stdout, "source: api.key (~/.grevconfig:2)") ||
		!strings.Contains(r.Stdout, "key:    …5b4a") || !strings.Contains(r.Stdout, "check:  ok (2 models") {
		t.Fatalf("status from config: %+v", r)
	}
	noLeak(t, r, key)

	// A config holding the key with loose permissions: works, warns, never prints the key.
	os.Chmod(keyPath, 0o644)
	r = run(t, env, "", "jev", "key", "status", "--no-check")
	if r.Code != 0 || unixPerms && !strings.Contains(r.Stdout, "chmod 600") {
		t.Fatalf("status with a loose config: %+v", r)
	}
	noLeak(t, r, key)
	r = run(t, env, "yes", "isv", "q")
	if r.Code != 0 || unixPerms && !strings.Contains(r.Stderr, "chmod 600") {
		t.Fatalf("tools warn about a loose config holding the key: %+v", r)
	}
	noLeak(t, r, key)
	os.Chmod(keyPath, 0o600)

	// A key the server rejects (here via TYPESAFE_API_KEY_FILE, which beats the
	// config): status fails the check without leaking.
	bad := filepath.Join(t.TempDir(), "bad")
	os.WriteFile(bad, []byte("tsk-bad-key-000111\n"), 0o600)
	r = run(t, append(env, "TYPESAFE_API_KEY_FILE="+bad), "", "jev", "key", "status")
	if r.Code != 1 || !strings.Contains(r.Stdout, "source: TYPESAFE_API_KEY_FILE") || !strings.Contains(r.Stdout, "FAILED") {
		t.Fatalf("status with bad key: %+v", r)
	}
	noLeak(t, r, "tsk-bad-key-000111")

	r = run(t, env, "", "jev", "key", "rm")
	if b, _ := os.ReadFile(keyPath); r.Code != 0 || strings.Contains(string(b), key) {
		t.Fatalf("key rm: %+v", r)
	}
	if r := run(t, env, "", "jev", "key", "rm"); r.Code != 1 {
		t.Fatalf("key rm with no key: %+v", r)
	}

	// key import: adopt the key from the environment into the config.
	kf := writeFile(t, t.TempDir(), "env.key", key+"\n")
	r = run(t, append(env, "TYPESAFE_API_KEY_FILE="+kf), "", "jev", "key", "import")
	if r.Code != 0 || !strings.Contains(r.Stderr, "importing") || !strings.Contains(r.Stderr, "delete "+kf) {
		t.Fatalf("key import: %+v", r)
	}
	noLeak(t, r, key)
	if b, _ := os.ReadFile(keyPath); !strings.Contains(string(b), "key = "+key) {
		t.Fatalf("config after key import:\n%s", b)
	}
	if fi, _ := os.Stat(keyPath); !modeIs(fi, 0o600) {
		t.Fatalf("config mode after import: %v", fi.Mode())
	}
	if r := run(t, env, "", "jev", "key", "import"); r.Code != 2 || !strings.Contains(r.Stderr, "nothing to import") {
		t.Fatalf("import with nothing in the environment: %+v", r)
	}
	// The removed options are gone.
	if r := run(t, env, "", "jev", "key", "set", "--file", kf); r.Code != 2 {
		t.Fatalf("--file should be gone: %+v", r)
	}
	if r := run(t, env, "yes", "isv", "--key-file", kf, "q"); r.Code != 2 {
		t.Fatalf("--key-file should be gone: %+v", r)
	}
}
