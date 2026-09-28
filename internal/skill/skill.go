// Package skill installs the grev agent skill (skills/grev/SKILL.md) where
// coding agents look for skills:
//
//   - ~/.claude/skills/grev: Claude Code
//   - ~/.agents/skills/grev: the cross-agent directory read by Codex, Gemini
//     CLI, GitHub Copilot, Cursor, OpenCode, Goose, Amp and others
//
// plus the same pair under a project directory, or the admin directories
// read by every user on the machine (Claude Code's managed settings
// directory, /etc/codex/skills).
//
// When a package installed the skill under share/grev/skills/grev and it
// matches this binary's copy, the target is a symlink to it, so package
// upgrades update the skill too. Otherwise the embedded files are copied.
package skill

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Name is the skill's directory name and frontmatter name.
const Name = "grev"

// Source identifies files we installed (the skill's metadata.source).
const Source = "https://github.com/aurorainfra/grev"

// Scope says whose agents get the skill.
type Scope int

const (
	User    Scope = iota // the current user's home directory
	Project              // a project directory (the current one)
	System               // admin directories, for every user on the machine
)

func (s Scope) String() string {
	return [...]string{"user", "project", "system"}[s]
}

// Target is one skill directory to manage.
type Target struct {
	Agent string // "claude" or "agents"
	For   string // who reads it, for messages
	Dir   string // the skill directory itself, e.g. ~/.claude/skills/grev
}

const agentsFor = "Codex, Gemini CLI, Copilot, Cursor, OpenCode, Goose, Amp"

// Targets lists the skill directories for scope and agent ("all", "claude"
// or "agents"). base is the home directory (User) or project directory
// (Project); it is ignored for System.
func Targets(scope Scope, agent, base string) ([]Target, error) {
	var out []Target
	add := func(a, forWhom, dir string) {
		if agent == "all" || agent == a {
			out = append(out, Target{Agent: a, For: forWhom, Dir: filepath.Join(dir, Name)})
		}
	}
	switch agent {
	case "all", "claude", "agents":
	default:
		return nil, fmt.Errorf("unknown agent %q: want all, claude or agents", agent)
	}
	switch scope {
	case User, Project:
		add("claude", "Claude Code", filepath.Join(base, ".claude", "skills"))
		add("agents", agentsFor, filepath.Join(base, ".agents", "skills"))
	case System:
		add("claude", "Claude Code (managed, all users)", filepath.Join(claudeManagedDir(), ".claude", "skills"))
		if runtime.GOOS != "windows" {
			add("agents", "Codex (admin, all users)", "/etc/codex/skills")
		}
	}
	return out, nil
}

// claudeManagedDir is Claude Code's managed settings directory.
func claudeManagedDir() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/ClaudeCode"
	case "windows":
		return `C:\Program Files\ClaudeCode`
	}
	return "/etc/claude-code"
}

// PackagedDirs are where a package may have installed the skill, most
// stable first. exe is this binary's path (or "").
func PackagedDirs(exe string) []string {
	dirs := []string{
		"/usr/share/grev/skills/grev",
		"/usr/local/share/grev/skills/grev",
		"/opt/homebrew/share/grev/skills/grev",
		"/home/linuxbrew/.linuxbrew/share/grev/skills/grev",
	}
	if exe != "" {
		bin := filepath.Dir(exe)
		dirs = append(dirs,
			filepath.Join(bin, "..", "share", "grev", "skills", "grev"), // PREFIX/bin → PREFIX/share
			filepath.Join(bin, "skills", "grev"))                        // release archive layout
	}
	return dirs
}

// State of a target directory.
type State int

const (
	Missing  State = iota
	Current        // ours, same content as this binary's copy
	Outdated       // ours, different content (another version)
	Broken         // ours, a symlink whose package copy is gone
	Foreign        // not ours: someone else's skill named grev
)

func (s State) String() string {
	return [...]string{"not installed", "installed", "outdated", "broken link", "not ours"}[s]
}

// Skill is the embedded copy (rooted at the skill directory) and where to
// look for a packaged one.
type Skill struct {
	FS       fs.FS    // files of the skill: SKILL.md, …
	Packaged []string // candidate packaged directories, see PackagedDirs
	NoLink   bool     // always copy
}

// Status reports the state of dir, and the symlink target if it is one.
func (s Skill) Status(dir string) (State, string) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return Missing, ""
	}
	link := ""
	if fi.Mode()&fs.ModeSymlink != 0 {
		link, _ = os.Readlink(dir)
		if _, err := os.Stat(dir); err != nil {
			if filepath.Base(link) == Name && filepath.Base(filepath.Dir(link)) == "skills" {
				return Broken, link
			}
			return Foreign, link
		}
	}
	if !ours(dir) {
		return Foreign, link
	}
	if s.same(os.DirFS(dir)) {
		return Current, link
	}
	return Outdated, link
}

// ours reports whether dir holds our skill: frontmatter name grev and our
// source URL.
func ours(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return false
	}
	text := strings.ReplaceAll(string(b), "\r\n", "\n") // edited or copied on Windows
	fm, _, ok := strings.Cut(strings.TrimPrefix(text, "---\n"), "\n---")
	return ok && strings.Contains("\n"+fm+"\n", "\nname: "+Name+"\n") && strings.Contains(fm, Source)
}

// same reports whether other has every file of the embedded skill, with the
// same content.
func (s Skill) same(other fs.FS) bool {
	same := true
	err := fs.WalkDir(s.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		a, _ := fs.ReadFile(s.FS, p)
		b, err := fs.ReadFile(other, p)
		if err != nil || !bytes.Equal(a, b) {
			same = false
			return fs.SkipAll
		}
		return nil
	})
	return err == nil && same
}

// packaged returns a packaged copy identical to the embedded one, if any.
func (s Skill) packaged() string {
	if s.NoLink || runtime.GOOS == "windows" {
		return ""
	}
	for _, d := range s.Packaged {
		if abs, err := filepath.Abs(d); err == nil && s.same(os.DirFS(abs)) {
			return filepath.Clean(abs)
		}
	}
	return ""
}

// ErrForeign is returned when a target holds a skill we didn't install.
var ErrForeign = errors.New("a different skill named grev is already there")

// Result describes what Install did to one target.
type Result struct {
	Target
	Link   string // symlink target, if linked
	Backup string // where a foreign skill was moved (with force)
	Was    State
}

// Install puts the skill at t.Dir, replacing an older copy of ours. A
// foreign skill is left alone unless force, in which case it is renamed
// aside first.
func (s Skill) Install(t Target, force bool) (Result, error) {
	res := Result{Target: t}
	st, _ := s.Status(t.Dir)
	res.Was = st
	if st == Foreign {
		if !force {
			return res, fmt.Errorf("%s: %w (use --force to move it aside)", t.Dir, ErrForeign)
		}
		res.Backup = t.Dir + ".bak-" + time.Now().Format("20060102-150405")
		if err := os.Rename(t.Dir, res.Backup); err != nil {
			return res, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(t.Dir), 0o755); err != nil {
		return res, err
	}
	if src := s.packaged(); src != "" {
		if err := removeOurs(t.Dir); err != nil {
			return res, err
		}
		res.Link = src
		return res, os.Symlink(src, t.Dir)
	}
	// Copy into a fresh directory, then swap it in.
	tmp := t.Dir + ".tmp"
	os.RemoveAll(tmp)
	err := fs.WalkDir(s.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(tmp, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := fs.ReadFile(s.FS, p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err == nil {
		if err = removeOurs(t.Dir); err == nil {
			err = os.Rename(tmp, t.Dir)
		}
	}
	if err != nil {
		os.RemoveAll(tmp)
	}
	return res, err
}

// Uninstall removes the skill at t.Dir if it is ours. It reports whether
// anything was removed; a foreign skill is an error.
func (s Skill) Uninstall(t Target) (bool, error) {
	switch st, _ := s.Status(t.Dir); st {
	case Missing:
		return false, nil
	case Foreign:
		return false, fmt.Errorf("%s: %w; left alone", t.Dir, ErrForeign)
	}
	return true, removeOurs(t.Dir)
}

// removeOurs removes dir, which is missing, a symlink, or our copy.
func removeOurs(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return os.Remove(dir)
	}
	return os.RemoveAll(dir)
}
