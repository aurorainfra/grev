package clitest

import (
	"strings"
	"sync"
	"testing"

	"github.com/aurorainfra/grev/internal/jev"
	"github.com/aurorainfra/grev/internal/jevtest"
)

func TestRank(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	in := "a p=0.2\nb p=0.9\nc p=0.5\nd p=0.5\n"
	spicy := "korma level=0\nvindaloo level=3\nmadras level=2\ntikka level=1\n"
	cases := []struct {
		name  string
		stdin string
		args  []string
		out   string
	}{
		{"best first, ties stable", in, []string{"q"}, "b p=0.9\nc p=0.5\nd p=0.5\na p=0.2\n"},
		{"reverse", in, []string{"-r", "q"}, "a p=0.2\nc p=0.5\nd p=0.5\nb p=0.9\n"},
		{"top", in, []string{"-m", "2", "q"}, "b p=0.9\nc p=0.5\n"},
		{"scores", in, []string{"-s", "-m1", "q"}, "0.90\tb p=0.9\n"},
		{"numbers", in, []string{"-n", "q"}, "2:b p=0.9\n3:c p=0.5\n4:d p=0.5\n1:a p=0.2\n"},
		{"levels", spicy, []string{"-L", "mild|medium|hot|very hot", "How spicy is {}?"},
			"vindaloo level=3\nmadras level=2\ntikka level=1\nkorma level=0\n"},
		{"levels with scores", spicy, []string{"-s", "-m2", "-L", "mild|medium|hot|very hot", "How spicy?"},
			"3.00\tvindaloo level=3\n2.00\tmadras level=2\n"},
		{"paragraphs", "x\np=0.1\n\ny\np=0.8\n", []string{"--para", "q"}, "y\np=0.8\n\nx\np=0.1\n\n"},
		{"fields", "a\tp=0.3\nb\tp=0.7\n", []string{"-d", `\t`, "Is {2} good?"}, "b\tp=0.7\na\tp=0.3\n"},
		{"window", in, []string{"-W1", "q"}, "b p=0.9\nc p=0.5\nd p=0.5\na p=0.2\n"},
		{"empty", "", []string{"q"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expect(t, run(t, env, tc.stdin, "rank", tc.args...), 0, tc.out)
		})
	}

	dir := t.TempDir()
	f1 := writeFile(t, dir, "one", "x p=0.3\n")
	f2 := writeFile(t, dir, "two", "y p=0.6\n")
	expect(t, run(t, env, "", "rank", "q", f1, f2), 0, "y p=0.6\nx p=0.3\n")
	expect(t, run(t, env, "", "rank", "-n", "q", f1, f2), 0, f2+":1:y p=0.6\n"+f1+":1:x p=0.3\n")

	usageError(t, env, in, "rank")
	usageError(t, env, in, "rank", "-L", "only", "q")
	usageError(t, env, in, "rank", "-L", "1|2|3|4|5|6|7|8|9|10|11", "q")
	usageError(t, env, in, "rank", "Is {1} spicy?")
	if r := run(t, env, "", "rank", "q", dir+"/missing"); r.Code != 2 {
		t.Errorf("missing file: %+v", r)
	}
	declineQuote(t, jevtest.Options{}, in, "rank", "q")
	overBudget(t, jevtest.Options{}, in, "rank", "q")
}

func TestRankRequest(t *testing.T) {
	var mu sync.Mutex
	var qs []jev.Question
	_, env := fake(t, jevtest.Options{Oracle: func(state any, q jev.Question) jev.Answer {
		mu.Lock()
		qs = append(qs, q)
		mu.Unlock()
		return jevtest.DefaultOracle(state, q)
	}})
	expect(t, run(t, env, "a level=1\n", "rank", "-L", "low|high", "How good?"), 0, "a level=1\n")
	expect(t, run(t, env, "a yes\n", "rank", "is good"), 0, "a yes\n")
	if len(qs) != 2 {
		t.Fatalf("questions: %d", len(qs))
	}
	if qs[0].Type != jev.TypeScore || instrText(qs[0]) != "Regarding `text`: How good?" {
		t.Fatalf("score question: %#v", qs[0])
	}
	if lv, _ := qs[0].Criteria.([]any); len(lv) != 2 || lv[0] != "low" || lv[1] != "high" {
		t.Fatalf("levels: %#v", qs[0].Criteria)
	}
	if qs[1].Type != jev.TypeNoul || instrText(qs[1]) != "Is it true that `text` is good?" {
		t.Fatalf("noul question: %#v", qs[1])
	}
}

func TestRankErrors(t *testing.T) {
	_, env := fake(t, failAll)
	r := run(t, env, "a p=0.2\nb p=0.9\n", "rank", "-s", "q")
	// Every record failed: all rank last, in input order, and the exit is 2.
	if r.Code != 2 || r.Stdout != "-\ta p=0.2\n-\tb p=0.9\n" || !strings.Contains(r.Stderr, "failed") {
		t.Fatalf("failed records: %+v", r)
	}
}
