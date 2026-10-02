package clitest

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/aurorainfra/grev/internal/jev"
	"github.com/aurorainfra/grev/internal/jevtest"
	"github.com/aurorainfra/grev/test/harness"
)

// glide is a fake of Fastino's GLiDE, reached the way a Fastino model id
// reaches it: the model picks the protocol.
func glide(t *testing.T, o jevtest.Options, extra ...string) (*jevtest.Server, []string) {
	t.Helper()
	o.Dialect = "fastino"
	return fake(t, o, append([]string{"TYPESAFE_DEFAULT_MODEL=fastino/GLiDE"}, extra...)...)
}

// TestFastinoSameAnswers: every tool reads GLiDE's answers as it reads Jev's,
// and every request it sends is one GLiDE accepts (the fake refuses the rest).
func TestFastinoSameAnswers(t *testing.T) {
	_, ts := fake(t, jevtest.Options{})
	srv, fs := glide(t, jevtest.Options{})
	spicy := "korma level=0\nvindaloo level=3\nmadras level=2\ntikka level=1\n"
	for _, c := range []struct {
		stdin string
		args  []string
	}{
		{fruits, []string{"grev", "q"}},
		{fruits, []string{"grev", "-v", "--about", "fruit", "q"}},
		{spicy, []string{"rank", "-s", "-L", "mild|medium|hot|very hot", "How spicy?"}},
		{spicy, []string{"sortv", "-s", "by heat, mildest first"}},
		{"a vindaloo\nb korma\n", []string{"tagv", "korma", "vindaloo", "other"}},
		{"x yes\ny no\n", []string{"isv", "is it"}},
		{"acme yes\nacme inc yes\nbeta no\n", []string{"uniqv", "-c"}},
	} {
		want := run(t, ts, c.stdin, c.args[0], c.args[1:]...)
		got := run(t, fs, c.stdin, c.args[0], c.args[1:]...)
		if got.Code != want.Code || got.Stdout != want.Stdout {
			t.Errorf("%v on GLiDE: exit %d %q; on Jev: exit %d %q\n%s", c.args, got.Code, got.Stdout, want.Code, want.Stdout, got.Stderr)
		}
	}
	for _, r := range srv.Log() {
		if r.Status != 200 || r.Model != "fastino/GLiDE" {
			t.Errorf("request %+v", r)
		}
	}
}

// TestFastinoExpectedLevel: a Score's value is GLiDE's expected_level, not
// its winning level, so close scores still order.
func TestFastinoExpectedLevel(t *testing.T) {
	_, env := glide(t, jevtest.Options{Oracle: func(state any, q jev.Question) jev.Answer {
		f, _ := strconv.ParseFloat(regexp.MustCompile(`s=([\d.]+)`).FindStringSubmatch(jevtest.Subject(state, q))[1], 64)
		return jev.Answer{Type: jev.TypeScore, Score: f}
	}})
	// Both win level 1; only the expected level tells them apart.
	expect(t, run(t, env, "a s=1.2\nb s=1.4\n", "rank", "-s", "-L", "low|mid|high", "How good?"), 0, "1.40\tb s=1.4\n1.20\ta s=1.2\n")
}

// TestFastinoWireShape: forced to speak TypeSafe's dialect, grev is refused
// by GLiDE, which is what the Fastino protocol is for.
func TestFastinoWireShape(t *testing.T) {
	_, env := glide(t, jevtest.Options{})
	writeConfig(t, env, "[api]\n\tprotocol = typesafe\n")
	r := run(t, env, fruits, "grev", "q")
	if r.Code != 2 || !strings.Contains(r.Stderr, "instructions' must be a string") {
		t.Fatalf("typesafe dialect against GLiDE: %+v", r)
	}
	writeConfig(t, env, "[api]\n\tprotocol = fastino\n")
	expect(t, run(t, env, fruits, "grev", "q"), 0, "apple yes\ncherry yes\n")
}

// TestFastinoQuote: GLiDE bills the shared state once per question, at its
// own price, and the quote says so.
func TestFastinoQuote(t *testing.T) {
	ref := writeFile(t, t.TempDir(), "ref", strings.Repeat("Some shared reference text about fruit. ", 300))
	args := []string{"-S", ref, "--max-cost", "0.0000001", "q"}
	tokens := func(stderr string) float64 {
		m := regexp.MustCompile(`≈([\d.]+)(k?) input tokens`).FindStringSubmatch(stderr)
		if m == nil {
			t.Fatalf("no quote in %q", stderr)
		}
		n, _ := strconv.ParseFloat(m[1], 64)
		if m[2] == "k" {
			n *= 1000
		}
		return n
	}
	_, ts := fake(t, jevtest.Options{})
	_, fs := glide(t, jevtest.Options{})
	jevQ := run(t, ts, fruits, "grev", args...)
	glideQ := run(t, fs, fruits, "grev", args...)
	if !strings.Contains(glideQ.Stderr, "× $0.3/Mtok") || !strings.Contains(glideQ.Stderr, "fastino/GLiDE") {
		t.Errorf("GLiDE quote: %s", glideQ.Stderr)
	}
	// Three questions about one ~2.5k-token state: about three states' worth.
	if j, g := tokens(jevQ.Stderr), tokens(glideQ.Stderr); g < 2*j {
		t.Errorf("GLiDE quote %v tokens vs Jev's %v: the state should count per question", g, j)
	}
}

// TestFastinoKeyAndModels: the key check uses a request that needs the key
// (Fastino's model lists are public), and models lists only GLiDE.
func TestFastinoKeyAndModels(t *testing.T) {
	_, env := glide(t, jevtest.Options{})
	if r := run(t, env, "", "grev-settings", "key", "status"); r.Code != 0 || !strings.Contains(r.Stdout, "check:  ok (1 decision model available)") {
		t.Fatalf("key status: %+v", r)
	}
	bad := append(append([]string(nil), env...), "TYPESAFE_API_KEY=fast_sk_wrong000000000000000")
	if r := run(t, bad, "", "grev-settings", "key", "status"); r.Code == 0 || !strings.Contains(r.Stdout, "FAILED") {
		t.Fatalf("a wrong key must fail the check: %+v", r)
	}
	r := run(t, env, "", "grev-settings", "models")
	if r.Code != 0 || !strings.HasPrefix(r.Stdout, "fastino/GLiDE") || strings.Contains(r.Stdout, "GLiNER") {
		t.Fatalf("models: %+v", r)
	}
}

// TestFastinoDefaultModel: with a Fastino endpoint and no model set, the
// model is GLiDE.
func TestFastinoDefaultModel(t *testing.T) {
	srv := jevtest.New(t, jevtest.Options{Dialect: "fastino", Key: testKey})
	env := harness.Env(t, "TYPESAFE_API_KEY="+testKey) // no endpoint or model from the environment
	// A fastino.ai endpoint is what picks the protocol; the fake stands in
	// for it through the api.protocol override.
	writeConfig(t, env, fmt.Sprintf("[api]\n\tendpoint = %s\n\tprotocol = fastino\n", srv.URL))
	expect(t, run(t, env, fruits, "grev", "q"), 0, "apple yes\ncherry yes\n")
	if log := srv.Log(); len(log) == 0 || log[0].Model != "fastino/GLiDE" {
		t.Fatalf("model: %+v", log)
	}
}
