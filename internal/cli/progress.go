package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aurorainfra/grev/internal/jev"
)

// Progress is the -p overlay: two lines redrawn in place on a terminal, or a
// plain status line every few seconds otherwise. It always ends with a
// one-line summary.
type Progress struct {
	mu    sync.Mutex
	name  string
	eng   *jev.Engine
	tty   bool
	drawn bool
	quit  chan struct{}
	done  chan struct{}
}

func startProgress(name string, e *jev.Engine) *Progress {
	// Without ANSI escapes (an old Windows console) the overlay can't redraw
	// in place, so it falls back to plain status lines.
	p := &Progress{name: name, eng: e, tty: IsTerminal(os.Stderr) && EnableVT(os.Stderr),
		quit: make(chan struct{}), done: make(chan struct{})}
	go p.loop()
	return p
}

func (p *Progress) loop() {
	defer close(p.done)
	every := 100 * time.Millisecond
	if !p.tty {
		every = 5 * time.Second
	}
	tk := time.NewTicker(every)
	defer tk.Stop()
	for {
		select {
		case <-p.quit:
			return
		case <-tk.C:
			p.mu.Lock()
			if p.tty {
				p.draw()
			} else {
				l1, l2 := p.render(0)
				fmt.Fprintf(os.Stderr, "%s | %s\n", l1, l2)
			}
			p.mu.Unlock()
		}
	}
}

// pause clears the overlay while fn writes to the terminal.
func (p *Progress) pause(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clear()
	fn()
}

func (p *Progress) stop(summary bool) {
	close(p.quit)
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clear()
	if summary {
		fmt.Fprintln(os.Stderr, p.summary())
	}
}

func (p *Progress) clear() {
	if p.drawn {
		os.Stderr.WriteString("\r\x1b[J")
		p.drawn = false
	}
}

func (p *Progress) draw() {
	w := TermWidth(os.Stderr)
	if w <= 0 {
		w = 100
	}
	l1, l2 := p.render(w - 1)
	p.clear()
	// Leave the cursor at the start of the first line so clear() can wipe
	// both lines with one "erase below".
	os.Stderr.WriteString(l1 + "\n" + l2 + "\x1b[1A\r")
	p.drawn = true
}

func (p *Progress) render(width int) (string, string) {
	s := p.eng.Stats.Snapshot()
	limit, adaptive, trend, inflight := p.eng.Sched.State()
	el := time.Since(s.Start).Seconds()
	rate := 0.0
	if el > 0 {
		rate = float64(s.DoneQ) / el
	}

	var l1 strings.Builder
	l1.WriteString(p.name)
	if s.PlanQ > 0 {
		frac := float64(s.DoneQ) / float64(s.PlanQ)
		eta := "--"
		if rate > 0 && s.DoneQ < s.PlanQ {
			eta = fmtDur(float64(s.PlanQ-s.DoneQ) / rate)
		} else if s.DoneQ >= s.PlanQ {
			eta = "0s"
		}
		fmt.Fprintf(&l1, " %s %s/%s q  %3.0f%%  %s q/s  ETA %s",
			bar(frac, 20), commas(s.DoneQ), commas(s.PlanQ), frac*100, fmtRate(rate), eta)
	} else {
		fmt.Fprintf(&l1, "  %s q  %s q/s  %s", commas(s.DoneQ), fmtRate(rate), fmtDur(el))
	}

	parts := []string{fmt.Sprintf("req %d✓ %d⟳", s.Reqs, inflight)}
	if s.Queued > 0 {
		parts[0] += fmt.Sprintf(" %dq", s.Queued)
	}
	j := fmt.Sprintf("J %d", limit)
	if adaptive {
		arrow := "="
		switch trend {
		case 1:
			arrow = "↑"
		case -1:
			arrow = "↓"
		}
		j = fmt.Sprintf("J %d%s max", limit, arrow)
	}
	parts = append(parts, j)
	if s.Retries > 0 {
		parts = append(parts, fmt.Sprintf("%d retry (%d×429)", s.Retries, s.Throttles))
	}
	proj := s.Projected()
	if s.PlanEst > 0 {
		parts = append(parts, fmt.Sprintf("%s/%s tok", fmtTokens(s.Tokens),
			fmtTokens(int(float64(s.PlanEst)*s.Ratio()))))
	} else {
		parts = append(parts, fmtTokens(s.Tokens)+" tok")
	}
	switch {
	case s.PriceUnknown:
		parts = append(parts, "$?")
	case proj >= 0:
		parts = append(parts, fmt.Sprintf("%s → ~%s", fmtCost(s.Cost), fmtCost(proj)))
	case el > 1:
		parts = append(parts, fmt.Sprintf("%s (%s/min)", fmtCost(s.Cost), fmtCost(s.Cost/el*60)))
	default:
		parts = append(parts, fmtCost(s.Cost))
	}
	if s.FailedQ > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", s.FailedQ))
	}
	parts = append(parts, s.Model)
	l2 := strings.Repeat(" ", min(len(p.name)+1, 8)) + strings.Join(parts, " · ")
	return trunc(l1.String(), width), trunc(l2, width)
}

func (p *Progress) summary() string {
	s := p.eng.Stats.Snapshot()
	el := time.Since(s.Start).Seconds()
	cost := fmtCost(s.Cost)
	if s.PriceUnknown {
		cost = "$?"
	}
	parts := []string{
		commas(s.DoneQ) + " q",
		commas(s.Reqs) + " req",
		commas(s.Tokens) + " tok",
		cost,
		fmtDur(el),
		s.Model,
	}
	if s.Retries > 0 {
		parts = append(parts, fmt.Sprintf("%d retries", s.Retries))
	}
	if s.FailedQ > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", s.FailedQ))
	}
	return p.name + ": " + strings.Join(parts, " · ")
}

func bar(frac float64, width int) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	parts := []rune(" ▏▎▍▌▋▊▉")
	cells := frac * float64(width)
	full := int(cells)
	var b strings.Builder
	b.WriteRune('▕')
	b.WriteString(strings.Repeat("█", full))
	if full < width {
		b.WriteRune(parts[int((cells-float64(full))*8)])
		b.WriteString(strings.Repeat(" ", width-full-1))
	}
	b.WriteRune('▏')
	return b.String()
}

func fmtRate(r float64) string {
	switch {
	case r >= 10_000:
		return fmt.Sprintf("%.0fk", r/1000)
	case r >= 1000:
		return fmt.Sprintf("%.1fk", r/1000)
	case r >= 10:
		return fmt.Sprintf("%.0f", r)
	default:
		return fmt.Sprintf("%.1f", r)
	}
}

func trunc(s string, width int) string {
	if width <= 0 || utf8.RuneCountInString(s) <= width {
		return s
	}
	r := []rune(s)
	return string(r[:width-1]) + "…"
}

// Out is stdout, coordinated with the overlay: when both share a terminal,
// the overlay is lifted for every write. Output is flushed per write on a
// terminal or with SetLineBuffered, and at exit otherwise.
type Out struct {
	mu      sync.Mutex
	w       *bufio.Writer
	prog    *Progress
	tty     bool
	lineBuf bool
	err     error       // first write error (e.g. a closed pipe)
	onError func(error) // called once, on the first write error
}

// Err is the first error writing to stdout, if any.
func (o *Out) Err() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.err
}

func newOut(prog *Progress) *Out {
	tty := IsTerminal(os.Stdout)
	o := &Out{w: bufio.NewWriterSize(os.Stdout, 64<<10), prog: prog, tty: tty}
	o.SetLineBuffered(false)
	return o
}

// SetLineBuffered flushes after every write (for --line-buffered). Writes to
// a terminal or a pipe are always flushed, so `grev … | head -3` sees results
// as they come and closing the pipe stops the run (and the spending).
func (o *Out) SetLineBuffered(b bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lineBuf = b || o.tty || isPipe(os.Stdout)
}

func (o *Out) setProgress(p *Progress) {
	o.mu.Lock()
	o.prog = p
	o.mu.Unlock()
}

func isPipe(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeNamedPipe != 0
}

// Write writes b to stdout.
func (o *Out) Write(b []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var n int
	var err error
	write := func() {
		n, err = o.w.Write(b)
		if err == nil && o.lineBuf {
			err = o.w.Flush()
		}
	}
	if o.err != nil {
		return 0, o.err // stdout is gone; don't keep trying
	}
	if o.prog != nil && o.tty && o.prog.tty {
		o.prog.pause(write)
	} else {
		write()
	}
	if err != nil {
		o.err = err
		if o.onError != nil {
			go o.onError(err)
		}
	}
	return n, err
}

// WriteString writes s to stdout.
func (o *Out) WriteString(s string) (int, error) { return o.Write([]byte(s)) }

// Flush flushes buffered output.
func (o *Out) Flush() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.w.Flush()
}
