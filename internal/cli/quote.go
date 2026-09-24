package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/aurorainfra/grev/internal/jev"
)

func printQuote(w io.Writer, name string, e *jev.Engine, q jev.Quote) {
	p, known := jev.PriceOf(e.Model)
	pad := strings.Repeat(" ", len(name)+8)
	maxQ := e.MaxQ
	fmt.Fprintf(w, "%s quote: %s questions → %s request(s) (≤%d q each) · %s\n",
		name, commas(q.Questions), commas(q.Requests), maxQ, e.Model)
	if known {
		fmt.Fprintf(w, "%s≈%s input tokens (±20%%) × $%g/Mtok ≈ %s · output free\n",
			pad, fmtTokens(q.EstTokens), p.In, fmtCost(float64(q.EstTokens)*p.In/1e6))
	} else {
		fmt.Fprintf(w, "%s≈%s input tokens · price of %s unknown (set GREV_PRICE_PER_MTOK)\n",
			pad, fmtTokens(q.EstTokens), e.Model)
	}
	rpm, tps := e.Sched.Rates()
	limit, adaptive, _, _ := e.Sched.State()
	j := fmt.Sprintf("-J %d", limit)
	if adaptive {
		j = "-J max"
	}
	floor := math.Max(float64(q.Requests)/(rpm/60), float64(q.EstTokens)/tps)
	fmt.Fprintf(w, "%s%s · ceiling %s req/min, %s tok/s · ≥%s\n",
		pad, j, commas(int(rpm)), fmtTokens(int(tps)), fmtDur(floor))
	if q.TooBig > 0 {
		fmt.Fprintf(w, "%s%d record(s) too large for one request will fail\n", pad, q.TooBig)
	}
}

// askYes prompts on the controlling terminal (GREV_TTY overrides it for
// tests), since stdin usually carries the data.
func askYes(ctx context.Context, prompt string) (bool, error) {
	path := os.Getenv("GREV_TTY")
	if path == "" {
		path = ttyPath
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	fmt.Fprint(os.Stderr, prompt)
	type answer struct {
		line string
		err  error
	}
	ch := make(chan answer, 1)
	go func() {
		line, err := readLine(f)
		ch <- answer{line, err}
	}()
	var line string
	select {
	case <-ctx.Done(): // Ctrl-C at the prompt declines
		fmt.Fprintln(os.Stderr)
		return false, nil
	case a := <-ch:
		if a.err != nil && a.line == "" {
			fmt.Fprintln(os.Stderr)
			return false, nil
		}
		line = a.line
	}
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes", nil
}

// readLine reads up to a newline one byte at a time, so nothing past the
// line is consumed from a shared descriptor.
func readLine(f *os.File) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := f.Read(buf)
		if n == 1 {
			if buf[0] == '\n' {
				return b.String(), nil
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			return b.String(), err
		}
	}
}

// ReadLineFrom reads one line from r (used by tools reading small inputs).
func ReadLineFrom(r io.Reader) (string, error) {
	s, err := bufio.NewReader(r).ReadString('\n')
	return strings.TrimRight(s, "\r\n"), err
}

func fmtCost(c float64) string {
	switch {
	case c == 0:
		return "$0"
	case c < 0.000001:
		return fmt.Sprintf("$%.2g", c)
	case c < 0.0001:
		return fmt.Sprintf("$%.6f", c)
	case c < 1:
		return fmt.Sprintf("$%.4f", c)
	default:
		return fmt.Sprintf("$%.2f", c)
	}
}

func fmtTokens(n int) string {
	switch {
	case n >= 10_000_000:
		return fmt.Sprintf("%.0fM", float64(n)/1e6)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprint(n)
	}
}

func fmtDur(sec float64) string {
	switch {
	case sec < 1:
		return fmt.Sprintf("%.1fs", sec)
	case sec < 60:
		return fmt.Sprintf("%.0fs", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm%02ds", int(sec)/60, int(sec)%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(sec)/3600, int(sec)%3600/60)
	}
}

func commas(n int) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return "-" + commas(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
