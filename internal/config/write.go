package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// SplitName splits a dotted name into section, subsection and key:
// "api.key", "tool.grev.about", "model.jev-1.14.0.price".
func SplitName(name string) (section, sub, key string, err error) {
	i := strings.IndexByte(name, '.')
	j := strings.LastIndexByte(name, '.')
	if i <= 0 || j == len(name)-1 {
		return "", "", "", fmt.Errorf("want section.key or section.subsection.key, got %q", name)
	}
	section, key = name[:i], name[j+1:]
	if j > i {
		sub = name[i+1 : j]
	}
	if !validName(section) || !validName(key) {
		return "", "", "", fmt.Errorf("bad name %q", name)
	}
	return Norm(section), sub, key, nil
}

// Set sets section[.sub].key = value in the file at path, editing in place:
// the last existing occurrence is replaced, else the value is appended to
// the last matching section, else a new section is added. Comments and
// layout are kept. A new file is created with mode 0600.
func Set(path, section, sub, key, value string) error {
	return edit(path, section, sub, key, &value)
}

// Unset removes every occurrence of section[.sub].key from the file.
func Unset(path, section, sub, key string) error {
	return edit(path, section, sub, key, nil)
}

func edit(path, section, sub, key string, value *string) error {
	section, nkey := Norm(section), Norm(key)
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real // keep a dotfiles symlink intact; write its target
	}
	var phys []string
	var lines []line
	mode := os.FileMode(0o600)
	if f, err := os.Open(path); err == nil {
		fi, _ := f.Stat()
		mode = fi.Mode().Perm()
		lines, err = scanLines(f, path, func(Value) {})
		f.Close()
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		phys = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		if len(b) == 0 {
			phys = nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	match := func(i int) bool { return lines[i].section == section && lines[i].sub == sub }
	var out []string
	switch {
	case value == nil:
		found := false
		for i := 0; i < len(phys); i++ {
			if match(i) && lines[i].key == nkey {
				found = true
				i = lines[i].last
				continue
			}
			out = append(out, phys[i])
		}
		if !found {
			return fmt.Errorf("%s: no such key", dotted(section, sub, key))
		}
	default:
		entry := "\t" + key + " = " + quote(*value)
		lastKey, lastInSection := -1, -1
		for i := range phys {
			if match(i) && (lines[i].header || lines[i].key != "") {
				lastInSection = lines[i].last
				if lines[i].key == nkey {
					lastKey = i
				}
			}
		}
		switch {
		case lastKey >= 0:
			out = append(append(append(out, phys[:lastKey]...), entry), phys[lines[lastKey].last+1:]...)
		case lastInSection >= 0:
			out = append(append(append(out, phys[:lastInSection+1]...), entry), phys[lastInSection+1:]...)
		default:
			out = append(out, phys...)
			if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
				out = append(out, "")
			}
			out = append(out, header(section, sub), entry)
		}
	}
	return writeAtomic(path, strings.Join(out, "\n")+"\n", mode)
}

func dotted(section, sub, key string) string {
	if sub != "" {
		return section + "." + sub + "." + key
	}
	return section + "." + key
}

func header(section, sub string) string {
	if sub == "" {
		return "[" + section + "]"
	}
	return fmt.Sprintf("[%s %q]", section, sub)
}

// quote writes a value so that parsing gives it back unchanged.
func quote(v string) string {
	needs := v == "" || strings.TrimSpace(v) != v || strings.ContainsAny(v, "#;\"\\\n\t")
	if !needs {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`)
	return `"` + r.Replace(v) + `"`
}

func isWindows() bool { return runtime.GOOS == "windows" }

func writeAtomic(path, content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".grevconfig.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil && !isWindows() {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
