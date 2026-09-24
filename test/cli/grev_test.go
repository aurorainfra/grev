package clitest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aurorainfra/grev/internal/jevtest"
)

// The fake oracle answers a record containing "yes" with 0.95, "maybe" with
// 0.5, "p=0.73" with 0.73, and anything else with 0.05.
const fruits = "apple yes\nbanana no\ncherry yes\n"

func TestGrevBasic(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	cases := []struct {
		name  string
		stdin string
		args  []string
		code  int
		out   string
	}{
		{"select", fruits, []string{"is a fruit"}, 0, "apple yes\ncherry yes\n"},
		{"invert", fruits, []string{"-v", "is a fruit"}, 0, "banana no\n"},
		{"count", fruits, []string{"-c", "is a fruit"}, 0, "2\n"},
		{"line numbers", fruits, []string{"-n", "is a fruit"}, 0, "1:apple yes\n3:cherry yes\n"},
		{"scores", "a p=0.73\nb p=0.20\n", []string{"-s", "q"}, 0, "0.73\ta p=0.73\n"},
		{"threshold", "a p=0.73\nb p=0.20\n", []string{"-t", "0.8", "q"}, 1, ""},
		{"low threshold", "a p=0.73\nb p=0.20\n", []string{"-t0.1", "q"}, 0, "a p=0.73\nb p=0.20\n"},
		{"none", "x no\n", []string{"q"}, 1, ""},
		{"empty input", "", []string{"q"}, 1, ""},
		{"quiet match", fruits, []string{"-q", "q"}, 0, ""},
		{"quiet none", "x no\n", []string{"-q", "q"}, 1, ""},
		{"max count", "a yes\nb yes\nc yes\n", []string{"-m1", "q"}, 0, "a yes\n"},
		{"max count trailing context", "a yes\nb yes\nc no\n", []string{"-m1", "-n", "-A1", "q"}, 0, "1:a yes\n2-b yes\n"},
		{"placeholder", fruits, []string{"Is {} a fruit?"}, 0, "apple yes\ncherry yes\n"},
		{"fields", "x\tyes\ny\tno\n", []string{"-d", `\t`, "Is {1} like {2}?"}, 0, "x\tyes\n"},
		{"crlf kept out of the answer", "a yes\r\nb no\r\n", []string{"q"}, 0, "a yes\n"},
		{"nul records", "a\nyes\x00b no\x00", []string{"-z", "q"}, 0, "a\nyes\x00"},
		{"paragraphs", "a\nyes\n\nb\nno\n", []string{"--para", "q"}, 0, "a\nyes\n\n"},
		{"window", "a yes\nb no\n", []string{"-W1", "q"}, 0, "a yes\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expect(t, run(t, env, tc.stdin, "grev", tc.args...), tc.code, tc.out)
		})
	}
}

func TestGrevContext(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	in := "a no\nb no\nc yes\nd no\ne no\nf no\ng yes\nh no\n"
	want := "2-b no\n3:c yes\n4-d no\n--\n6-f no\n7:g yes\n8-h no\n"
	expect(t, run(t, env, in, "grev", "-n", "-C1", "q"), 0, want)
	// Adjacent groups merge without a separator.
	expect(t, run(t, env, "a yes\nb no\nc yes\n", "grev", "-A1", "q"), 0, "a yes\nb no\nc yes\n")
	expect(t, run(t, env, in, "grev", "-B2", "-m1", "q"), 0, "a no\nb no\nc yes\n")
	// With scores, context records show their own probability.
	expect(t, run(t, env, "a no\nb yes\n", "grev", "-s", "-B1", "q"), 0, "0.05\ta no\n0.95\tb yes\n")
}

func TestGrevFiles(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	dir := t.TempDir()
	f1 := writeFile(t, dir, "f1.txt", "hello yes\nother\n")
	f2 := writeFile(t, dir, "f2.txt", "nothing here\n")
	expect(t, run(t, env, "", "grev", "q", f1, f2), 0, f1+":hello yes\n")
	expect(t, run(t, env, "", "grev", "-h", "q", f1, f2), 0, "hello yes\n")
	expect(t, run(t, env, "", "grev", "-H", "q", f1), 0, f1+":hello yes\n")
	expect(t, run(t, env, "", "grev", "-c", "q", f1, f2), 0, f1+":1\n"+f2+":0\n")
	expect(t, run(t, env, "", "grev", "-l", "q", f1, f2), 0, f1+"\n")
	expect(t, run(t, env, "", "grev", "-L", "q", f1, f2), 0, f2+"\n")
	expect(t, run(t, env, "", "grev", "-l", "-s", "q", f1, f2), 0, "0.95\t"+f1+"\n")
	expect(t, run(t, env, f1+"\n"+f2+"\n", "grev", "-l", "--files-from", "-", "q"), 0, f1+"\n")
	// -l honours -q (silent, exit status only) and -m (at most N names).
	expect(t, run(t, env, "", "grev", "-lq", "q", f1, f2), 0, "")
	expect(t, run(t, env, "", "grev", "-lq", "q", f2), 1, "")
	f3 := writeFile(t, dir, "f3.txt", "yes again\n")
	expect(t, run(t, env, "", "grev", "-l", "-m", "1", "q", f1, f2, f3), 0, f1+"\n")
	expect(t, run(t, env, "", "grev", "-l", "q", f1, f2, f3), 0, f1+"\n"+f3+"\n")
	// Placeholders must refer to something: {N} needs -d.
	usageError(t, env, "a\n", "grev", "Is {1} a fruit?")
	expect(t, run(t, env, "x,yes\n", "grev", "-d", ",", "Is {2} a fruit?"), 0, "x,yes\n")
	r := run(t, env, "", "grev", "q", filepath.Join(dir, "missing"), f1)
	if r.Code != 2 || !strings.Contains(r.Stderr, "missing") || !strings.Contains(r.Stdout, "hello yes") {
		t.Fatalf("a missing file should be reported and exit 2 while others still print: %+v", r)
	}
}

func TestGrevUncertain(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	expect(t, run(t, env, "x maybe\ny no\n", "grev", "--band", "0.3:0.7", "q"), 3, "")
	unc := filepath.Join(t.TempDir(), "unc.txt")
	expect(t, run(t, env, "a yes\nb maybe\nc no\n", "grev", "--uncertain", unc, "q"), 0, "a yes\n")
	if b, _ := os.ReadFile(unc); string(b) != "b maybe\n" {
		t.Fatalf("uncertain file = %q", b)
	}
	// -v selects only confident no's; the uncertain ones stay out.
	expect(t, run(t, env, "a yes\nb maybe\nc no\n", "grev", "-v", "--band", "0.3:0.7", "q"), 0, "c no\n")
	r := run(t, env, "", "grev", "--band", "0.9:0.1", "q")
	if r.Code != 2 {
		t.Fatalf("bad band should be a usage error: %+v", r)
	}
}

func TestGrevStreaming(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	expect(t, run(t, env, fruits, "grev", "--line-buffered", "--flush", "10ms", "q"), 0, "apple yes\ncherry yes\n")
	expect(t, run(t, env, "a yes\nb yes\n", "grev", "--line-buffered", "-m1", "q"), 0, "a yes\n")
	r := run(t, env, fruits, "grev", "--line-buffered", "-Q", "q")
	if r.Code != 2 || !strings.Contains(r.Stderr, "streaming") {
		t.Fatalf("-Q with streaming should be refused: %+v", r)
	}
}

func TestGrevQuote(t *testing.T) {
	dir := t.TempDir()
	no := writeFile(t, dir, "no", "n\n")
	yes := writeFile(t, dir, "yes", "y\n")

	srv, env := fake(t, jevtest.Options{}, "GREV_TTY="+no)
	r := run(t, env, fruits, "grev", "-Q", "q")
	if r.Code != 4 || r.Stdout != "" || !strings.Contains(r.Stderr, "grev quote: 3 questions → 1 request(s)") ||
		!strings.Contains(r.Stderr, "Proceed?") {
		t.Fatalf("declined quote: %+v", r)
	}
	if srv.Requests() != 0 {
		t.Fatalf("declining must send nothing, sent %d", srv.Requests())
	}

	srv, env = fake(t, jevtest.Options{}, "GREV_TTY="+yes)
	expect(t, run(t, env, fruits, "grev", "-Q", "q"), 0, "apple yes\ncherry yes\n")
	if srv.Requests() != 1 {
		t.Fatalf("accepted quote sent %d requests", srv.Requests())
	}

	srv, env = fake(t, jevtest.Options{}, "GREV_TTY="+filepath.Join(dir, "no-such-tty"))
	r = run(t, env, fruits, "grev", "-Q", "q")
	if r.Code != 2 || !strings.Contains(r.Stderr, "--max-cost") || srv.Requests() != 0 {
		t.Fatalf("-Q without a terminal: %+v (requests %d)", r, srv.Requests())
	}
}

func TestGrevMaxCost(t *testing.T) {
	srv, env := fake(t, jevtest.Options{})
	r := run(t, env, fruits, "grev", "--max-cost", "0.0000000001", "q")
	if r.Code != 4 || !strings.Contains(r.Stderr, "exceeds --max-cost") || srv.Requests() != 0 {
		t.Fatalf("over-budget run should be refused up front: %+v (requests %d)", r, srv.Requests())
	}
	expect(t, run(t, env, fruits, "grev", "--max-cost", "1", "q"), 0, "apple yes\ncherry yes\n")
	r = run(t, env, fruits, "grev", "--max-cost", "-1", "q")
	if r.Code != 2 {
		t.Fatalf("negative budget should be a usage error: %+v", r)
	}
}

func TestGrevProgressSummary(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	r := run(t, env, fruits, "grev", "-p", "q")
	expect(t, r, 0, "apple yes\ncherry yes\n")
	// stderr is not a terminal here: no overlay, just the summary line.
	if !strings.Contains(r.Stderr, "grev: 3 q · 1 req · ") || strings.Contains(r.Stderr, "\x1b[") {
		t.Fatalf("summary: %q", r.Stderr)
	}
}

func TestGrevParallelAndPacking(t *testing.T) {
	srv, env := fake(t, jevtest.Options{}, "GREV_MAX_Q=2")
	var b strings.Builder
	var want strings.Builder
	for i := 0; i < 25; i++ {
		if i%3 == 0 {
			b.WriteString("line yes\n")
			want.WriteString("line yes\n")
		} else {
			b.WriteString("line no\n")
		}
	}
	expect(t, run(t, env, b.String(), "grev", "-Jmax", "q"), 0, want.String())
	if srv.Requests() != 13 {
		t.Fatalf("25 records at 2 per request should be 13 requests, got %d", srv.Requests())
	}
}

func TestGrevErrors(t *testing.T) {
	_, env := fake(t, jevtest.Options{Key: "other-key"}, "TYPESAFE_API_KEY=wrong-key-123")
	r := run(t, env, fruits, "grev", "q")
	if r.Code != 2 || !strings.Contains(r.Stderr, "401") || !strings.Contains(r.Stderr, "jev key status") {
		t.Fatalf("bad key: %+v", r)
	}
	noLeak(t, r, "wrong-key-123")

	srv := jevtest.New(t, jevtest.Options{})
	r = run(t, harnessEnvNoKey(t, srv.URL), fruits, "grev", "q")
	if r.Code != 2 || !strings.Contains(r.Stderr, "no API key") {
		t.Fatalf("no key: %+v", r)
	}

	_, env = fake(t, jevtest.Options{})
	for _, args := range [][]string{{}, {"--nope", "q"}, {"-J0", "q"}, {"--flush", "soon", "q"}} {
		if r := run(t, env, "", "grev", args...); r.Code != 2 || !strings.Contains(r.Stderr, "grev --help") {
			t.Errorf("grev %q: want usage error, got %+v", args, r)
		}
	}
	r = run(t, env, "", "grev", "--help")
	if r.Code != 0 || !strings.Contains(r.Stdout, "usage: grev") || !strings.Contains(r.Stdout, "--max-cost") {
		t.Fatalf("--help: %+v", r)
	}
	r = run(t, env, "", "grev", "--version")
	if r.Code != 0 || !strings.HasPrefix(r.Stdout, "grev ") {
		t.Fatalf("--version: %+v", r)
	}
}
