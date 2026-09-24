// Command lookv is look(1)/git-bisect for questions: it binary-searches an
// ordered input for the first record where a yes/no answer flips.
//
//	lookv 'shows the service is down' app.log   →  the first line where it is
package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/aurorainfra/grev/internal/cli"
	"github.com/aurorainfra/grev/internal/jev"
)

func main() {
	t := cli.New("lookv")
	p := t.P
	p.Synopsis = []string{"[OPTIONS] QUERY [FILE]"}
	p.About = `Search the ordered records of FILE (or stdin) for the first one for which the
model answers yes to QUERY, assuming the answer is no up to some record and yes
from there on: the moment a log starts showing an outage, the first commit that
touches a subsystem, where a sorted list crosses a line. With -v, find the first
record where the answer stops being yes.

The search is k-ary, not linear: each round asks about K evenly spaced records
at once (one request) and narrows the range to the gap where the answer flips,
so a million lines take about five rounds and a fraction of a cent. The first
round always checks the first and the last record. QUERY may reference the
record as {} and fields (with -d) as {1}, {2}, …; --about names the domain,
which helps accuracy most.

If the answers are not monotonic (a no after a yes), lookv reports the first
flip it found and warns; --verify re-asks around the result with more context.`
	p.ExitStatus = `0 found the flip, 1 the answer never flips (no record says yes), 2 error,
3 uncertain (--band) or --verify disagreed, 4 declined at -Q or over a budget.`
	p.Examples = []string{
		`lookv --about 'application log' 'shows the service is down' app.log`,
		`lookv -n -B1 'the line is about the licensing terms' LICENSE-APACHE`,
		`git log --reverse --oneline | lookv --about 'commit subjects' 'mentions the new parser'`,
		`lookv -v --about 'build log' 'the build is still passing' ci.log`,
		`lookv --trace --about 'periodic health checks' 'reports a failing dependency' health.log`,
	}
	invert := p.Flag('v', "invert", "find the first record where the answer stops being yes")
	lineNum := p.Flag('n', "line-number", "prefix records with their record number")
	scores := p.Flag('s', "scores", "prefix printed records with P(yes) (- where not asked)")
	after := p.Int('A', "after-context", "N", 0, "also print N records after the flip")
	before := p.Int('B', "before-context", "N", 0, "also print N records before the flip")
	context_ := p.Int('C', "context", "N", 0, "print N records around the flip")
	threshold := p.Float('t', "threshold", "P", 0.5, "yes when P(yes) ≥ P (default 0.5)")
	band := p.Str(0, "band", "LO:HI", "", "exit 3 when the flip or the record before it has LO ≤ P(yes) ≤ HI")
	k := p.Int('k', "probes", "K", 16, "records asked about per round (default 16)")
	window := p.Int('W', "window", "N", 0, "show the model N neighbouring records on each side")
	verify := p.Flag(0, "verify", "re-ask about the records around the flip with more context")
	trace := p.Flag(0, "trace", "show each round's probes and the narrowed range on stderr")
	yes := p.Str(0, "yes", "TEXT", "", "describe what counts as yes")
	no := p.Str(0, "no", "TEXT", "", "describe what counts as no")
	refFile := p.Str('S', "reference", "FILE", "", "shared context for every question (e.g. a spec)")
	about := p.Str(0, "about", "TEXT", "", "shared context, e.g. what the input is")
	delim := p.Str('d', "delimiter", "DELIM", "", "split records into fields {1}, {2}, … on DELIM")
	mode := cli.Records(p)

	args := t.Parse()
	if len(args) < 1 || len(args) > 2 {
		p.Usagef("want QUERY [FILE]")
	}
	query := args[0]
	name := ""
	if len(args) == 2 {
		name = args[1]
	}
	maxField := 0
	if *delim != "" {
		maxField = -1
	}
	if err := cli.CheckPlaceholders(query, true, maxField); err != nil {
		p.Usagef("%v", err)
	}
	if *k < 1 || *k > 255 {
		p.Usagef("-k wants 1..255")
	}
	if *context_ > 0 {
		*before, *after = max(*before, *context_), max(*after, *context_)
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

	f, err := cli.Open(name)
	if err != nil {
		t.Fatalf("%v", err)
	}
	recs, err := cli.ReadAll(f, mode())
	f.Close()
	if err != nil {
		t.Fatalf("%v", err)
	}
	n := len(recs)
	if n == 0 {
		t.Exit(cli.ExitNo)
	}
	state, err := cli.SharedState(*refFile, *about)
	if err != nil {
		t.Fatalf("%v", err)
	}

	e := t.Engine()
	out := t.Output()
	st := jev.NewState(state)
	question := cli.Template(query)
	item := func(i, w int) *jev.Item {
		r := cli.Rec{Text: recs[i]}
		if *delim != "" {
			r.Fields = strings.Split(recs[i], cli.Unescape(*delim))
		}
		if w > 0 {
			r.Before = append([]string(nil), recs[max(0, i-w):i]...)
			r.After = append([]string(nil), recs[i+1:min(n, i+1+w)]...)
		}
		return jev.NewItem(st, jev.Noul(cli.Instr(r, question), yesC, noC), i)
	}

	// Quote an upper bound: the rounds needed to narrow n records with K
	// probes per round, plus a --verify round.
	rounds := 1
	for span := float64(n); span > float64(*k); span /= float64(*k + 1) {
		rounds++
	}
	perItem := 0
	sample := min(n, 200)
	for j := 0; j < sample; j++ {
		perItem += item(j*n/sample, *window).Est()
	}
	perItem = perItem/sample + 1
	questions := min(n, rounds**k+2)
	if *verify {
		rounds++
		questions += 2
	}
	q := jev.Quote{Questions: questions, Requests: rounds,
		EstTokens: rounds*(jev.EstRequest(nil)+st.Est()) + questions*perItem}
	e.Stats.AddPlan(q)
	t.Confirm(q)

	// P(yes) per asked record, and the search decision derived from it.
	asked := map[int]float64{}
	isYes := func(pv float64) bool {
		y := pv >= *threshold
		if hasBand {
			y = pv > hi
		}
		return y != *invert
	}
	ask := func(idx []int, w int) {
		items := make([]*jev.Item, 0, len(idx))
		for _, i := range idx {
			items = append(items, item(i, w))
		}
		var firstErr error
		runErr := e.RunAll(t.Ctx(), items, func(r jev.Result) {
			if r.Err != nil {
				if firstErr == nil {
					firstErr = r.Err
				}
				return
			}
			asked[r.Item.Tag.(int)] = r.Answer.Noul
		})
		if runErr == nil {
			runErr = firstErr
		}
		if runErr != nil {
			t.Exit(t.Finish(runErr))
		}
	}

	// Invariant: every record ≤ lo answered no (or lo = -1), and hi answered
	// yes (or hi = n: nothing found yet).
	lower, upper := -1, n
	inversions := map[int]bool{}
	for round := 1; upper-lower > 1; round++ {
		probes := pickProbes(lower, upper, *k, round == 1, n)
		var fresh []int
		for _, i := range probes {
			if _, ok := asked[i]; !ok {
				fresh = append(fresh, i)
			}
		}
		if len(fresh) == 0 {
			break // nothing left to learn (cannot happen with a sane interval)
		}
		ask(fresh, *window)
		// Narrow to the first yes among everything known inside the range.
		var known []int
		for i := range asked {
			if i > lower && i < upper {
				known = append(known, i)
			}
		}
		sort.Ints(known)
		newLower, newUpper := lower, upper
		for _, i := range known {
			if isYes(asked[i]) {
				newUpper = i
				break
			}
			newLower = i
		}
		for _, i := range known {
			if i > newUpper && !isYes(asked[i]) {
				inversions[i] = true
			}
		}
		if *trace {
			var b strings.Builder
			for _, i := range fresh {
				fmt.Fprintf(&b, " %d:%.2f", i+1, asked[i])
			}
			t.Warnf("round %d: asked%s → flip in (%s, %s]", round, b.String(),
				recNo(newLower, n), recNo(newUpper, n))
		}
		lower, upper = newLower, newUpper
	}

	if len(inversions) > 0 {
		var ids []string
		for i := range inversions {
			ids = append(ids, strconv.Itoa(i+1))
		}
		sort.Slice(ids, func(a, b int) bool { x, _ := strconv.Atoi(ids[a]); y, _ := strconv.Atoi(ids[b]); return x < y })
		if len(ids) > 5 {
			ids = append(ids[:5], "…")
		}
		t.Warnf("warning: the answers are not monotonic (no again at record %s after a yes); reporting the first flip found",
			strings.Join(ids, ", "))
	}
	if upper >= n {
		if *trace {
			t.Warnf("the answer never flips: no record says yes")
		}
		t.Exit(cli.ExitNo)
	}
	if upper == 0 {
		t.Warnf("note: the answer is already %s at the first record", yn(!*invert))
	}

	code := cli.ExitYes
	if *verify {
		var idx []int
		for i := max(0, upper-1); i <= upper; i++ {
			idx = append(idx, i)
		}
		for _, i := range idx {
			delete(asked, i) // ask again, with more context
		}
		ask(idx, max(*window, 3))
		ok := isYes(asked[upper]) && (upper == 0 || !isYes(asked[upper-1]))
		if !ok {
			t.Warnf("--verify disagrees: with more context, record %d answers %s and record %d answers %s",
				upper, yn(asked[max(0, upper-1)] >= *threshold), upper+1, yn(asked[upper] >= *threshold))
			code = cli.ExitUncertain
		}
	}
	if hasBand {
		for _, i := range []int{upper - 1, upper} {
			if pv, ok := asked[i]; ok && pv >= lo && pv <= hi {
				code = cli.ExitUncertain
			}
		}
	}

	sep := mode().Sep()
	for i := max(0, upper-*before); i <= min(n-1, upper+*after); i++ {
		mark := "-"
		if i == upper {
			mark = ":"
		}
		var b strings.Builder
		if *scores {
			if pv, ok := asked[i]; ok {
				fmt.Fprintf(&b, "%.2f\t", pv)
			} else {
				b.WriteString("-\t")
			}
		}
		if *lineNum {
			b.WriteString(strconv.Itoa(i+1) + mark)
		}
		b.WriteString(recs[i])
		b.WriteString(sep)
		out.WriteString(b.String())
	}
	t.Exit(code)
}

// pickProbes chooses up to k records strictly inside (lower, upper), evenly
// spaced; the first round always includes the first and the last record.
func pickProbes(lower, upper, k int, first bool, n int) []int {
	a, b := lower+1, upper-1
	if b < a {
		return nil
	}
	if b-a+1 <= k {
		out := make([]int, 0, b-a+1)
		for i := a; i <= b; i++ {
			out = append(out, i)
		}
		return out
	}
	seen := map[int]bool{}
	var out []int
	add := func(i int) {
		if i >= a && i <= b && !seen[i] {
			seen[i] = true
			out = append(out, i)
		}
	}
	if first {
		add(0)
		add(n - 1)
	}
	span := float64(upper - lower)
	for j := 1; len(out) < k && j <= k; j++ {
		add(lower + int(math.Round(float64(j)*span/float64(k+1))))
	}
	for i := a; len(out) < k && i <= b; i++ { // fill if rounding collided
		add(i)
	}
	sort.Ints(out)
	return out
}

func recNo(i, n int) string {
	switch {
	case i < 0:
		return "start"
	case i >= n:
		return "end"
	}
	return strconv.Itoa(i + 1)
}

func yn(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
