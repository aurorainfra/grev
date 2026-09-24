package clitest

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/aurorainfra/grev/internal/jev"
	"github.com/aurorainfra/grev/internal/jevtest"
)

// The default oracle reads the record: "yes" → P 0.95, a label it contains →
// that Choice option, "level=N" → Score N.
const probeIn = "refund yes billing level=2\nhello tech level=0\n"

var probeQs = []string{
	"-q", "r: asks for a refund",
	"-q", "dept: Which team? [billing|tech|sales]",
	"-q", "mood: How upset? <calm|annoyed|angry>",
}

func TestProbe(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	cases := []struct {
		name  string
		stdin string
		args  []string
		out   string
	}{
		{"columns", probeIn, nil,
			"0.95\tbilling\t2.00\trefund yes billing level=2\n0.05\ttech\t0.00\thello tech level=0\n"},
		{"header", probeIn, []string{"-H"},
			"r\tdept\tmood\trecord\n0.95\tbilling\t2.00\trefund yes billing level=2\n0.05\ttech\t0.00\thello tech level=0\n"},
		{"scores", probeIn, []string{"-H", "-s"},
			"r\tdept\tdept.conf\tmood\tmood.conf\trecord\n" +
				"0.95\tbilling\t0.80\t2.00\t1.00\trefund yes billing level=2\n0.05\ttech\t0.80\t0.00\t1.00\thello tech level=0\n"},
		{"no record", probeIn, []string{"--no-record"}, "0.95\tbilling\t2.00\n0.05\ttech\t0.00\n"},
		{"streaming", probeIn, []string{"--line-buffered", "--flush", "5ms"},
			"0.95\tbilling\t2.00\trefund yes billing level=2\n0.05\ttech\t0.00\thello tech level=0\n"},
		{"pack", probeIn, []string{"--pack"},
			"0.95\tbilling\t2.00\trefund yes billing level=2\n0.05\ttech\t0.00\thello tech level=0\n"},
		{"placeholder", probeIn, []string{"--no-record", "-q", "x: Does {} mention a refund?"},
			"0.95\tbilling\t2.00\t0.95\n0.05\ttech\t0.00\t0.05\n"},
		{"about", probeIn, []string{"--about", "support tickets", "--no-record"}, "0.95\tbilling\t2.00\n0.05\ttech\t0.00\n"},
		{"empty", "", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expect(t, run(t, env, tc.stdin, "probev", append(append([]string{}, probeQs...), tc.args...)...), 0, tc.out)
		})
	}

	// Unnamed questions are q1, q2, …; -f adds raw API questions in file order.
	qf := writeFile(t, t.TempDir(), "q.json",
		`{"z_first": {"type": "noul", "instructions": "is it yes?"}, "a_second": {"type": "score", "instructions": "how much?", "criteria": ["lo", "hi"]}}`)
	expect(t, run(t, env, "yes level=1\n", "probev", "-H", "--no-record", "-q", "is urgent", "-f", qf), 0,
		"q1\tz_first\ta_second\n0.95\t0.95\t1.00\n")

	usageError(t, env, probeIn, "probev")
	usageError(t, env, probeIn, "probev", "-q", "a: x", "-q", "a: y")
	usageError(t, env, probeIn, "probev", "-q", "a: pick [only|]")
	usageError(t, env, probeIn, "probev", "--jsonl", "--pack", "-q", "a: x")
	usageError(t, env, probeIn, "probev", "-q", "a: x", "one", "two")
	bad := writeFile(t, t.TempDir(), "bad.json", `{"a": {"type": "essay", "instructions": "write"}}`)
	if r := run(t, env, probeIn, "probev", "-f", bad); r.Code != 2 || !strings.Contains(r.Stderr, "unknown type") {
		t.Errorf("bad question file: %+v", r)
	}
	declineQuote(t, jevtest.Options{}, probeIn, "probev", probeQs...)
	overBudget(t, jevtest.Options{}, probeIn, "probev", probeQs...)
}

func TestProbeJSON(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	r := run(t, env, probeIn, "probev", append([]string{"--json"}, probeQs...)...)
	if r.Code != 0 {
		t.Fatalf("--json: %+v", r)
	}
	lines := strings.Split(strings.TrimSpace(r.Stdout), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 JSON lines, got %q", r.Stdout)
	}
	var row struct {
		Record  string                    `json:"record"`
		Answers map[string]map[string]any `json:"answers"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatal(err)
	}
	if row.Record != "refund yes billing level=2" || row.Answers["r"]["noul"] != 0.95 ||
		row.Answers["dept"]["choice"] != "billing" || row.Answers["mood"]["score"] != 2.0 ||
		row.Answers["dept"]["probabilities"] == nil {
		t.Fatalf("row: %+v", row)
	}
	// Answers keep the question order.
	if i, j := strings.Index(lines[0], `"r"`), strings.Index(lines[0], `"mood"`); i < 0 || j < i {
		t.Fatalf("answer order: %s", lines[0])
	}

	// JSONL records are the state; the record comes back as JSON.
	in := `{"text": "yes please", "id": 1}` + "\n\n" + `{"text": "no thanks", "id": 2}` + "\n"
	r = run(t, env, in, "probev", "--jsonl", "--json", "-q", "a: wants it")
	lines = strings.Split(strings.TrimSpace(r.Stdout), "\n")
	var jr struct {
		Record  map[string]any            `json:"record"`
		Answers map[string]map[string]any `json:"answers"`
	}
	if r.Code != 0 || len(lines) != 2 || json.Unmarshal([]byte(lines[0]), &jr) != nil ||
		jr.Record["id"] != 1.0 || jr.Answers["a"]["noul"] != 0.95 {
		t.Fatalf("--jsonl --json: %+v", r)
	}
	expect(t, run(t, env, in, "probev", "--jsonl", "--no-record", "-q", "a: wants it"), 0, "0.95\n0.05\n")
	if r := run(t, env, "{not json}\n", "probev", "--jsonl", "-q", "a: x"); r.Code != 2 || !strings.Contains(r.Stderr, "not valid JSON") {
		t.Fatalf("bad JSONL: %+v", r)
	}
}

// TestProbeRequests checks packing: by default each record is the state of
// its own request; --pack puts records in one request, each question
// carrying its record.
func TestProbeRequests(t *testing.T) {
	var mu sync.Mutex
	var states []any
	var qs []jev.Question
	o := jevtest.Options{Oracle: func(state any, q jev.Question) jev.Answer {
		mu.Lock()
		states, qs = append(states, state), append(qs, q)
		mu.Unlock()
		return jevtest.DefaultOracle(state, q)
	}}
	srv, env := fake(t, o)
	expect(t, run(t, env, "a yes\nb no\n", "probev", "--no-record", "-q", "x: q1", "-q", "y: q2"), 0, "0.95\t0.95\n0.05\t0.05\n")
	// Requests run concurrently, so compare the set of states, not their order.
	seen := map[any]int{}
	for _, st := range states {
		seen[st]++
	}
	if srv.Requests() != 2 || len(states) != 4 || len(seen) != 2 || seen["a yes"] != 2 || seen["b no"] != 2 {
		t.Fatalf("per-record requests: %d requests, states %v", srv.Requests(), states)
	}
	mu.Lock()
	states, qs = nil, nil
	mu.Unlock()
	srv, env = fake(t, o)
	expect(t, run(t, env, "a yes\nb no\n", "probev", "--pack", "--no-record", "-q", "x: q1", "-q", "y: q2"), 0, "0.95\t0.95\n0.05\t0.05\n")
	if srv.Requests() != 1 || len(qs) != 4 {
		t.Fatalf("--pack: %d requests, %d questions", srv.Requests(), len(qs))
	}
	for _, q := range qs {
		if m := instrMap(q); m == nil || m["text"] == nil || !strings.Contains(instrText(q), "`text`") {
			t.Fatalf("--pack question should carry its record: %#v", q.Instructions)
		}
	}
}

// TestProbePackStructured checks that --pack keeps -f questions' structured
// instructions structured instead of flattening them to text.
func TestProbePackStructured(t *testing.T) {
	var mu sync.Mutex
	var qs []jev.Question
	_, env := fake(t, jevtest.Options{Oracle: func(state any, q jev.Question) jev.Answer {
		mu.Lock()
		qs = append(qs, q)
		mu.Unlock()
		return jevtest.DefaultOracle(state, q)
	}})
	f := writeFile(t, t.TempDir(), "qs.json", `{"x": {"type": "noul", "instructions": {"question": "Is `+"`text`"+` urgent?", "focus": "tone"}}}`)
	if r := run(t, env, "a yes\n", "probev", "--pack", "--no-record", "-f", f); r.Code != 0 {
		t.Fatalf("--pack -f: %+v", r)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(qs) != 1 {
		t.Fatalf("questions: %#v", qs)
	}
	m := instrMap(qs[0])
	inner, _ := m["question"].(map[string]any)
	if m["text"] != "a yes" || inner["focus"] != "tone" {
		t.Fatalf("structured instructions were not kept: %#v", qs[0].Instructions)
	}
}

func TestProbePlaceholders(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	usageError(t, env, "a\n", "probev", "-q", "x: Is {1} urgent?")
	usageError(t, env, `{"m":"a"}`+"\n", "probev", "--jsonl", "-q", "x: Is {} urgent?")
	expect(t, run(t, env, "a yes\n", "probev", "--no-record", "-q", "x: Is {} urgent?"), 0, "0.95\n")
}

func TestProbeErrors(t *testing.T) {
	_, env := fake(t, failAll)
	r := run(t, env, "a yes\n", "probev", "-s", "-q", "x: q", "-q", "d: which [a|b]")
	if r.Code != 2 || r.Stdout != "-\t-\t-\ta yes\n" || !strings.Contains(r.Stderr, "failed") {
		t.Fatalf("failed questions: %+v", r)
	}
}

func TestProbeStreamingBadJSON(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	in := `{"text": "yes"}` + "\n{broken\n" + `{"text": "no"}` + "\n"
	r := run(t, env, in, "probev", "--jsonl", "--line-buffered", "--flush", "5ms", "--no-record", "-q", "a: q")
	if r.Code != 2 || r.Stdout != "0.95\n0.05\n" || !strings.Contains(r.Stderr, "not valid JSON") {
		t.Fatalf("streaming with a bad record: %+v", r)
	}
}
