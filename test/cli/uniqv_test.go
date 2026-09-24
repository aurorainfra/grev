package clitest

import (
	"strings"
	"sync"
	"testing"

	"github.com/aurorainfra/grev/internal/jev"
	"github.com/aurorainfra/grev/internal/jevtest"
)

// sameFirstWord judges a pair the same when both records start with the same
// word (case-insensitive); a p=X marker in the second record overrides.
func sameFirstWord(state any, q jev.Question) jev.Answer {
	m := instrMap(q)
	a, _ := m["f1"].(string)
	b, _ := m["f2"].(string)
	if p, ok := marked(b); ok {
		return noul(p)
	}
	fa, fb := strings.Fields(strings.ToLower(a)), strings.Fields(strings.ToLower(b))
	if len(fa) > 0 && len(fb) > 0 && fa[0] == fb[0] {
		return noul(0.95)
	}
	return noul(0.05)
}

const companies = "Apple Inc\napple\nAPPLE computers\nGoogle LLC\ngoogle\nMicrosoft\n"

func TestUniqv(t *testing.T) {
	_, env := fake(t, jevtest.Options{Oracle: sameFirstWord})
	cases := []struct {
		name  string
		stdin string
		args  []string
		out   string
	}{
		{"collapse", companies, nil, "Apple Inc\nGoogle LLC\nMicrosoft\n"},
		{"count", companies, []string{"-c"}, "      3 Apple Inc\n      2 Google LLC\n      1 Microsoft\n"},
		{"repeated", companies, []string{"-d"}, "Apple Inc\nGoogle LLC\n"},
		{"all repeated", companies, []string{"-D"}, "Apple Inc\napple\nAPPLE computers\nGoogle LLC\ngoogle\n"},
		{"unique", companies, []string{"-u"}, "Microsoft\n"},
		{"scores", companies, []string{"-s"}, "-\tApple Inc\n0.05\tGoogle LLC\n0.05\tMicrosoft\n"},
		{"all repeated with scores", "a x\na y\n", []string{"-D", "-s"}, "-\ta x\n0.95\ta y\n"},
		{"threshold", companies, []string{"-t", "0.99"}, companies},
		{"marker threshold", "a\nb p=0.6\n", []string{"-t", "0.5"}, "a\n"},
		{"marker below threshold", "a\nb p=0.6\n", []string{"-t", "0.7"}, "a\nb p=0.6\n"},
		{"single record", "only\n", nil, "only\n"},
		{"empty", "", nil, ""},
		{"question", companies, []string{"Are {1} and {2} the same company?"}, "Apple Inc\nGoogle LLC\nMicrosoft\n"},
		{"streaming", companies, []string{"--line-buffered", "--flush", "5ms"}, "Apple Inc\nGoogle LLC\nMicrosoft\n"},
		{"streaming single", "only\n", []string{"--line-buffered"}, "only\n"},
		{"nul records", "a 1\x00a 2\x00b\x00", []string{"-z"}, "a 1\x00b\x00"},
		{"paragraphs", "a\none\n\na\ntwo\n\nb\n", []string{"--para", "-c"}, "      2 a\none\n\n      1 b\n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expect(t, run(t, env, tc.stdin, "uniqv", tc.args...), 0, tc.out)
		})
	}

	dir := t.TempDir()
	f := writeFile(t, dir, "c.txt", companies)
	expect(t, run(t, env, "", "uniqv", f), 0, "Apple Inc\nGoogle LLC\nMicrosoft\n")
	expect(t, run(t, env, "", "uniqv", "-d", "Are {1} and {2} the same?", f), 0, "Apple Inc\nGoogle LLC\n")

	usageError(t, env, companies, "uniqv", "no placeholders here", f)
	usageError(t, env, companies, "uniqv", "-c", "-D")
	usageError(t, env, companies, "uniqv", "a {1} {2}", f, "extra")
	usageError(t, env, companies, "uniqv", "Is {} a duplicate?")
	usageError(t, env, companies, "uniqv", "Are {1} and {3} the same?", f)
	if r := run(t, env, "", "uniqv", dir+"/missing"); r.Code != 2 {
		t.Errorf("missing file: %+v", r)
	}
	declineQuote(t, jevtest.Options{}, companies, "uniqv")
	overBudget(t, jevtest.Options{}, companies, "uniqv")
}

// TestUniqvRequest: one question per adjacent pair, the two records as f1/f2
// with {1}/{2} rewritten to them, all packed together.
func TestUniqvRequest(t *testing.T) {
	var mu sync.Mutex
	var qs []jev.Question
	srv, env := fake(t, jevtest.Options{Oracle: func(state any, q jev.Question) jev.Answer {
		mu.Lock()
		qs = append(qs, q)
		mu.Unlock()
		return sameFirstWord(state, q)
	}})
	expect(t, run(t, env, "a\nb\nc\n", "uniqv"), 0, "a\nb\nc\n")
	if srv.Requests() != 1 || len(qs) != 2 {
		t.Fatalf("%d requests, %d questions", srv.Requests(), len(qs))
	}
	m := instrMap(qs[0])
	if m["question"] != "Do `f1` and `f2` refer to the same thing?" || m["f1"] == m["f2"] {
		t.Fatalf("question: %#v", m)
	}
}

func TestUniqvErrors(t *testing.T) {
	_, env := fake(t, failAll)
	r := run(t, env, "a\na\n", "uniqv")
	// Failed comparisons keep records apart, and the exit is 2.
	if r.Code != 2 || r.Stdout != "a\na\n" || !strings.Contains(r.Stderr, "failed") {
		t.Fatalf("failed comparisons: %+v", r)
	}
}
