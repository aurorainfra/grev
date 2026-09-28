package skill

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/aurorainfra/grev/internal/cli"
	"github.com/aurorainfra/grev/skills"
)

const body = "---\nname: grev\ndescription: test\nmetadata:\n  source: " + Source + "\n---\n\n# grev\n"

func testSkill(t *testing.T) Skill {
	t.Helper()
	return Skill{FS: fstest.MapFS{"SKILL.md": {Data: []byte(body)}, "references/x.md": {Data: []byte("x\n")}}}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTargets(t *testing.T) {
	ts, err := Targets(User, "all", "/home/u")
	if err != nil || len(ts) != 2 || ts[0].Dir != filepath.FromSlash("/home/u/.claude/skills/grev") ||
		ts[1].Dir != filepath.FromSlash("/home/u/.agents/skills/grev") {
		t.Fatalf("user targets: %+v %v", ts, err)
	}
	if ts, _ := Targets(Project, "agents", "/p"); len(ts) != 1 || ts[0].Dir != filepath.FromSlash("/p/.agents/skills/grev") {
		t.Fatalf("project agents: %+v", ts)
	}
	if ts, _ := Targets(System, "claude", ""); len(ts) != 1 || !strings.Contains(ts[0].Dir, "ClaudeCode") && !strings.HasPrefix(ts[0].Dir, "/etc/claude-code/") {
		t.Fatalf("system claude: %+v", ts)
	}
	if _, err := Targets(User, "vim", "/h"); err == nil {
		t.Fatal("unknown agent accepted")
	}
}

func TestPackagedDirs(t *testing.T) {
	dirs := PackagedDirs(filepath.FromSlash("/opt/grev/bin/grev-settings"))
	want := filepath.Join(filepath.FromSlash("/opt/grev/bin"), "..", "share", "grev", "skills", "grev")
	if dirs[len(dirs)-1] != want {
		t.Fatalf("PREFIX/share candidate missing: %v", dirs)
	}
	for _, d := range dirs {
		if strings.Contains(d, filepath.Join("bin", "skills")) {
			t.Fatalf("a release archive's skills/ must be copied, not linked: %v", dirs)
		}
	}
}

func TestInstallCopyUpdateUninstall(t *testing.T) {
	s := testSkill(t)
	dir := filepath.Join(t.TempDir(), ".claude", "skills", "grev")
	tg := Target{Agent: "claude", Dir: dir}
	if st, _ := s.Status(dir); st != Missing {
		t.Fatalf("fresh: %v", st)
	}
	r, err := s.Install(tg, false)
	if err != nil || r.Link != "" || r.Was != Missing {
		t.Fatalf("install: %+v %v", r, err)
	}
	if read(t, filepath.Join(dir, "SKILL.md")) != body || read(t, filepath.Join(dir, "references", "x.md")) != "x\n" {
		t.Fatal("copied content differs")
	}
	if st, _ := s.Status(dir); st != Current {
		t.Fatalf("after install: %v", st)
	}
	// Idempotent.
	if _, err := s.Install(tg, false); err != nil {
		t.Fatal(err)
	}
	// An older copy of ours is outdated, and replaced by install.
	os.WriteFile(filepath.Join(dir, "references", "x.md"), []byte("old\n"), 0o644)
	if st, _ := s.Status(dir); st != Outdated {
		t.Fatalf("modified: %v", st)
	}
	if r, err := s.Install(tg, false); err != nil || r.Was != Outdated {
		t.Fatalf("update: %+v %v", r, err)
	}
	if st, _ := s.Status(dir); st != Current {
		t.Fatalf("after update: %v", st)
	}
	if _, err := os.Stat(dir + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temporary directory left behind")
	}
	if ok, err := s.Uninstall(tg); !ok || err != nil {
		t.Fatalf("uninstall: %v %v", ok, err)
	}
	if ok, err := s.Uninstall(tg); ok || err != nil {
		t.Fatalf("uninstall again: %v %v", ok, err)
	}
}

func TestForeignSkillIsKept(t *testing.T) {
	s := testSkill(t)
	dir := filepath.Join(t.TempDir(), "grev")
	os.MkdirAll(dir, 0o755)
	mine := "---\nname: grev\ndescription: my own grev notes\n---\n"
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(mine), 0o644)
	tg := Target{Dir: dir}
	if st, _ := s.Status(dir); st != Foreign {
		t.Fatalf("status: %v", st)
	}
	if _, err := s.Install(tg, false); !errors.Is(err, ErrForeign) {
		t.Fatalf("install over foreign: %v", err)
	}
	if _, err := s.Uninstall(tg); !errors.Is(err, ErrForeign) || read(t, filepath.Join(dir, "SKILL.md")) != mine {
		t.Fatalf("uninstall must leave a foreign skill alone: %v", err)
	}
	r, err := s.Install(tg, true)
	if err != nil || r.Backup == "" || read(t, filepath.Join(r.Backup, "SKILL.md")) != mine {
		t.Fatalf("force: %+v %v", r, err)
	}
	if st, _ := s.Status(dir); st != Current {
		t.Fatalf("after force: %v", st)
	}
}

func TestOursToleratesCRLF(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(strings.ReplaceAll(body, "\n", "\r\n")), 0o644)
	if !ours(dir) {
		t.Fatal("a CRLF copy of our skill is not recognised")
	}
}

func TestInstallLinksToPackagedCopy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("copies on Windows")
	}
	root := t.TempDir()
	share := filepath.Join(root, "usr", "share", "grev", "skills", "grev")
	os.MkdirAll(filepath.Join(share, "references"), 0o755)
	os.WriteFile(filepath.Join(share, "SKILL.md"), []byte(body), 0o644)
	os.WriteFile(filepath.Join(share, "references", "x.md"), []byte("x\n"), 0o644)
	other := filepath.Join(root, "old", "grev")
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, "SKILL.md"), []byte(body+"older version\n"), 0o644)

	s := testSkill(t)
	s.Packaged = []string{filepath.Join(root, "missing"), other, share} // other differs, so share wins
	dir := filepath.Join(root, "home", ".agents", "skills", "grev")
	r, err := s.Install(Target{Dir: dir}, false)
	if err != nil || r.Link != share {
		t.Fatalf("link install: %+v %v", r, err)
	}
	if st, link := s.Status(dir); st != Current || link != share {
		t.Fatalf("status: %v %q", st, link)
	}
	// The package goes away: the link is broken, and install replaces it with a copy.
	os.RemoveAll(filepath.Join(root, "usr"))
	if st, _ := s.Status(dir); st != Broken {
		t.Fatalf("after package removal: %v", st)
	}
	if r, err := s.Install(Target{Dir: dir}, false); err != nil || r.Link != "" || r.Was != Broken {
		t.Fatalf("reinstall: %+v %v", r, err)
	}
	if fi, _ := os.Lstat(dir); fi.Mode()&fs.ModeSymlink != 0 || read(t, filepath.Join(dir, "SKILL.md")) != body {
		t.Fatal("expected a copy")
	}
	// NoLink forces a copy even when a packaged copy matches.
	s.NoLink = true
	if s.packaged() != "" {
		t.Fatal("NoLink ignored")
	}
}

// TestShippedSkill checks skills/grev/SKILL.md against the Agent Skills
// spec (agentskills.io/specification) and that it mentions every tool.
func TestShippedSkill(t *testing.T) {
	b, err := fs.ReadFile(skills.FS, "grev/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "\r") {
		t.Fatal("SKILL.md has CR line endings; .gitattributes should keep skills/ at LF")
	}
	text := string(b)
	if !strings.HasPrefix(text, "---\n") {
		t.Fatal("no frontmatter")
	}
	fm, rest, ok := strings.Cut(text[4:], "\n---\n")
	if !ok {
		t.Fatal("unterminated frontmatter")
	}
	fields := map[string]string{}
	for _, line := range strings.Split(fm, "\n") {
		if strings.HasPrefix(line, "  ") {
			continue // metadata entries
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("frontmatter line %q", line)
		}
		fields[k] = strings.TrimSpace(v)
	}
	for k := range fields {
		switch k {
		case "name", "description", "license", "compatibility", "metadata", "allowed-tools":
		default:
			t.Errorf("frontmatter key %q is not in the spec (claude.ai rejects unknown keys)", k)
		}
	}
	if fields["name"] != Name || !regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`).MatchString(fields["name"]) {
		t.Errorf("name %q", fields["name"])
	}
	if d := fields["description"]; len(d) == 0 || len(d) > 1024 || strings.Contains(d, ": ") {
		t.Errorf("description: %d chars (1..1024, and no ': ' so it stays a plain YAML scalar)", len(d))
	}
	if len(fields["compatibility"]) > 500 {
		t.Error("compatibility over 500 chars")
	}
	if !strings.Contains(fm, "source: "+Source) {
		t.Error("metadata.source missing; install can't recognise its own files")
	}
	if n := strings.Count(rest, "\n"); n > 500 {
		t.Errorf("body is %d lines; the spec recommends under 500", n)
	}
	for _, tool := range cli.Tools {
		if !strings.Contains(text, tool.Name) {
			t.Errorf("SKILL.md never mentions %s", tool.Name)
		}
	}
	// And the embedded copy is what Install writes.
	sub, _ := fs.Sub(skills.FS, "grev")
	s := Skill{FS: sub}
	dir := filepath.Join(t.TempDir(), "grev")
	if _, err := s.Install(Target{Dir: dir}, false); err != nil || read(t, filepath.Join(dir, "SKILL.md")) != text {
		t.Fatalf("install of the shipped skill: %v", err)
	}
	if st, _ := s.Status(dir); st != Current {
		t.Fatalf("shipped skill not recognised as ours: %v", st)
	}
}
