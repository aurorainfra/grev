// Command grev is grep where the pattern is a question: it prints the records
// (lines by default) for which the model answers yes.
//
//	printf 'steak\nboiled carrots\n' | grev 'is vegan meal'   →  boiled carrots
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aurorainfra/grev/internal/cli"
	"github.com/aurorainfra/grev/internal/jev"
)

type rec struct {
	file int
	num  int // 1-based record number within its file
	text string
}

func main() {
	t := cli.New("grev")
	p := t.P
	p.Synopsis = []string{"[OPTIONS] QUERY [FILE...]", "-l|-L [OPTIONS] QUERY FILE..."}
	p.About = `Print the records of each FILE (or stdin) for which the model answers yes to
QUERY. QUERY may reference the record as {} and fields (with -d) as {1}, {2}, …;
otherwise it is read as a predicate about the record ("is a vegan meal",
"mentions a timeout"). Output is always the input, verbatim.

Naming the domain helps accuracy most: --about 'git commit messages', or a
full question such as 'Does the commit message {} describe a bug fix?'.`
	p.ExitStatus = `0 if a record was selected, 1 if none, 2 on error, 3 if only
uncertain records (see --band), 4 if declined at -Q or stopped by --max-cost.`
	p.Examples = []string{
		`printf 'steak\nboiled carrots\n' | grev 'is vegan meal'`,
		`grev -v -S spec.md 'Is {} addressed by the reference document?' < requirements.txt`,
		`paste a.txt b.txt | grev -d '\t' 'Is {1} the same product as {2}?'`,
		`tail -f app.log | grev --line-buffered -m1 'says the server finished starting'`,
	}

	invert := p.Flag('v', "invert-match", "select records the model answers no to")
	count := p.Flag('c', "count", "print only a count of selected records per file")
	lineNum := p.Flag('n', "line-number", "prefix records with their record number")
	withName := p.Flag('H', "with-filename", "prefix records with the file name")
	noName := p.Flag('h', "no-filename", "never prefix file names")
	quiet := p.Flag('q', "quiet", "print nothing; exit 0 at the first selected record")
	maxCount := p.Int('m', "max-count", "N", 0, "stop after N selected records")
	after := p.Int('A', "after-context", "N", 0, "print N records after each selected record")
	before := p.Int('B', "before-context", "N", 0, "print N records before each selected record")
	context_ := p.Int('C', "context", "N", 0, "print N records around each selected record")
	filesWith := p.Flag('l', "files-with-matches", "judge whole files; print the names of those that match")
	filesWithout := p.Flag('L', "files-without-match", "judge whole files; print the names of those that don't")
	filesFrom := p.Str(0, "files-from", "FILE", "", "read file names from FILE ('-' for stdin), one per line")
	scores := p.Flag('s', "scores", "prefix each printed record with the model's probability")
	threshold := p.Float('t', "threshold", "P", 0.5, "select when P(yes) ≥ P (default 0.5)")
	band := p.Str(0, "band", "LO:HI", "", "treat LO ≤ P(yes) ≤ HI as uncertain: neither selected nor rejected")
	uncertainFile := p.Str(0, "uncertain", "FILE", "", "write uncertain records to FILE (implies --band 0.3:0.7)")
	yes := p.Str(0, "yes", "TEXT", "", "describe what counts as yes")
	no := p.Str(0, "no", "TEXT", "", "describe what counts as no")
	window := p.Int('W', "window", "N", 0, "show the model N neighbouring records on each side (with --line-buffered, each record waits for N later ones)")
	refFile := p.Str('S', "reference", "FILE", "", "shared context for every question (e.g. a spec)")
	about := p.Str(0, "about", "TEXT", "", "shared context, e.g. what the input is")
	delim := p.Str('d', "delimiter", "DELIM", "", "split records into fields {1}, {2}, … on DELIM")
	mode := cli.Records(p)
	lineBuf := p.Flag(0, "line-buffered", "stream: answer and print records as they arrive")
	flush := p.Str(0, "flush", "DUR", "250ms", "with --line-buffered: send a partial request after DUR")

	args := t.Parse()
	if len(args) < 1 {
		p.Usagef("missing QUERY")
	}
	query, files := args[0], args[1:]
	if *filesFrom != "" {
		names, err := cli.ReadInput(*filesFrom)
		if err != nil {
			t.Fatalf("%v", err)
		}
		for _, n := range strings.Split(names, "\n") {
			if n = strings.TrimRight(n, "\r"); n != "" {
				files = append(files, n)
			}
		}
	}
	if len(files) == 0 {
		files = []string{"-"}
	}
	if *context_ > 0 {
		*before, *after = max(*before, *context_), max(*after, *context_)
	}
	if err := cli.CheckPlaceholders(query, true, fieldsAllowed(*delim)); err != nil {
		p.Usagef("%v", err)
	}
	lo, hi, hasBand := 0.0, 0.0, *band != "" || *uncertainFile != ""
	if hasBand {
		b := *band
		if b == "" {
			b = "0.3:0.7"
		}
		var err error
		if lo, hi, err = cli.ParseBand(b); err != nil {
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
	flushDur, err := time.ParseDuration(*flush)
	if err != nil {
		p.Usagef("--flush: %v", err)
	}
	if *lineBuf {
		t.Streaming()
	}
	state, err := cli.SharedState(*refFile, *about)
	if err != nil {
		t.Fatalf("%v", err)
	}

	// decide classifies P(yes): 1 selected, 0 rejected, -1 uncertain.
	decide := func(pr float64) int {
		if hasBand && pr >= lo && pr <= hi {
			return -1
		}
		yesAns := pr >= *threshold
		if hasBand {
			yesAns = pr > hi
		}
		if yesAns != *invert {
			return 1
		}
		return 0
	}

	var unc *os.File
	if *uncertainFile != "" {
		if unc, err = os.Create(*uncertainFile); err != nil {
			t.Fatalf("%v", err)
		}
		defer unc.Close()
	}

	if *filesWith || *filesWithout {
		filesMode(t, query, files, *filesWithout, *scores, *quiet, *maxCount, state, yesC, noC, decide, unc, mode())
		return
	}

	e := t.Engine()
	out := t.Output()
	out.SetLineBuffered(*lineBuf)
	st := jev.NewState(state)
	question := cli.Template(query)
	sep := mode().Sep()
	mk := func(r *rec, bef, aft []string) *jev.Item {
		cr := cli.Rec{Text: r.text, Before: bef, After: aft}
		if *delim != "" {
			cr.Fields = strings.Split(r.text, cli.Unescape(*delim))
		}
		return jev.NewItem(st, jev.Noul(cli.Instr(cr, question), yesC, noC), r)
	}

	pr := &printer{
		out: out, sep: sep, names: files, scores: *scores, lineNum: *lineNum,
		before: *before, after: *after,
		showFile: (len(files) > 1 || *withName) && !*noName,
		context:  *before > 0 || *after > 0,
	}
	counts := make([]int, len(files))
	var selected, uncertain, failed int
	var openFailed atomic.Int32 // inputs that couldn't be opened (counted by the producer)
	var firstErr error
	ctx, stop := context.WithCancel(t.Ctx())
	defer stop()
	stopped := false

	emit := func(res jev.Result) {
		if stopped {
			return
		}
		r := res.Item.Tag.(*rec)
		if res.Err != nil {
			failed++
			if firstErr == nil {
				firstErr = res.Err
			}
			pr.skip(r)
			return
		}
		pv := res.Answer.Noul
		switch decide(pv) {
		case 1:
			selected++
			counts[r.file]++
			switch {
			case *quiet:
				stopped = true
				stop()
			case *count:
			default:
				pr.match(r, pv)
			}
			if *maxCount > 0 && selected >= *maxCount {
				stopped = true
				stop()
			}
		case -1:
			uncertain++
			if unc != nil {
				fmt.Fprint(unc, r.text+sep)
			}
			if !*count && !*quiet {
				pr.other(r, pv)
			}
		default:
			if !*count && !*quiet {
				pr.other(r, pv)
			}
		}
	}

	produce := func(send func(*jev.Item) bool) error {
		for fi, name := range files {
			f, err := cli.Open(name)
			if err != nil {
				t.Warnf("%v", err)
				openFailed.Add(1)
				continue
			}
			var hist []string
			var pend []*rec
			num := 0
			flushOne := func() bool {
				r := pend[0]
				aft := make([]string, 0, *window)
				for _, x := range pend[1:] {
					aft = append(aft, x.text)
				}
				ok := send(mk(r, clone(hist), aft))
				hist = append(hist, r.text)
				if len(hist) > *window {
					hist = hist[1:]
				}
				pend = pend[1:]
				return ok
			}
			ok := true
			err = cli.Scan(f, mode(), func(text string) bool {
				num++
				pend = append(pend, &rec{file: fi, num: num, text: text})
				if len(pend) > *window {
					ok = flushOne()
				}
				return ok
			})
			for ok && len(pend) > 0 {
				ok = flushOne()
			}
			f.Close()
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if !ok {
				return nil
			}
		}
		return nil
	}

	var runErr error
	if *lineBuf {
		in := make(chan *jev.Item, 64)
		go func() {
			defer close(in)
			if err := produce(func(it *jev.Item) bool {
				select {
				case in <- it:
					return true
				case <-ctx.Done():
					return false
				}
			}); err != nil {
				t.Warnf("%v", err)
			}
		}()
		runErr = e.Run(ctx, in, flushDur, emit)
	} else {
		var items []*jev.Item
		if err := produce(func(it *jev.Item) bool { items = append(items, it); return true }); err != nil {
			t.Fatalf("%v", err)
		}
		pr.all = items
		t.Confirm(e.Plan(items))
		runErr = e.RunAll(ctx, items, emit)
	}
	if stopped && errors.Is(runErr, context.Canceled) && ctx.Err() != nil && t.Ctx().Err() == nil {
		runErr = nil // we stopped on purpose (-q / -m)
	}
	if stopped && !*quiet {
		pr.trailing()
	}
	if *count {
		for fi, n := range counts {
			if pr.showFile {
				fmt.Fprintf(out, "%s:%d\n", cli.Display(files[fi]), n)
			} else {
				fmt.Fprintf(out, "%d\n", n)
			}
		}
	}
	code := finish(t, runErr, firstErr, failed+int(openFailed.Load()), selected, uncertain, *quiet)
	t.Exit(code)
}

// finish picks the exit status the way grep does, plus 3 for "only uncertain".
func finish(t *cli.Tool, runErr, firstErr error, failed, selected, uncertain int, quiet bool) int {
	if runErr != nil {
		return t.Finish(runErr)
	}
	if failed > 0 {
		if firstErr != nil {
			t.Warnf("%d record(s) failed: %v", failed, firstErr)
		}
		if quiet && selected > 0 {
			return cli.ExitYes
		}
		return cli.ExitError
	}
	switch {
	case selected > 0:
		return cli.ExitYes
	case uncertain > 0:
		return cli.ExitUncertain
	}
	return cli.ExitNo
}

// fieldsAllowed is the CheckPlaceholders field limit: any {N} with -d, none without.
func fieldsAllowed(delim string) int {
	if delim != "" {
		return -1
	}
	return 0
}

func clone(s []string) []string { return append([]string(nil), s...) }

// printer writes selected records with grep-style prefixes and context.
type printer struct {
	out              *cli.Out
	sep              string
	names            []string
	showFile         bool
	scores, lineNum  bool
	before, after    int
	context          bool
	all              []*jev.Item // every item, when known (for trailing context)
	beforeBuf        []ctxRec
	afterLeft        int
	lastFile, lastNo int
	printed          bool
}

type ctxRec struct {
	r    *rec
	p    float64
	hasP bool
}

func (pr *printer) line(r *rec, p float64, hasP bool, mark byte) {
	if pr.context && pr.printed && (r.file != pr.lastFile || r.num != pr.lastNo+1) {
		pr.out.WriteString("--\n")
	}
	var b strings.Builder
	if pr.scores {
		if hasP {
			fmt.Fprintf(&b, "%.2f\t", p)
		} else {
			b.WriteString("-\t")
		}
	}
	if pr.showFile {
		b.WriteString(cli.Display(pr.names[r.file]))
		b.WriteByte(mark)
	}
	if pr.lineNum {
		b.WriteString(strconv.Itoa(r.num))
		b.WriteByte(mark)
	}
	b.WriteString(r.text)
	b.WriteString(pr.sep)
	pr.out.WriteString(b.String())
	pr.printed, pr.lastFile, pr.lastNo = true, r.file, r.num
}

func (pr *printer) sync(r *rec) {
	if pr.printed && r.file != pr.lastFile {
		pr.beforeBuf, pr.afterLeft = nil, 0
	}
}

func (pr *printer) match(r *rec, p float64) {
	pr.sync(r)
	for _, c := range pr.beforeBuf {
		if c.r.file == r.file {
			pr.line(c.r, c.p, c.hasP, '-')
		}
	}
	pr.beforeBuf = nil
	pr.line(r, p, true, ':')
	pr.afterLeft = pr.after
}

func (pr *printer) other(r *rec, p float64) { pr.ctx(ctxRec{r, p, true}) }

func (pr *printer) skip(r *rec) { pr.ctx(ctxRec{r: r}) }

func (pr *printer) ctx(c ctxRec) {
	if !pr.context {
		return
	}
	pr.sync(c.r)
	if pr.afterLeft > 0 && c.r.file == pr.lastFile {
		pr.afterLeft--
		pr.line(c.r, c.p, c.hasP, '-')
		return
	}
	if pr.before > 0 {
		pr.beforeBuf = append(pr.beforeBuf, c)
		if len(pr.beforeBuf) > pr.before {
			pr.beforeBuf = pr.beforeBuf[1:]
		}
	}
}

// trailing prints after-context that follows the last match when a run was
// stopped early (-m), without scores for records that were never judged.
func (pr *printer) trailing() {
	if pr.afterLeft == 0 || pr.all == nil {
		return
	}
	for _, it := range pr.all {
		r := it.Tag.(*rec)
		if r.file != pr.lastFile || r.num <= pr.lastNo {
			continue
		}
		if pr.afterLeft == 0 {
			break
		}
		pr.afterLeft--
		pr.line(r, 0, false, '-')
	}
}

// filesMode judges whole files (-l/-L): one question per file, chunked when a
// file is too large, where any chunk answering yes makes the file match.
func filesMode(t *cli.Tool, query string, files []string, without, scores, quiet bool, maxCount int,
	shared any, yesC, noC any, decide func(float64) int, unc *os.File, mode cli.RecMode) {
	e := t.Engine()
	out := t.Output()
	var question string
	switch q := strings.TrimSpace(query); {
	case cli.HasPlaceholder(q):
		question = strings.ReplaceAll(cli.Rewrite(q), "`text`", "`document`")
	case strings.HasSuffix(q, "?"):
		question = "Regarding `document`: " + q
	default:
		question = "Is it true that `document` " + strings.TrimSuffix(q, ".") + "?"
	}
	// Every chunk carries the shared -S/--about context too; leave room for it.
	budget := chunkTokens
	if _, isStr := shared.(string); shared != nil && !isStr {
		budget -= jev.NewState(shared).Est()
		if budget < 2000 {
			t.Fatalf("the -S/--about context is too large to send with each file (≈%d tokens)",
				jev.NewState(shared).Est())
		}
	}
	var items []*jev.Item
	failed := 0
	for fi, name := range files {
		text, err := cli.ReadInput(name)
		if err != nil {
			t.Warnf("%v", err)
			failed++
			continue
		}
		for _, chunk := range cli.Chunks(text, budget) {
			st := jev.Obj{{K: "name", V: cli.Display(name)}, {K: "document", V: chunk}}
			if shared != nil {
				if _, isStr := shared.(string); !isStr {
					st = append(st, jev.KV{K: "context", V: shared})
				}
			}
			items = append(items, jev.NewItem(jev.NewState(st), jev.Noul(question, yesC, noC), fi))
		}
	}
	t.Confirm(e.Plan(items))
	best := make([]float64, len(files))
	judged := make([]bool, len(files))
	var firstErr error
	runErr := e.RunAll(t.Ctx(), items, func(r jev.Result) {
		fi := r.Item.Tag.(int)
		if r.Err != nil {
			failed++
			if firstErr == nil {
				firstErr = r.Err
			}
			return
		}
		if !judged[fi] || r.Answer.Noul > best[fi] {
			best[fi] = r.Answer.Noul
		}
		judged[fi] = true
	})
	selected, uncertain := 0, 0
	for fi, name := range files {
		if !judged[fi] {
			continue
		}
		d := decide(best[fi])
		if without && d != -1 {
			d = 1 - d
		}
		switch d {
		case 1:
			if maxCount > 0 && selected >= maxCount {
				continue
			}
			selected++
			if quiet {
				continue
			}
			if scores {
				fmt.Fprintf(out, "%.2f\t", best[fi])
			}
			fmt.Fprintf(out, "%s%s", cli.Display(name), mode.Sep())
		case -1:
			uncertain++
			if unc != nil {
				fmt.Fprintln(unc, name)
			}
		}
	}
	t.Exit(finish(t, runErr, firstErr, failed, selected, uncertain, quiet))
}

const chunkTokens = 20_000
