package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aurorainfra/grev/internal/config"
)

func docParser() *Parser {
	p := NewParser("demo")
	p.Short = "do things by meaning"
	p.Synopsis = []string{"[OPTIONS] QUERY [FILE...]"}
	p.About = "Prose with --max-cost, a \\ backslash, an ellipsis … and limits.daily.\n" +
		".starts with a dot\n\n  indented line\n  another -x\nback to prose."
	p.Flag('v', "invert", "select the others")
	p.Int('m', "max-count", "N", 0, "stop after N")
	p.OptFunc(0, "color", "WHEN", "always", "colour output", func(string) error { return nil })
	p.List('q', "question", "SPEC", "a question (repeatable)")
	p.Str(0, "input", "FILE", "", "read FILE")
	p.Str(0, "mode", "any|all|mean", "", "combine chunks")
	p.Func(0, "hidden", "", "", func(string) error { return nil })
	p.Func(0, "help", "", "show this help", func(string) error { return nil })
	p.markCommon() // all of the above become "common"; add tool options next
	p.Flag('s', "scores", "print scores; a ']' and a 'quote' and a : colon")
	p.Str('d', "dir", "DIR", "", "write into DIR")
	p.Notes = "A note."
	p.ExitStatus = "0 ok, 1 none."
	p.Examples = []string{`demo -s 'a\tb' | sort`, "demo --long x |\n  awk '{print $1}'", ".dotcmd"}
	return p
}

func groffLint(t *testing.T, page []byte) {
	t.Helper()
	groff, err := exec.LookPath("groff")
	if err != nil {
		t.Log("groff not installed; skipping roff lint")
		return
	}
	cmd := exec.Command(groff, "-man", "-Tutf8", "-ww", "-z")
	cmd.Stdin = bytes.NewReader(page)
	out, err := cmd.CombinedOutput()
	if err != nil || len(out) > 0 {
		t.Fatalf("groff warnings: %v\n%s", err, out)
	}
}

func TestEsc(t *testing.T) {
	got := esc("use --max-cost, `jev spend`, a\\b, 1…2, re-run and limits.daily")
	for _, want := range []string{`\%\-\-max\-cost,`, `\fBjev spend\fR`, `\%a\eb`, `1\[u2026]2`, `re-run`, `\%limits.daily`} {
		if !strings.Contains(got, want) {
			t.Errorf("esc: missing %q in %q", want, got)
		}
	}
	if escCode("-a --b") != `\-a \-\-b` {
		t.Errorf("escCode: %q", escCode("-a --b"))
	}
	if safe(".x") != `\&.x` || safe("'x") != `\&'x` || safe("x.") != "x." {
		t.Error("safe")
	}
}

func TestMan(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1000000000")
	p := docParser()
	var b bytes.Buffer
	p.Man(&b, manDate(), "v1.2.3")
	out := b.String()
	for _, want := range []string{
		`.TH DEMO 1 2001-09-09 "grev tools v1.2.3" "grev tools manual"`,
		".SH NAME\ndemo \\- do things by meaning\n",
		"\\fBdemo\\fR [OPTIONS] QUERY [FILE...]",
		".SH OPTIONS\n.TP\n\\fB\\-s\\fR, \\fB\\-\\-scores\\fR\n",
		"\\fB\\-d\\fR, \\fB\\-\\-dir\\fR=\\fIDIR\\fR",
		"\\fB\\-\\-color\\fR[=\\fIWHEN\\fR]",
		".SH COMMON OPTIONS",
		"\n\\%.starts with a dot\n", // \% (no hyphenation) also keeps the dot from starting a request
		".RS 4\n.nf\nindented line\nanother \\%\\-x\n.fi\n.RE\nback to prose.",
		".SH NOTES\nA note.",
		".SH EXIT STATUS\n0 ok, 1 none.",
		"demo \\-s 'a\\etb' | sort\ndemo \\-\\-long x |\n  awk '{print $1}'\n\\&.dotcmd\n",
		".SH ENVIRONMENT", "GREV_CONFIG", ".SH FILES", ".SH SEE ALSO", ".BR \\%grevconfig (5),",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("man page missing %q", want)
		}
	}
	if strings.Contains(out, "hidden") {
		t.Error("hidden option documented")
	}
	groffLint(t, b.Bytes())
}

func TestConfigAndToolsMan(t *testing.T) {
	var b bytes.Buffer
	configMan(&b, "2026-01-01", "v1")
	out := b.String()
	for _, k := range config.Schema {
		if !strings.Contains(out, `\fB`+escCode(k.Name)+`\fR`) {
			t.Errorf("grevconfig(5) misses %s.%s", k.Section, k.Name)
		}
	}
	if !strings.Contains(out, `.SS "[tool \(dqNAME\(dq]"`) {
		t.Error("subsection header not quoted as one macro argument")
	}
	groffLint(t, b.Bytes())

	b.Reset()
	toolsMan(&b, "2026-01-01", "v1")
	for _, tool := range Tools {
		if !strings.Contains(b.String(), `\fB`+escCode(tool.Name)+`\fR(1)`) {
			t.Errorf("grev-tools(7) misses %s", tool.Name)
		}
	}
	groffLint(t, b.Bytes())
}

func TestHelpSections(t *testing.T) {
	var b bytes.Buffer
	docParser().Help(&b)
	out := b.String()
	for _, want := range []string{"\nA note.\n", "\nExit status: 0 ok, 1 none.\n",
		"\nExamples:\n  demo -s 'a\\tb' | sort\n  demo --long x |\n    awk"} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %q in:\n%s", want, out)
		}
	}
}

func TestCompletion(t *testing.T) {
	p := docParser()
	dir := t.TempDir()
	for _, shell := range []string{"bash", "zsh", "fish"} {
		var b bytes.Buffer
		if err := p.Completion(&b, shell); err != nil {
			t.Fatal(err)
		}
		out := b.String()
		for _, o := range []string{"invert", "max-count", "color", "question", "scores", "dir"} {
			if !strings.Contains(out, o) {
				t.Errorf("%s completion misses --%s", shell, o)
			}
		}
		if strings.Contains(out, "hidden") {
			t.Errorf("%s completion offers a hidden option", shell)
		}
		path := filepath.Join(dir, "demo."+shell)
		os.WriteFile(path, b.Bytes(), 0o644)
		if sh, err := exec.LookPath(shell); err == nil {
			if out, err := exec.Command(sh, "-n", path).CombinedOutput(); err != nil {
				t.Errorf("%s -n: %v\n%s", shell, err, out)
			}
		}
		switch shell {
		case "bash":
			for _, want := range []string{"--input) COMPREPLY=($(compgen -f", "-d|--dir) COMPREPLY=($(compgen -d",
				"--mode) COMPREPLY=($(compgen -W 'any all mean'", "complete -o filenames -F _grev_demo demo"} {
				if !strings.Contains(out, want) {
					t.Errorf("bash: missing %q", want)
				}
			}
		case "zsh":
			for _, want := range []string{"#compdef demo", `'*-q+[a question (repeatable)]:SPEC:'`,
				`'--color=-[colour output]::WHEN:'`, `(-d --dir)-d+[write into DIR]:DIR:_files -/`,
				`\]`, `'\''quote'\''`} {
				if !strings.Contains(out, want) {
					t.Errorf("zsh: missing %q in\n%s", want, out)
				}
			}
		case "fish":
			for _, want := range []string{"complete -c demo -l input -r -F", `-l mode -x -a 'any all mean'`, `\'quote\'`} {
				if !strings.Contains(out, want) {
					t.Errorf("fish: missing %q", want)
				}
			}
		}
	}
	if err := p.Completion(&bytes.Buffer{}, "tcsh"); err == nil {
		t.Error("unknown shell accepted")
	}
}
