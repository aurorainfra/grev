// Command pickv finds the one line (or regex match) that best answers a
// question: a relative choice, unlike grev's per-line yes/no, plus a check that
// the input answers the question at all.
//
//	man rsync | pickv 'how do I exclude a directory?'
//	pickv -e '[\w.+-]+@[\w.-]+\.\w+' 'where should receipts be sent?' < mail.eml
package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/aurorainfra/grev/internal/cli"
	"github.com/aurorainfra/grev/internal/jev"
)

// Window limits: a Choice takes at most 255 options, the document plus the
// question must fit the model's 32k-token context, and a smaller state means
// fewer distractors.
const (
	maxOpts = 255
	winTok  = 16_000 // estimated tokens of document per window
	// finalists kept per window for the next round
	keepPerWindow = 3
	// longer regex matches (or multi-line ones) are offered by id, not text
	maxKeyLen = 100
)

// Questions, following TypeSafe's line-by-line search cookbook. A QUESTION
// without a "?" describes the line instead ("the most interesting line"); asked
// whether any line "answers" it, the model says no.
const (
	whereQ  = `Which line of the document contains the answer to: "%s"?`
	existsQ = `Does any line of the document address or answer: "%s"?`
	existsT = "At least one line of the document states or directly implies the answer"
	existsF = "No line of the document addresses this"

	whereD   = `Which line of the document best fits this description: "%s"?`
	existsD  = `Does any line of the document fit this description: "%s"?`
	existsDT = "At least one line of the document fits the description"
	existsDF = "No line of the document fits the description"
)

type opts struct {
	top      int
	scores   bool
	lineNum  bool
	context  int
	exists   float64
	force    bool
	minConf  float64
	about    string
	patterns []string
}

func main() {
	t := cli.New("pickv")
	p := t.P
	p.Synopsis = []string{"[OPTIONS] QUESTION [FILE]", "-e REGEX... [OPTIONS] QUESTION [FILE]"}
	p.About = `Print the record (line by default) of FILE (or stdin) that best answers
QUESTION. Unlike grev, which judges every line on its own, pick compares them:
the model points at the single best line, and a separate yes/no check decides
whether the input answers QUESTION at all. A QUESTION without a "?" is taken
as a description of the line: 'the most interesting line', 'the line that
names the database host'.

With -e, candidates are the matches of REGEX instead of lines, and pick prints
the chosen match exactly as it appears (or nothing if none fits).`
	p.Notes = `Inputs longer than 255 lines are searched in windows, then the best lines of
every window compete in a final round. Windows hold about 16k tokens of input;
a record larger than that is skipped with a warning.`
	p.ExitStatus = `0 found, 1 nothing answers QUESTION (nothing printed, see --force),
2 error, 3 found but the choice is uncertain (confidence below --min-conf),
4 declined at -Q or stopped by --max-cost. With --force the best candidate is
printed even when the status is 1.`
	p.Examples = []string{
		`man rsync | pickv 'how do I exclude a directory?'`,
		`pickv -m3 -s -n 'where is the retry limit configured?' config.yaml`,
		`pickv -C2 'the most unusual line' app.log`,
		`pickv -e '[\w.+-]+@[\w.-]+\.\w+' 'where should receipts be sent?' < mail.eml`,
		`pickv -e '\$[0-9][0-9,.]*' 'the invoice total' invoice.txt`,
	}
	var o opts
	top := p.Int('m', "max-count", "N", 1, "print the N best candidates, best first")
	scores := p.Flag('s', "scores", "prefix candidates with their probability")
	lineNum := p.Flag('n', "line-number", "prefix candidates with their record (line) number")
	ctxN := p.Int('C', "context", "N", 0, "lines mode: print N records around each pick")
	exists := p.Float('t', "threshold", "P", 0.5, "lines mode: need P(the input answers QUESTION) ≥ P (default 0.5)")
	force := p.Flag(0, "force", "print the best candidate even when nothing answers QUESTION")
	minConf := p.Float(0, "min-conf", "C", 0.5, "exit 3 when the choice's confidence is below C (default 0.5)")
	about := p.Str(0, "about", "TEXT", "", "shared context, e.g. what the input is")
	patterns := p.List('e', "regexp", "REGEX", "candidates are REGEX matches (repeatable; Go RE2 syntax)")
	mode := cli.Records(p)
	args := t.Parse()
	if len(args) < 1 || len(args) > 2 {
		p.Usagef("want QUESTION [FILE]")
	}
	o = opts{top: max(1, *top), scores: *scores, lineNum: *lineNum, context: *ctxN,
		exists: *exists, force: *force, minConf: *minConf, about: *about, patterns: *patterns}
	question := strings.TrimSpace(args[0])
	file := ""
	if len(args) == 2 {
		file = args[1]
	}
	if len(o.patterns) > 0 {
		regexMode(t, o, question, file)
		return
	}
	linesMode(t, o, question, file, mode())
}

// cand is one line candidate: its record index and text.
type cand struct {
	i    int
	text string
	tok  float64 // estimated tokens as a document line
	p    float64
}

func linesMode(t *cli.Tool, o opts, question, file string, m cli.RecMode) {
	f, err := cli.Open(file)
	if err != nil {
		t.Fatalf("%v", err)
	}
	recs, err := cli.ReadAll(f, m)
	f.Close()
	if err != nil {
		t.Fatalf("%v", err)
	}
	where, exists, existsYes, existsNo := whereQ, existsQ, existsT, existsF
	if !strings.HasSuffix(question, "?") {
		where, exists, existsYes, existsNo = whereD, existsD, existsDT, existsDF
	}
	prefix := "L"
	if m != cli.Lines {
		prefix = "R"
	}
	id := func(i int) string { return fmt.Sprintf("%s%04d", prefix, i+1) }
	var cands []*cand
	skipped := map[int]bool{}
	for i, r := range recs {
		tok := jev.Tokens(id(i) + "| " + r + "\n")
		switch {
		case strings.TrimSpace(r) == "":
		case tok > winTok:
			skipped[i] = true
			t.Warnf("skipping record %d: too large for one request (≈%d tokens)", i+1, int(tok))
		default:
			cands = append(cands, &cand{i: i, text: r, tok: tok})
		}
	}
	if len(cands) == 0 {
		if len(skipped) > 0 {
			t.Exit(cli.ExitError)
		}
		t.Exit(cli.ExitNo)
	}
	state := func(doc string) any {
		if o.about != "" {
			return jev.Obj{{K: "about", V: o.about}, {K: "document", V: doc}}
		}
		return doc
	}
	// windows groups candidates under the option and size limits.
	windows := func(cs []*cand) [][]*cand {
		var out [][]*cand
		var cur []*cand
		size := 0.0
		for _, c := range cs {
			if len(cur) > 0 && (len(cur) >= maxOpts || size+c.tok > winTok) {
				out = append(out, cur)
				cur, size = nil, 0
			}
			cur = append(cur, c)
			size += c.tok
		}
		return append(out, cur)
	}
	// round asks one Choice per window (plus the exists Noul in round one).
	// It returns the candidates with probabilities, per window, and the
	// best exists and the confidence of each window's choice.
	type winResult struct {
		cands []*cand
		conf  float64
	}
	e := t.Engine()
	existsP := 0.0
	// A window the API still finds too large (the estimate is only an
	// estimate) is halved and asked again; a single record that doesn't fit
	// is skipped.
	round := func(n int, wins [][]*cand) []winResult {
		var res []winResult
		for first := true; len(wins) > 0; first = false {
			part := make([]winResult, len(wins))
			var items []*jev.Item
			for w, win := range wins {
				var b strings.Builder
				if n == 1 {
					// The window's full span of records, blank lines included.
					for i := win[0].i; i <= win[len(win)-1].i; i++ {
						if strings.TrimSpace(recs[i]) != "" && !skipped[i] {
							b.WriteString(id(i) + "| " + recs[i])
						}
						b.WriteByte('\n')
					}
				} else {
					for _, c := range win {
						b.WriteString(id(c.i) + "| " + c.text + "\n")
					}
				}
				st := jev.NewState(state(b.String()))
				var opts jev.Opts
				for _, c := range win {
					opts = append(opts, jev.Opt{Key: id(c.i)})
				}
				items = append(items, jev.NewItem(st, jev.Choice(fmt.Sprintf(where, question), opts), w))
				if n == 1 {
					items = append(items, jev.NewItem(st, jev.Noul(fmt.Sprintf(exists, question), existsYes, existsNo), -1-w))
				}
				part[w].cands = win
			}
			q := e.Plan(items)
			if n == 1 && first {
				if len(wins) > 1 {
					// Account for the final round in the quote: one request over
					// the finalists.
					final := 0.0
					for _, win := range wins {
						for _, c := range win[:min(len(win), keepPerWindow)] {
							final += c.tok
						}
					}
					q.Requests++
					q.EstTokens += jev.EstRequest(nil) + int(final) + keepPerWindow*len(wins)*8
				}
				t.Confirm(q)
			}
			tooBig := map[int]bool{}
			var firstErr error
			existsW := map[int]float64{}
			runErr := e.RunAll(t.Ctx(), items, func(r jev.Result) {
				w := r.Item.Tag.(int)
				if r.Err != nil {
					switch {
					case jev.IsOverLimit(r.Err):
						tooBig[max(w, -1-w)] = true
					case firstErr == nil:
						firstErr = r.Err
					}
					return
				}
				if w < 0 {
					existsW[-1-w] = r.Answer.Noul
					return
				}
				for _, c := range part[w].cands {
					c.p = r.Answer.Probabilities[id(c.i)]
				}
				part[w].conf = r.Answer.Confidence
			})
			if runErr == nil {
				runErr = firstErr
			}
			if runErr != nil {
				t.Exit(t.Finish(runErr))
			}
			var again [][]*cand
			for w, win := range wins {
				switch {
				case !tooBig[w]:
					res = append(res, part[w])
					existsP = max(existsP, existsW[w])
				case len(win) > 1:
					again = append(again, win[:len(win)/2], win[len(win)/2:])
				default:
					skipped[win[0].i] = true
					t.Warnf("skipping record %d: too large for one request", win[0].i+1)
				}
			}
			wins = again
		}
		if len(res) == 0 {
			t.Fatalf("no record fits in a request")
		}
		return res
	}

	wins := windows(cands)
	res := round(1, wins)
	for n := 2; len(res) > 1; n++ {
		var finalists []*cand
		for _, r := range res {
			sorted := append([]*cand(nil), r.cands...)
			sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].p > sorted[b].p })
			finalists = append(finalists, sorted[:min(len(sorted), max(keepPerWindow, o.top))]...)
		}
		sort.SliceStable(finalists, func(a, b int) bool { return finalists[a].i < finalists[b].i })
		res = round(n, windows(finalists))
	}
	final := append([]*cand(nil), res[0].cands...)
	sort.SliceStable(final, func(a, b int) bool { return final[a].p > final[b].p })
	final = final[:min(len(final), o.top)]

	answered := existsP >= o.exists
	if !answered && !o.force {
		t.Exit(cli.ExitNo)
	}
	out := t.Output()
	printed := false
	for _, c := range final {
		if o.context > 0 {
			if printed {
				out.WriteString("--\n")
			}
			for i := max(0, c.i-o.context); i <= min(len(recs)-1, c.i+o.context); i++ {
				mark := byte('-')
				if i == c.i {
					mark = ':'
				}
				writeCand(out, o, c.p, i == c.i, mark, i+1, recs[i], m.Sep())
			}
		} else {
			writeCand(out, o, c.p, true, ':', c.i+1, c.text, m.Sep())
		}
		printed = true
	}
	code := cli.ExitYes
	switch {
	case !answered:
		code = cli.ExitNo // printed only because of --force
	case res[0].conf < o.minConf:
		code = cli.ExitUncertain
	}
	t.Exit(code)
}

func writeCand(out *cli.Out, o opts, p float64, hasP bool, mark byte, num int, text, sep string) {
	var b strings.Builder
	if o.scores {
		if hasP {
			fmt.Fprintf(&b, "%.2f\t", p)
		} else {
			b.WriteString("-\t")
		}
	}
	if o.lineNum {
		b.WriteString(strconv.Itoa(num))
		b.WriteByte(mark)
	}
	b.WriteString(text)
	b.WriteString(sep)
	out.WriteString(b.String())
}

// regexMode: candidates are the distinct regex matches; the model picks the
// one QUESTION asks for (or none), and pick prints it verbatim.
func regexMode(t *cli.Tool, o opts, question, file string) {
	var res []*regexp.Regexp
	for _, pat := range o.patterns {
		re, err := regexp.Compile(pat)
		if err != nil {
			t.P.Usagef("-e: %v", err)
		}
		res = append(res, re)
	}
	text, err := cli.ReadInput(file)
	if err != nil {
		t.Fatalf("%v", err)
	}
	type span struct {
		text  string
		start int
	}
	var spans []span
	seen := map[string]bool{}
	for _, re := range res {
		for _, loc := range re.FindAllStringIndex(text, -1) {
			s := text[loc[0]:loc[1]]
			if strings.TrimSpace(s) == "" || seen[s] {
				continue
			}
			seen[s] = true
			spans = append(spans, span{s, loc[0]})
		}
	}
	sort.SliceStable(spans, func(a, b int) bool { return spans[a].start < spans[b].start })
	if len(spans) == 0 {
		t.Exit(cli.ExitNo)
	}
	if len(spans) >= maxOpts {
		t.Fatalf("%d distinct matches; at most %d fit one choice — narrow the regex", len(spans), maxOpts-1)
	}
	// Short single-line matches are offered as themselves (as in TypeSafe's
	// pre-parsed extraction cookbook); if any match is long or spans lines,
	// all are offered by id, with the matches listed in the state.
	byID := false
	for _, s := range spans {
		if len(s.text) > maxKeyLen || strings.ContainsAny(s.text, "\n\r") {
			byID = true
		}
	}
	key := func(i int) string {
		if byID {
			return fmt.Sprintf("M%03d", i+1)
		}
		return spans[i].text
	}
	none := "none"
	for seen[none] {
		none = "(" + none + ")"
	}
	var opts jev.Opts
	for i := range spans {
		opts = append(opts, jev.Opt{Key: key(i)})
	}
	opts = append(opts, jev.Opt{Key: none, Desc: "None of these is the requested value."})
	if !strings.HasSuffix(question, "?") {
		question = "Which candidate is " + strings.TrimSuffix(question, ".") + "?"
	}

	// The whole document is the state when it fits; otherwise excerpts
	// around each candidate's first occurrence.
	var doc any = text
	if jev.EstText(text) > 20_000 {
		var ex []string
		for _, s := range spans {
			lo, hi := max(0, s.start-300), min(len(text), s.start+min(len(s.text), 300)+300)
			ex = append(ex, "…"+text[lo:hi]+"…")
		}
		doc = jev.Obj{{K: "excerpts", V: ex}}
	}
	var instr any = question
	if byID || o.about != "" {
		st := jev.Obj{}
		if o.about != "" {
			st = append(st, jev.KV{K: "about", V: o.about})
		}
		st = append(st, jev.KV{K: "document", V: doc})
		if byID {
			var cs []jev.Obj
			for i, s := range spans {
				cs = append(cs, jev.Obj{{K: "id", V: key(i)}, {K: "match", V: cli.Clip(s.text, 300)}})
			}
			st = append(st, jev.KV{K: "candidates", V: cs})
			instr = jev.Obj{{K: "question", V: question}, {K: "options", V: "the ids of `candidates`"}}
		}
		doc = st
	}
	ans := t.Ask(doc, []string{"pick"}, []jev.Question{jev.Choice(instr, opts)})["pick"]

	type scored struct {
		span
		p float64
	}
	var ranked []scored
	for i, s := range spans {
		ranked = append(ranked, scored{s, ans.Probabilities[key(i)]})
	}
	sort.SliceStable(ranked, func(a, b int) bool { return ranked[a].p > ranked[b].p })
	if ans.Choice == none && !o.force {
		t.Exit(cli.ExitNo)
	}
	out := t.Output()
	for _, r := range ranked[:min(len(ranked), o.top)] {
		line := strings.Count(text[:r.start], "\n") + 1
		writeCand(out, o, r.p, true, ':', line, r.text, "\n")
	}
	code := cli.ExitYes
	switch {
	case ans.Choice == none:
		code = cli.ExitNo // printed only because of --force
	case ans.Confidence < o.minConf:
		code = cli.ExitUncertain
	}
	t.Exit(code)
}
