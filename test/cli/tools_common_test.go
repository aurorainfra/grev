package clitest

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/aurorainfra/grev/internal/jev"
	"github.com/aurorainfra/grev/internal/jevtest"
)

// Helpers shared by the tests of the record tools (oneof … seek).

// declineQuote runs tool with -Q answered "n": exit 4, nothing on stdout and
// no request sent.
func declineQuote(t *testing.T, o jevtest.Options, stdin, tool string, args ...string) {
	t.Helper()
	no := writeFile(t, t.TempDir(), "no", "n\n")
	srv, env := fake(t, o, "GREV_TTY="+no)
	r := run(t, env, stdin, tool, append([]string{"-Q"}, args...)...)
	if r.Code != 4 || r.Stdout != "" || !strings.Contains(r.Stderr, tool+" quote:") || srv.Requests() != 0 {
		t.Fatalf("%s -Q declined: %+v (requests %d)", tool, r, srv.Requests())
	}
	noLeak(t, r, testKey)
}

// overBudget runs tool with a tiny --max-cost: refused up front with exit 4
// and no request sent.
func overBudget(t *testing.T, o jevtest.Options, stdin, tool string, args ...string) {
	t.Helper()
	srv, env := fake(t, o)
	r := run(t, env, stdin, tool, append([]string{"--max-cost", "0.0000000001"}, args...)...)
	if r.Code != 4 || r.Stdout != "" || !strings.Contains(r.Stderr, "exceeds --max-cost") || srv.Requests() != 0 {
		t.Fatalf("%s over --max-cost: %+v (requests %d)", tool, r, srv.Requests())
	}
}

// usageError checks that the args are refused with exit 2.
func usageError(t *testing.T, env []string, stdin, tool string, args ...string) {
	t.Helper()
	if r := run(t, env, stdin, tool, args...); r.Code != 2 {
		t.Errorf("%s %q: want exit 2, got %+v", tool, args, r)
	}
}

// failAll makes every request fail with a context-length error that no split
// can fix, so every question ends in an error.
var failAll = jevtest.Options{MaxBody: 10}

func instrMap(q jev.Question) map[string]any {
	m, _ := q.Instructions.(map[string]any)
	return m
}

// instrText is the question text: string instructions, or their "question".
func instrText(q jev.Question) string {
	if s, ok := q.Instructions.(string); ok {
		return s
	}
	if m := instrMap(q); m != nil {
		s, _ := m["question"].(string)
		return s
	}
	return ""
}

// stateDoc is the text of a state: the string itself, or its "document" or
// "records" member.
func stateDoc(state any) string {
	switch st := state.(type) {
	case string:
		return st
	case map[string]any:
		for _, k := range []string{"document", "records", "input"} {
			if s, ok := st[k].(string); ok {
				return s
			}
		}
	}
	return ""
}

// tagged parses "ID| text" lines of an id-tagged document.
func tagged(doc string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(doc, "\n") {
		if id, text, ok := strings.Cut(line, "| "); ok {
			out[id] = text
		}
	}
	return out
}

// choose answers a Choice: 0.9 on key, the rest shared by the other options;
// key "" gives a uniform distribution with confidence 0.
func choose(opts jev.Opts, key string, conf float64) jev.Answer {
	probs := map[string]float64{}
	if key == "" || len(opts) == 0 {
		for _, o := range opts {
			probs[o.Key] = 1 / float64(len(opts))
		}
		first := ""
		if len(opts) > 0 {
			first = opts[0].Key
		}
		return jev.Answer{Type: jev.TypeChoice, Choice: first, Probabilities: probs}
	}
	for _, o := range opts {
		probs[o.Key] = 0.1 / float64(max(1, len(opts)-1))
	}
	probs[key] = 0.9
	if len(opts) == 1 {
		probs[key] = 1
	}
	return jev.Answer{Type: jev.TypeChoice, Choice: key, Probabilities: probs, Confidence: conf}
}

func noul(p float64) jev.Answer { return jev.Answer{Type: jev.TypeNoul, Noul: p} }

var pMarker = regexp.MustCompile(`p=([0-9]*\.?[0-9]+)`)

// marked returns the p=X marker in s, if any.
func marked(s string) (float64, bool) {
	m := pMarker.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	p, err := strconv.ParseFloat(m[1], 64)
	return p, err == nil
}

// keywordOracle answers Choices with the first option whose key appears in the
// question's subject (confidence 0.8, or 0.3 when the subject says
// "unsure"), falling back to the last option (where tools put their escape
// label); Nouls and Scores go to the default oracle.
func keywordOracle(state any, q jev.Question) jev.Answer {
	if q.Type != jev.TypeChoice {
		return jevtest.DefaultOracle(state, q)
	}
	opts, _ := q.Criteria.(jev.Opts)
	subj := strings.ToLower(jevtest.Subject(state, q))
	conf := 0.8
	if strings.Contains(subj, "unsure") {
		conf = 0.3
	}
	for _, o := range opts {
		if strings.Contains(subj, strings.ToLower(o.Key)) {
			return choose(opts, o.Key, conf)
		}
	}
	if len(opts) == 0 {
		return choose(opts, "", 0)
	}
	return choose(opts, opts[len(opts)-1].Key, conf)
}
