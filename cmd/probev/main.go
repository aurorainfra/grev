// Command probev asks many questions about every record and prints the
// answers as TSV columns — features for awk, sort or a spreadsheet.
//
//	probev -q 'u: conveys urgency' -q 'r: asks for a refund' < t.txt | awk -F'\t' '.6*$1+.4*$2>.7'
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aurorainfra/grev/internal/cli"
	"github.com/aurorainfra/grev/internal/jev"
)

type question struct {
	name string
	q    jev.Question
}

// prec is one record and the answers collected for it so far.
type prec struct {
	text    string
	answers []jev.Answer
	errs    []error
	got     int
}

type tag struct {
	r  *prec
	qi int
}

func main() {
	t := cli.New("probev")
	p := t.P
	p.Synopsis = []string{"[OPTIONS] -q SPEC... [FILE]", "[OPTIONS] -f QUESTIONS.json [FILE]"}
	p.About = `Ask every SPEC question about each record of FILE (or stdin) and print one
TSV row per record: one column per question, in order, then the record.
All questions about a record go in one request, so extra questions are
nearly free.

Columns: a yes/no question prints P(yes), a Score prints its position on the
levels (0 = first level), a Choice prints the chosen option. -s adds a
confidence column after each Choice and Score.`
	p.Notes = `SPEC:  name: question             yes/no (Noul)                → P(yes)
       name: question [a|b|c]     pick one option (Choice)      → option
       name: question <lo|mid|hi> ordered levels (Score)        → 0..N-1
Options may be a=description. {} in a question refers to the record.`
	p.ExitStatus = `0 ok, 2 error, 4 declined at -Q or over --max-cost.`
	p.Examples = []string{
		`probev -q 'u: conveys urgency' -q 'r: asks for a refund' < tickets.txt |
  awk -F'\t' '.6*$1 + .4*$2 > .7'`,
		`probev -H -s -q 'dept: Which team? [billing|tech|sales]' -q 'mood: How upset? <calm|annoyed|angry>' < t.txt`,
		`probev --jsonl --json -q 'spam: Is the body unsolicited advertising?' < mails.jsonl`,
	}
	specs := p.List('q', "question", "SPEC", "a question (repeatable), see SPEC below")
	qFile := p.Str('f', "questions", "FILE", "", "questions as a JSON object {name: API question, …}")
	header := p.Flag('H', "header", "print a header row of column names")
	scores := p.Flag('s', "scores", "add a confidence column after each Choice and Score")
	noRecord := p.Flag(0, "no-record", "don't print the record as the last column")
	asJSON := p.Flag(0, "json", "print JSON lines {record, answers} with full probabilities")
	jsonl := p.Flag(0, "jsonl", "records are JSON objects, one per line, used as structured state")
	pack := p.Flag(0, "pack", "put many records in one request (each question carries its record); faster for short records")
	refFile := p.Str('S', "reference", "FILE", "", "shared context for every question")
	about := p.Str(0, "about", "TEXT", "", "shared context, e.g. what the records are")
	mode := cli.Records(p)
	lineBuf := p.Flag(0, "line-buffered", "stream: answer and print records as they arrive")
	flush := p.Str(0, "flush", "DUR", "250ms", "with --line-buffered: send a partial request after DUR")
	args := t.Parse()
	if len(args) > 1 {
		p.Usagef("want at most one FILE")
	}
	input := ""
	if len(args) == 1 {
		input = args[0]
	}
	flushDur, err := time.ParseDuration(*flush)
	if err != nil {
		p.Usagef("--flush: %v", err)
	}
	if *jsonl && *pack {
		p.Usagef("--pack works on text records, not --jsonl")
	}

	// Questions, in order: -q specs, then -f.
	var qs []question
	seen := map[string]bool{}
	add := func(name string, q jev.Question) {
		if seen[name] {
			p.Usagef("duplicate question name %q", name)
		}
		seen[name] = true
		qs = append(qs, question{name, q})
	}
	usesRecord := false
	for i, s := range *specs {
		sp, err := cli.ParseSpec(s, i+1)
		if err != nil {
			p.Usagef("-q: %v", err)
		}
		if err := cli.CheckPlaceholders(sp.Text, !*jsonl, 0); err != nil {
			if *jsonl {
				err = fmt.Errorf("%w with --jsonl: refer to the record's fields by name, e.g. `message`", err)
			}
			p.Usagef("-q %s: %v", sp.Name, err)
		}
		if cli.HasPlaceholder(sp.Text) {
			usesRecord = true
			sp.Q.Instructions = cli.Rewrite(sp.Text)
		}
		add(sp.Name, sp.Q)
	}
	if *qFile != "" {
		more, err := readQuestions(*qFile)
		if err != nil {
			t.Fatalf("%s: %v", *qFile, err)
		}
		for _, q := range more {
			add(q.name, q.q)
		}
	}
	if len(qs) == 0 {
		p.Usagef("need at least one -q SPEC or -f FILE")
	}

	// The record's state. Text records are the state itself unless shared
	// context or a {} reference needs named fields.
	var ctxFields jev.Obj
	if *about != "" {
		ctxFields = append(ctxFields, jev.KV{K: "about", V: *about})
	}
	if *refFile != "" {
		ref, err := cli.ReadInput(*refFile)
		if err != nil {
			t.Fatalf("%v", err)
		}
		ctxFields = append(ctxFields, jev.KV{K: "reference", V: ref})
	}
	var shared *jev.State
	if *pack {
		sv, err := cli.SharedState(*refFile, *about)
		if err != nil {
			t.Fatalf("%v", err)
		}
		shared = jev.NewState(sv)
	}
	mkItems := func(r *prec) ([]*jev.Item, error) {
		items := make([]*jev.Item, len(qs))
		var st *jev.State
		switch {
		case *pack:
			st = shared
		case *jsonl:
			raw := json.RawMessage(r.text)
			if !json.Valid(raw) {
				return nil, fmt.Errorf("not valid JSON: %.60s", r.text)
			}
			if len(ctxFields) > 0 {
				st = jev.NewState(append(append(jev.Obj{}, ctxFields...), jev.KV{K: "record", V: raw}))
			} else {
				st = jev.NewState(raw)
			}
		case len(ctxFields) > 0 || usesRecord:
			st = jev.NewState(append(append(jev.Obj{}, ctxFields...), jev.KV{K: "text", V: r.text}))
		default:
			st = jev.NewState(r.text)
		}
		for i, q := range qs {
			if *pack {
				// Each question carries its record. Structured instructions
				// (from -f) stay structured under "question".
				var ins any = q.q.Instructions
				if s, ok := ins.(string); ok && !strings.Contains(s, "`text`") {
					ins = "Regarding `text`: " + s
				}
				q.q.Instructions = cli.Instr(cli.Rec{Text: r.text}, ins)
			}
			items[i] = jev.NewItem(st, q.q, tag{r, i})
		}
		return items, nil
	}

	out := t.Output()
	out.SetLineBuffered(*lineBuf)
	if *header && !*asJSON {
		var cols []string
		for _, q := range qs {
			cols = append(cols, q.name)
			if *scores && q.q.Type != jev.TypeNoul {
				cols = append(cols, q.name+".conf")
			}
		}
		if !*noRecord {
			cols = append(cols, "record")
		}
		out.WriteString(strings.Join(cols, "\t") + "\n")
	}
	failedRecs := 0
	var firstErr error
	print := func(r *prec) {
		for _, e := range r.errs {
			if e != nil {
				failedRecs++
				if firstErr == nil {
					firstErr = e
				}
				break
			}
		}
		if *asJSON {
			ans := jev.Obj{}
			for i, q := range qs {
				if r.errs[i] != nil {
					ans = append(ans, jev.KV{K: q.name, V: map[string]string{"error": r.errs[i].Error()}})
				} else {
					ans = append(ans, jev.KV{K: q.name, V: r.answers[i]})
				}
			}
			var rv any = r.text
			if *jsonl && json.Valid([]byte(r.text)) {
				rv = json.RawMessage(r.text)
			}
			row := jev.Obj{}
			if !*noRecord {
				row = append(row, jev.KV{K: "record", V: rv})
			}
			row = append(row, jev.KV{K: "answers", V: ans})
			b, err := json.Marshal(row)
			if err != nil {
				t.Fatalf("%v", err)
			}
			out.Write(append(b, '\n'))
			return
		}
		var cols []string
		for i, q := range qs {
			a := r.answers[i]
			if r.errs[i] != nil {
				cols = append(cols, "-")
				if *scores && q.q.Type != jev.TypeNoul {
					cols = append(cols, "-")
				}
				continue
			}
			switch a.Type {
			case jev.TypeNoul:
				cols = append(cols, fmt.Sprintf("%.2f", a.Noul))
			case jev.TypeScore:
				cols = append(cols, fmt.Sprintf("%.2f", a.Score))
			default:
				cols = append(cols, a.Choice)
			}
			if *scores && q.q.Type != jev.TypeNoul {
				cols = append(cols, fmt.Sprintf("%.2f", a.Confidence))
			}
		}
		if !*noRecord {
			cols = append(cols, r.text)
		}
		out.WriteString(strings.Join(cols, "\t") + mode().Sep())
	}
	emit := func(res jev.Result) {
		tg := res.Item.Tag.(tag)
		r := tg.r
		r.answers[tg.qi], r.errs[tg.qi] = res.Answer, res.Err
		if r.got++; r.got == len(qs) {
			print(r)
		}
	}
	newRec := func(text string) *prec {
		if *jsonl {
			text = strings.TrimSpace(text)
		}
		return &prec{text: text, answers: make([]jev.Answer, len(qs)), errs: make([]error, len(qs))}
	}
	skip := func(text string) bool { return *jsonl && strings.TrimSpace(text) == "" }

	f, err := cli.Open(input)
	if err != nil {
		t.Fatalf("%v", err)
	}
	defer f.Close()
	var runErr error
	var badInput atomic.Int64 // streaming: unparsable records, counted off the main goroutine
	if *lineBuf {
		ctx, stop := context.WithCancel(t.Ctx())
		defer stop()
		in := make(chan *jev.Item, 64)
		go func() {
			defer close(in)
			err := cli.Scan(f, mode(), func(text string) bool {
				if skip(text) {
					return true
				}
				items, err := mkItems(newRec(text))
				if err != nil {
					t.Warnf("%v", err)
					badInput.Add(1)
					return true
				}
				for _, it := range items {
					select {
					case in <- it:
					case <-ctx.Done():
						return false
					}
				}
				return true
			})
			if err != nil {
				t.Warnf("%v", err)
			}
		}()
		runErr = t.StreamItems(ctx, in, flushDur, emit)
		failedRecs += int(badInput.Load())
	} else {
		texts, err := cli.ReadAll(f, mode())
		if err != nil {
			t.Fatalf("%v", err)
		}
		var items []*jev.Item
		for _, text := range texts {
			if skip(text) {
				continue
			}
			its, err := mkItems(newRec(text))
			if err != nil {
				t.Fatalf("%v", err)
			}
			items = append(items, its...)
		}
		runErr = t.RunItems(t.Ctx(), items, emit)
	}
	if runErr != nil {
		t.Exit(t.Finish(runErr))
	}
	if failedRecs > 0 {
		if firstErr != nil {
			t.Warnf("%d record(s) had failed questions (shown as -): %v", failedRecs, firstErr)
		} else {
			t.Warnf("%d record(s) failed", failedRecs)
		}
		t.Exit(cli.ExitError)
	}
	t.Exit(cli.ExitYes)
}

// readQuestions reads {name: question, …} keeping the file's order.
func readQuestions(path string) ([]question, error) {
	text, err := cli.ReadInput(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("want a JSON object of questions")
	}
	var out []question
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		name, _ := tok.(string)
		var q jev.Question
		if err := dec.Decode(&q); err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		switch q.Type {
		case jev.TypeNoul, jev.TypeChoice, jev.TypeScore:
		default:
			return nil, fmt.Errorf("%s: unknown type %q", name, q.Type)
		}
		out = append(out, question{name, q})
	}
	return out, nil
}
