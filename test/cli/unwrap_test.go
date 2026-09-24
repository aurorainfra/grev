package clitest

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/aurorainfra/grev/internal/jev"
	"github.com/aurorainfra/grev/internal/jevtest"
)

var lineRef = regexp.MustCompile(`line (L\d{4})`)

// continuation looks up the line a join question is about (the first id in
// "Does line L0003 pick up mid-sentence … line L0002?") in the tagged
// document: a p=X marker wins, a lowercase start is a continuation (0.9), and
// anything else starts afresh (0.05).
func continuation(state any, q jev.Question) jev.Answer {
	m := lineRef.FindStringSubmatch(instrText(q))
	if m == nil {
		return noul(0)
	}
	text := strings.TrimLeft(tagged(stateDoc(state))[m[1]], " \t>")
	if p, ok := marked(text); ok {
		return noul(p)
	}
	if r, _ := utf8.DecodeRuneInString(text); unicode.IsLower(r) {
		return noul(0.9)
	}
	return noul(0.05)
}

func TestUnwrap(t *testing.T) {
	_, env := fake(t, jevtest.Options{Oracle: continuation})
	cases := []struct {
		name  string
		stdin string
		args  []string
		out   string
	}{
		{"joins wrapped sentences",
			"The quick brown fox\njumps over the lazy dog.\nA new sentence starts here.\n", nil,
			"The quick brown fox jumps over the lazy dog.\nA new sentence starts here.\n"},
		{"blank lines kept", "one para\nwraps here.\n\nsecond para\ncontinues.\n", nil,
			"one para wraps here.\n\nsecond para continues.\n"},
		{"code fences untouched", "Intro that\ncontinues.\n```\ncode line\nmore code\n```\nafter the fence\n", nil,
			"Intro that continues.\n```\ncode line\nmore code\n```\nafter the fence\n"},
		{"list items", "- first item\n- second item\n  wraps here\n1. numbered\n2) also\n", nil,
			"- first item\n- second item wraps here\n1. numbered\n2) also\n"},
		{"headings, rules and tables", "# Title\nlowercase body\n---\nbody\n| a | b |\n| c | d |\n", nil,
			"# Title\nlowercase body\n---\nbody\n| a | b |\n| c | d |\n"},
		{"indented code", "text:\n    code()\n    more()\nend\n", nil, "text:\n    code()\n    more()\nend\n"},
		{"quotes continue", "> quoted line\n> continued quote\n", nil, "> quoted line continued quote\n"},
		{"dangling threshold", "no punctuation\np=0.3 continues\n", nil, "no punctuation p=0.3 continues\n"},
		{"terminal threshold", "ends here.\np=0.3 continues\n", nil, "ends here.\np=0.3 continues\n"},
		{"custom thresholds", "no punctuation\np=0.3 a\nends here.\np=0.3 b\n", []string{"-t", "0.4,0.2"},
			"no punctuation\np=0.3 a ends here. p=0.3 b\n"},
		{"join separator", "a line\nwraps\n", []string{"-j", " / "}, "a line / wraps\n"},
		{"no dehyphen by default", "an exam-\nple here\n", nil, "an exam- ple here\n"},
		{"dehyphen", "an exam-\nple here\n", []string{"--dehyphen"}, "an example here\n"},
		{"scores", "a line\nwraps\nNew one\n", []string{"-s"}, "-\ta line\n0.90\twraps\n0.05\tNew one\n"},
		{"streaming", "The quick brown fox\njumps over.\nNew.\n", []string{"--line-buffered", "--flush", "5ms"},
			"The quick brown fox jumps over.\nNew.\n"},
		{"about", "a line\nwraps\n", []string{"--about", "an email"}, "a line wraps\n"},
		{"no trailing newline", "a line\nwraps", nil, "a line wraps\n"},
		{"empty", "", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expect(t, run(t, env, tc.stdin, "unwrap", tc.args...), 0, tc.out)
		})
	}
	f := writeFile(t, t.TempDir(), "memo.txt", "a line\nwraps\n")
	expect(t, run(t, env, "", "unwrap", f), 0, "a line wraps\n")

	usageError(t, env, "", "unwrap", "-t", "0.5")
	usageError(t, env, "", "unwrap", "a", "b")
	in := "a line\nwraps\n"
	declineQuote(t, jevtest.Options{}, in, "unwrap")
	overBudget(t, jevtest.Options{}, in, "unwrap")
	// Lines the code settles on its own must not leak out before -Q is answered.
	settled := "# Title\npara one\n\nfoo\nbar\n"
	declineQuote(t, jevtest.Options{}, settled, "unwrap")
	overBudget(t, jevtest.Options{}, settled, "unwrap")
}

// TestUnwrapLongStream: a long input streams through bounded buffers and
// comes out the same as in batch mode.
func TestUnwrapLongStream(t *testing.T) {
	_, env := fake(t, jevtest.Options{Oracle: continuation})
	var in, want strings.Builder
	for i := 0; i < 1500; i++ {
		fmt.Fprintf(&in, "Line %d starts\ncontinues here.\n", i)
		fmt.Fprintf(&want, "Line %d starts continues here.\n", i)
		if i%100 == 99 {
			in.WriteString("\n")
			want.WriteString("\n")
		}
	}
	expect(t, run(t, env, in.String(), "unwrap"), 0, want.String())
	expect(t, run(t, env, in.String(), "unwrap", "--line-buffered", "--flush", "5ms"), 0, want.String())
}

// TestUnwrapRequests: breaks the code settles never reach the model, and the
// rest share one id-tagged document state.
func TestUnwrapRequests(t *testing.T) {
	var mu sync.Mutex
	var qs []jev.Question
	var states []any
	srv, env := fake(t, jevtest.Options{Oracle: func(state any, q jev.Question) jev.Answer {
		mu.Lock()
		qs, states = append(qs, q), append(states, state)
		mu.Unlock()
		return continuation(state, q)
	}})
	expect(t, run(t, env, "para one\n\n# Head\n- item\n- item\n", "unwrap"), 0, "para one\n\n# Head\n- item\n- item\n")
	if srv.Requests() != 0 {
		t.Fatalf("nothing needed the model, yet %d requests", srv.Requests())
	}
	expect(t, run(t, env, "one\ntwo\nthree\n", "unwrap"), 0, "one two three\n")
	if srv.Requests() != 1 || len(qs) != 2 {
		t.Fatalf("%d requests, %d questions", srv.Requests(), len(qs))
	}
	doc := stateDoc(states[0])
	if doc != "L0001| one\nL0002| two\nL0003| three\n" || states[0] != states[1] {
		t.Fatalf("state: %q", doc)
	}
	// The server answers questions in no particular order.
	texts := instrText(qs[0]) + "|" + instrText(qs[1])
	if !strings.Contains(texts, "Does line L0002 pick up mid-sentence") ||
		!strings.Contains(texts, "Does line L0003 pick up mid-sentence") ||
		instrMap(qs[0]) != nil || qs[0].Criteria == nil {
		t.Fatalf("questions: %#v", qs)
	}
}

func TestUnwrapErrors(t *testing.T) {
	_, env := fake(t, failAll)
	r := run(t, env, "a line\nwraps\n", "unwrap")
	// Failed breaks stay line breaks.
	if r.Code != 2 || r.Stdout != "a line\nwraps\n" || !strings.Contains(r.Stderr, "failed") {
		t.Fatalf("failed breaks: %+v", r)
	}
}
