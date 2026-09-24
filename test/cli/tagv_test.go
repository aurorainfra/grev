package clitest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/aurorainfra/grev/internal/jev"
	"github.com/aurorainfra/grev/internal/jevtest"
)

const tickets = "card charged twice, billing problem\napp crashes, tech issue\nwant a sales quote\nunsure: billing or not\n"

func TestTag(t *testing.T) {
	_, env := fake(t, jevtest.Options{Oracle: keywordOracle})
	labels := []string{"billing", "tech", "sales"}
	cases := []struct {
		name string
		args []string
		out  string
	}{
		{"labels", nil, "billing\tcard charged twice, billing problem\ntech\tapp crashes, tech issue\n" +
			"sales\twant a sales quote\nbilling\tunsure: billing or not\n"},
		{"scores", []string{"-s"}, "0.80\tbilling\tcard charged twice, billing problem\n0.80\ttech\tapp crashes, tech issue\n" +
			"0.80\tsales\twant a sales quote\n0.30\tbilling\tunsure: billing or not\n"},
		{"threshold", []string{"-t", "0.5"}, "billing\tcard charged twice, billing problem\ntech\tapp crashes, tech issue\n" +
			"sales\twant a sales quote\n?\tunsure: billing or not\n"},
		{"unsure name", []string{"-t", "0.5", "--unsure", "review"}, "billing\tcard charged twice, billing problem\n" +
			"tech\tapp crashes, tech issue\nsales\twant a sales quote\nreview\tunsure: billing or not\n"},
		{"only", []string{"--only", "billing"}, "card charged twice, billing problem\nunsure: billing or not\n"},
		{"only several with scores", []string{"--only", "tech,sales", "-s"}, "0.80\tapp crashes, tech issue\n0.80\twant a sales quote\n"},
		{"only unsure", []string{"-t", "0.5", "--only", "?"}, "unsure: billing or not\n"},
		{"streaming", []string{"--line-buffered", "--flush", "5ms"}, "billing\tcard charged twice, billing problem\n" +
			"tech\tapp crashes, tech issue\nsales\twant a sales quote\nbilling\tunsure: billing or not\n"},
		{"window", []string{"-W", "1"}, "billing\tcard charged twice, billing problem\ntech\tapp crashes, tech issue\n" +
			"sales\twant a sales quote\nbilling\tunsure: billing or not\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expect(t, run(t, env, tickets, "tagv", append(tc.args, labels...)...), 0, tc.out)
		})
	}

	// --other is the last option, so the keyword oracle falls back to it.
	expect(t, run(t, env, "hello there\nbilling help\n", "tagv", "--other", "billing", "tech"), 0,
		"other\thello there\nbilling\tbilling help\n")
	// Fields and a {} question.
	expect(t, run(t, env, "x\ttech\n", "tagv", "-d", `\t`, "-q", "Which team owns {2}?", "billing", "tech"), 0, "tech\tx\ttech\n")
	// NUL and paragraph records.
	expect(t, run(t, env, "a\ntech\x00b billing\x00", "tagv", "-z", "billing", "tech"), 0, "tech\ta\ntech\x00billing\tb billing\x00")
	expect(t, run(t, env, "a\ntech\n\nb billing\n", "tagv", "--para", "billing", "tech"), 0, "tech\ta\ntech\n\nbilling\tb billing\n\n")

	dir := t.TempDir()
	f1 := writeFile(t, dir, "a.txt", "tech one\n")
	f2 := writeFile(t, dir, "b.txt", "billing two\n")
	expect(t, run(t, env, "", "tagv", "-i", f1, "-i", f2, "billing", "tech"), 0, "tech\ttech one\nbilling\tbilling two\n")
	lf := writeFile(t, dir, "labels", "billing\tmoney\ntech\n")
	expect(t, run(t, env, "tech stuff\n", "tagv", "-f", lf), 0, "tech\ttech stuff\n")

	usageError(t, env, tickets, "tagv", "billing")
	usageError(t, env, tickets, "tagv", "-b", "0", "billing", "tech")
	usageError(t, env, tickets, "tagv", "--flush", "x", "billing", "tech")
	declineQuote(t, jevtest.Options{}, tickets, "tagv", "billing", "tech")
	overBudget(t, jevtest.Options{}, tickets, "tagv", "billing", "tech")
}

func TestTagSplit(t *testing.T) {
	_, env := fake(t, jevtest.Options{Oracle: keywordOracle})
	dir := filepath.Join(t.TempDir(), "out")
	r := run(t, env, tickets+"more billing\n", "tagv", "--split", dir, "-t", "0.5", "billing", "tech", "sales")
	expect(t, r, 0, "")
	want := map[string]string{
		"billing": "card charged twice, billing problem\nmore billing\n",
		"tech":    "app crashes, tech issue\n",
		"sales":   "want a sales quote\n",
		"?":       "unsure: billing or not\n",
	}
	for label, content := range want {
		if label == "?" && runtime.GOOS == "windows" {
			label = "%3F" // Windows forbids ? in file names
		}
		if b, err := os.ReadFile(filepath.Join(dir, label)); err != nil || string(b) != content {
			t.Errorf("%s: %q, %v", label, b, err)
		}
	}
	if !strings.Contains(r.Stderr, "tagv: billing\t2") || !strings.Contains(r.Stderr, "tagv: ?\t1") {
		t.Fatalf("split counts on stderr: %q", r.Stderr)
	}
	// Splitting appends.
	expect(t, run(t, env, "tech again\n", "tagv", "--split", dir, "billing", "tech"), 0, "")
	if b, _ := os.ReadFile(filepath.Join(dir, "tech")); string(b) != "app crashes, tech issue\ntech again\n" {
		t.Fatalf("append: %q", b)
	}
}

func TestTagRequest(t *testing.T) {
	var mu sync.Mutex
	var qs []jev.Question
	srv, env := fake(t, jevtest.Options{Oracle: func(state any, q jev.Question) jev.Answer {
		mu.Lock()
		qs = append(qs, q)
		mu.Unlock()
		return keywordOracle(state, q)
	}})
	expect(t, run(t, env, "tech a\nbilling b\n", "tagv", "billing=money matters", "tech"), 0, "tech\ttech a\nbilling\tbilling b\n")
	if srv.Requests() != 1 || len(qs) != 2 {
		t.Fatalf("2 records should be one request of 2 questions: %d requests, %d questions", srv.Requests(), len(qs))
	}
	for _, q := range qs {
		m := instrMap(q)
		opts, _ := q.Criteria.(jev.Opts)
		if q.Type != jev.TypeChoice || m == nil || m["question"] != "Which option best fits `text`?" ||
			len(opts) != 2 || opts[0].Desc != "money matters" {
			t.Fatalf("question: %#v", q)
		}
	}
}

func TestTagTree(t *testing.T) {
	_, env := fake(t, jevtest.Options{Oracle: keywordOracle})
	tree := writeFile(t, t.TempDir(), "tree.txt", `# a taxonomy
food
  vegan=plant-based dishes
    soups
    salads
  meat
    beef
drinks
	hot
	cold
`)
	in := "food vegan soups: lentil\nfood meat steak\ndrinks cold lemonade\n"
	expect(t, run(t, env, in, "tagv", "--tree", tree), 0,
		"food/vegan/soups\tfood vegan soups: lentil\nfood/meat/beef\tfood meat steak\ndrinks/cold\tdrinks cold lemonade\n")
	expect(t, run(t, env, in, "tagv", "--tree", tree, "-b", "1", "--only", "drinks/cold"), 0, "drinks cold lemonade\n")

	usageError(t, env, in, "tagv", "--tree", tree, "extra-label")
	usageError(t, env, in, "tagv", "--tree", tree, "--line-buffered")
	bad := writeFile(t, t.TempDir(), "bad.txt", "a/b\n")
	if r := run(t, env, in, "tagv", "--tree", bad); r.Code != 2 || !strings.Contains(r.Stderr, "bad name") {
		t.Fatalf("bad taxonomy: %+v", r)
	}
	declineQuote(t, jevtest.Options{}, in, "tagv", "--tree", tree)
}

func TestTagErrors(t *testing.T) {
	_, env := fake(t, failAll)
	// Failed records keep their place, labelled "!", so output stays aligned.
	r := run(t, env, "tech\n", "tagv", "billing", "tech")
	if r.Code != 2 || r.Stdout != "!\ttech\n" || !strings.Contains(r.Stderr, "failed") {
		t.Fatalf("failed records: %+v", r)
	}
	if r := run(t, env, "tech\n", "tagv", "-s", "billing", "tech"); r.Code != 2 || r.Stdout != "-\t!\ttech\n" {
		t.Fatalf("failed record with -s: %+v", r)
	}
	// --split doesn't route failed records anywhere.
	dir := t.TempDir()
	if r := run(t, env, "tech\n", "tagv", "--split", dir, "billing", "tech"); r.Code != 2 || r.Stdout != "" {
		t.Fatalf("failed record with --split: %+v", r)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("--split wrote files for failed records: %v", entries)
	}

	// A fatal error mid-walk (--tree) prints nothing rather than empty labels.
	_, bad := fake(t, jevtest.Options{Key: "other-key"}, "TYPESAFE_API_KEY=wrong-key-123")
	tree := writeFile(t, t.TempDir(), "tree.txt", "food\n  vegan\n  meat\ndrinks\n")
	if r := run(t, bad, "lentil soup\n", "tagv", "--tree", tree); r.Code != 2 || r.Stdout != "" {
		t.Fatalf("tree walk cut short: %+v", r)
	}

	// Placeholders must refer to something that exists.
	usageError(t, env, "a,b\n", "tagv", "-q", "Is {2} a bill?", "billing", "tech")
}

func TestTagTreeForcedSteps(t *testing.T) {
	// "food" has a single child chain food/vegan/soups: only the model's
	// choice at the root counts in the score, forced steps don't.
	_, env := fake(t, jevtest.Options{Oracle: func(state any, q jev.Question) jev.Answer {
		opts, _ := json.Marshal(q.Criteria)
		var m map[string]any
		json.Unmarshal(opts, &m)
		probs := map[string]float64{}
		for k := range m {
			probs[k] = 0.1
		}
		probs["food"] = 0.9
		if len(m) == 2 {
			probs["drinks"] = 0.1
		}
		return jev.Answer{Type: jev.TypeChoice, Choice: "food", Confidence: 0.8, Probabilities: probs}
	}})
	tree := writeFile(t, t.TempDir(), "tree.txt", "food\n  vegan\n    soups\ndrinks\n")
	expect(t, run(t, env, "lentil soup\n", "tagv", "-s", "--tree", tree), 0, "0.90\tfood/vegan/soups\tlentil soup\n")
}
