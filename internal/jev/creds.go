package jev

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Credential sources, in resolution order.
const (
	SrcEnv     = "TYPESAFE_API_KEY"
	SrcEnvFile = "TYPESAFE_API_KEY_FILE"
	SrcSystemd = "$CREDENTIALS_DIRECTORY/typesafe_api_key"
	SrcConfig  = "config"
)

// Key is a resolved API key and where it came from. Never print Value.
type Key struct {
	Value  string
	Source string // one of the Src* constants, or "api.key (~/.grevconfig:2)"
	Path   string // file the key was read from, if any
	Warn   string // non-fatal problem, e.g. loose file permissions
}

// Masked shows just enough of the key to tell keys apart.
func (k Key) Masked() string {
	if len(k.Value) <= 8 {
		return "…"
	}
	return "…" + k.Value[len(k.Value)-4:]
}

// KeyConfig is the key setting from the config file: api.key or
// api.keyCommand, with the origin (file:line) it came from.
type KeyConfig struct {
	Key, Command string
	Name         string // api.key or api.keyCommand
	Origin       string // file:line
	ConfigFile   string // the config file itself (for api.key permission checks)
}

// ErrNoKey means no source provided a key.
var ErrNoKey = errors.New("no API key: run `jev key set` to store one in ~/.grevconfig")

// LoadKey resolves the API key: TYPESAFE_API_KEY, TYPESAFE_API_KEY_FILE, a
// systemd credential (for CI, containers and services), then the config
// (api.key or api.keyCommand). The first source present wins.
func LoadKey(kc KeyConfig) (Key, error) {
	env, envSet := os.LookupEnv("TYPESAFE_API_KEY")
	file := os.Getenv("TYPESAFE_API_KEY_FILE")
	if envSet && file != "" {
		return Key{}, errors.New("both TYPESAFE_API_KEY and TYPESAFE_API_KEY_FILE are set; unset one")
	}
	if envSet {
		v, err := CleanKey(env)
		if err != nil {
			return Key{}, fmt.Errorf("TYPESAFE_API_KEY: %w", err)
		}
		return Key{Value: v, Source: SrcEnv}, nil
	}
	if file != "" {
		return keyFromFile(file, SrcEnvFile, true)
	}
	if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
		p := filepath.Join(dir, "typesafe_api_key")
		if _, err := os.Stat(p); err == nil {
			return keyFromFile(p, SrcSystemd, false)
		}
	}
	src := kc.Name + " (" + kc.Origin + ")"
	switch {
	case kc.Key != "":
		v, err := CleanKey(kc.Key)
		if err != nil {
			return Key{}, fmt.Errorf("%s: %w", src, err)
		}
		k := Key{Value: v, Source: src, Path: kc.ConfigFile}
		k.Warn = permWarning(kc.ConfigFile)
		return k, nil
	case kc.Command != "":
		return keyFromCommand(kc.Command, src)
	}
	return Key{}, ErrNoKey
}

func permWarning(path string) string {
	if path == "" || runtime.GOOS == "windows" {
		return ""
	}
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 {
		return fmt.Sprintf("%s holds the API key but is accessible by others (mode %04o); run: chmod 600 %s",
			path, fi.Mode().Perm(), path)
	}
	return ""
}

// keyFromCommand runs a password-manager style command; its stdout is the
// key. Errors never include the command's output.
func keyFromCommand(command, src string) (Key, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Stdin = nil
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			err = errors.New("timed out after 10s")
		}
		return Key{}, fmt.Errorf("%s: command failed: %v", src, err)
	}
	v, err := CleanKey(string(out))
	if err != nil {
		return Key{}, fmt.Errorf("%s: command output: %w", src, err)
	}
	return Key{Value: v, Source: src}, nil
}

func keyFromFile(path, src string, checkPerm bool) (Key, error) {
	path = expandHome(path)
	b, err := os.ReadFile(path)
	if err != nil {
		// The error names the path, never the content.
		return Key{}, fmt.Errorf("%s: %w", src, err)
	}
	v, err := CleanKey(string(b))
	if err != nil {
		return Key{}, fmt.Errorf("%s (%s): %w", src, path, err)
	}
	k := Key{Value: v, Source: src, Path: path}
	if checkPerm {
		k.Warn = permWarning(path)
	}
	return k, nil
}

// CleanKey trims surrounding whitespace and rejects malformed keys, following
// the official SDK's rules. Errors never include the key itself.
func CleanKey(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("empty key")
	}
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			return "", errors.New("key contains whitespace")
		case r < 0x20 || r == 0x7f:
			return "", errors.New("key contains control characters")
		case r > 0x7e:
			return "", errors.New("key contains non-ASCII characters")
		}
	}
	return s, nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
