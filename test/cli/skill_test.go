package clitest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aurorainfra/grev/internal/jevtest"
	"github.com/aurorainfra/grev/test/harness"
)

func TestSkillInstallStatusUninstall(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	home := homeOf(env)
	want, err := os.ReadFile(filepath.Join(harness.Root(), "skills", "grev", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(home, ".claude", "skills", "grev")
	agents := filepath.Join(home, ".agents", "skills", "grev")

	if r := run(t, env, "", "grev-settings", "skill", "status"); r.Code != 1 || strings.Count(r.Stdout, "not installed") != 2 {
		t.Fatalf("status before install: %+v", r)
	}
	r := run(t, env, "", "grev-settings", "skill", "install", "--copy")
	if r.Code != 0 || strings.Count(r.Stdout, "installed") != 2 || !strings.Contains(r.Stdout, "~/.claude/skills/grev") {
		t.Fatalf("install: %+v", r)
	}
	for _, dir := range []string{claude, agents} {
		got, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		if err != nil || string(got) != string(want) {
			t.Fatalf("%s: %v", dir, err)
		}
		if fi, _ := os.Lstat(dir); fi.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("%s: --copy made a symlink", dir)
		}
	}
	if r := run(t, env, "", "grev-settings", "skill", "status"); r.Code != 0 || strings.Count(r.Stdout, "installed") != 2 {
		t.Fatalf("status after install: %+v", r)
	}
	if r := run(t, env, "", "grev-settings", "skill", "install", "--copy"); r.Code != 0 || strings.Count(r.Stdout, "up to date") != 2 {
		t.Fatalf("reinstall: %+v", r)
	}
	// An outdated copy is reported, and refreshed by install.
	os.WriteFile(filepath.Join(agents, "SKILL.md"), append(want, "old\n"...), 0o644)
	if r := run(t, env, "", "grev-settings", "skill", "status"); r.Code != 1 || !strings.Contains(r.Stdout, "outdated") {
		t.Fatalf("status of an outdated copy: %+v", r)
	}
	if r := run(t, env, "", "grev-settings", "skill", "install"); r.Code != 0 || !strings.Contains(r.Stdout, "updated") {
		t.Fatalf("update: %+v", r)
	}
	// --agent limits the targets.
	if r := run(t, env, "", "grev-settings", "skill", "uninstall", "--agent", "claude"); r.Code != 0 || !strings.Contains(r.Stdout, "removed    ~/.claude/skills/grev") {
		t.Fatalf("uninstall claude: %+v", r)
	}
	if _, err := os.Stat(claude); !os.IsNotExist(err) {
		t.Fatal("claude copy still there")
	}
	if _, err := os.Stat(filepath.Join(agents, "SKILL.md")); err != nil {
		t.Fatal("agents copy removed too")
	}
	if r := run(t, env, "", "grev-settings", "skill", "show"); r.Code != 0 || r.Stdout != string(want) {
		t.Fatalf("show: %+v", r)
	}
	usageError(t, env, "", "grev-settings", "skill", "install", "--agent", "vim")
	usageError(t, env, "", "grev-settings", "skill", "install", "--project", "--system")
	usageError(t, env, "", "grev-settings", "skill", "frobnicate")
}

func TestSkillProjectAndForeign(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	proj := t.TempDir()
	mine := "---\nname: grev\ndescription: my own notes\n---\n"
	os.MkdirAll(filepath.Join(proj, ".claude", "skills", "grev"), 0o755)
	os.WriteFile(filepath.Join(proj, ".claude", "skills", "grev", "SKILL.md"), []byte(mine), 0o644)

	r := harness.RunDir(t, proj, env, "", "grev-settings", "skill", "install", "--project")
	if r.Code != 2 || !strings.Contains(r.Stderr, "--force") {
		t.Fatalf("install over a foreign skill: %+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(proj, ".claude", "skills", "grev", "SKILL.md")); string(b) != mine {
		t.Fatal("foreign skill was touched")
	}
	if _, err := os.Stat(filepath.Join(proj, ".agents", "skills", "grev", "SKILL.md")); err != nil {
		t.Fatal("the other target should still be installed")
	}
	r = harness.RunDir(t, proj, env, "", "grev-settings", "skill", "install", "--project", "--force")
	if r.Code != 0 || !strings.Contains(r.Stdout, "the skill that was there is now") {
		t.Fatalf("install --force: %+v", r)
	}
	baks, _ := filepath.Glob(filepath.Join(proj, ".claude", "skills", "grev.bak-*", "SKILL.md"))
	if len(baks) != 1 {
		t.Fatalf("backup: %v", baks)
	}
	if b, _ := os.ReadFile(baks[0]); string(b) != mine {
		t.Fatal("backup content differs")
	}
	// Nothing was written to the user's home.
	if _, err := os.Stat(filepath.Join(homeOf(env), ".claude", "skills")); !os.IsNotExist(err) {
		t.Fatal("--project wrote to HOME")
	}
}
