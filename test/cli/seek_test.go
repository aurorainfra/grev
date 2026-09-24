package clitest

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aurorainfra/grev/internal/jev"
	"github.com/aurorainfra/grev/internal/jevtest"
)

// pathOracle reads the query (state.looking_for) as a list of path words.
// Choice: the first entry whose name (without a trailing "/", each part of a
// compressed "a/b") is one of those words, then "." if the query says HERE,
// else "(none)". Noul (big directories, --verify): 0.95 when the entry's or
// path's base name is one of the words.
func pathOracle(state any, q jev.Question) jev.Answer {
	lf, _ := state.(map[string]any)["looking_for"].(string)
	words := map[string]bool{}
	for _, w := range strings.Fields(lf) {
		words[w] = true
	}
	if q.Type == jev.TypeNoul {
		m := instrMap(q)
		name, _ := m["entry"].(string)
		if p, ok := m["path"].(string); ok {
			name = path.Base(strings.TrimSuffix(p, "/"))
		}
		if words[strings.TrimSuffix(name, "/")] {
			return noul(0.95)
		}
		return noul(0.05)
	}
	opts, _ := q.Criteria.(jev.Opts)
	for _, o := range opts {
		if o.Key == "." || o.Key == "(none)" {
			continue
		}
		all := true
		for _, part := range strings.Split(strings.TrimSuffix(o.Key, "/"), "/") {
			all = all && words[part]
		}
		if all {
			return choose(opts, o.Key, 0.8)
		}
	}
	for _, o := range opts {
		if o.Key == "." && words["HERE"] {
			return choose(opts, ".", 0.8)
		}
	}
	for _, o := range opts {
		if o.Key == "(none)" {
			return choose(opts, "(none)", 0.8)
		}
	}
	return choose(opts, "", 0)
}

// tree creates files (paths relative to a new temp dir) and returns the dir.
func tree(t *testing.T, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("content of "+f+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSeek(t *testing.T) {
	_, env := fake(t, jevtest.Options{Oracle: pathOracle})
	root := tree(t, "docs/readme.txt", "notes.txt", "src/server/http.go", "src/server/util.go",
		"src/client/fetch.go", ".hidden/secret.txt", "node_modules/pkg/index.js", "deep/a/b/c/leaf.txt")
	j := func(p string) string { return filepath.Join(root, p) }
	cases := []struct {
		name string
		args []string
		code int
		out  string
	}{
		{"file", []string{"src server http.go", root}, 0, j("src/server/http.go") + "\n"},
		{"scores", []string{"-s", "src server http.go", root}, 0, "0.90\t" + j("src/server/http.go") + "\n"},
		{"directory itself", []string{"src server HERE", root}, 0, j("src/server") + "\n"},
		{"dirs only", []string{"-d", "src server", root}, 0, j("src/server") + "\n"},
		{"files only", []string{"-f", "notes.txt", root}, 0, j("notes.txt") + "\n"},
		{"compressed chain", []string{"deep a b c leaf.txt", root}, 0, j("deep/a/b/c/leaf.txt") + "\n"},
		// With -d, a chain is not compressed: every directory on it can be the answer.
		{"dirs only inside a chain", []string{"-d", "deep HERE", root}, 0, j("deep") + "\n"},
		{"about", []string{"--about", "a test tree", "src server http.go", root}, 0, j("src/server/http.go") + "\n"},
		{"nothing matches", []string{"banana bread", root}, 1, ""},
		{"hidden skipped", []string{".hidden secret.txt", root}, 1, ""},
		{"hidden with -a", []string{"-a", ".hidden secret.txt", root}, 0, j(".hidden/secret.txt") + "\n"},
		{"node_modules skipped", []string{"node_modules pkg index.js", root}, 1, ""},
		{"verify", []string{"--verify", "-s", "src server http.go", root}, 0, "0.95\t" + j("src/server/http.go") + "\n"},
		{"threshold", []string{"-t", "0.95", "src server http.go", root}, 1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expect(t, run(t, env, "", "seek", tc.args...), tc.code, tc.out)
		})
	}

	// Several results, best first.
	r := run(t, env, "", "seek", "-m", "3", "-s", "-t", "0", "src server http.go", root)
	lines := strings.Split(strings.TrimSpace(r.Stdout), "\n")
	if r.Code != 0 || len(lines) < 2 || lines[0] != "0.90\t"+j("src/server/http.go") {
		t.Fatalf("-m 3: %+v", r)
	}

	usageError(t, env, "", "seek")
	usageError(t, env, "", "seek", "-d", "-f", "x", root)
	usageError(t, env, "", "seek", "-b", "0", "x", root)
	usageError(t, env, "", "seek", "-m", "0", "x", root)
	if r := run(t, env, "", "seek", "x", j("notes.txt")); r.Code != 2 || !strings.Contains(r.Stderr, "not a directory") {
		t.Fatalf("not a directory: %+v", r)
	}
	if r := run(t, env, "", "seek", "x", t.TempDir()); r.Code != 2 || !strings.Contains(r.Stderr, "nothing to search") {
		t.Fatalf("empty directory: %+v", r)
	}
	declineQuote(t, jevtest.Options{}, "", "seek", "src server http.go", root)
	overBudget(t, jevtest.Options{}, "", "seek", "src server http.go", root)
}

func TestSeekPathList(t *testing.T) {
	_, env := fake(t, jevtest.Options{Oracle: pathOracle})
	list := "src/server/http.go\n./src/client/fetch.go\ndocs/readme.txt\n\n../outside.txt\ndocs/\n"
	expect(t, run(t, env, list, "seek", "src server http.go", "-"), 0, "src/server/http.go\n")
	expect(t, run(t, env, list, "seek", "-d", "docs", "-"), 0, "docs\n")
	expect(t, run(t, env, list, "seek", "outside.txt", "-"), 1, "")
	// Absolute paths print back absolute, directories on the way included.
	abs := "/etc/ssh/sshd_config\n/etc/hosts\n/usr/bin/env\n"
	expect(t, run(t, env, abs, "seek", "etc ssh sshd_config", "-"), 0, "/etc/ssh/sshd_config\n")
	expect(t, run(t, env, abs, "seek", "-d", "etc ssh HERE", "-"), 0, "/etc/ssh\n")
	if r := run(t, env, "\n", "seek", "x", "-"); r.Code != 2 {
		t.Fatalf("empty list: %+v", r)
	}
}

// TestSeekBigDirectory: more entries than one Choice takes fall back to one
// Noul per entry, used as is (not normalized against hundreds of others), so
// the right entry keeps its probability and passes the default threshold.
func TestSeekBigDirectory(t *testing.T) {
	var mu sync.Mutex
	nouls := 0
	srv, env := fake(t, jevtest.Options{Oracle: func(state any, q jev.Question) jev.Answer {
		if q.Type == jev.TypeNoul {
			mu.Lock()
			nouls++
			mu.Unlock()
		}
		return pathOracle(state, q)
	}})
	var files []string
	for i := 0; i < 300; i++ {
		files = append(files, fmt.Sprintf("f%03d.txt", i))
	}
	root := tree(t, files...)
	expect(t, run(t, env, "", "seek", "-s", "f123.txt", root), 0, "0.95\t"+filepath.Join(root, "f123.txt")+"\n")
	// One Noul per entry; "(none)" is 1 − the best entry, computed in code.
	if nouls != 300 || srv.Requests() != 3 {
		t.Fatalf("%d nouls in %d requests, want 300 in 3", nouls, srv.Requests())
	}
}

// TestSeekBigSubdirectory: below the root, a big directory also gets a direct
// question about itself (".") instead of a negated or relative one.
func TestSeekBigSubdirectory(t *testing.T) {
	var mu sync.Mutex
	var here []string
	_, env := fake(t, jevtest.Options{Oracle: func(state any, q jev.Question) jev.Answer {
		if m := instrMap(q); q.Type == jev.TypeNoul && m["entry"] == nil {
			mu.Lock()
			here = append(here, instrText(q))
			mu.Unlock()
			return noul(0.9)
		}
		return pathOracle(state, q)
	}})
	var files []string
	for i := 0; i < 300; i++ {
		files = append(files, fmt.Sprintf("big/f%03d.txt", i))
	}
	root := tree(t, append(files, "other/x.txt")...)
	expect(t, run(t, env, "", "seek", "big HERE", root), 0, filepath.Join(root, "big")+"\n")
	mu.Lock()
	defer mu.Unlock()
	if len(here) != 1 || here[0] != "Is `directory` itself what `looking_for` describes?" {
		t.Fatalf("questions about the directory itself: %q", here)
	}
}

// TestSeekPeekBudget: --peek shows file heads, but a directory with many
// files still makes one request of bounded size.
func TestSeekPeekBudget(t *testing.T) {
	var mu sync.Mutex
	var heads [][]any
	srv, env := fake(t, jevtest.Options{Oracle: func(state any, q jev.Question) jev.Answer {
		opts, _ := q.Criteria.(jev.Opts)
		mu.Lock()
		for _, o := range opts {
			if m, ok := o.Desc.(map[string]any); ok {
				if h, ok := m["file_starting_with"].([]any); ok {
					heads = append(heads, h)
				}
			}
		}
		mu.Unlock()
		return pathOracle(state, q)
	}})
	dir := t.TempDir()
	line := strings.Repeat("x", 100) + "\n"
	for i := 0; i < 200; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d.txt", i)), []byte(strings.Repeat(line, 50)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	expect(t, run(t, env, "", "seek", "--peek", "50", "f007.txt", dir), 0, filepath.Join(dir, "f007.txt")+"\n")
	mu.Lock()
	n := len(heads)
	heads = nil
	mu.Unlock()
	if n != 200 {
		t.Fatalf("%d files had heads, want 200", n)
	}
	for _, r := range srv.Log() {
		if r.Bytes > 70_000 {
			t.Fatalf("request of %d bytes: --peek ignored the budget", r.Bytes)
		}
	}
	// With few files, the whole --peek fits.
	small := tree(t, "a.txt", "b.txt")
	if err := os.WriteFile(filepath.Join(small, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	expect(t, run(t, env, "", "seek", "--peek", "2", "a.txt", small), 0, filepath.Join(small, "a.txt")+"\n")
	mu.Lock()
	defer mu.Unlock()
	if len(heads) != 2 || fmt.Sprint(heads[0]) != "[one two]" {
		t.Fatalf("heads: %v", heads)
	}
}

// TestSeekRequests: each level is one request, with one Choice per open path.
func TestSeekRequests(t *testing.T) {
	var mu sync.Mutex
	var qs []jev.Question
	srv, env := fake(t, jevtest.Options{Oracle: func(state any, q jev.Question) jev.Answer {
		mu.Lock()
		qs = append(qs, q)
		mu.Unlock()
		return pathOracle(state, q)
	}})
	root := tree(t, "a/x.txt", "b/y.txt")
	expect(t, run(t, env, "", "seek", "-b", "1", "a x.txt", root), 0, filepath.Join(root, "a/x.txt")+"\n")
	if srv.Requests() != 2 {
		t.Fatalf("two levels should be two requests, got %d", srv.Requests())
	}
	opts, _ := qs[0].Criteria.(jev.Opts)
	var keys []string
	for _, o := range opts {
		keys = append(keys, o.Key)
	}
	// No "." at the root, and always a way out.
	if strings.Join(keys, " ") != "a/ b/ (none)" {
		t.Fatalf("root options: %q", keys)
	}
	if states := fmt.Sprint(qs[0].Instructions); !strings.Contains(states, "directory") {
		t.Fatalf("instructions: %v", states)
	}
	opts, _ = qs[1].Criteria.(jev.Opts)
	keys = keys[:0]
	for _, o := range opts {
		keys = append(keys, o.Key)
	}
	if strings.Join(keys, " ") != "x.txt . (none)" {
		t.Fatalf("level two options: %q", keys)
	}
}

func TestSeekErrors(t *testing.T) {
	_, env := fake(t, failAll)
	root := tree(t, "a/x.txt", "b/y.txt")
	if r := run(t, env, "", "seek", "a x.txt", root); r.Code != 2 || r.Stdout != "" {
		t.Fatalf("failed request: %+v", r)
	}
}
