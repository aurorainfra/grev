package clitest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/aurorainfra/grev/internal/jev"
	"github.com/aurorainfra/grev/internal/jevtest"
)

var recRef = regexp.MustCompile(`R\d{4}`)

// newTopic answers a boundary question from the record after it (the highest
// id the question names): a p=X marker wins, "NEW" means a new segment
// (0.95), anything else continues (0.05).
func newTopic(state any, q jev.Question) jev.Answer {
	ids := recRef.FindAllString(instrText(q), -1)
	if len(ids) == 0 {
		return noul(0)
	}
	b := ids[0]
	for _, id := range ids {
		if id > b {
			b = id
		}
	}
	text := tagged(stateDoc(state))[b]
	if p, ok := marked(text); ok {
		return noul(p)
	}
	if strings.Contains(text, "NEW") {
		return noul(0.95)
	}
	return noul(0.05)
}

const segIn = "a1\na2\nNEW b1\nb2\nNEW c1\n"

func TestSeg(t *testing.T) {
	_, env := fake(t, jevtest.Options{Oracle: newTopic})
	cases := []struct {
		name  string
		stdin string
		args  []string
		out   string
	}{
		{"default separator", segIn, nil, "a1\na2\n\nNEW b1\nb2\n\nNEW c1\n"},
		{"custom separator", segIn, []string{"--sep=---"}, "a1\na2\n---\nNEW b1\nb2\n---\nNEW c1\n"},
		{"scores", segIn, []string{"-s"}, "a1\na2\n--- 0.95\nNEW b1\nb2\n--- 0.95\nNEW c1\n"},
		{"scores with separator", segIn, []string{"-s", "--sep=##"}, "a1\na2\n## 0.95\nNEW b1\nb2\n## 0.95\nNEW c1\n"},
		{"min length", segIn, []string{"--min", "3"}, "a1\na2\nNEW b1\nb2\n\nNEW c1\n"},
		{"threshold", segIn, []string{"-t", "0.99"}, segIn},
		{"marker", "a\np=0.6 b\n", []string{"-t", "0.5"}, "a\n\np=0.6 b\n"},
		{"predicate question", segIn, []string{"the topic changes"}, "a1\na2\n\nNEW b1\nb2\n\nNEW c1\n"},
		{"placeholder question", segIn, []string{"Does {2} start a new story after {1}?"}, "a1\na2\n\nNEW b1\nb2\n\nNEW c1\n"},
		{"no window", segIn, []string{"-W", "0"}, "a1\na2\n\nNEW b1\nb2\n\nNEW c1\n"},
		{"paragraphs", "a\nx\n\nNEW b\ny\n", []string{"--para"}, "a\nx\n\n---\n\nNEW b\ny\n\n"},
		{"streaming", segIn, []string{"--line-buffered", "--flush", "5ms"}, "a1\na2\n\nNEW b1\nb2\n\nNEW c1\n"},
		{"single record", "only\n", nil, "only\n"},
		{"empty", "", nil, ""},
		{"about", segIn, []string{"--about", "a meeting transcript"}, "a1\na2\n\nNEW b1\nb2\n\nNEW c1\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expect(t, run(t, env, tc.stdin, "seg", tc.args...), 0, tc.out)
		})
	}

	dir := t.TempDir()
	f := writeFile(t, dir, "talk.txt", segIn)
	expect(t, run(t, env, "", "seg", f), 0, "a1\na2\n\nNEW b1\nb2\n\nNEW c1\n")
	expect(t, run(t, env, "", "seg", "the topic changes", f), 0, "a1\na2\n\nNEW b1\nb2\n\nNEW c1\n")

	usageError(t, env, segIn, "seg", "--min", "0")
	usageError(t, env, segIn, "seg", "-W", "-1")
	usageError(t, env, segIn, "seg", "a", "b", "c")
	usageError(t, env, segIn, "seg", "Does {3} start a new topic?")
	declineQuote(t, jevtest.Options{}, segIn, "seg")
	overBudget(t, jevtest.Options{}, segIn, "seg")
}

// TestSegLongStream: a long input streams through bounded buffers and comes
// out the same as in batch mode.
func TestSegLongStream(t *testing.T) {
	_, env := fake(t, jevtest.Options{Oracle: newTopic})
	var in, want strings.Builder
	for i := 0; i < 2000; i++ {
		rec := fmt.Sprintf("record %d", i)
		if i > 0 && i%10 == 0 {
			rec = "NEW " + rec
			want.WriteString("\n")
		}
		in.WriteString(rec + "\n")
		want.WriteString(rec + "\n")
	}
	expect(t, run(t, env, in.String(), "seg"), 0, want.String())
	expect(t, run(t, env, in.String(), "seg", "--line-buffered", "--flush", "5ms"), 0, want.String())
}

func TestSegSplit(t *testing.T) {
	_, env := fake(t, jevtest.Options{Oracle: newTopic})
	prefix := filepath.Join(t.TempDir(), "part-")
	expect(t, run(t, env, segIn, "seg", "--split", prefix), 0, prefix+"00\n"+prefix+"01\n"+prefix+"02\n")
	for i, want := range []string{"a1\na2\n", "NEW b1\nb2\n", "NEW c1\n"} {
		name := prefix + []string{"00", "01", "02"}[i]
		if b, err := os.ReadFile(name); err != nil || string(b) != want {
			t.Errorf("%s = %q, %v", name, b, err)
		}
	}

	// A declined quote must not create files either.
	no := writeFile(t, t.TempDir(), "no", "n\n")
	srv, env := fake(t, jevtest.Options{Oracle: newTopic}, "GREV_TTY="+no)
	prefix = filepath.Join(t.TempDir(), "declined-")
	r := run(t, env, segIn, "seg", "-Q", "--split", prefix)
	if r.Code != 4 || r.Stdout != "" || srv.Requests() != 0 {
		t.Fatalf("declined split: %+v", r)
	}
	if m, _ := filepath.Glob(prefix + "*"); len(m) != 0 {
		t.Fatalf("declined split created %v", m)
	}
}

// TestSegRequests: boundaries share one id-tagged state per window.
func TestSegRequests(t *testing.T) {
	var mu sync.Mutex
	var qs []jev.Question
	var states []any
	srv, env := fake(t, jevtest.Options{Oracle: func(state any, q jev.Question) jev.Answer {
		mu.Lock()
		qs, states = append(qs, q), append(states, state)
		mu.Unlock()
		return newTopic(state, q)
	}})
	expect(t, run(t, env, "x\ny\nNEW z\n", "seg"), 0, "x\ny\n\nNEW z\n")
	if srv.Requests() != 1 || len(qs) != 2 {
		t.Fatalf("%d requests, %d questions", srv.Requests(), len(qs))
	}
	if stateDoc(states[0]) != "R0001| x\nR0002| y\nR0003| NEW z\n" {
		t.Fatalf("state: %q", stateDoc(states[0]))
	}
	texts := []string{instrText(qs[0]), instrText(qs[1])}
	if !(strings.Contains(strings.Join(texts, "|"), "Does a new topic begin at record R0002?") &&
		strings.Contains(strings.Join(texts, "|"), "Does a new topic begin at record R0003?")) {
		t.Fatalf("questions: %q", texts)
	}
}

func TestSegErrors(t *testing.T) {
	_, env := fake(t, failAll)
	r := run(t, env, segIn, "seg")
	// Failed boundaries don't split; every record is still printed.
	if r.Code != 2 || r.Stdout != segIn || !strings.Contains(r.Stderr, "failed") {
		t.Fatalf("failed boundaries: %+v", r)
	}
}
