// Command trv is tr(1) for questions: it translates, deletes or squeezes
// characters, or replaces regex matches, but only where the model says the
// instruction applies to that occurrence.
//
//	trv "'" '"' 'is a quotation mark, not an apostrophe' < story.txt
package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aurorainfra/grev/internal/cli"
	"github.com/aurorainfra/grev/internal/jev"
)

// cand is one occurrence the model is asked about: a byte range of the input.
type cand struct {
	start, end int
	sub        []int // regex submatch offsets (for -r $1 expansion), else nil
	re         *regexp.Regexp
	p          float64
	choice     string
	asked      bool
}

const keep = "keep"

func main() {
	t := cli.New("trv")
	p := t.P
	p.Synopsis = []string{
		"[OPTIONS] SET1 SET2 INSTRUCTION",
		"[OPTIONS] -d|-s SET INSTRUCTION",
		"[OPTIONS] -e REGEX (-r TEXT | -o 'A|B|…' | -d) INSTRUCTION",
	}
	p.About = `Edit stdin like tr(1), but only where the model says INSTRUCTION applies to each
occurrence. Code finds the candidates: runs of SET characters, or matches of
-e REGEX. The model sees each one marked ⟦like this⟧ in its line and answers
whether the instruction applies there. Everything else is copied byte for byte,
and the model never writes text: replacements come from SET2, -r or -o.

  SET1 SET2   translate the chosen occurrences, character by character, as tr
  -d SET      delete the chosen runs of SET characters
  -s SET      squeeze the chosen runs of a repeated character to one (",," → ",")
  -e REGEX    act on regex matches instead: replace with -r TEXT ($1 works),
              choose a replacement per match with -o 'A|B|…', or delete with -d

SET is tr-style: characters, ranges (a-z), escapes (\n \t \\) and classes
([:punct:] [:space:] [:digit:] [:alpha:] [:alnum:] [:upper:] [:lower:]).
INSTRUCTION may name the occurrence as {}; otherwise it is read as a condition
("is a typo", "is used as a quotation mark") or an instruction ("remove
doubled commas") about the marked text.`
	p.ExitStatus = `0 something changed, 1 nothing changed, 2 error, 4 declined at -Q or over a
budget.`
	p.Examples = []string{
		`trv "'" '"' 'is used as a quotation mark, not an apostrophe' < story.txt`,
		`trv -s , 'the repeated comma is a typo, not an empty field' < notes.txt`,
		`trv -e '\bSt\.' -o 'Saint|Street' 'what St. abbreviates here' < addresses.txt`,
		`trv -e '\bcolou?r\b' -r colour 'is written in British English prose' < doc.txt`,
		`trv -d -e ' ?\(sic\)' 'is an editorial note that can go' < quotes.txt`,
	}
	del := p.Flag('d', "delete", "delete the chosen occurrences")
	squeeze := p.Flag('s', "squeeze", "squeeze the chosen runs of a repeated character to one")
	regexes := p.List('e', "regexp", "REGEX", "act on matches of REGEX (repeatable) instead of SET characters")
	repl := p.Str('r', "replace", "TEXT", "", "with -e: replace chosen matches with TEXT ($1, ${name} expand)")
	options := p.Str('o', "options", "A|B|…", "", "with -e: let the model pick a replacement per match (or keep it)")
	threshold := p.Float('t', "threshold", "P", 0.5, "act when P(yes) ≥ P; with -o, the minimum confidence")
	input := p.Str('i', "input", "FILE", "", "read FILE instead of stdin")
	window := p.Int('W', "window", "N", 0, "show the model N neighbouring lines on each side")
	width := p.Int(0, "context", "CHARS", 160, "characters of the line shown on each side of an occurrence")
	about := p.Str(0, "about", "TEXT", "", "shared context, e.g. what the text is")
	refFile := p.Str('S', "reference", "FILE", "", "shared context for every question (e.g. a style guide)")
	yes := p.Str(0, "yes", "TEXT", "", "describe when the instruction applies")
	no := p.Str(0, "no", "TEXT", "", "describe when it doesn't")
	trace := p.Flag(0, "trace", "show every occurrence, its answer and the decision on stderr")

	args := t.Parse()
	regexMode := len(*regexes) > 0
	var set1, set2 []rune
	var instruction string
	switch {
	case regexMode:
		if len(args) != 1 {
			p.Usagef("with -e, give just the INSTRUCTION")
		}
		instruction = args[0]
		n := 0
		for _, b := range []bool{*del, *repl != "", *options != ""} {
			if b {
				n++
			}
		}
		if n != 1 || *squeeze {
			p.Usagef("with -e, choose exactly one of -d, -r TEXT or -o 'A|B|…'")
		}
	case *del || *squeeze:
		if *del && *squeeze {
			p.Usagef("choose -d or -s")
		}
		if len(args) != 2 {
			p.Usagef("want -d|-s SET INSTRUCTION")
		}
		instruction = args[1]
	default:
		if len(args) != 3 {
			p.Usagef("want SET1 SET2 INSTRUCTION, -d|-s SET INSTRUCTION, or -e REGEX … INSTRUCTION")
		}
		instruction = args[2]
	}
	if !regexMode {
		if *repl != "" || *options != "" {
			p.Usagef("-r and -o need -e REGEX")
		}
		var err error
		if set1, err = parseSet(args[0]); err != nil {
			p.Usagef("SET1: %v", err)
		}
		if !*del && !*squeeze {
			if set2, err = parseSet(args[1]); err != nil {
				p.Usagef("SET2: %v", err)
			}
			if len(set2) == 0 {
				p.Usagef("SET2 is empty")
			}
		}
	}
	if err := cli.CheckPlaceholders(instruction, true, 0); err != nil {
		p.Usagef("%v", err)
	}
	var res []*regexp.Regexp
	for _, r := range *regexes {
		re, err := regexp.Compile(r)
		if err != nil {
			p.Usagef("-e %q: %v", r, err)
		}
		res = append(res, re)
	}
	var choices []string
	if *options != "" {
		for _, o := range strings.Split(*options, "|") {
			if o == "" || o == keep {
				p.Usagef("-o: options must be non-empty and not %q", keep)
			}
			choices = append(choices, o)
		}
	}

	var r io.Reader = os.Stdin
	if *input != "" {
		f, err := os.Open(*input)
		if err != nil {
			t.Fatalf("%v", err)
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("%v", err)
	}
	text := string(b)

	// Find the candidates in code.
	var cands []*cand
	if regexMode {
		cands = regexCands(text, res)
	} else {
		cands = setCands(text, set1, *squeeze)
	}
	out := t.Output()
	if len(cands) == 0 {
		out.WriteString(text)
		t.Exit(cli.ExitNo)
	}

	state, err := cli.SharedState(*refFile, *about)
	if err != nil {
		t.Fatalf("%v", err)
	}
	st := jev.NewState(state)
	question := "Does `instruction` apply to the occurrence marked ⟦like this⟧ in `text`?"
	if cli.HasPlaceholder(instruction) {
		question = strings.ReplaceAll(cli.Rewrite(instruction), "`text`", "`marked`")
	}
	var yesC, noC any
	if *yes != "" {
		yesC = *yes
	}
	if *no != "" {
		noC = *no
	}
	// Key order matters: with the instruction first and the text last, the
	// model separated apostrophes from quote marks and typos from CSV fields
	// far better than with the text first (see docs/DESIGN.md).
	items := make([]*jev.Item, len(cands))
	for i, c := range cands {
		instr := jev.Obj{
			{K: "instruction", V: instruction},
			{K: "marked", V: text[c.start:c.end]},
			{K: "question", V: question},
		}
		if *window > 0 {
			instr = append(instr, jev.KV{K: "before", V: neighbours(text, c.start, -*window)})
		}
		instr = append(instr, jev.KV{K: "text", V: marked(text, c.start, c.end, *width)})
		if *window > 0 {
			instr = append(instr, jev.KV{K: "after", V: neighbours(text, c.end, *window)})
		}
		var q jev.Question
		if choices != nil {
			instr[2] = jev.KV{K: "question",
				V: "Which option should replace the occurrence marked ⟦like this⟧ in `text`, judging by `instruction`?"}
			opts := jev.Opts{{Key: keep, Desc: "none of the options fits; leave the marked text as it is"}}
			for _, o := range choices {
				opts = append(opts, jev.Opt{Key: o, Desc: fmt.Sprintf("replace the marked text with %q", o)})
			}
			q = jev.Choice(instr, opts)
		} else {
			q = jev.Noul(instr, yesC, noC)
		}
		items[i] = jev.NewItem(st, q, i)
	}
	var firstErr error
	failed := 0
	runErr := t.RunItems(t.Ctx(), items, func(res jev.Result) {
		c := cands[res.Item.Tag.(int)]
		if res.Err != nil {
			failed++
			if firstErr == nil {
				firstErr = res.Err
			}
			return
		}
		c.asked = true
		if choices != nil {
			c.choice, c.p = res.Answer.Choice, res.Answer.Confidence
		} else {
			c.p = res.Answer.Noul
		}
	})
	if runErr != nil {
		t.Exit(t.Finish(runErr))
	}

	// Apply the chosen edits; everything else is copied as is.
	trans := map[rune]rune{}
	for i, ch := range set1 {
		if set2 != nil {
			trans[ch] = set2[min(i, len(set2)-1)]
		}
	}
	var sb strings.Builder
	last, changed := 0, 0
	for _, c := range cands {
		act := c.asked && c.p >= *threshold
		if choices != nil {
			act = act && c.choice != keep
		}
		if *trace {
			line, col := position(text, c.start)
			verdict := "kept"
			if act {
				verdict = "changed"
			}
			ans := fmt.Sprintf("p=%.2f", c.p)
			if choices != nil {
				ans = fmt.Sprintf("%s (conf %.2f)", c.choice, c.p)
			}
			t.Warnf("%d:%d ⟦%s⟧ %s → %s", line, col, cli.Clip(text[c.start:c.end], 40), ans, verdict)
		}
		if !act {
			continue
		}
		sb.WriteString(text[last:c.start])
		occ := text[c.start:c.end]
		before := sb.Len()
		switch {
		case *del:
		case *squeeze:
			r, _ := utf8.DecodeRuneInString(occ)
			sb.WriteRune(r)
		case choices != nil:
			sb.WriteString(c.choice)
		case *repl != "":
			sb.Write(c.re.ExpandString(nil, *repl, text, c.sub))
		default:
			for _, r := range occ {
				if m, ok := trans[r]; ok {
					sb.WriteRune(m)
				} else {
					sb.WriteRune(r)
				}
			}
		}
		last = c.end
		if sb.String()[before:] != occ { // a replacement equal to the original is no change
			changed++
		}
	}
	sb.WriteString(text[last:])
	out.WriteString(sb.String())
	if failed > 0 {
		t.Warnf("%d occurrence(s) left unchanged because their question failed: %v", failed, firstErr)
		t.Exit(cli.ExitError)
	}
	if changed == 0 {
		t.Exit(cli.ExitNo)
	}
	t.Exit(cli.ExitYes)
}

// setCands finds maximal runs of SET characters (for -s: runs of one
// repeated character, at least two long).
func setCands(text string, set []rune, squeeze bool) []*cand {
	in := map[rune]bool{}
	for _, r := range set {
		in[r] = true
	}
	var out []*cand
	for i := 0; i < len(text); {
		r, n := utf8.DecodeRuneInString(text[i:])
		if !in[r] {
			i += n
			continue
		}
		j := i + n
		for j < len(text) {
			r2, n2 := utf8.DecodeRuneInString(text[j:])
			if !in[r2] || (squeeze && r2 != r) {
				break
			}
			j += n2
		}
		if !squeeze || utf8.RuneCountInString(text[i:j]) >= 2 {
			out = append(out, &cand{start: i, end: j})
		}
		i = j
	}
	return out
}

// regexCands finds non-overlapping matches of all regexes, leftmost first.
func regexCands(text string, res []*regexp.Regexp) []*cand {
	var all []*cand
	for _, re := range res {
		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			if m[1] > m[0] {
				all = append(all, &cand{start: m[0], end: m[1], sub: m, re: re})
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].start != all[j].start {
			return all[i].start < all[j].start
		}
		return all[i].end > all[j].end
	})
	var out []*cand
	end := -1
	for _, c := range all {
		if c.start >= end {
			out = append(out, c)
			end = c.end
		}
	}
	return out
}

// marked is the line around [start,end) with the occurrence in ⟦ ⟧, clipped
// to width characters on each side.
func marked(text string, start, end, width int) string {
	ls := strings.LastIndexByte(text[:start], '\n') + 1
	le := strings.IndexByte(text[end:], '\n')
	if le < 0 {
		le = len(text)
	} else {
		le += end
	}
	before, after := []rune(text[ls:start]), []rune(text[end:le])
	pre, post := "", ""
	if len(before) > width {
		before, pre = before[len(before)-width:], "…"
	}
	if len(after) > width {
		after, post = after[:width], "…"
	}
	return pre + string(before) + "⟦" + text[start:end] + "⟧" + string(after) + post
}

// neighbours returns up to n whole lines before (n < 0) or after offset.
func neighbours(text string, off, n int) []string {
	if n < 0 {
		ls := strings.LastIndexByte(text[:off], '\n')
		if ls < 0 {
			return nil
		}
		lines := strings.Split(text[:ls], "\n")
		return lines[max(0, len(lines)+n):]
	}
	le := strings.IndexByte(text[off:], '\n')
	if le < 0 {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(text[off+le+1:], "\n"), "\n")
	return lines[:min(len(lines), n)]
}

func position(text string, off int) (line, col int) {
	line = strings.Count(text[:off], "\n") + 1
	col = utf8.RuneCountInString(text[strings.LastIndexByte(text[:off], '\n')+1:off]) + 1
	return
}

// parseSet expands a tr-style SET: characters, ranges, escapes, [:classes:].
func parseSet(s string) ([]rune, error) {
	var out []rune
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == '[' && i+1 < len(rs) && rs[i+1] == ':' {
			end := strings.Index(string(rs[i:]), ":]")
			if end < 0 {
				return nil, fmt.Errorf("unterminated class in %q", s)
			}
			name := string(rs[i+2 : i+end])
			cls, ok := classes[name]
			if !ok {
				return nil, fmt.Errorf("unknown class [:%s:]", name)
			}
			for r := rune(0); r < 128; r++ {
				if cls(r) {
					out = append(out, r)
				}
			}
			i += end + 1
			continue
		}
		r := rs[i]
		if r == '\\' && i+1 < len(rs) {
			i++
			switch rs[i] {
			case 'n':
				r = '\n'
			case 't':
				r = '\t'
			case 'r':
				r = '\r'
			default:
				r = rs[i]
			}
		}
		if i+2 < len(rs) && rs[i+1] == '-' {
			hi := rs[i+2]
			if hi < r {
				return nil, fmt.Errorf("reversed range %c-%c", r, hi)
			}
			for c := r; c <= hi; c++ {
				out = append(out, c)
			}
			i += 2
			continue
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty set")
	}
	return out, nil
}

var classes = map[string]func(rune) bool{
	"punct": func(r rune) bool { return r > 32 && r < 127 && !unicode.IsLetter(r) && !unicode.IsDigit(r) },
	"space": unicode.IsSpace,
	"blank": func(r rune) bool { return r == ' ' || r == '\t' },
	"digit": unicode.IsDigit,
	"alpha": unicode.IsLetter,
	"alnum": func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) },
	"upper": unicode.IsUpper,
	"lower": unicode.IsLower,
}
