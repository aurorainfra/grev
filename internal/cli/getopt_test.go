package cli

import (
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	type res struct {
		v, n, c bool
		ctx     int
		j       string
		model   string
		list    []string
		other   string
		pos     []string
	}
	cases := []struct {
		name string
		args []string
		want res
		err  string
	}{
		{"bundled", []string{"-vnc", "q"}, res{v: true, n: true, c: true, pos: []string{"q"}}, ""},
		{"attached int", []string{"-C3", "q"}, res{ctx: 3, pos: []string{"q"}}, ""},
		{"attached string", []string{"-Jmax"}, res{j: "max"}, ""},
		{"bundle then value", []string{"-vC", "2"}, res{v: true, ctx: 2}, ""},
		{"bundle attached value", []string{"-nJ16", "x"}, res{n: true, j: "16", pos: []string{"x"}}, ""},
		{"separate value", []string{"-J", "4"}, res{j: "4"}, ""},
		{"long eq", []string{"--context=5"}, res{ctx: 5}, ""},
		{"long space", []string{"--model", "jev-1.13.0"}, res{model: "jev-1.13.0"}, ""},
		{"permute", []string{"query", "-v", "file", "-n"}, res{v: true, n: true, pos: []string{"query", "file"}}, ""},
		{"double dash", []string{"-v", "--", "-n", "--x"}, res{v: true, pos: []string{"-n", "--x"}}, ""},
		{"dash positional", []string{"q", "-"}, res{pos: []string{"q", "-"}}, ""},
		{"repeatable", []string{"-q", "a", "--question=b", "-qc"}, res{list: []string{"a", "b", "c"}}, ""},
		{"optional default", []string{"--other"}, res{other: "other"}, ""},
		{"optional value", []string{"--other=misc"}, res{other: "misc"}, ""},
		{"value may start with dash", []string{"-M", "-weird"}, res{model: "-weird"}, ""},
		{"unknown short", []string{"-x"}, res{}, "unknown option -x"},
		{"unknown long", []string{"--nope"}, res{}, "unknown option --nope"},
		{"missing short value", []string{"-C"}, res{}, "needs a value"},
		{"missing long value", []string{"--context"}, res{}, "needs a value"},
		{"bool with value", []string{"--invert=yes"}, res{}, "takes no value"},
		{"bad int", []string{"-Cx"}, res{}, "want an integer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewParser("t")
			v := p.Flag('v', "invert", "")
			n := p.Flag('n', "line-number", "")
			c := p.Flag('c', "count", "")
			ctx := p.Int('C', "context", "N", 0, "")
			j := p.Str('J', "jobs", "N", "", "")
			model := p.Str('M', "model", "M", "", "")
			list := p.List('q', "question", "Q", "")
			other := p.OptStr("other", "NAME", "other", "")
			pos, err := p.Parse(tc.args)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := res{v: *v, n: *n, c: *c, ctx: *ctx, j: *j, model: *model, list: *list, pos: pos}
			if *other != nil {
				got.other = **other
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSeenAndFloat(t *testing.T) {
	p := NewParser("t")
	f := p.Float('t', "threshold", "P", 0.5, "")
	p.Flag('H', "with-filename", "")
	if _, err := p.Parse([]string{"-t", "0.8"}); err != nil {
		t.Fatal(err)
	}
	if *f != 0.8 || !p.Seen("threshold") || p.Seen("with-filename") {
		t.Fatalf("threshold=%v seen(threshold)=%v seen(with-filename)=%v", *f, p.Seen("threshold"), p.Seen("with-filename"))
	}
	if _, err := p.Parse([]string{"--threshold=abc"}); err == nil || !strings.Contains(err.Error(), "--threshold") {
		t.Fatalf("want error naming --threshold, got %v", err)
	}
}

func TestDuplicateOptionPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate -v")
		}
	}()
	p := NewParser("t")
	p.Flag('v', "a", "")
	p.Flag('v', "b", "")
}

func TestHelp(t *testing.T) {
	tl := New("grev")
	tl.P.Synopsis = []string{"[OPTIONS] QUERY [FILE...]"}
	tl.P.About = "About text."
	tl.P.Flag('v', "invert-match", "select non-matching")
	tl.P.Int('C', "context", "N", 0, "context records")
	tl.P.OptStr("other", "NAME", "other", "escape option")
	var b strings.Builder
	tl.P.Help(&b)
	out := b.String()
	for _, want := range []string{
		"usage: grev [OPTIONS] QUERY [FILE...]",
		"About text.",
		"Options:",
		"-v, --invert-match",
		"-C, --context=N",
		"--other[=NAME]",
		"Common options:",
		"-J, --jobs=N|max",
		"-Q, --quote",
		"-p, --progress",
		"--max-cost=USD",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q:\n%s", want, out)
		}
	}
	// Tool options come before common ones.
	if strings.Index(out, "Options:") > strings.Index(out, "Common options:") {
		t.Errorf("tool options should precede common options")
	}
}
