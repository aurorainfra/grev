package cli

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aurorainfra/grev/internal/jev"
)

// RecMode says how input splits into records.
type RecMode int

const (
	Lines RecMode = iota // newline-terminated lines
	NUL                  // NUL-terminated (-z)
	Para                 // blank-line separated paragraphs (--para)
)

// Records registers -z and --para on p and returns a getter for the mode.
func Records(p *Parser) func() RecMode {
	z := p.Flag('z', "null-data", "records are NUL-terminated instead of lines")
	para := p.Flag(0, "para", "records are paragraphs separated by blank lines")
	return func() RecMode {
		switch {
		case *z:
			return NUL
		case *para:
			return Para
		}
		return Lines
	}
}

// Sep is the record terminator used when printing records of mode m.
func (m RecMode) Sep() string {
	switch m {
	case NUL:
		return "\x00"
	case Para:
		return "\n\n"
	}
	return "\n"
}

func (m RecMode) split(data []byte, atEOF bool) (int, []byte, error) {
	switch m {
	case NUL:
		if i := bytes.IndexByte(data, 0); i >= 0 {
			return i + 1, data[:i], nil
		}
	case Para:
		// Skip leading blank lines, then cut at the next blank line.
		start := 0
		for start < len(data) && (data[start] == '\n' || data[start] == '\r') {
			start++
		}
		for i := start; i < len(data); i++ {
			if data[i] != '\n' {
				continue
			}
			j := i + 1
			if j < len(data) && data[j] == '\r' {
				j++
			}
			if j < len(data) && data[j] == '\n' {
				return j + 1, bytes.TrimRight(data[start:i], "\r"), nil
			}
		}
		if atEOF {
			if rest := bytes.TrimRight(data[start:], "\r\n"); len(rest) > 0 {
				return len(data), rest, nil
			}
			return len(data), nil, nil
		}
		return start, nil, nil
	default:
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			return i + 1, bytes.TrimSuffix(data[:i], []byte("\r")), nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// Scan calls fn for every record in r until fn returns false.
func Scan(r io.Reader, m RecMode, fn func(rec string) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	sc.Split(m.split)
	for sc.Scan() {
		if m == Para && len(sc.Bytes()) == 0 {
			continue
		}
		if !fn(sc.Text()) {
			return nil
		}
	}
	return sc.Err()
}

// ReadAll returns every record of r.
func ReadAll(r io.Reader, m RecMode) ([]string, error) {
	var out []string
	err := Scan(r, m, func(s string) bool { out = append(out, s); return true })
	return out, err
}

// Open opens a named input; "-" and "" mean stdin.
func Open(name string) (io.ReadCloser, error) {
	if name == "" || name == "-" {
		return io.NopCloser(os.Stdin), nil
	}
	return os.Open(name)
}

// ReadInput returns the whole of the named input as a string.
func ReadInput(name string) (string, error) {
	f, err := Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	return string(b), err
}

// Unescape turns \t, \n, \0 and \\ in a delimiter argument into characters.
func Unescape(s string) string {
	r := strings.NewReplacer(`\t`, "\t", `\n`, "\n", `\0`, "\x00", `\\`, `\`)
	return r.Replace(s)
}

// ---- Questions about records ----

var placeholder = regexp.MustCompile(`\{(\d*)\}`)

// HasPlaceholder reports whether q references the record ({}) or a field ({N}).
func HasPlaceholder(q string) bool { return placeholder.MatchString(q) }

// Rewrite replaces {} with `text` and {N} with `fN`, the names under which
// Instr stores the record and its fields.
func Rewrite(q string) string {
	return placeholder.ReplaceAllStringFunc(q, func(m string) string {
		if n := m[1 : len(m)-1]; n != "" {
			return "`f" + n + "`"
		}
		return "`text`"
	})
}

// Template wraps a query without placeholders into a question about `text`.
// GREV_TEMPLATE selects an alternative wording (used by the eval harness).
func Template(query string) string {
	q := strings.TrimSpace(query)
	if HasPlaceholder(q) {
		return Rewrite(q)
	}
	switch os.Getenv("GREV_TEMPLATE") {
	case "statement":
		return "`text` " + strings.TrimSuffix(q, ".") + "."
	case "about":
		return "Regarding `text`: " + q
	}
	if strings.HasSuffix(q, "?") {
		return "Regarding `text`: " + q
	}
	return "Is it true that `text` " + strings.TrimSuffix(q, ".") + "?"
}

// Rec is one record with optional fields and model-visible neighbours.
type Rec struct {
	Text          string
	Fields        []string
	Before, After []string
}

// Instr builds structured instructions: the record under "text", fields
// under "f1".."fN", neighbours under "before"/"after", then the question.
func Instr(r Rec, question any) jev.Obj {
	o := jev.Obj{}
	if len(r.Before) > 0 {
		o = append(o, jev.KV{K: "before", V: r.Before})
	}
	o = append(o, jev.KV{K: "text", V: r.Text})
	for i, f := range r.Fields {
		o = append(o, jev.KV{K: "f" + strconv.Itoa(i+1), V: f})
	}
	if len(r.After) > 0 {
		o = append(o, jev.KV{K: "after", V: r.After})
	}
	return append(o, jev.KV{K: "question", V: question})
}

// SharedState builds the state shared by per-record questions from -S FILE
// and --about TEXT. With neither, it is a short neutral description.
func SharedState(refFile, about string) (any, error) {
	o := jev.Obj{}
	if about != "" {
		o = append(o, jev.KV{K: "about", V: about})
	}
	if refFile != "" {
		b, err := os.ReadFile(refFile)
		if err != nil {
			return nil, err
		}
		o = append(o, jev.KV{K: "reference", V: string(b)})
	}
	if len(o) == 0 {
		return "Each question carries its own text to judge.", nil
	}
	return o, nil
}

// Display is how a file name appears in output; "-" is stdin.
func Display(name string) string {
	if name == "-" || name == "" {
		return "(standard input)"
	}
	return name
}

// Clip shortens s to at most n runes, marking the cut with "…".
func Clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// Chunks splits text on line boundaries into pieces of at most ~maxTok
// tokens, for inputs too large for one request.
func Chunks(text string, maxTok int) []string {
	if jev.EstText(text) <= maxTok {
		return []string{text}
	}
	var out []string
	var cur strings.Builder
	maxBytes := max(1, int(float64(maxTok)*3.6)) // matches jev's estimate
	for _, line := range strings.SplitAfter(text, "\n") {
		for jev.EstText(line) > maxTok { // a single huge line (minified code…)
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			cut := maxBytes
			for cut > 0 && !utf8.RuneStart(line[cut]) {
				cut--
			}
			out = append(out, line[:cut])
			line = line[cut:]
		}
		if cur.Len() > 0 && jev.EstText(cur.String()+line) > maxTok {
			out = append(out, cur.String())
			cur.Reset()
		}
		cur.WriteString(line)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// CheckPlaceholders rejects placeholders a tool can't fill: {} unless
// allowRecord, and {N} beyond maxField (0: no fields; -1: any, e.g. with -d).
func CheckPlaceholders(q string, allowRecord bool, maxField int) error {
	for _, m := range placeholder.FindAllStringSubmatch(q, -1) {
		switch n := m[1]; {
		case n == "" && !allowRecord:
			return fmt.Errorf("{} isn't available here")
		case n == "":
		case maxField == 0:
			return fmt.Errorf("{%s} refers to a field, but there are no fields (use -d DELIM)", n)
		case maxField > 0:
			if i, _ := strconv.Atoi(n); i < 1 || i > maxField {
				return fmt.Errorf("{%s} is out of range: only {1}..{%d} exist", n, maxField)
			}
		}
	}
	return nil
}

// ParseBand parses --band LO:HI with 0 ≤ LO ≤ HI ≤ 1.
func ParseBand(s string) (lo, hi float64, err error) {
	a, b, found := strings.Cut(s, ":")
	lo, e1 := strconv.ParseFloat(a, 64)
	hi, e2 := strconv.ParseFloat(b, 64)
	if !found || e1 != nil || e2 != nil || lo > hi || lo < 0 || hi > 1 {
		return 0, 0, fmt.Errorf("--band wants LO:HI with 0 ≤ LO ≤ HI ≤ 1, got %q", s)
	}
	return lo, hi, nil
}
