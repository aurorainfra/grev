package clitest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aurorainfra/grev/internal/cli"
	"github.com/aurorainfra/grev/test/harness"
)

// TestDocs checks every tool's man page and completion scripts: they
// generate, lint cleanly (groff, bash -n, zsh -n when installed) and ignore
// a broken config file.
func TestDocs(t *testing.T) {
	env := harness.Env(t, "SOURCE_DATE_EPOCH=1000000000")
	// A broken config must not break help or docs (they're built without it).
	os.WriteFile(filepath.Join(homeOf(env), ".grevconfig"), []byte("[api\n"), 0o600)
	groff, _ := exec.LookPath("groff")
	dir := t.TempDir()
	lint := func(name string, page string) {
		t.Helper()
		if groff == "" {
			return
		}
		cmd := exec.Command(groff, "-man", "-Tutf8", "-ww", "-z")
		cmd.Stdin = strings.NewReader(page)
		if out, err := cmd.CombinedOutput(); err != nil || len(out) > 0 {
			t.Errorf("%s: groff: %v\n%s", name, err, out)
		}
	}
	for _, tool := range cli.Tools {
		r := run(t, env, "", tool.Name, "--help-man")
		if r.Code != 0 || !strings.Contains(r.Stdout, ".TH "+strings.ToUpper(tool.Name)+" 1 2001-09-09") ||
			!strings.Contains(r.Stdout, ".SH EXAMPLES") || !strings.Contains(r.Stdout, ".SH EXIT STATUS") {
			t.Errorf("%s --help-man: %+v", tool.Name, r)
			continue
		}
		lint(tool.Name+"(1)", r.Stdout)
		if r := run(t, env, "", tool.Name, "--help"); r.Code != 0 || !strings.Contains(r.Stdout, "Examples:") ||
			strings.Contains(r.Stdout, "help-man") {
			t.Errorf("%s --help: %+v", tool.Name, r)
		}
		for _, shell := range []string{"bash", "zsh", "fish"} {
			r := run(t, env, "", tool.Name, "--help-completion="+shell)
			if r.Code != 0 || !strings.Contains(r.Stdout, tool.Name) || strings.Contains(r.Stdout, "help-man") {
				t.Errorf("%s --help-completion=%s: %+v", tool.Name, shell, r)
				continue
			}
			sh, err := exec.LookPath(shell)
			if err != nil {
				continue
			}
			f := filepath.Join(dir, tool.Name+"."+shell)
			os.WriteFile(f, []byte(r.Stdout), 0o644)
			if out, err := exec.Command(sh, "-n", f).CombinedOutput(); err != nil {
				t.Errorf("%s: %s -n: %v\n%s", tool.Name, shell, err, out)
			}
		}
	}
	for _, page := range []string{"config", "tools"} {
		r := run(t, env, "", "jev", "--help-man="+page)
		if r.Code != 0 || !strings.Contains(r.Stdout, ".TH ") {
			t.Errorf("jev --help-man=%s: %+v", page, r)
			continue
		}
		lint(page, r.Stdout)
	}
	if r := run(t, env, "", "jev", "--help-man=nope"); r.Code != 2 {
		t.Errorf("unknown page: %+v", r)
	}
	if r := run(t, env, "", "grev", "--help-completion=tcsh"); r.Code != 2 {
		t.Errorf("unknown shell: %+v", r)
	}
}
