package clitest

import (
	"strings"
	"sync"
	"testing"

	"github.com/aurorainfra/grev/internal/jev"
	"github.com/aurorainfra/grev/internal/jevtest"
)

func TestOneof(t *testing.T) {
	_, env := fake(t, jevtest.Options{Oracle: keywordOracle})
	crash := "the app crashes on start, clearly a bug"
	cases := []struct {
		name  string
		stdin string
		args  []string
		code  int
		out   string
	}{
		{"pickv", crash, []string{"bug", "feature", "question"}, 0, "bug\n"},
		{"score", crash, []string{"-s", "bug", "feature", "question"}, 0, "0.80\tbug\n"},
		{"probs", crash, []string{"--probs", "bug", "feature", "question"}, 0, "0.90\tbug\n0.05\tfeature\n0.05\tquestion\n"},
		{"below threshold still prints", crash, []string{"-t", "0.9", "bug", "feature"}, 3, "bug\n"},
		{"unsure", "unsure: maybe a feature", []string{"-t", "0.5", "bug", "feature"}, 3, "feature\n"},
		{"other wins", "just saying hello", []string{"--other", "bug", "feature"}, 1, "other\n"},
		{"named other", "just saying hello", []string{"--other=misc", "bug", "feature"}, 1, "misc\n"},
		{"other loses", crash, []string{"--other", "bug", "feature"}, 0, "bug\n"},
		{"descriptions", crash, []string{"bug=something is broken", "feature=a new capability"}, 0, "bug\n"},
		{"about", crash, []string{"--about", "GitHub issues", "bug", "feature"}, 0, "bug\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expect(t, run(t, env, tc.stdin, "oneof", tc.args...), tc.code, tc.out)
		})
	}

	dir := t.TempDir()
	labels := writeFile(t, dir, "labels.tsv", "# comment\nbug\tsomething is broken\nfeature\n\nquestion\tasks how\n")
	expect(t, run(t, env, crash, "oneof", "-f", labels), 0, "bug\n")
	in := writeFile(t, dir, "issue.txt", "please add a feature for export")
	expect(t, run(t, env, "", "oneof", "-i", in, "bug", "feature"), 0, "feature\n")

	usageError(t, env, crash, "oneof", "bug")
	usageError(t, env, crash, "oneof", "bug", "bug")
	usageError(t, env, crash, "oneof", "bug", "=x")
	if r := run(t, env, "  \n", "oneof", "bug", "feature"); r.Code != 2 || !strings.Contains(r.Stderr, "empty input") {
		t.Errorf("empty input: %+v", r)
	}
	if r := run(t, env, crash, "oneof", "-i", dir+"/missing", "bug", "feature"); r.Code != 2 {
		t.Errorf("missing -i file: %+v", r)
	}
	declineQuote(t, jevtest.Options{}, crash, "oneof", "bug", "feature")
	overBudget(t, jevtest.Options{}, crash, "oneof", "bug", "feature")
	if r := run(t, env, crash, "oneof", "--help"); r.Code != 0 || !strings.Contains(r.Stdout, "usage: oneof") {
		t.Errorf("--help: %+v", r)
	}
}

// TestOneofRequest checks what the model is asked: the whole input as state,
// one Choice over the labels in order, descriptions attached, escape last.
func TestOneofRequest(t *testing.T) {
	var mu sync.Mutex
	var gotState any
	var gotQ jev.Question
	srv, env := fake(t, jevtest.Options{Oracle: func(state any, q jev.Question) jev.Answer {
		mu.Lock()
		gotState, gotQ = state, q
		mu.Unlock()
		return keywordOracle(state, q)
	}})
	expect(t, run(t, env, "a bug report", "oneof", "-q", "What kind of issue is this?", "--other",
		"bug=something is broken", "feature"), 0, "bug\n")
	if srv.Requests() != 1 {
		t.Fatalf("one request expected, got %d", srv.Requests())
	}
	opts, _ := gotQ.Criteria.(jev.Opts)
	if gotState != "a bug report" || gotQ.Type != jev.TypeChoice || instrText(gotQ) != "What kind of issue is this?" ||
		len(opts) != 3 || opts[0].Key != "bug" || opts[0].Desc != "something is broken" ||
		opts[1].Key != "feature" || opts[1].Desc != nil || opts[2].Key != "other" {
		t.Fatalf("request: state %#v, question %#v", gotState, gotQ)
	}
}

func TestOneofErrors(t *testing.T) {
	_, env := fake(t, failAll)
	r := run(t, env, "a bug", "oneof", "bug", "feature")
	if r.Code != 2 || r.Stdout != "" {
		t.Fatalf("a failed request: %+v", r)
	}
}
