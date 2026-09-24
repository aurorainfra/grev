package clitest

import (
	"strings"
	"sync"
	"testing"

	"github.com/aurorainfra/grev/internal/jev"
	"github.com/aurorainfra/grev/internal/jevtest"
)

// columnOracle picks the column whose header appears in (or contains) the
// wanted description; "lowconf" in the description gives confidence 0.3; no
// match picks "none".
func columnOracle(state any, q jev.Question) jev.Answer {
	opts, _ := q.Criteria.(jev.Opts)
	wanted, _ := instrMap(q)["wanted"].(string)
	wanted = strings.ToLower(wanted)
	conf := 0.9
	if strings.Contains(wanted, "lowconf") {
		conf = 0.3
	}
	cols, _ := state.(map[string]any)["columns"].([]any)
	for _, c := range cols {
		col, _ := c.(map[string]any)
		h := strings.ToLower(strings.TrimSpace(col["header"].(string)))
		if h != "" && (strings.Contains(wanted, h) || strings.Contains(h, wanted)) {
			return choose(opts, col["id"].(string), conf)
		}
	}
	return choose(opts, "none", 0.9)
}

const users = "Name,Email,Phone\nAda,ada@x.com,123\n\"Lovelace, B\",b@x.com,456\nShort\n"

func TestCutv(t *testing.T) {
	_, env := fake(t, jevtest.Options{Oracle: columnOracle})
	cases := []struct {
		name  string
		stdin string
		args  []string
		out   string
	}{
		{"one column", users, []string{"customer email"}, "Email\nada@x.com\nb@x.com\n\n"},
		{"argument order", users, []string{"phone number", "email"}, "Phone,Email\n123,ada@x.com\n456,b@x.com\n,\n"},
		{"quoted fields stay quoted", users, []string{"name"}, "Name\nAda\n\"Lovelace, B\"\nShort\n"},
		{"no header", users, []string{"--no-header", "email"}, "ada@x.com\nb@x.com\n\n"},
		{"small sample, all rows", users, []string{"--sample", "1", "email"}, "Email\nada@x.com\nb@x.com\n\n"},
		{"no sample", users, []string{"--sample", "0", "email"}, "Email\nada@x.com\nb@x.com\n\n"},
		{"tsv", "Name\tEmail\nAda\tada@x.com\n", []string{"email"}, "Email\nada@x.com\n"},
		{"semicolons", "Name;Email\nAda;ada@x.com\n", []string{"email"}, "Email\nada@x.com\n"},
		{"delimiter flag", "Name|Email\nAda|ada@x.com\n", []string{"-d", "|", "email"}, "Email\nada@x.com\n"},
		{"header only", "Name,Email\n", []string{"email"}, "Email\n"},
		{"crlf", "Name,Email\r\nAda,ada@x.com\r\n", []string{"email"}, "Email\nada@x.com\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expect(t, run(t, env, tc.stdin, "cutv", tc.args...), 0, tc.out)
		})
	}

	r := run(t, env, users, "cutv", "-s", "email")
	if r.Code != 0 || !strings.Contains(r.Stderr, `email → col 2 "Email" (0.90)`) {
		t.Fatalf("--show: %+v", r)
	}
	r = run(t, env, users, "cutv", "email", "shoe size")
	if r.Code != 1 || r.Stdout != "" || !strings.Contains(r.Stderr, "shoe size → none") {
		t.Fatalf("no matching column: %+v", r)
	}
	r = run(t, env, users, "cutv", "email lowconf")
	if r.Code != 3 || r.Stdout != "" || !strings.Contains(r.Stderr, "below -t") {
		t.Fatalf("low confidence: %+v", r)
	}
	expect(t, run(t, env, users, "cutv", "-t", "0.2", "email lowconf"), 0, "Email\nada@x.com\nb@x.com\n\n")
	// none wins over low confidence.
	if r := run(t, env, users, "cutv", "email lowconf", "shoe size"); r.Code != 1 {
		t.Fatalf("none and low confidence: %+v", r)
	}

	usageError(t, env, users, "cutv")
	usageError(t, env, users, "cutv", "-d", "ab", "email")
	usageError(t, env, users, "cutv", "--sample", "-1", "email")
	if r := run(t, env, "", "cutv", "email"); r.Code != 2 || !strings.Contains(r.Stderr, "empty input") {
		t.Fatalf("empty input: %+v", r)
	}
	declineQuote(t, jevtest.Options{}, users, "cutv", "email")
	overBudget(t, jevtest.Options{}, users, "cutv", "email")
}

// TestCutvRequest: one request for all descriptions; the state holds header
// and sample values per column.
func TestCutvRequest(t *testing.T) {
	var mu sync.Mutex
	var states []any
	var qs []jev.Question
	srv, env := fake(t, jevtest.Options{Oracle: func(state any, q jev.Question) jev.Answer {
		mu.Lock()
		states, qs = append(states, state), append(qs, q)
		mu.Unlock()
		return columnOracle(state, q)
	}})
	var b strings.Builder
	b.WriteString(users)
	for i := 0; i < 50; i++ {
		b.WriteString("X,x@x.com,9\n")
	}
	r := run(t, env, b.String(), "cutv", "--sample", "2", "email", "phone", "name")
	if r.Code != 0 || strings.Count(r.Stdout, "\n") != 1+3+50 {
		t.Fatalf("run: %+v", r)
	}
	if srv.Requests() != 1 || len(qs) != 3 {
		t.Fatalf("%d requests, %d questions", srv.Requests(), len(qs))
	}
	cols := states[0].(map[string]any)["columns"].([]any)
	c2 := cols[1].(map[string]any)
	samples, _ := c2["samples"].([]any)
	if len(cols) != 3 || c2["id"] != "c2" || c2["header"] != "Email" || len(samples) != 2 || samples[0] != "ada@x.com" {
		t.Fatalf("state: %#v", states[0])
	}
	opts, _ := qs[0].Criteria.(jev.Opts)
	if len(opts) != 4 || opts[3].Key != "none" {
		t.Fatalf("options: %#v", opts)
	}
}

func TestCutvInputAndAbout(t *testing.T) {
	var mu sync.Mutex
	var states []any
	_, env := fake(t, jevtest.Options{Oracle: func(state any, q jev.Question) jev.Answer {
		mu.Lock()
		states = append(states, state)
		mu.Unlock()
		return columnOracle(state, q)
	}})
	f := writeFile(t, t.TempDir(), "users.csv", users)
	expect(t, run(t, env, "", "cutv", "-i", f, "--about", "a CRM export", "email"), 0, "Email\nada@x.com\nb@x.com\n\n")
	mu.Lock()
	defer mu.Unlock()
	if len(states) != 1 || states[0].(map[string]any)["about"] != "a CRM export" {
		t.Fatalf("--about should be in the state: %#v", states)
	}
	usageError(t, env, "", "cutv", "-i", f)
	if r := run(t, env, "", "cutv", "-i", f+".missing", "email"); r.Code != 2 {
		t.Fatalf("missing -i file: %+v", r)
	}
}

func TestCutvErrors(t *testing.T) {
	_, env := fake(t, failAll)
	if r := run(t, env, users, "cutv", "email"); r.Code != 2 || r.Stdout != "" {
		t.Fatalf("failed request: %+v", r)
	}
}
