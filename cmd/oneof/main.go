// Command oneof is case(1) for questions: it picks which of the given labels
// best describes the whole input and prints it.
//
//	case $(oneof bug feature question < issue.md) in …
package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/aurorainfra/grev/internal/cli"
	"github.com/aurorainfra/grev/internal/jev"
)

func main() {
	t := cli.New("oneof")
	p := t.P
	p.Synopsis = []string{"[OPTIONS] LABEL[=DESCRIPTION]... [< input]", "[OPTIONS] -f LABELFILE [< input]"}
	p.About = `Pick the LABEL that best describes the whole of stdin (or -i FILE) and print it.
A label may carry a description after '=' to sharpen its boundary, e.g.
bug='something is broken'. The model always picks one of the labels; add
--other to give it an explicit way out.`
	p.ExitStatus = `0 a label was picked, 1 the --other label won, 2 error,
3 confidence below -t (the label is still printed), 4 declined at -Q or over
--max-cost.`
	p.Examples = []string{
		`case $(oneof bug feature question < issue.md) in bug) … ;; esac`,
		`oneof -s billing='payments, refunds' technical='bugs, outages' sales < ticket.txt`,
		`oneof --probs --other positive negative neutral < review.txt`,
	}
	question := p.Str('q', "question", "QUESTION", "Which option best describes the input?", "the question to ask about the input")
	labelFile := p.Str('f', "labels", "FILE", "", "read labels from FILE, one 'label<TAB>description' per line")
	input := p.Str('i', "input", "FILE", "", "read the input from FILE instead of stdin")
	other := p.OptStr("other", "NAME", "other", "add an escape label (default 'other') for inputs that fit none")
	show := p.Flag('s', "score", "prefix the label with its confidence and a tab")
	probs := p.Flag(0, "probs", "print every label with its probability, most likely first")
	threshold := p.Float('t', "threshold", "C", 0, "exit 3 when the answer's confidence is below C")
	about := p.Str(0, "about", "TEXT", "", "context about the input, e.g. what it is")
	args := t.Parse()

	if *labelFile != "" {
		more, err := cli.LabelsFile(*labelFile)
		if err != nil {
			t.Fatalf("%v", err)
		}
		args = append(args, more...)
	}
	otherName := ""
	if *other != nil {
		otherName = **other
		args = append(args, otherName+"=none of the other options fits the input")
	}
	opts, err := cli.ParseLabels(args)
	if err != nil {
		p.Usagef("%v", err)
	}
	if len(opts) < 2 {
		p.Usagef("need at least two labels")
	}

	text, err := cli.ReadInput(*input)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if strings.TrimSpace(text) == "" {
		t.Fatalf("empty input")
	}
	var state any = text
	if *about != "" {
		state = jev.Obj{{K: "about", V: *about}, {K: "input", V: text}}
	}

	ans := t.Ask(state, []string{"q"}, []jev.Question{jev.Choice(*question, opts)})["q"]
	out := t.Output()
	switch {
	case *probs:
		keys := make([]string, 0, len(opts))
		for _, o := range opts {
			keys = append(keys, o.Key)
		}
		sort.SliceStable(keys, func(i, j int) bool { return ans.Probabilities[keys[i]] > ans.Probabilities[keys[j]] })
		for _, k := range keys {
			fmt.Fprintf(out, "%.2f\t%s\n", ans.Probabilities[k], k)
		}
	case *show:
		fmt.Fprintf(out, "%.2f\t%s\n", ans.Confidence, ans.Choice)
	default:
		fmt.Fprintln(out, ans.Choice)
	}

	code := cli.ExitYes
	switch {
	case otherName != "" && ans.Choice == otherName:
		code = cli.ExitNo
	case ans.Confidence < *threshold:
		code = cli.ExitUncertain
	}
	t.Exit(code)
}
