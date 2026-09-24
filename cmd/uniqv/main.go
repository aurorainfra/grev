// Command uniqv is uniq(1) by meaning: it collapses runs of adjacent records
// that the model judges to refer to the same thing, keeping the first.
//
//	sort companies.txt | uniqv -c
package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aurorainfra/grev/internal/cli"
	"github.com/aurorainfra/grev/internal/jev"
)

const defaultQuestion = "Do {1} and {2} refer to the same thing?"

// pair is the boundary between record i-1 (a) and record i (b).
type pair struct {
	i    int
	a, b string
}

func main() {
	t := cli.New("uniqv")
	p := t.P
	p.Synopsis = []string{"[OPTIONS] [QUESTION] [FILE]"}
	p.About = `Collapse runs of adjacent records (lines by default) of FILE (or stdin) that
the model judges to be the same, printing the first of each run. Like uniq, only
neighbours are compared, so group the input first (sort, rank, tag).

QUESTION decides when two neighbours count as the same; it must reference them
as {1} (the earlier record) and {2} (the later one). Default:
  ` + defaultQuestion
	p.ExitStatus = `0 ok, 2 error, 4 declined at -Q or stopped by --max-cost.`
	p.Examples = []string{
		`sort companies.txt | uniqv -c`,
		`uniqv -D 'Are {1} and {2} the same person?' < contacts.txt`,
		`tail -f alerts.log | uniqv --line-buffered 'Do {1} and {2} report the same incident?'`,
	}
	count := p.Flag('c', "count", "prefix records with the number of records in their run")
	dupOnly := p.Flag('d', "repeated", "print only runs of two or more records, one per run")
	dupAll := p.Flag('D', "all-repeated", "print every record of runs of two or more")
	uniqOnly := p.Flag('u', "unique", "print only records that are not part of a run")
	scores := p.Flag('s', "scores", "prefix records with P(same as the record before it)")
	threshold := p.Float('t', "threshold", "P", 0.5, "treat neighbours as the same when P ≥ P (default 0.5)")
	about := p.Str(0, "about", "TEXT", "", "shared context, e.g. what the records are")
	mode := cli.Records(p)
	lineBuf := p.Flag(0, "line-buffered", "stream: print runs as soon as they end")
	flush := p.Str(0, "flush", "DUR", "250ms", "with --line-buffered: send a partial request after DUR")
	args := t.Parse()

	question, file := defaultQuestion, ""
	switch len(args) {
	case 0:
	case 1:
		if cli.HasPlaceholder(args[0]) {
			question = args[0]
		} else {
			file = args[0]
		}
	case 2:
		question, file = args[0], args[1]
		if !cli.HasPlaceholder(question) {
			p.Usagef("QUESTION must reference the two records as {1} and {2}")
		}
	default:
		p.Usagef("want [QUESTION] [FILE]")
	}
	if err := cli.CheckPlaceholders(question, false, 2); err != nil {
		p.Usagef("QUESTION: %v (use {1} for the earlier record and {2} for the later one)", err)
	}
	if *count && *dupAll {
		p.Usagef("-c and -D can't be combined")
	}
	flushDur, err := time.ParseDuration(*flush)
	if err != nil {
		p.Usagef("--flush: %v", err)
	}
	q := cli.Rewrite(question)
	state, _ := cli.SharedState("", *about)
	st := jev.NewState(state)
	sep := mode().Sep()

	out := t.Output()
	out.SetLineBuffered(*lineBuf)
	var run []string   // current run of same records
	var runP []float64 // P(same as previous) per record in run; -1 for none
	failed := 0
	var firstErr error

	flushRun := func() {
		if len(run) == 0 {
			return
		}
		dup := len(run) > 1
		switch {
		case *dupAll:
			if dup {
				for i, r := range run {
					write(out, *scores, runP[i], "", r, sep)
				}
			}
		case *dupOnly && !dup, *uniqOnly && dup:
		default:
			prefix := ""
			if *count {
				prefix = fmt.Sprintf("%7d ", len(run))
			}
			write(out, *scores, runP[0], prefix, run[0], sep)
		}
		run, runP = run[:0], runP[:0]
	}
	emit := func(r jev.Result) {
		pr := r.Item.Tag.(*pair)
		if len(run) == 0 {
			run, runP = append(run, pr.a), append(runP, -1)
		}
		pv := -1.0
		same := false
		if r.Err != nil {
			failed++
			if firstErr == nil {
				firstErr = r.Err
			}
		} else {
			pv = r.Answer.Noul
			same = pv >= *threshold
		}
		if !same {
			flushRun()
		}
		run, runP = append(run, pr.b), append(runP, pv)
	}
	mk := func(i int, a, b string) *jev.Item {
		instr := jev.Obj{{K: "f1", V: a}, {K: "f2", V: b}, {K: "question", V: q}}
		return jev.NewItem(st, jev.Noul(instr, nil, nil), &pair{i: i, a: a, b: b})
	}

	f, err := cli.Open(file)
	if err != nil {
		t.Fatalf("%v", err)
	}
	defer f.Close()
	// first and n are written by the streaming reader, which may still be
	// running when a run stops early; mu guards them.
	var mu sync.Mutex
	var first string
	n := 0
	var runErr error
	if *lineBuf {
		ctx, stop := context.WithCancel(t.Ctx())
		defer stop()
		in := make(chan *jev.Item, 64)
		go func() {
			defer close(in)
			prev := ""
			err := cli.Scan(f, mode(), func(rec string) bool {
				mu.Lock()
				n++
				i := n
				if i == 1 {
					first = rec
				}
				mu.Unlock()
				if i == 1 {
					prev = rec
					return true
				}
				select {
				case in <- mk(i-1, prev, rec):
				case <-ctx.Done():
					return false
				}
				prev = rec
				return true
			})
			if err != nil {
				t.Warnf("%v", err)
			}
		}()
		runErr = t.StreamItems(ctx, in, flushDur, emit)
	} else {
		recs, err := cli.ReadAll(f, mode())
		if err != nil {
			t.Fatalf("%v", err)
		}
		mu.Lock()
		n = len(recs)
		if n > 0 {
			first = recs[0]
		}
		mu.Unlock()
		items := make([]*jev.Item, 0, max(len(recs)-1, 0))
		for i := 1; i < len(recs); i++ {
			items = append(items, mk(i, recs[i-1], recs[i]))
		}
		if len(items) > 0 {
			runErr = t.RunItems(t.Ctx(), items, emit)
		}
	}
	mu.Lock()
	single, only := n == 1, first
	mu.Unlock()
	if single {
		run, runP = []string{only}, []float64{-1}
	}
	flushRun()
	code := t.Finish(runErr)
	if code == cli.ExitYes && failed > 0 {
		t.Warnf("%d comparison(s) failed (kept as different): %v", failed, firstErr)
		code = cli.ExitError
	}
	t.Exit(code)
}

func write(out *cli.Out, scores bool, p float64, prefix, rec, sep string) {
	var b strings.Builder
	if scores {
		if p < 0 {
			b.WriteString("-\t")
		} else {
			fmt.Fprintf(&b, "%.2f\t", p)
		}
	}
	b.WriteString(prefix)
	b.WriteString(rec)
	b.WriteString(sep)
	out.WriteString(b.String())
}
