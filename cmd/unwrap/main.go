// Command unwrap rejoins hard-wrapped text: it asks, for every line break the
// code can't settle itself, whether the break tore a sentence apart, and joins
// the lines that continue one. Every output byte comes from the input.
//
//	pdftotext paper.pdf - | unwrap
package main

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aurorainfra/grev/internal/cli"
	"github.com/aurorainfra/grev/internal/jev"
)

// Wording and criteria from TypeSafe's structure-recovery cookbook: the state
// is the text with every line tagged by an id, and each question points at
// two ids. (Asking about the pair alone, without the document around it,
// separated headings from continuations much worse.)
const (
	joinQuestion = "Does line %s pick up mid-sentence, continuing a sentence left unfinished at the end of line %s?"
	joinTrue     = "The line starts in the middle of a sentence that began on the previous line - the line break tore the sentence apart"
	joinFalse    = "The line begins a new sentence, item, heading, or thought of its own"
)

// Window limits: each window of lines becomes one state (plus two lines of
// context before it), small enough to keep distractors down.
const (
	winLines = 120
	winBytes = 20_000
)

func lineID(i int) string { return fmt.Sprintf("L%04d", i+1) }

var (
	reFence    = regexp.MustCompile("^ {0,3}(```|~~~)")
	reHeading  = regexp.MustCompile(`^ {0,3}#{1,6}(\s|$)`)
	reRule     = regexp.MustCompile(`^ {0,3}([-=*_])(\s*[-=*_]){2,}\s*$`)
	reList     = regexp.MustCompile(`^\s*([-*+•‣◦]|\d{1,3}[.)]|[a-zA-Z][.)])\s+`)
	reQuote    = regexp.MustCompile(`^\s*>`)
	reTable    = regexp.MustCompile(`^\s*\|`)
	reTerminal = regexp.MustCompile(`[.!?:;…]["'”’)\]]*$`)
	reHyphen   = regexp.MustCompile(`\p{L}-$`)
)

// kind is what the code knows about a line without asking the model.
type kind int

const (
	plain  kind = iota
	blank       // paragraph break
	fixed       // fence, fenced content, heading, rule, table, code-indented: never joined
	starts      // list item or quote start: begins a block, but later lines may continue it
)

func classify(line string, inFence bool) kind {
	switch {
	case strings.TrimSpace(line) == "":
		return blank
	case inFence, reFence.MatchString(line), reHeading.MatchString(line),
		reRule.MatchString(line), reTable.MatchString(line),
		strings.HasPrefix(line, "\t"), strings.HasPrefix(line, "    "):
		return fixed
	case reList.MatchString(line), reQuote.MatchString(line):
		return starts
	}
	return plain
}

// candidate reports whether the break between lines a and b needs the model.
func candidate(ka, kb kind, a, b string) bool {
	if ka == blank || kb == blank || ka == fixed || kb == fixed {
		return false
	}
	if kb == starts {
		// A quote line may continue a quote; nothing else continues into a new item.
		return reQuote.MatchString(a) && reQuote.MatchString(b)
	}
	return true
}

// doc holds lines and the join decision for the break before each line, and
// prints lines once their own and the next line's decisions are known. Lines
// are indexed from the start of the input; printed lines are dropped (base
// is the index of lines[0]), so streaming runs in bounded memory.
type doc struct {
	mu        sync.Mutex
	out       io.Writer
	base      int
	lines     []string
	join      []int8    // for line i: 1 join into i-1, 0 break, -1 undecided
	p         []float64 // P(join) for line i; -1 when the code decided
	next      int       // next line to print
	eof       bool
	hold      bool // print nothing yet (until -Q/--max-cost have passed)
	sep       string
	dehyphen  bool
	showScore bool
}

func (d *doc) add(line string, decided int8) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lines = append(d.lines, line)
	d.join = append(d.join, decided)
	d.p = append(d.p, -1)
	d.flush()
	return d.base + len(d.lines) - 1
}

func (d *doc) decide(i int, j bool, p float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	i -= d.base
	d.join[i], d.p[i] = 0, p
	if j {
		d.join[i] = 1
	}
	d.flush()
}

// release lets held output through.
func (d *doc) release() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.hold = false
	d.flush()
}

// settle turns breaks never answered (budget, interrupt) into line breaks.
func (d *doc) settle() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.join {
		if d.join[i] < 0 {
			d.join[i] = 0
		}
	}
}

func (d *doc) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.eof = true
	d.flush()
}

func (d *doc) flush() {
	if d.hold {
		return
	}
	for d.next < d.base+len(d.lines) {
		i := d.next - d.base
		if d.join[i] < 0 {
			return
		}
		last := i+1 >= len(d.lines)
		if last && !d.eof {
			return
		}
		if !last && d.join[i+1] < 0 {
			return
		}
		d.print(i, !last && d.join[i+1] == 1)
		d.next++
	}
	// Keep the last printed line (a quote continuation looks back at it).
	if drop := d.next - 1 - d.base; drop >= 256 {
		d.lines = append([]string(nil), d.lines[drop:]...)
		d.join = append([]int8(nil), d.join[drop:]...)
		d.p = append([]float64(nil), d.p[drop:]...)
		d.base += drop
	}
}

// print writes line i (an index into d.lines); joinNext says the following
// line joins onto it.
func (d *doc) print(i int, joinNext bool) {
	line := d.lines[i]
	if d.showScore {
		if d.p[i] < 0 {
			fmt.Fprintf(d.out, "-\t%s\n", line)
		} else {
			fmt.Fprintf(d.out, "%.2f\t%s\n", d.p[i], line)
		}
		return
	}
	if d.join[i] == 1 {
		line = strings.TrimLeft(line, " \t")
		if i > 0 && reQuote.MatchString(d.lines[i-1]) {
			// A quote continuing a quote: its marker would land mid-sentence.
			line = strings.TrimLeft(strings.TrimPrefix(line, ">"), " \t")
		}
	}
	if !joinNext {
		io.WriteString(d.out, line+"\n")
		return
	}
	line = strings.TrimRight(line, " \t")
	next := strings.TrimLeft(d.lines[i+1], " \t")
	if r, _ := utf8.DecodeRuneInString(next); d.dehyphen && reHyphen.MatchString(line) && unicode.IsLower(r) {
		io.WriteString(d.out, strings.TrimSuffix(line, "-"))
		return
	}
	io.WriteString(d.out, line+d.sep)
}

type brk struct {
	i    int // index of the second line
	term bool
}

func main() {
	t := cli.New("unwrap")
	p := t.P
	p.Synopsis = []string{"[OPTIONS] [FILE]"}
	p.About = `Rejoin text that was hard-wrapped mid-sentence (PDF extracts, emails, old
READMEs). Blank lines, headings, list items, quotes, tables, indented code and
fenced blocks are handled in code and never joined; for every other line break
the model judges whether the next line picks up mid-sentence. Joined lines are
separated by one space; nothing else changes.`
	p.Notes = `A join needs P ≥ 0.2 after a line with no closing punctuation, and P ≥ 0.5
after one ending in . ! ? : ; (thresholds from TypeSafe's structure-recovery
cookbook; override with -t DANGLING,TERMINAL).`
	p.ExitStatus = `0 ok, 2 error, 4 declined at -Q or stopped by --max-cost.`
	p.Examples = []string{
		`pdftotext paper.pdf - | unwrap`,
		`unwrap -s notes.txt            # show P(join) per line instead of joining`,
		`unwrap --dehyphen < scan.txt   # also mend words split with a hyphen`,
	}
	scores := p.Flag('s', "scores", "print each line with its P(join) instead of joining (for tuning)")
	thresh := p.Str('t', "threshold", "D,T", "0.2,0.5", "join thresholds after a dangling / a terminally punctuated line")
	joinSep := p.Str('j', "join", "SEP", " ", "separator put between joined lines")
	dehyphen := p.Flag(0, "dehyphen", "join 'exam-' + 'ple' into 'example' (changes bytes)")
	about := p.Str(0, "about", "TEXT", "", "shared context, e.g. what the text is")
	lineBuf := p.Flag(0, "line-buffered", "stream: print lines as soon as their breaks are decided")
	flush := p.Str(0, "flush", "DUR", "250ms", "with --line-buffered: send a partial request after DUR")
	args := t.Parse()
	if len(args) > 1 {
		p.Usagef("want at most one FILE")
	}
	file := ""
	if len(args) == 1 {
		file = args[0]
	}
	dStr, tStr, ok := strings.Cut(*thresh, ",")
	tDangling, e1 := strconv.ParseFloat(dStr, 64)
	tTerminal, e2 := strconv.ParseFloat(tStr, 64)
	if !ok || e1 != nil || e2 != nil {
		p.Usagef("-t wants DANGLING,TERMINAL, e.g. 0.2,0.5")
	}
	flushDur, err := time.ParseDuration(*flush)
	if err != nil {
		p.Usagef("--flush: %v", err)
	}

	out := t.Output()
	out.SetLineBuffered(*lineBuf)
	// In batch mode nothing is printed before the run is confirmed (-Q,
	// --max-cost), even lines whose breaks the code already settled.
	d := &doc{out: out, sep: cli.Unescape(*joinSep), dehyphen: *dehyphen, showScore: *scores, hold: !*lineBuf}

	f, err := cli.Open(file)
	if err != nil {
		t.Fatalf("%v", err)
	}
	defer f.Close()

	// produce walks the lines, decides what code can, and sends a question for
	// every other break. Breaks are grouped into windows; each window's state
	// is its tagged lines. When streaming, a window is also cut after flushDur.
	produce := func(send func(*jev.Item) bool, every time.Duration) error {
		lineCh := make(chan string, 256)
		errCh := make(chan error, 1)
		go func() {
			errCh <- cli.Scan(f, cli.Lines, func(l string) bool { lineCh <- l; return true })
			close(lineCh)
		}()
		inFence := false
		// Lines are indexed from the start of the input; only those the next
		// window can still show are kept (base is the index of lines[0]).
		var kinds []kind
		var lines []string
		base := 0
		total := func() int { return base + len(lines) }
		var pend []*brk
		winStart, winSize := 0, 0
		var timer <-chan time.Time
		cut := func() bool {
			defer func() {
				pend, winStart, winSize, timer = nil, total(), 0, nil
				if keep := max(0, winStart-2); keep > base {
					lines = append([]string(nil), lines[keep-base:]...)
					kinds = append([]kind(nil), kinds[keep-base:]...)
					base = keep
				}
			}()
			if len(pend) == 0 {
				return true
			}
			var b strings.Builder
			for j := max(0, winStart-2); j < total(); j++ {
				if kinds[j-base] != blank {
					b.WriteString(lineID(j) + "| " + lines[j-base])
				}
				b.WriteByte('\n')
			}
			var sv any = b.String()
			if *about != "" {
				sv = jev.Obj{{K: "about", V: *about}, {K: "document", V: b.String()}}
			}
			st := jev.NewState(sv)
			for _, br := range pend {
				q := fmt.Sprintf(joinQuestion, lineID(br.i), lineID(br.i-1))
				if !send(jev.NewItem(st, jev.Noul(q, joinTrue, joinFalse), br)) {
					return false
				}
			}
			return true
		}
		for {
			select {
			case line, ok := <-lineCh:
				if !ok {
					cut()
					return <-errCh
				}
				k := classify(line, inFence)
				if reFence.MatchString(line) {
					inFence = !inFence
					k = fixed
				}
				kinds = append(kinds, k)
				lines = append(lines, line)
				i := total() - 1
				var decided int8
				if i > 0 && candidate(kinds[i-1-base], k, lines[i-1-base], line) {
					decided = -1
					pend = append(pend, &brk{i: i, term: reTerminal.MatchString(strings.TrimRight(lines[i-1-base], " \t"))})
					if timer == nil && every > 0 {
						timer = time.After(every)
					}
				}
				d.add(line, decided)
				winSize += len(line) + 8
				if total()-winStart >= winLines || winSize >= winBytes {
					if !cut() {
						return nil
					}
				}
			case <-timer:
				if !cut() {
					return nil
				}
			}
		}
	}

	failed := 0
	var firstErr error
	emit := func(r jev.Result) {
		b := r.Item.Tag.(*brk)
		if r.Err != nil {
			failed++
			if firstErr == nil {
				firstErr = r.Err
			}
			d.decide(b.i, false, -1)
			return
		}
		pv := r.Answer.Noul
		th := tDangling
		if b.term {
			th = tTerminal
		}
		d.decide(b.i, pv >= th, pv)
	}

	var runErr error
	if *lineBuf {
		ctx, stop := context.WithCancel(t.Ctx())
		defer stop()
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
			}, flushDur); err != nil {
				t.Warnf("%v", err)
			}
		}()
		runErr = t.StreamItems(ctx, in, flushDur, emit)
	} else {
		var items []*jev.Item
		if err := produce(func(it *jev.Item) bool { items = append(items, it); return true }, 0); err != nil {
			t.Fatalf("%v", err)
		}
		if len(items) > 0 {
			e := t.Engine()
			t.Confirm(e.Plan(items)) // exits on decline, before any output
			d.release()
			runErr = e.RunAll(t.Ctx(), items, emit)
		}
		d.release()
	}
	d.settle()
	d.close()
	code := t.Finish(runErr)
	if code == cli.ExitYes && failed > 0 {
		t.Warnf("%d break(s) failed and were kept: %v", failed, firstErr)
		code = cli.ExitError
	}
	t.Exit(code)
}
