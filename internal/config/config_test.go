package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestParse(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg")
	write(t, p, `# comment
; another
[api]
	keyFile = ~/jev.key   # trailing comment
	Endpoint="https://x.example/v1" ; comment
[Defaults]
	max-cost = 0.50
	progress
[tool "grev"]
	about = "application logs; with \"quotes\"\tand tab"
	long = one \
two
[tool "Grev"]
	about = other
[model "jev-1.14.0"]
	price = $0.05
`)
	c, err := ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ sec, sub, key, want string }{
		{"api", "", "keyfile", "~/jev.key"},
		{"API", "", "key-file", "~/jev.key"},
		{"api", "", "endpoint", "https://x.example/v1"},
		{"defaults", "", "maxCost", "0.50"},
		{"defaults", "", "progress", "true"},
		{"tool", "grev", "about", "application logs; with \"quotes\"\tand tab"},
		{"tool", "grev", "long", "one two"},
		{"tool", "Grev", "about", "other"},
		{"model", "jev-1.14.0", "price", "$0.05"},
	}
	for _, tc := range cases {
		if got := c.Str(tc.sec, tc.sub, tc.key); got != tc.want {
			t.Errorf("%s.%s.%s = %q, want %q", tc.sec, tc.sub, tc.key, got, tc.want)
		}
	}
	v, _ := c.Get("api", "", "keyFile")
	if v.Line != 4 || !strings.HasSuffix(v.Origin(), "cfg:4") || v.Name() != "api.keyfile" {
		t.Errorf("origin: %+v %s", v, v.Origin())
	}
	if subs := c.Subsections("tool"); len(subs) != 2 {
		t.Errorf("subsections: %v", subs)
	}
	if f, err := Float(c.Str("model", "jev-1.14.0", "price")); err != nil || f != 0.05 {
		t.Errorf("price: %v %v", f, err)
	}
}

func TestParseErrors(t *testing.T) {
	for in, want := range map[string]string{
		"key = x\n":              "outside of any [section]",
		"[api\nkey = x\n":        "missing ]",
		"[tool grev]\n":          "must be quoted",
		"[api]\nbad key = 1\n":   "bad key name",
		"[api]\nkey = \"open\n":  "unterminated quote",
		"[api]\nkey = a\\q\n":    "unknown escape",
		"[a b c]\n":              "must be quoted",
		"[api] junk\n":           "unexpected text",
		"[api]\n[]\n":            "bad section name",
		"[api]\nkey = x\\\\\\\n": "trailing backslash",
	} {
		p := filepath.Join(t.TempDir(), "cfg")
		write(t, p, in)
		_, err := ReadFile(p)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "cfg:") {
			t.Errorf("%q: got %v, want error containing %q with file:line", in, err, want)
		}
	}
}

func TestLastWinsAndSection(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg")
	write(t, p, "[defaults]\njobs = 4\nprogress = auto\n[defaults]\njobs = max\n")
	c, err := ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Str("defaults", "", "jobs") != "max" {
		t.Fatal("last value should win")
	}
	sec := c.Section("defaults", "")
	if len(sec) != 2 || sec[0].Key != "jobs" || sec[0].Raw != "max" || sec[1].Key != "progress" {
		t.Fatalf("section: %+v", sec)
	}
}

func TestIncludeAndLoad(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)                   // Windows home
	t.Setenv("APPDATA", filepath.Join(home, "xdg")) // Windows os.UserConfigDir
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	os.Unsetenv("GREV_CONFIG")
	write(t, filepath.Join(home, "xdg", "grev", "config"), "[defaults]\njobs = 2\nprogress = auto\n")
	write(t, filepath.Join(home, ".grevconfig"), "[defaults]\njobs = 8\n[include]\npath = secrets/key.conf\npath = missing.conf\n")
	write(t, filepath.Join(home, "secrets", "key.conf"), "[api]\nkey = inc-key\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Str("defaults", "", "jobs") != "8" || c.Str("defaults", "", "progress") != "auto" {
		t.Fatalf("~/.grevconfig should override the XDG file: %+v", c.Values)
	}
	if c.Str("api", "", "key") != "inc-key" {
		t.Fatal("include not read")
	}
	if len(c.Files) != 3 {
		t.Fatalf("files: %v", c.Files)
	}

	// GREV_CONFIG replaces both; empty disables config.
	alt := filepath.Join(home, "alt")
	write(t, alt, "[defaults]\njobs = 1\n")
	t.Setenv("GREV_CONFIG", alt)
	if c, _ := Load(); c.Str("defaults", "", "jobs") != "1" || len(c.Files) != 1 {
		t.Fatalf("GREV_CONFIG: %+v", c)
	}
	t.Setenv("GREV_CONFIG", "")
	if c, _ := Load(); len(c.Values) != 0 {
		t.Fatalf("empty GREV_CONFIG should disable config: %+v", c.Values)
	}

	// Include loops are cut off.
	loop := filepath.Join(home, "loop")
	write(t, loop, "[include]\npath = loop\n")
	if _, err := ReadFile(loop); err == nil || !strings.Contains(err.Error(), "nested") {
		t.Fatalf("include loop: %v", err)
	}
}

func TestSetUnsetRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".grevconfig")
	orig := "# my settings\n[api]\n\tkeyFile = ~/old.key # old\n\n# limits below\n[limits]\n\tdaily = 5\n"
	write(t, p, orig)
	os.Chmod(p, 0o640)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(Set(p, "api", "", "keyFile", "~/jev.key"))
	must(Set(p, "limits", "", "monthly", "50"))
	must(Set(p, "tool", "grev", "about", "app logs; \"quoted\""))
	must(Set(p, "defaults", "", "progress", "auto"))
	b, _ := os.ReadFile(p)
	got := string(b)
	for _, want := range []string{
		"# my settings\n[api]\n\tkeyFile = ~/jev.key\n\n# limits below\n[limits]\n\tdaily = 5\n\tmonthly = 50\n",
		"[tool \"grev\"]\n\tabout = \"app logs; \\\"quoted\\\"\"\n",
		"[defaults]\n\tprogress = auto\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("after set, missing %q in:\n%s", want, got)
		}
	}
	c, err := ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Str("tool", "grev", "about") != "app logs; \"quoted\"" || c.Str("api", "", "keyfile") != "~/jev.key" {
		t.Fatalf("round trip: %+v", c.Values)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o640 && runtime.GOOS != "windows" {
		t.Fatalf("mode not kept: %v", fi.Mode())
	}
	must(Unset(p, "limits", "", "daily"))
	if err := Unset(p, "limits", "", "daily"); err == nil {
		t.Fatal("unsetting a missing key should fail")
	}
	c, _ = ReadFile(p)
	if _, ok := c.Get("limits", "", "daily"); ok {
		t.Fatal("unset didn't remove the key")
	}

	// A new file is created 0600; a symlink is written through, not replaced.
	n := filepath.Join(t.TempDir(), "new")
	must(Set(n, "api", "", "key", "k"))
	if fi, _ := os.Stat(n); fi.Mode().Perm() != 0o600 && runtime.GOOS != "windows" {
		t.Fatalf("new file mode %v", fi.Mode())
	}
	link := filepath.Join(t.TempDir(), "link")
	if os.Symlink(n, link) == nil {
		must(Set(link, "defaults", "", "jobs", "max"))
		if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
			t.Fatal("symlink replaced by a file")
		}
		if c, _ := ReadFile(n); c.Str("defaults", "", "jobs") != "max" {
			t.Fatal("write didn't reach the symlink target")
		}
	}
}

func TestSplitName(t *testing.T) {
	for in, want := range map[string][3]string{
		"api.keyFile":            {"api", "", "keyFile"},
		"tool.grev.about":        {"tool", "grev", "about"},
		"model.jev-1.14.0.price": {"model", "jev-1.14.0", "price"},
	} {
		s, sub, k, err := SplitName(in)
		if err != nil || [3]string{s, sub, k} != want {
			t.Errorf("SplitName(%q) = %q %q %q %v", in, s, sub, k, err)
		}
	}
	for _, bad := range []string{"api", "api.", ".key", "a b.c"} {
		if _, _, _, err := SplitName(bad); err == nil {
			t.Errorf("SplitName(%q) should fail", bad)
		}
	}
}

func TestCheck(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg")
	write(t, p, "[api]\nkey = a\nkeyCommand = b\nkeyFile = c\n[limits]\ndayly = 5\ndaily = lots\n[defaults]\nprogress = sometimes\nmaxCost = off\n[bogus]\nx = 1\n[tool \"grev\"]\nanything = 1\n")
	c, err := ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	w := strings.Join(c.Check(), "\n")
	for _, want := range []string{"unknown config key limits.dayly", "limits.daily: want a number",
		"defaults.progress: want always, never or auto", "unknown config key bogus.x", "both api.key and api.keyCommand",
		"unknown config key api.keyfile"} {
		if !strings.Contains(w, want) {
			t.Errorf("Check() missing %q in:\n%s", want, w)
		}
	}
	if strings.Contains(w, "maxcost") || strings.Contains(w, "tool.grev") {
		t.Errorf("false positives in:\n%s", w)
	}
}
