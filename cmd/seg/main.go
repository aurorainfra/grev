// Command seg is csplit(1) by meaning: it asks, at every boundary between two
// records, whether a new segment (topic, speaker, request…) starts there, and
// splits the input at the boundaries the model agrees with.
//
//	seg meeting-transcript.txt
//	seg --split part- 'Does a different speaker start talking at {2}?' talk.txt
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aurorainfra/grev/internal/cli"
	"github.com/aurorainfra/grev/internal/jev"
)

const defaultQuestion = "Does a new topic begin at {2}?"

// Window limits: each window of boundaries shares one state, the records it
// covers tagged with ids, plus -W records of context on each side.
const (
	winRecs  = 64
	winBytes = 20_000
)

func recID(i int) string { return fmt.Sprintf("R%04d", i+1) }

// bnd is the boundary between record i-1 (prev) and record i.
type bnd struct {
	i    int
	prev string
}

// buffer holds records indexed from the start of the input; records before
// base have been dropped (streaming keeps only what later windows and the
// output still need).
type buffer struct {
	base int
	recs []string
}

func (b *buffer) get(j int) string { return b.recs[j-b.base] }
func (b *buffer) n() int           { return b.base + len(b.recs) }

// trim drops records before keep.
func (b *buffer) trim(keep int) {
	if keep > b.base {
		b.recs = append([]string(nil), b.recs[keep-b.base:]...)
		b.base = keep
	}
}

func main() {
	t := cli.New("seg")
	p := t.P
	p.Synopsis = []string{"[OPTIONS] [QUESTION] [FILE]"}
	p.About = `Split the records (lines by default) of FILE (or stdin) into segments: at
each boundary the model judges whether a new segment begins, and a separator line
is printed there (or, with --split, a new file is started).

QUESTION may reference the record before the boundary as {1} and the one after as
{2}. Default:
  ` + defaultQuestion + `
Without placeholders QUESTION is read as a predicate, e.g. 'the speaker changes'.
The model sees the records around each boundary, tagged with ids.`
	p.ExitStatus = `0 ok, 2 error, 4 declined at -Q or stopped by --max-cost.`
	p.Examples = []string{
		`seg meeting-notes.txt`,
		`seg --para -s 'Does {2} start a different news story than {1}?' digest.txt`,
		`seg --split chapter- --min 20 book.txt`,
		`tail -f app.log | seg --line-buffered 'Does {2} start handling a new request?'`,
	}
	scores := p.Flag('s', "scores", "put P(new segment) on each separator line")
	threshold := p.Float('t', "threshold", "P", 0.5, "start a new segment when P ≥ P (default 0.5)")
	sepFlag := p.OptStr("sep", "STR", "", "separator line between segments (default: empty line; '---' with --para)")
	split := p.Str(0, "split", "PREFIX", "", "write segments to PREFIX00, PREFIX01, … and print their names")
	minRecs := p.Int(0, "min", "N", 1, "never start a segment before the current one has N records")
	window := p.Int('W', "window", "N", 3, "records of context the model sees on each side of a window")
	about := p.Str(0, "about", "TEXT", "", "shared context, e.g. what the input is")
	mode := cli.Records(p)
	lineBuf := p.Flag(0, "line-buffered", "stream: print records as soon as their boundary is decided")
	flush := p.Str(0, "flush", "DUR", "250ms", "with --line-buffered: send a partial request after DUR")
	args := t.Parse()

	question, file := defaultQuestion, ""
	switch len(args) {
	case 0:
	case 1:
		if _, err := os.Stat(args[0]); err == nil && !cli.HasPlaceholder(args[0]) {
			file = args[0]
		} else {
			question = args[0]
		}
	case 2:
		question, file = args[0], args[1]
	default:
		p.Usagef("want [QUESTION] [FILE]")
	}
	if err := cli.CheckPlaceholders(question, true, 2); err != nil {
		p.Usagef("QUESTION: %v ({1} is the record before the boundary, {2} or {} the one after)", err)
	}
	flushDur, err := time.ParseDuration(*flush)
	if err != nil {
		p.Usagef("--flush: %v", err)
	}
	if *window < 0 || *minRecs < 1 {
		p.Usagef("-W wants N ≥ 0 and --min N ≥ 1")
	}
	m := mode()
	sepLine := ""
	if m == cli.Para {
		sepLine = "---"
	}
	if *sepFlag != nil {
		sepLine = **sepFlag
	}
	ask := func(i int) string { return askAt(question, i) }

	f, err := cli.Open(file)
	if err != nil {
		t.Fatalf("%v", err)
	}
	defer f.Close()

	// items builds the questions for boundaries from..to (inclusive), sharing
	// one state: the records from..to cover, plus W records on each side.
	items := func(recs *buffer, from, to int) []*jev.Item {
		lo, hi := max(0, from-1-*window), min(recs.n(), to+1+*window)
		var b strings.Builder
		for j := lo; j < hi; j++ {
			b.WriteString(recID(j) + "| " + recs.get(j) + "\n")
		}
		var sv any = b.String()
		if *about != "" {
			sv = jev.Obj{{K: "about", V: *about}, {K: "records", V: b.String()}}
		}
		st := jev.NewState(sv)
		out := make([]*jev.Item, 0, to-from+1)
		for i := from; i <= to; i++ {
			out = append(out, jev.NewItem(st, jev.Noul(ask(i), nil, nil), &bnd{i: i, prev: recs.get(i - 1)}))
		}
		return out
	}
	// windows cuts boundaries from..last into windows and sends their items.
	windows := func(recs *buffer, from, last int, send func(*jev.Item) bool) bool {
		for from <= last {
			to, size := from, 0
			for to < last && to-from+1 < winRecs && size < winBytes {
				size += len(recs.get(to))
				to++
			}
			for _, it := range items(recs, from, to) {
				if !send(it) {
					return false
				}
			}
			from = to + 1
		}
		return true
	}

	// Output: a record is printed once the boundary after it is decided.
	out := t.Output()
	out.SetLineBuffered(*lineBuf)
	var w io.Writer = out
	part := 0
	var partFile *os.File
	openPart := func() {
		if *split == "" {
			return
		}
		if partFile != nil {
			partFile.Close()
		}
		name := fmt.Sprintf("%s%02d", *split, part)
		part++
		var err error
		if partFile, err = os.Create(name); err != nil {
			t.Fatalf("%v", err)
		}
		w = partFile
		fmt.Fprintln(out, name)
	}
	// write prints one record, opening the first part file on first use so
	// that nothing is created before -Q is answered.
	write := func(rec string) {
		if *split != "" && partFile == nil {
			openPart()
		}
		io.WriteString(w, rec+m.Sep())
	}
	// printed: records before this index have been written. The streaming
	// reader reads it to know which records it may drop.
	var printed atomic.Int64
	segLen := 0
	failed := 0
	var firstErr error
	emit := func(r jev.Result) {
		b := r.Item.Tag.(*bnd)
		write(b.prev)
		printed.Store(int64(b.i))
		segLen++
		if r.Err != nil {
			failed++
			if firstErr == nil {
				firstErr = r.Err
			}
			return
		}
		pv := r.Answer.Noul
		if pv < *threshold || segLen < *minRecs {
			return
		}
		segLen = 0
		if *split != "" {
			openPart()
			return
		}
		line := sepLine
		if *scores {
			if line == "" {
				line = "---"
			}
			line = fmt.Sprintf("%s %.2f", line, pv)
		}
		io.WriteString(w, line+m.Sep())
	}

	var recs *buffer
	var runErr error
	if *lineBuf {
		ctx, stop := context.WithCancel(t.Ctx())
		defer stop()
		in := make(chan *jev.Item, 64)
		recCh := make(chan string, 256)
		errCh := make(chan error, 1)
		go func() {
			errCh <- cli.Scan(f, m, func(r string) bool { recCh <- r; return true })
			close(recCh)
		}()
		done := make(chan *buffer, 1)
		go func() {
			defer close(in)
			local := &buffer{}
			defer func() { done <- local }()
			send := func(it *jev.Item) bool {
				select {
				case in <- it:
					return true
				case <-ctx.Done():
					return false
				}
			}
			next := 1 // next boundary to ask about
			var timer <-chan time.Time
			// ready is the last boundary whose W records of lookahead exist.
			ready := func(final bool) int {
				if final {
					return local.n() - 1
				}
				return local.n() - 1 - *window
			}
			// drop keeps what later windows show (from next-1-W) and what
			// is not printed yet.
			drop := func() {
				if keep := min(next-1-*window, int(printed.Load())); keep-local.base >= 256 {
					local.trim(keep)
				}
			}
			for {
				select {
				case <-ctx.Done():
					return
				case r, ok := <-recCh:
					if !ok {
						windows(local, next, ready(true), send)
						if err := <-errCh; err != nil {
							t.Warnf("%v", err)
						}
						return
					}
					local.recs = append(local.recs, r)
					if timer == nil && local.n() > 1 {
						timer = time.After(flushDur)
					}
					if ready(false)-next+1 >= winRecs {
						if !windows(local, next, ready(false), send) {
							return
						}
						next, timer = ready(false)+1, nil
						drop()
					}
				case <-timer:
					if last := ready(false); last >= next {
						if !windows(local, next, last, send) {
							return
						}
						next = last + 1
						drop()
					}
					timer = nil
				}
			}
		}()
		runErr = t.StreamItems(ctx, in, flushDur, emit)
		stop()
		recs = <-done
	} else {
		all, err := cli.ReadAll(f, m)
		if err != nil {
			t.Fatalf("%v", err)
		}
		recs = &buffer{recs: all}
		var its []*jev.Item
		if len(all) > 1 {
			windows(recs, 1, len(all)-1, func(it *jev.Item) bool { its = append(its, it); return true })
			runErr = t.RunItems(t.Ctx(), its, emit)
		}
	}
	// Records after the last decided boundary (the tail, or everything left
	// when a run stopped early) are printed without further splitting.
	for j := max(int(printed.Load()), recs.base); j < recs.n(); j++ {
		write(recs.get(j))
	}
	if partFile != nil {
		partFile.Close()
	}
	code := t.Finish(runErr)
	if code == cli.ExitYes && failed > 0 {
		t.Warnf("%d boundary question(s) failed (no split there): %v", failed, firstErr)
		code = cli.ExitError
	}
	t.Exit(code)
}

// askAt phrases the question for the boundary before record i: {1} and {2}
// become the ids of the records on either side.
func askAt(q string, i int) string {
	a, b := recID(i-1), recID(i)
	q = strings.TrimSpace(q)
	if cli.HasPlaceholder(q) {
		q = strings.ReplaceAll(q, "{1}", "record "+a)
		q = strings.ReplaceAll(q, "{2}", "record "+b)
		return strings.ReplaceAll(q, "{}", "record "+b)
	}
	if strings.HasSuffix(q, "?") {
		return fmt.Sprintf("Regarding the boundary between record %s and record %s: %s", a, b, q)
	}
	return fmt.Sprintf("Is it true that %s at record %s, right after record %s?", strings.TrimSuffix(q, "."), b, a)
}
