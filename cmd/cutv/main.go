// Command cutv is cut(1) by description: it asks which column of a CSV or TSV
// table holds each DESCRIPTION, then prints those columns.
//
//	cutv 'customer email' 'signup date' < export.csv
package main

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/aurorainfra/grev/internal/cli"
	"github.com/aurorainfra/grev/internal/jev"
)

const none = "none"

func main() {
	t := cli.New("cutv")
	p := t.P
	p.Synopsis = []string{"[OPTIONS] DESCRIPTION... < TABLE", "[OPTIONS] -i TABLE DESCRIPTION..."}
	p.About = `Print the columns of a CSV or TSV table that hold each DESCRIPTION, in
argument order. The model sees only the header and a few sample rows, in a
single request; the rest of the table streams through without further calls.
TSV rows pass through verbatim. CSV is parsed and written back with the same
delimiter, so field values are unchanged but quoting may be normalized (quotes
only where needed) and CRLF line endings become LF.

The delimiter is detected from the header (tab → TSV, otherwise comma, or ';'
when the header has no comma); -d overrides it.`
	p.ExitStatus = `0 ok, 1 if a DESCRIPTION matches no column, 2 on error, 3 if a
match is below the -t confidence (nothing is printed), 4 if declined at -Q or
over --max-cost.`
	p.Examples = []string{
		`cutv 'customer email' < export.csv`,
		`cutv -s 'signup date' 'plan name' < users.tsv > slim.tsv`,
		`cutv --no-header 'phone number' < contacts.csv | sort -u`,
	}
	delim := p.Str('d', "delimiter", "DELIM", "", "field delimiter (default: detect)")
	sample := p.Int(0, "sample", "N", 5, "show the model N sample rows (default 5)")
	noHeader := p.Flag(0, "no-header", "don't print the header row")
	show := p.Flag('s', "show", "print the column mapping and confidences on stderr")
	threshold := p.Float('t', "threshold", "C", 0.5, "refuse matches with confidence below C (default 0.5)")
	input := p.Str('i', "input", "FILE", "", "read the table from FILE instead of stdin")
	about := p.Str(0, "about", "TEXT", "", "context about the table, e.g. what it is an export of")
	descs := t.Parse()
	if len(descs) == 0 {
		p.Usagef("missing DESCRIPTION")
	}
	if *sample < 0 {
		p.Usagef("--sample must be ≥ 0")
	}

	in, err := cli.Open(*input)
	if err != nil {
		t.Fatalf("%v", err)
	}
	defer in.Close()
	br := bufio.NewReaderSize(in, 64<<10)
	first, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		t.Fatalf("%v", err)
	}
	if strings.TrimSpace(first) == "" {
		t.Fatalf("empty input: want a table with a header row")
	}
	sep := detect(first)
	if *delim != "" {
		d := []rune(cli.Unescape(*delim))
		if len(d) != 1 {
			p.Usagef("-d wants a single character")
		}
		sep = d[0]
	}
	tab := newTable(io.MultiReader(strings.NewReader(first), br), sep)

	header, err := tab.next()
	if err != nil {
		t.Fatalf("reading header: %v", err)
	}
	if len(header) > 254 {
		t.Fatalf("%d columns: at most 254 are supported", len(header))
	}
	var rows [][]string
	for len(rows) < *sample {
		row, err := tab.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("%v", err)
		}
		rows = append(rows, row)
	}

	// State: one entry per column with its header and sample values, which is
	// what the model needs to tell columns apart.
	cols := make([]any, len(header))
	opts := make(jev.Opts, 0, len(header)+1)
	for i, h := range header {
		var vals []string
		for _, r := range rows {
			if i < len(r) {
				vals = append(vals, cli.Clip(r[i], 80))
			}
		}
		id := colID(i)
		cols[i] = jev.Obj{{K: "id", V: id}, {K: "header", V: cli.Clip(h, 80)}, {K: "samples", V: vals}}
		name := strings.TrimSpace(h)
		if name == "" {
			name = "(unnamed)"
		}
		opts = append(opts, jev.Opt{Key: id, Desc: "column " + strconv.Itoa(i+1) + ": " + cli.Clip(name, 80)})
	}
	opts = append(opts, jev.Opt{Key: none, Desc: "no column holds this"})
	state := jev.Obj{{K: "columns", V: cols}}
	if *about != "" {
		state = append(jev.Obj{{K: "about", V: *about}}, state...)
	}

	names := make([]string, len(descs))
	qs := make([]jev.Question, len(descs))
	for i, d := range descs {
		names[i] = "d" + strconv.Itoa(i)
		qs[i] = jev.Choice(jev.Obj{
			{K: "wanted", V: d},
			{K: "question", V: "Which column in `columns` holds `wanted`?"},
		}, opts)
	}
	ans := t.Ask(state, names, qs)

	pick := make([]int, len(descs))
	code := cli.ExitYes
	for i, d := range descs {
		a := ans[names[i]]
		pick[i] = -1
		if a.Choice != none {
			if n, err := strconv.Atoi(strings.TrimPrefix(a.Choice, "c")); err == nil && n >= 1 && n <= len(header) {
				pick[i] = n - 1
			}
		}
		verdict := ""
		switch {
		case pick[i] < 0:
			verdict = "  no matching column"
			code = cli.ExitNo // takes precedence over low confidence
		case a.Confidence < *threshold:
			verdict = fmt.Sprintf("  below -t %.2f", *threshold)
			if code != cli.ExitNo {
				code = cli.ExitUncertain
			}
		}
		if *show || verdict != "" {
			target := "none"
			if pick[i] >= 0 {
				target = fmt.Sprintf("col %d %q", pick[i]+1, header[pick[i]])
			}
			t.Warnf("%s → %s (%.2f)%s", d, target, a.Confidence, verdict)
		}
	}
	if code != cli.ExitYes {
		t.Exit(code)
	}

	out := t.Output()
	w := newWriter(out, sep)
	if !*noHeader {
		w.write(selectCols(header, pick))
	}
	for _, r := range rows {
		w.write(selectCols(r, pick))
	}
	for {
		row, err := tab.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			w.flush()
			t.Fatalf("%v", err)
		}
		w.write(selectCols(row, pick))
	}
	w.flush()
	t.Exit(cli.ExitYes)
}

func colID(i int) string { return "c" + strconv.Itoa(i+1) }

// detect guesses the delimiter from the header line.
func detect(line string) rune {
	switch {
	case strings.Contains(line, "\t"):
		return '\t'
	case strings.Contains(line, ","):
		return ','
	case strings.Contains(line, ";"):
		return ';'
	}
	return ','
}

func selectCols(row []string, pick []int) []string {
	out := make([]string, len(pick))
	for i, c := range pick {
		if c < len(row) {
			out[i] = row[c]
		}
	}
	return out
}

// table reads rows: TSV is split on tabs verbatim; anything else goes through
// encoding/csv (quotes allowed, ragged rows tolerated).
type table struct {
	tsv *bufio.Scanner
	csv *csv.Reader
}

func newTable(r io.Reader, sep rune) *table {
	if sep == '\t' {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64<<10), 64<<20)
		return &table{tsv: sc}
	}
	cr := csv.NewReader(r)
	cr.Comma = sep
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.ReuseRecord = false
	return &table{csv: cr}
}

func (t *table) next() ([]string, error) {
	if t.tsv != nil {
		if !t.tsv.Scan() {
			if err := t.tsv.Err(); err != nil {
				return nil, err
			}
			return nil, io.EOF
		}
		return strings.Split(strings.TrimSuffix(t.tsv.Text(), "\r"), "\t"), nil
	}
	return t.csv.Read()
}

type writer struct {
	out *cli.Out
	csv *csv.Writer
}

func newWriter(out *cli.Out, sep rune) *writer {
	if sep == '\t' {
		return &writer{out: out}
	}
	cw := csv.NewWriter(out)
	cw.Comma = sep
	return &writer{out: out, csv: cw}
}

func (w *writer) write(fields []string) {
	if w.csv != nil {
		w.csv.Write(fields)
		return
	}
	w.out.WriteString(strings.Join(fields, "\t") + "\n")
}

func (w *writer) flush() {
	if w.csv != nil {
		w.csv.Flush()
	}
}
