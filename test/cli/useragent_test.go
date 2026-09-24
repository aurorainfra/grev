package clitest

import (
	"regexp"
	"testing"

	"github.com/aurorainfra/grev/internal/jevtest"
)

// TestUserAgent: every request names the project, its version, the tool and
// where it comes from, so the API side can attribute traffic.
func TestUserAgent(t *testing.T) {
	srv, env := fake(t, jevtest.Options{})
	expect(t, run(t, env, "yes\nno\n", "grev", "q"), 0, "yes\n")
	if r := run(t, env, "", "jev", "ask", "--state", "s", "-q", "x: is it"); r.Code != 0 {
		t.Fatalf("jev ask: %+v", r)
	}
	log := srv.Log()
	if len(log) < 2 {
		t.Fatalf("requests: %+v", log)
	}
	for i, tool := range []string{"grev", "jev"} {
		ua := log[len(log)-2+i].UserAgent
		want := `^grev/\S+ \(` + tool + `; \w+/\w+; \+https://github\.com/aurorainfra/grev\)$`
		if !regexp.MustCompile(want).MatchString(ua) {
			t.Errorf("%s User-Agent %q", tool, ua)
		}
	}
}
