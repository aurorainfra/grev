// Command isv is test(1) for questions: it asks one yes/no question about the
// whole input and answers with its exit status.
//
//	git diff --cached | isv 'adds a secret or credential' && exit 1
package main

import (
	"fmt"
	"strings"

	"github.com/aurorainfra/grev/internal/cli"
	"github.com/aurorainfra/grev/internal/jev"
)

func main() {
	t := cli.New("isv")
	p := t.P
	p.Synopsis = []string{"[OPTIONS] QUESTION [FILE]"}
	p.About = `Ask QUESTION about the whole of FILE (or stdin) and exit 0 for yes, 1 for no.
QUESTION is read as a yes/no question or a statement about the input, e.g.
'Does this diff add a credential?' or 'the log shows a flaky test'. Nothing is
printed unless -s is given.`
	p.ExitStatus = `0 yes, 1 no, 2 error, 3 uncertain (with --band), 4 declined at -Q
or over --max-cost.`
	p.Examples = []string{
		`git diff --cached | isv 'adds a secret or credential' && exit 1`,
		`if isv 'the build failed because of a flaky test' < ci.log; then retry; fi`,
		`isv -s 'written in a formal tone' letter.txt`,
	}
	show := p.Flag('s', "score", "print P(yes) on stdout")
	invert := p.Flag('v', "invert", "exit 0 for no and 1 for yes")
	threshold := p.Float('t', "threshold", "P", 0.5, "answer yes when P(yes) ≥ P (default 0.5)")
	band := p.Str(0, "band", "LO:HI", "", "exit 3 when LO ≤ P(yes) ≤ HI; yes means above HI (overrides -t)")
	yes := p.Str(0, "yes", "TEXT", "", "describe what counts as yes")
	no := p.Str(0, "no", "TEXT", "", "describe what counts as no")
	about := p.Str(0, "about", "TEXT", "", "context about the input, e.g. what it is")
	chunks := p.Str(0, "chunks", "any|all|mean", "", "split input too large for one request; combine chunk answers")
	args := t.Parse()
	if len(args) < 1 || len(args) > 2 {
		p.Usagef("want QUESTION [FILE]")
	}
	question := strings.TrimSpace(args[0])
	name := ""
	if len(args) == 2 {
		name = args[1]
	}
	switch *chunks {
	case "", "any", "all", "mean":
	default:
		p.Usagef("--chunks wants any, all or mean")
	}
	lo, hi, hasBand := 0.0, 0.0, *band != ""
	if hasBand {
		var err error
		if lo, hi, err = cli.ParseBand(*band); err != nil {
			p.Usagef("%v", err)
		}
	}
	var yesC, noC any
	if *yes != "" {
		yesC = *yes
	}
	if *no != "" {
		noC = *no
	}

	text, err := cli.ReadInput(name)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if strings.TrimSpace(text) == "" {
		t.Fatalf("empty input")
	}
	q := jev.Noul(question, yesC, noC)
	state := func(s string) any {
		if *about != "" {
			return jev.Obj{{K: "about", V: *about}, {K: "input", V: s}}
		}
		return s
	}

	var pr float64
	parts := []string{text}
	if *chunks != "" {
		parts = cli.Chunks(text, 20_000)
	}
	if len(parts) == 1 {
		ans := t.Ask(state(text), []string{"q"}, []jev.Question{q})
		pr = ans["q"].Noul
	} else {
		items := make([]*jev.Item, len(parts))
		for i, part := range parts {
			items[i] = jev.NewItem(jev.NewState(state(part)), q, i)
		}
		ps := make([]float64, 0, len(parts))
		var firstErr error
		runErr := t.RunItems(t.Ctx(), items, func(r jev.Result) {
			if r.Err != nil {
				if firstErr == nil {
					firstErr = r.Err
				}
				return
			}
			ps = append(ps, r.Answer.Noul)
		})
		if runErr == nil {
			runErr = firstErr
		}
		if runErr != nil {
			t.Exit(t.Finish(runErr))
		}
		pr = combine(*chunks, ps)
	}

	if *show {
		fmt.Fprintf(t.Output(), "%.2f\n", pr)
	}
	code := cli.ExitNo
	switch {
	case hasBand && pr >= lo && pr <= hi:
		code = cli.ExitUncertain
	case hasBand && pr > hi, !hasBand && pr >= *threshold:
		code = cli.ExitYes
	}
	if *invert && code != cli.ExitUncertain {
		code = 1 - code
	}
	t.Exit(code)
}

func combine(how string, ps []float64) float64 {
	out := ps[0]
	for _, p := range ps[1:] {
		switch how {
		case "any":
			out = max(out, p)
		case "all":
			out = min(out, p)
		default:
			out += p
		}
	}
	if how == "mean" {
		out /= float64(len(ps))
	}
	return out
}
