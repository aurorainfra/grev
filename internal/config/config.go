// Package config reads and writes ~/.grevconfig, a git-config style file:
//
//	[api]
//		key = tsk-…
//	[defaults]
//		progress = auto
//	[tool "grev"]
//		about = application logs
//
// Section and key names are case-insensitive and ignore dashes (maxCost,
// max-cost and maxcost are one key); subsection names are case-sensitive.
// Later values win. Every value remembers its file and line.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Value is one "key = value" from a config file.
type Value struct {
	Section string // normalized
	Sub     string // as written (case-sensitive), "" for none
	Key     string // normalized
	Raw     string // unquoted value; "true" for a bare key
	File    string
	Line    int
}

// Origin is "file:line", for messages.
func (v Value) Origin() string { return fmt.Sprintf("%s:%d", tildePath(v.File), v.Line) }

// Name is the dotted name of the value: section.key or section.sub.key.
func (v Value) Name() string {
	if v.Sub != "" {
		return v.Section + "." + v.Sub + "." + v.Key
	}
	return v.Section + "." + v.Key
}

// Config is every value from the files that were read, in order.
type Config struct {
	Values []Value
	Files  []string // files actually read, in order
}

// Norm normalizes a section or key name: lower case, without dashes or
// underscores.
func Norm(s string) string {
	s = strings.ToLower(s)
	return strings.NewReplacer("-", "", "_", "").Replace(s)
}

// Get returns the last value of section[.sub].key.
func (c *Config) Get(section, sub, key string) (Value, bool) {
	if c == nil {
		return Value{}, false
	}
	section, key = Norm(section), Norm(key)
	for i := len(c.Values) - 1; i >= 0; i-- {
		v := c.Values[i]
		if v.Section == section && v.Sub == sub && v.Key == key {
			return v, true
		}
	}
	return Value{}, false
}

// Str returns the value of section[.sub].key, or "".
func (c *Config) Str(section, sub, key string) string {
	v, _ := c.Get(section, sub, key)
	return v.Raw
}

// Section returns the effective values of one section, one per key (the
// last one wins), in first-appearance order.
func (c *Config) Section(section, sub string) []Value {
	if c == nil {
		return nil
	}
	section = Norm(section)
	idx := map[string]int{}
	var out []Value
	for _, v := range c.Values {
		if v.Section != section || v.Sub != sub {
			continue
		}
		if i, ok := idx[v.Key]; ok {
			out[i] = v
			continue
		}
		idx[v.Key] = len(out)
		out = append(out, v)
	}
	return out
}

// Subsections lists the subsection names used with section.
func (c *Config) Subsections(section string) []string {
	if c == nil {
		return nil
	}
	section = Norm(section)
	seen := map[string]bool{}
	var out []string
	for _, v := range c.Values {
		if v.Section == section && v.Sub != "" && !seen[v.Sub] {
			seen[v.Sub] = true
			out = append(out, v.Sub)
		}
	}
	return out
}

// ---- Locations ----

// ReadPaths are the files read, in order (later wins): the XDG file, then
// ~/.grevconfig. GREV_CONFIG replaces both; set but empty, it disables config.
func ReadPaths() []string {
	if p, ok := os.LookupEnv("GREV_CONFIG"); ok {
		if p == "" {
			return nil
		}
		return []string{expandHome(p)}
	}
	var out []string
	if x := xdgPath(); x != "" {
		out = append(out, x)
	}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, ".grevconfig"))
	}
	return out
}

// WritePath is where `jev config set` writes: GREV_CONFIG if set, else
// ~/.grevconfig unless only the XDG file exists (git's rule).
func WritePath() string {
	if p, ok := os.LookupEnv("GREV_CONFIG"); ok && p != "" {
		return expandHome(p)
	}
	home, _ := os.UserHomeDir()
	dot := filepath.Join(home, ".grevconfig")
	if _, err := os.Stat(dot); err != nil {
		if x := xdgPath(); x != "" {
			if _, err := os.Stat(x); err == nil {
				return x
			}
		}
	}
	return dot
}

func xdgPath() string {
	if runtime.GOOS == "windows" {
		if d, err := os.UserConfigDir(); err == nil {
			return filepath.Join(d, "grev", "config")
		}
		return ""
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "grev", "config")
}

// Load reads the config files. Missing files are skipped; syntax errors are
// returned with their file:line.
func Load() (*Config, error) {
	c := &Config{}
	for _, p := range ReadPaths() {
		if err := c.readFile(p, 0); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
	}
	return c, nil
}

// ReadFile reads a single file (and its includes).
func ReadFile(path string) (*Config, error) {
	c := &Config{}
	return c, c.readFile(path, 0)
}

const maxInclude = 10

func (c *Config) readFile(path string, depth int) error {
	if depth > maxInclude {
		return fmt.Errorf("%s: includes nested more than %d deep", tildePath(path), maxInclude)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	c.Files = append(c.Files, path)
	vals, err := parse(f, path)
	if err != nil {
		return err
	}
	for _, v := range vals {
		c.Values = append(c.Values, v)
		if v.Section == "include" && v.Key == "path" && v.Sub == "" {
			inc := expandHome(v.Raw)
			if !filepath.IsAbs(inc) {
				inc = filepath.Join(filepath.Dir(path), inc)
			}
			if err := c.readFile(inc, depth+1); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%s: include: %w", v.Origin(), err)
			}
		}
	}
	return nil
}

// ---- Parsing ----

// line is one parsed source line, kept for the writer.
type line struct {
	section, sub string // section in effect after this line
	header       bool   // a [section] header
	key          string // normalized key, "" if not a key line
	last         int    // index of the last physical line (continuations)
}

func parse(f *os.File, path string) ([]Value, error) {
	var out []Value
	_, err := scanLines(f, path, func(v Value) { out = append(out, v) })
	return out, err
}

// scanLines parses the file line by line, calling fn per value, and returns
// per-physical-line structure for the writer.
func scanLines(f *os.File, path string, fn func(Value)) ([]line, error) {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var phys []string
	for sc.Scan() {
		phys = append(phys, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	lines := make([]line, len(phys))
	section, sub := "", ""
	for i := 0; i < len(phys); i++ {
		start := i
		text := phys[i]
		// Continuation: a trailing unescaped backslash joins the next line.
		for strings.HasSuffix(text, `\`) && !strings.HasSuffix(text, `\\`) && i+1 < len(phys) {
			i++
			text = text[:len(text)-1] + phys[i]
		}
		errf := func(format string, a ...any) error {
			return fmt.Errorf("%s:%d: %s", tildePath(path), start+1, fmt.Sprintf(format, a...))
		}
		s := strings.TrimSpace(text)
		switch {
		case s == "" || s[0] == '#' || s[0] == ';':
		case s[0] == '[':
			end := strings.IndexByte(s, ']')
			if end < 0 {
				return nil, errf("missing ] in section header")
			}
			rest := strings.TrimSpace(s[end+1:])
			if rest != "" && rest[0] != '#' && rest[0] != ';' {
				return nil, errf("unexpected text after section header")
			}
			hdr := strings.TrimSpace(s[1:end])
			name, subq, hasSub := strings.Cut(hdr, " ")
			section, sub = Norm(name), ""
			if !validName(name) {
				return nil, errf("bad section name %q", name)
			}
			if hasSub {
				subq = strings.TrimSpace(subq)
				if len(subq) < 2 || subq[0] != '"' || subq[len(subq)-1] != '"' {
					return nil, errf("subsection must be quoted: [%s \"name\"]", name)
				}
				sub = strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(subq[1 : len(subq)-1])
			}
			lines[start].header = true
		default:
			if section == "" {
				return nil, errf("key outside of any [section]")
			}
			k, v, hasV := strings.Cut(s, "=")
			k = strings.TrimSpace(k)
			if !validName(k) {
				return nil, errf("bad key name %q", k)
			}
			raw := "true"
			if hasV {
				var err error
				if raw, err = unquote(strings.TrimSpace(v)); err != nil {
					return nil, errf("%v", err)
				}
			}
			lines[start].key = Norm(k)
			fn(Value{Section: section, Sub: sub, Key: Norm(k), Raw: raw, File: path, Line: start + 1})
		}
		for j := start; j <= i; j++ {
			lines[j].section, lines[j].sub = section, sub
			lines[j].last = i
		}
	}
	return lines, nil
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// unquote handles "quoted" parts, escapes and trailing comments.
func unquote(s string) (string, error) {
	var b strings.Builder
	inQ := false
	pendingSpace := ""
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			if i+1 >= len(s) {
				return "", errors.New("trailing backslash")
			}
			i++
			b.WriteString(pendingSpace)
			pendingSpace = ""
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case '"', '\\':
				b.WriteByte(s[i])
			default:
				return "", fmt.Errorf("unknown escape \\%c", s[i])
			}
		case c == '"':
			b.WriteString(pendingSpace)
			pendingSpace = ""
			inQ = !inQ
		case !inQ && (c == '#' || c == ';'):
			return b.String(), nil
		case !inQ && (c == ' ' || c == '\t'):
			pendingSpace += string(c) // dropped if only trailing
		default:
			b.WriteString(pendingSpace)
			pendingSpace = ""
			b.WriteByte(c)
		}
	}
	if inQ {
		return "", errors.New("unterminated quote")
	}
	return b.String(), nil
}

// ---- Typed values ----

// Bool parses git-style booleans.
func Bool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "on", "1", "":
		return true, nil
	case "false", "no", "off", "0":
		return false, nil
	}
	return false, fmt.Errorf("want a boolean (true/false, yes/no, on/off, 1/0), got %q", s)
}

// Float parses a number, allowing a leading "$".
func Float(s string) (float64, error) {
	f, err := strconv.ParseFloat(strings.TrimPrefix(strings.TrimSpace(s), "$"), 64)
	if err != nil {
		return 0, fmt.Errorf("want a number, got %q", s)
	}
	return f, nil
}

// ExpandHome expands a leading ~/ to the home directory.
func ExpandHome(p string) string { return expandHome(p) }

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// tildePath shows p relative to the home directory as ~/…, with forward
// slashes on every platform (like git's ~/.gitconfig).
func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return p
}
