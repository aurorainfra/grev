// Command sortv is sort(1) by meaning: it orders records along a dimension
// you describe in words, using the model's pairwise comparisons.
//
//	sortv 'chronologically, earliest first' events.txt
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
	text  string
	num   int     // 1-based position in the input
	seed  float64 // 0 (start) … 4 (end), the model's first guess
	moved int     // swaps it took part in, for --trace
}

// Seed positions, lowest first.
var levels = []any{
	"at the very start of the order",
	"early in the order",
	"in the middle of the order",
	"late in the order",
	"at the very end of the order",
}

func main() {
	t := cli.New("sortv")
	p := t.P
	p.Synopsis = []string{"[OPTIONS] ORDER [FILE...]"}
	p.About = `Sort the records of the FILEs (or stdin) in the ORDER you describe:
"chronologically, earliest first", "from least to most spicy", "by how risky
the change is, safest first". Output is the input records, reordered.

It works in two steps. First, each record is placed on a five-step scale from
the start to the end of the order, with the whole list (or a sample of it) as
context. Then odd-even passes compare neighbours pairwise ("should A come
before B?", asked both ways round to cancel position bias) and swap the ones
that are out of order, until a pass changes nothing or --passes run out.

Use rank(1) when you want records scored on their own (how well they fit a
query, or a fixed rubric); use sortv when only the relative order matters.
Numbers and dates written as such are better sorted with sort(1). (The v is
the grev family's suffix, as in grev and pickv; for version sort, use sort -V.)`
	p.ExitStatus = `0 sorted, 1 empty input, 2 error, 4 declined at -Q or over a budget.`
	p.Examples = []string{
		`sortv 'chronologically, earliest first' events.txt`,
		`sortv -r 'by how spicy the dish is, mildest first' menu.txt`,
		`sortv --about 'pull request titles' 'by how risky the change is, safest first' prs.txt`,
		`sortv -n --passes 6 --trace 'by size, smallest first' animals.txt`,
	}
	reverse := p.Flag('r', "reverse", "reverse the result")
	lineNum := p.Flag('n', "line-number", "prefix records with their original position")
	scores := p.Flag('s', "scores", "prefix records with their seed position (0 start … 4 end)")
	maxOut := p.Int('m', "max-count", "N", 0, "print only the first N records")
	passes := p.Int(0, "passes", "N", 4, "odd-even comparison passes after the seed (default 4; 0 for seed only)")
	trace := p.Flag(0, "trace", "show each pass and how many pairs it swapped on stderr")
	about := p.Str(0, "about", "TEXT", "", "shared context, e.g. what the records are")
	refFile := p.Str('S', "reference", "FILE", "", "shared context for every question")
	mode := cli.Records(p)

	args := t.Parse()
	if len(args) < 1 {
		p.Usagef("want ORDER [FILE...]")
	}
	order, files := args[0], args[1:]
	if err := cli.CheckPlaceholders(order, false, 0); err != nil {
		p.Usagef("ORDER is a description of the order, without placeholders: %v", err)
	}
	if *passes < 0 {
		p.Usagef("--passes wants 0 or more")
	}
	if len(files) == 0 {
		files = []string{"-"}
	}
	var recs []*rec
	for _, name := range files {
		f, err := cli.Open(name)
		if err != nil {
			t.Fatalf("%v", err)
		}
		all, err := cli.ReadAll(f, mode())
		f.Close()
		if err != nil {
			t.Fatalf("%v", err)
		}
		for _, s := range all {
			recs = append(recs, &rec{text: s, num: len(recs) + 1})
		}
	}
	n := len(recs)
	if n == 0 {
		t.Exit(cli.ExitNo)
	}
	shared, err := cli.SharedState(*refFile, *about)
	if err != nil {
		t.Fatalf("%v", err)
	}
	e := t.Engine()

	// The seed sees the list (or an even sample of it) so "early" and "late"
	// mean something.
	ctxList := make([]string, 0, min(n, 80))
	budget := 12_000
	for i := 0; i < n && len(ctxList) < 80; i += max(1, n/80) {
		budget -= jev.EstText(recs[i].text)
		if budget < 0 {
			break
		}
		ctxList = append(ctxList, cli.Clip(recs[i].text, 300))
	}
	seedState := jev.Obj{{K: "order", V: order}, {K: "records", V: ctxList}}
	if s, ok := shared.(jev.Obj); ok {
		seedState = append(seedState, s...)
	}
	sst := jev.NewState(seedState)
	pst := jev.NewState(shared)
	seedQ := func(r *rec) *jev.Item {
		return jev.NewItem(sst, jev.Score(jev.Obj{
			{K: "order", V: order},
			{K: "question", V: "Where does `text` belong when `records` are sorted by `order`?"},
			{K: "text", V: r.text},
		}, levels), r)
	}
	type pair struct {
		i       int  // left index in the current order
		swapped bool // this question has b before a
	}
	pairQ := func(a, b *rec, pr pair) *jev.Item {
		return jev.NewItem(pst, jev.Noul(jev.Obj{
			{K: "order", V: order},
			{K: "question", V: "Sorted by `order`, should `a` come before `b`?"},
			{K: "a", V: a.text},
			{K: "b", V: b.text},
		}, nil, nil), pr)
	}

	// Quote everything up front: the seed plus every pass at most.
	var items []*jev.Item
	for _, r := range recs {
		items = append(items, seedQ(r))
	}
	q := e.Plan(items)
	pairsPerPass := n - 1 // two questions per pair, pairs on alternating halves
	if *passes > 0 && n > 1 {
		probe := pairQ(recs[0], recs[min(1, n-1)], pair{})
		extra := jev.Quote{Questions: *passes * pairsPerPass, Requests: *passes,
			EstTokens: *passes * (jev.EstRequest(nil) + pst.Est() + pairsPerPass*probe.Est())}
		e.Stats.AddPlan(extra)
		q.Questions += extra.Questions
		q.Requests += extra.Requests
		q.EstTokens += extra.EstTokens
	}
	t.Confirm(q)

	run := func(items []*jev.Item, emit func(jev.Result)) {
		var firstErr error
		err := e.RunAll(t.Ctx(), items, func(r jev.Result) {
			if r.Err != nil {
				if firstErr == nil {
					firstErr = r.Err
				}
				return
			}
			emit(r)
		})
		if err == nil {
			err = firstErr
		}
		if err != nil {
			t.Exit(t.Finish(err))
		}
	}

	// 1. Seed.
	run(items, func(r jev.Result) { r.Item.Tag.(*rec).seed = r.Answer.Score })
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].seed < recs[j].seed })
	if *trace {
		t.Warnf("seed: placed %d records on the scale", n)
	}

	// 2. Odd-even transposition passes over neighbours, both orders per pair.
	quiet := 0
	for pass := 0; pass < *passes && n > 1; pass++ {
		var qs []*jev.Item
		for i := pass % 2; i+1 < n; i += 2 {
			qs = append(qs, pairQ(recs[i], recs[i+1], pair{i: i}), pairQ(recs[i+1], recs[i], pair{i: i, swapped: true}))
		}
		if len(qs) == 0 {
			continue
		}
		ab, ba := map[int]float64{}, map[int]float64{}
		run(qs, func(r jev.Result) {
			pr := r.Item.Tag.(pair)
			if pr.swapped {
				ba[pr.i] = r.Answer.Noul
			} else {
				ab[pr.i] = r.Answer.Noul
			}
		})
		swaps := 0
		for i := pass % 2; i+1 < n; i += 2 {
			pa, okA := ab[i]
			pb, okB := ba[i]
			if !okA || !okB {
				continue
			}
			// P(a before b), averaged over both phrasings.
			if (pa+(1-pb))/2 < 0.5 {
				recs[i], recs[i+1] = recs[i+1], recs[i]
				recs[i].moved++
				recs[i+1].moved++
				swaps++
			}
		}
		if *trace {
			t.Warnf("pass %d: compared %d pairs, swapped %d", pass+1, len(qs)/2, swaps)
		}
		if swaps == 0 {
			if quiet++; quiet == 2 { // both parities are settled
				break
			}
		} else {
			quiet = 0
		}
	}

	if *reverse {
		for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
			recs[i], recs[j] = recs[j], recs[i]
		}
	}
	out := t.Output()
	sep := mode().Sep()
	for i, r := range recs {
		if *maxOut > 0 && i >= *maxOut {
			break
		}
		var b strings.Builder
		if *scores {
			fmt.Fprintf(&b, "%.2f\t", r.seed)
		}
		if *lineNum {
			b.WriteString(strconv.Itoa(r.num) + ":")
		}
		b.WriteString(r.text)
		b.WriteString(sep)
		out.WriteString(b.String())
	}
	t.Exit(cli.ExitYes)
}
