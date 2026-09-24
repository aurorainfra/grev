// Command rank is sort(1) by a question: it scores every record and prints
// them best first.
//
//	rank -L 'mild|medium|hot' 'How spicy is {}?' < menu
package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/aurorainfra/grev/internal/cli"
	"github.com/aurorainfra/grev/internal/jev"
)

type rec struct {
	file  int
	num   int
	text  string
	score float64
	ok    bool
}

func main() {
	t := cli.New("rank")
	p := t.P
	p.Synopsis = []string{"[OPTIONS] QUERY [FILE...]", "-L 'LOW|…|HIGH' [OPTIONS] QUERY [FILE...]"}
	p.About = `Score every record of each FILE (or stdin) against QUERY and print them,
highest first (a stable sort: ties keep input order).

By default each record gets a yes/no question and is ranked by P(yes), so
QUERY reads as a predicate ("answers: how do I reset my password") or a
question with {} for the record. With -L the model instead places each record
on ordered levels you describe, lowest first, and ranks by that position.
Output is always the input, verbatim.`
	p.ExitStatus = `0 ok, 2 error, 4 declined at -Q or over --max-cost.`
	p.Examples = []string{
		`rank -m 3 'explains how to reset a password' < faq.txt`,
		`rank -s -L 'mild|medium|hot|very hot' 'How spicy is {}?' < menu.txt`,
		`git log --oneline | rank -m 10 'is a risky change to production'`,
	}
	levels := p.Str('L', "levels", "LOW|…|HIGH", "", "rank on 2–10 ordered levels (a Score) instead of P(yes)")
	reverse := p.Flag('r', "reverse", "print lowest first")
	scores := p.Flag('s', "scores", "prefix each record with its score")
	lineNum := p.Flag('n', "line-number", "prefix records with their original record number")
	maxCount := p.Int('m', "max-count", "N", 0, "print only the top N records (all are still scored)")
	yes := p.Str(0, "yes", "TEXT", "", "describe what counts as yes (default mode)")
	no := p.Str(0, "no", "TEXT", "", "describe what counts as no (default mode)")
	window := p.Int('W', "window", "N", 0, "show the model N neighbouring records on each side")
	refFile := p.Str('S', "reference", "FILE", "", "shared context for every question")
	about := p.Str(0, "about", "TEXT", "", "shared context, e.g. what the input is")
	delim := p.Str('d', "delimiter", "DELIM", "", "split records into fields {1}, {2}, … on DELIM")
	mode := cli.Records(p)
	args := t.Parse()
	if len(args) < 1 {
		p.Usagef("missing QUERY")
	}
	query, files := args[0], args[1:]
	if len(files) == 0 {
		files = []string{"-"}
	}
	maxField := 0
	if *delim != "" {
		maxField = -1
	}
	if err := cli.CheckPlaceholders(query, true, maxField); err != nil {
		p.Usagef("%v", err)
	}

	var levelList []any
	if *levels != "" {
		for _, l := range strings.Split(*levels, "|") {
			if l = strings.TrimSpace(l); l != "" {
				levelList = append(levelList, l)
			}
		}
		if len(levelList) < 2 || len(levelList) > 10 {
			p.Usagef("-L wants 2 to 10 levels separated by '|'")
		}
	}
	var yesC, noC any
	if *yes != "" {
		yesC = *yes
	}
	if *no != "" {
		noC = *no
	}
	shared, err := cli.SharedState(*refFile, *about)
	if err != nil {
		t.Fatalf("%v", err)
	}
	st := jev.NewState(shared)

	var question string
	switch q := strings.TrimSpace(query); {
	case levelList == nil:
		question = cli.Template(q)
	case cli.HasPlaceholder(q):
		question = cli.Rewrite(q)
	default:
		question = "Regarding `text`: " + q
	}

	var recs []*rec
	var items []*jev.Item
	for fi, name := range files {
		f, err := cli.Open(name)
		if err != nil {
			t.Fatalf("%v", err)
		}
		texts, err := cli.ReadAll(f, mode())
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for i, text := range texts {
			r := &rec{file: fi, num: i + 1, text: text}
			cr := cli.Rec{Text: text}
			if *window > 0 {
				cr.Before = texts[max(0, i-*window):i]
				cr.After = texts[i+1 : min(len(texts), i+1+*window)]
			}
			if *delim != "" {
				cr.Fields = strings.Split(text, cli.Unescape(*delim))
			}
			instr := cli.Instr(cr, question)
			q := jev.Noul(instr, yesC, noC)
			if levelList != nil {
				q = jev.Score(instr, levelList)
			}
			recs = append(recs, r)
			items = append(items, jev.NewItem(st, q, r))
		}
	}

	failed := 0
	var firstErr error
	runErr := t.RunItems(t.Ctx(), items, func(res jev.Result) {
		r := res.Item.Tag.(*rec)
		if res.Err != nil {
			failed++
			if firstErr == nil {
				firstErr = res.Err
			}
			return
		}
		r.ok = true
		if levelList != nil {
			r.score = res.Answer.Score
		} else {
			r.score = res.Answer.Noul
		}
	})
	if runErr != nil {
		t.Exit(t.Finish(runErr))
	}

	// Unscored records sort last either way.
	sort.SliceStable(recs, func(i, j int) bool {
		a, b := recs[i], recs[j]
		if a.ok != b.ok {
			return a.ok
		}
		if *reverse {
			return a.score < b.score
		}
		return a.score > b.score
	})
	out := t.Output()
	sep := mode().Sep()
	showFile := len(files) > 1
	for i, r := range recs {
		if *maxCount > 0 && i >= *maxCount {
			break
		}
		var b strings.Builder
		if *scores {
			if r.ok {
				fmt.Fprintf(&b, "%.2f\t", r.score)
			} else {
				b.WriteString("-\t")
			}
		}
		if showFile && *lineNum {
			b.WriteString(cli.Display(files[r.file]) + ":")
		}
		if *lineNum {
			b.WriteString(strconv.Itoa(r.num) + ":")
		}
		b.WriteString(r.text)
		b.WriteString(sep)
		out.WriteString(b.String())
	}
	if failed > 0 {
		t.Warnf("%d record(s) failed and were ranked last: %v", failed, firstErr)
		t.Exit(cli.ExitError)
	}
	t.Exit(cli.ExitYes)
}
