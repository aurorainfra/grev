package clitest

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/aurorainfra/grev/internal/jev"
	"github.com/aurorainfra/grev/internal/jevtest"
)

// pickOracle reads pick's document: the Choice goes to the first option that
// appears on a document line marked ANSWER (a line id, or a regex match), else
// to "none" if offered, else uniformly; the existence Noul is 0.95 when some
// line says ANSWER, 0.4 when one says PARTIAL, and 0.05 otherwise. A marked
// line that also says UNSURE gets confidence 0.3.
func pickOracle(state any, q jev.Question) jev.Answer {
	doc := stateDoc(state)
	match := map[string]string{} // candidate id → matched text (regex mode by id)
	if m, ok := state.(map[string]any); ok {
		if m["excerpts"] != nil {
			doc = fmt.Sprint(m["excerpts"])
		}
		cs, _ := m["candidates"].([]any)
		for _, c := range cs {
			cm, _ := c.(map[string]any)
			id, _ := cm["id"].(string)
			match[id], _ = cm["match"].(string)
		}
	}
	if q.Type == jev.TypeNoul {
		switch {
		case strings.Contains(doc, "ANSWER"):
			return noul(0.95)
		case strings.Contains(doc, "PARTIAL"):
			return noul(0.4)
		}
		return noul(0.05)
	}
	opts, _ := q.Criteria.(jev.Opts)
	for _, o := range opts {
		if text, ok := match[o.Key]; ok && strings.Contains(text, "ANSWER") {
			return choose(opts, o.Key, 0.8)
		}
		for _, line := range strings.Split(doc, "\n") {
			if strings.Contains(line, "ANSWER") && strings.Contains(line, o.Key) {
				conf := 0.8
				if strings.Contains(line, "UNSURE") {
					conf = 0.3
				}
				return choose(opts, o.Key, conf)
			}
		}
	}
	for _, o := range opts {
		if o.Key == "none" {
			return choose(opts, "none", 0.8)
		}
	}
	return choose(opts, "", 0)
}

func TestPickLines(t *testing.T) {
	_, env := fake(t, jevtest.Options{Oracle: pickOracle})
	doc := "intro\nthe answer is here ANSWER\noutro\n"
	cases := []struct {
		name  string
		stdin string
		args  []string
		code  int
		out   string
	}{
		{"best line", doc, []string{"q?"}, 0, "the answer is here ANSWER\n"},
		{"scores and numbers", doc, []string{"-s", "-n", "q?"}, 0, "0.90\t2:the answer is here ANSWER\n"},
		{"top two", doc, []string{"-m2", "-s", "q?"}, 0, "0.90\tthe answer is here ANSWER\n0.05\tintro\n"},
		{"context", "a\nb\nc ANSWER\nd\ne\n", []string{"-n", "-C1", "q?"}, 0, "2-b\n3:c ANSWER\n4-d\n"},
		{"no answer", "nothing\nto see\n", []string{"q?"}, 1, ""},
		{"no answer forced", "nothing\nto see\n", []string{"--force", "q?"}, 1, "nothing\n"},
		{"partial below threshold", "a PARTIAL hint\nb\n", []string{"q?"}, 1, ""},
		{"partial with low threshold is uncertain", "a PARTIAL hint\nb\n", []string{"-t", "0.3", "q?"}, 3, "a PARTIAL hint\n"},
		{"unsure choice", "x\ny ANSWER UNSURE\n", []string{"q?"}, 3, "y ANSWER UNSURE\n"},
		{"min-conf", "x\ny ANSWER UNSURE\n", []string{"--min-conf", "0.2", "q?"}, 0, "y ANSWER UNSURE\n"},
		{"blank lines skipped", "\n\nonly ANSWER\n\n", []string{"-n", "q?"}, 0, "3:only ANSWER\n"},
		{"empty", "", []string{"q?"}, 1, ""},
		{"paragraphs", "one\npara\n\ntwo ANSWER\npara\n", []string{"--para", "q?"}, 0, "two ANSWER\npara\n\n"},
		{"about", doc, []string{"--about", "a manual", "q?"}, 0, "the answer is here ANSWER\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expect(t, run(t, env, tc.stdin, "pickv", tc.args...), tc.code, tc.out)
		})
	}
	file := writeFile(t, t.TempDir(), "doc.txt", doc)
	expect(t, run(t, env, "", "pickv", "q?", file), 0, "the answer is here ANSWER\n")

	usageError(t, env, doc, "pickv")
	usageError(t, env, doc, "pickv", "a", "b", "c")
	declineQuote(t, jevtest.Options{}, doc, "pickv", "q?")
	overBudget(t, jevtest.Options{}, doc, "pickv", "q?")
}

// TestPickRounds searches more lines than one Choice takes: windows first,
// then a final round among each window's best lines.
func TestPickRounds(t *testing.T) {
	var mu sync.Mutex
	var choices []int // option counts per Choice asked
	srv, env := fake(t, jevtest.Options{Oracle: func(state any, q jev.Question) jev.Answer {
		if q.Type == jev.TypeChoice {
			opts, _ := q.Criteria.(jev.Opts)
			mu.Lock()
			choices = append(choices, len(opts))
			mu.Unlock()
		}
		return pickOracle(state, q)
	}})
	var b strings.Builder
	for i := 1; i <= 600; i++ {
		if i == 400 {
			b.WriteString("line 400 holds the ANSWER\n")
		} else {
			fmt.Fprintf(&b, "filler line %d\n", i)
		}
	}
	expect(t, run(t, env, b.String(), "pickv", "-n", "q?"), 0, "400:line 400 holds the ANSWER\n")
	// Round one: 3 windows (255+255+90), each its own state and request; the
	// final round: one request over 3 finalists per window.
	if srv.Requests() != 4 {
		t.Fatalf("requests = %d, want 4", srv.Requests())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(choices) != 4 || choices[0]+choices[1]+choices[2] != 600 || choices[3] != 9 {
		t.Fatalf("choices asked: %v", choices)
	}
}

func TestPickRegex(t *testing.T) {
	_, env := fake(t, jevtest.Options{Oracle: pickOracle})
	mail := "From: bob@example.com\nPlease send receipts to alice@example.org ANSWER\ncc: carol@example.net\n"
	re := `[a-z]+@[a-z]+\.[a-z]+`
	expect(t, run(t, env, mail, "pickv", "-e", re, "where do receipts go?"), 0, "alice@example.org\n")
	expect(t, run(t, env, mail, "pickv", "-n", "-s", "-e", re, "where do receipts go?"), 0, "0.90\t2:alice@example.org\n")
	// Several patterns; duplicates collapse to one candidate.
	expect(t, run(t, env, mail+"again alice@example.org\n", "pickv", "-e", re, "-e", `[0-9]+`, "q?"), 0, "alice@example.org\n")

	plain := "From: bob@example.com\ncc: carol@example.net\n"
	expect(t, run(t, env, plain, "pickv", "-e", re, "q?"), 1, "")
	expect(t, run(t, env, plain, "pickv", "--force", "-e", re, "q?"), 1, "bob@example.com\n")
	expect(t, run(t, env, "no addresses at all\n", "pickv", "-e", re, "q?"), 1, "")
	expect(t, run(t, env, "x ANSWER y@z.io UNSURE\n", "pickv", "-e", re, "q?"), 3, "y@z.io\n")

	var many strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&many, "id%d\n", i)
	}
	if r := run(t, env, many.String(), "pickv", "-e", `id[0-9]+`, "q?"); r.Code != 2 || !strings.Contains(r.Stderr, "narrow the regex") {
		t.Fatalf("too many matches: %+v", r)
	}
	usageError(t, env, mail, "pickv", "-e", "(", "q?")
	declineQuote(t, jevtest.Options{}, mail, "pickv", "-e", re, "q?")
}

// TestPickRegexQuestion: a question that isn't one is wrapped; long or
// multi-line matches are offered by id and printed verbatim.
func TestPickRegexQuestion(t *testing.T) {
	var mu sync.Mutex
	var qs []jev.Question
	_, env := fake(t, jevtest.Options{Oracle: func(state any, q jev.Question) jev.Answer {
		mu.Lock()
		qs = append(qs, q)
		mu.Unlock()
		return pickOracle(state, q)
	}})
	last := func() jev.Question {
		mu.Lock()
		defer mu.Unlock()
		q := qs[len(qs)-1]
		qs = nil
		return q
	}
	inv := "Subtotal $10.00\nTotal $12.50 ANSWER\n"
	expect(t, run(t, env, inv, "pickv", "-e", `\$[0-9.]+`, "the invoice total"), 0, "$12.50\n")
	if got := instrText(last()); got != "Which candidate is the invoice total?" {
		t.Fatalf("question: %q", got)
	}
	expect(t, run(t, env, inv, "pickv", "-e", `\$[0-9.]+`, "what is the total?"), 0, "$12.50\n")
	if got := instrText(last()); got != "what is the total?" {
		t.Fatalf("question: %q", got)
	}

	blocks := "intro\nBEGIN\nthe ANSWER\nEND\nBEGIN\nother\nEND\n"
	expect(t, run(t, env, blocks, "pickv", "-e", `(?s)BEGIN.*?END`, "which block?"), 0, "BEGIN\nthe ANSWER\nEND\n")
	opts, _ := last().Criteria.(jev.Opts)
	if len(opts) != 3 || opts[0].Key != "M001" || opts[1].Key != "M002" {
		t.Fatalf("options: %+v", opts)
	}
}

// TestPickOversizeLine: a record too large for any request is skipped with a
// warning; the search goes on.
func TestPickOversizeLine(t *testing.T) {
	_, env := fake(t, jevtest.Options{Oracle: pickOracle})
	in := strings.Repeat("x9", 20_000) + "\nthe real ANSWER\nmore\n" // ≈30k tokens
	r := run(t, env, in, "pickv", "-n", "q?")
	if r.Code != 0 || r.Stdout != "2:the real ANSWER\n" || !strings.Contains(r.Stderr, "skipping record 1") {
		t.Fatalf("oversize line: %+v", r)
	}
	if r := run(t, env, strings.Repeat("y9", 20_000)+"\n", "pickv", "q?"); r.Code != 2 {
		t.Fatalf("only an oversize line: %+v", r)
	}
}

// TestPickDescription: a QUESTION without "?" is asked as a description of
// the line, for both the choice and the existence check.
func TestPickDescription(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	_, env := fake(t, jevtest.Options{Oracle: func(state any, q jev.Question) jev.Answer {
		mu.Lock()
		seen = append(seen, q.Type+": "+instrText(q))
		mu.Unlock()
		return pickOracle(state, q)
	}})
	expect(t, run(t, env, "boot\nthe ANSWER\n", "pickv", "the most interesting line"), 0, "the ANSWER\n")
	sort.Strings(seen)
	want := []string{
		`choice: Which line of the document best fits this description: "the most interesting line"?`,
		`noul: Does any line of the document fit this description: "the most interesting line"?`,
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("questions: %q", seen)
	}
	seen = nil
	expect(t, run(t, env, "boot\nthe ANSWER\n", "pickv", "what happened?"), 0, "the ANSWER\n")
	sort.Strings(seen)
	if len(seen) != 2 || seen[0] != `choice: Which line of the document contains the answer to: "what happened?"?` {
		t.Fatalf("questions: %q", seen)
	}
}

// TestPickResplitsOverLimit: when the API rejects a window as over the
// context (the estimate was low), pick halves it and asks again.
func TestPickResplitsOverLimit(t *testing.T) {
	srv, env := fake(t, jevtest.Options{Oracle: pickOracle, MaxBody: 3000})
	var b strings.Builder
	for i := 1; i <= 200; i++ {
		if i == 150 {
			b.WriteString("the ANSWER is here\n")
			continue
		}
		fmt.Fprintf(&b, "line %d with some filler text\n", i)
	}
	r := run(t, env, b.String(), "pickv", "-n", "q?")
	if r.Code != 0 || r.Stdout != "150:the ANSWER is here\n" || strings.Contains(r.Stderr, "skipping") {
		t.Fatalf("re-split: %+v", r)
	}
	if srv.Count(400) == 0 {
		t.Fatal("expected over-limit rejections")
	}
}

// TestPickStripsEscapes: the model sees text without colour codes; the output
// keeps them.
func TestPickStripsEscapes(t *testing.T) {
	var mu sync.Mutex
	sawEsc := false
	_, env := fake(t, jevtest.Options{Oracle: func(state any, q jev.Question) jev.Answer {
		if strings.Contains(stateDoc(state), "\x1b") {
			mu.Lock()
			sawEsc = true
			mu.Unlock()
		}
		return pickOracle(state, q)
	}})
	in := "\x1b[2m12:00\x1b[0m boot\n\x1b[31m12:01 the ANSWER\x1b[0m\n"
	expect(t, run(t, env, in, "pickv", "q?"), 0, "\x1b[31m12:01 the ANSWER\x1b[0m\n")
	if sawEsc {
		t.Fatal("escape sequences reached the model")
	}
}

func TestPickErrors(t *testing.T) {
	_, env := fake(t, failAll)
	if r := run(t, env, "a\nb ANSWER\n", "pickv", "q?"); r.Code != 2 || r.Stdout != "" {
		t.Fatalf("failed requests: %+v", r)
	}
}
