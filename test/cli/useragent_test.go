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
	if r := run(t, env, "", "grev-settings", "ask", "--state", "s", "-q", "x: is it"); r.Code != 0 {
		t.Fatalf("grev-settings ask: %+v", r)
	}
	log := srv.Log()
	if len(log) < 2 {
		t.Fatalf("requests: %+v", log)
	}
	for i, tool := range []string{"grev", "grev-settings"} {
		ua := log[len(log)-2+i].UserAgent
		want := `^grev/\S+ \(` + tool + `; \w+/\w+; \+https://github\.com/aurorainfra/grev\)$`
		if !regexp.MustCompile(want).MatchString(ua) {
			t.Errorf("%s User-Agent %q", tool, ua)
		}
	}
}

// TestAttribution: requests carry OpenRouter's app-attribution headers,
// naming the family as one app, and api.attribution = false drops them (but
// not the User-Agent).
func TestAttribution(t *testing.T) {
	srv, env := fake(t, jevtest.Options{})
	expect(t, run(t, env, "yes\nno\n", "grev", "q"), 0, "yes\n")
	if r := run(t, env, "", "grev-settings", "ask", "--state", "s", "-q", "x: is it"); r.Code != 0 {
		t.Fatalf("grev-settings ask: %+v", r)
	}
	log := srv.Log()
	for _, r := range log[len(log)-2:] {
		if r.Referer != "https://github.com/aurorainfra/grev" || r.Title != "grev" || r.Categories != "programming-app" {
			t.Errorf("attribution headers: %+v", r)
		}
	}

	writeConfig(t, env, "[api]\n\tattribution = false\n")
	expect(t, run(t, env, "yes\nno\n", "grev", "q"), 0, "yes\n")
	log = srv.Log()
	if r := log[len(log)-1]; r.Referer != "" || r.Title != "" || r.Categories != "" || r.UserAgent == "" {
		t.Errorf("with api.attribution = false: %+v", r)
	}
}
