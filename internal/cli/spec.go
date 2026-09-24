package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/aurorainfra/grev/internal/jev"
)

// Spec is one named question from the probe/jev-ask mini-syntax:
//
//	name: question            → Noul
//	name: question [a|b|c]    → Choice (options may be a=description)
//	name: question <lo|mid|hi> → Score, levels lowest first
//
// The "name:" prefix is optional; unnamed questions are called q1, q2, …
type Spec struct {
	Name string
	Text string // the question text, without the option list
	Q    jev.Question
}

var (
	specName   = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_.-]*)\s*:\s+`)
	specChoice = regexp.MustCompile(`\s*\[([^\[\]]*\|[^\[\]]*)\]\s*$`)
	specScore  = regexp.MustCompile(`\s*<([^<>]*\|[^<>]*)>\s*$`)
)

// ParseSpec parses one question spec; n numbers unnamed questions.
func ParseSpec(s string, n int) (Spec, error) {
	sp := Spec{Name: "q" + strconv.Itoa(n)}
	if m := specName.FindStringSubmatchIndex(s); m != nil {
		sp.Name = s[m[2]:m[3]]
		s = s[m[1]:]
	}
	switch {
	case specChoice.MatchString(s):
		m := specChoice.FindStringSubmatchIndex(s)
		var opts jev.Opts
		for _, o := range strings.Split(s[m[2]:m[3]], "|") {
			k, d, hasD := strings.Cut(strings.TrimSpace(o), "=")
			if k == "" {
				return sp, fmt.Errorf("%s: empty option", sp.Name)
			}
			var desc any
			if hasD {
				desc = strings.TrimSpace(d)
			}
			opts = append(opts, jev.Opt{Key: strings.TrimSpace(k), Desc: desc})
		}
		if len(opts) > 255 {
			return sp, fmt.Errorf("%s: at most 255 options", sp.Name)
		}
		sp.Text = strings.TrimSpace(s[:m[0]])
		sp.Q = jev.Choice(sp.Text, opts)
	case specScore.MatchString(s):
		m := specScore.FindStringSubmatchIndex(s)
		var levels []any
		for _, l := range strings.Split(s[m[2]:m[3]], "|") {
			levels = append(levels, strings.TrimSpace(l))
		}
		if len(levels) > 10 {
			return sp, fmt.Errorf("%s: at most 10 levels", sp.Name)
		}
		sp.Text = strings.TrimSpace(s[:m[0]])
		sp.Q = jev.Score(sp.Text, levels)
	default:
		sp.Text = strings.TrimSpace(s)
		sp.Q = jev.Noul(sp.Text, nil, nil)
	}
	if sp.Text == "" {
		return sp, fmt.Errorf("%s: empty question", sp.Name)
	}
	return sp, nil
}

// ParseLabels parses LABEL[=DESCRIPTION] arguments into Choice options.
func ParseLabels(args []string) (jev.Opts, error) {
	var opts jev.Opts
	seen := map[string]bool{}
	for _, a := range args {
		k, d, hasD := strings.Cut(a, "=")
		k = strings.TrimSpace(k)
		if k == "" {
			return nil, fmt.Errorf("empty label in %q", a)
		}
		if seen[k] {
			return nil, fmt.Errorf("duplicate label %q", k)
		}
		seen[k] = true
		var desc any
		if hasD {
			desc = strings.TrimSpace(d)
		}
		opts = append(opts, jev.Opt{Key: k, Desc: desc})
	}
	if len(opts) > 255 {
		return nil, fmt.Errorf("at most 255 labels")
	}
	return opts, nil
}

// LabelsFile reads "label<TAB>description" lines (description optional).
func LabelsFile(path string) ([]string, error) {
	s, err := ReadInput(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, d, _ := strings.Cut(line, "\t")
		if d != "" {
			out = append(out, k+"="+d)
		} else {
			out = append(out, k)
		}
	}
	return out, nil
}
