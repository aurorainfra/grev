// Command tagv labels every record with one of the given labels, like
// awk '{print > $1}' where the model picks $1.
//
//	tagv billing tech sales < tickets | cut -f1 | sort | uniq -c
package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/aurorainfra/grev/internal/cli"
	"github.com/aurorainfra/grev/internal/jev"
)

type rec struct {
	num    int
	text   string
	label  string
	conf   float64
	ok     bool
	failed bool   // the model couldn't label it: printed with the "!" label
	beam   []path // --tree only
}

// failLabel marks records whose question failed, so output stays aligned
// with the input.
const failLabel = "!"

func main() {
	t := cli.New("tagv")
	p := t.P
	p.Synopsis = []string{
		"[OPTIONS] LABEL[=DESCRIPTION]... [< input]",
		"[OPTIONS] -f LABELFILE [< input]",
		"[OPTIONS] --tree TAXONOMY [< input]",
	}
	p.About = `Label every record of stdin (or -i FILE) with the LABEL that fits it best and
print "label<TAB>record". A label may carry a description after '=' to sharpen
its boundary. Records are printed verbatim, in input order; a record the model
couldn't label (request failed) is printed with the label "!" and exit status 2.

With --tree, labels come from an indented taxonomy file (one name per line,
optionally name=description, children indented under their parent). Each
record walks the tree level by level, keeping the -b best paths, and is
labelled with the full path, e.g. food/vegan/soups.`
	p.ExitStatus = `0 ok, 2 error, 4 declined at -Q or over --max-cost.`
	p.Examples = []string{
		`tagv billing tech sales < tickets.txt | cut -f1 | sort | uniq -c`,
		`tagv -s -t 0.6 bug feature question < issues.txt`,
		`tagv --split out/ --other positive negative < reviews.txt`,
		`tagv --only bug bug feature question < issues.txt`,
		`tagv --tree categories.txt -b 3 < products.txt`,
	}
	question := p.Str('q', "question", "QUESTION", "", "the question; {} is the record (default: which label fits it)")
	labelFile := p.Str('f', "labels", "FILE", "", "read labels from FILE, one 'label<TAB>description' per line")
	inputs := p.List('i', "input", "FILE", "read records from FILE instead of stdin (repeatable)")
	other := p.OptStr("other", "NAME", "other", "add an escape label (default 'other') for records that fit none")
	scores := p.Flag('s', "scores", "prefix each line with the label's confidence")
	threshold := p.Float('t', "threshold", "C", 0, "use the unsure label when confidence is below C")
	unsure := p.Str(0, "unsure", "NAME", "?", "label for records below -t")
	only := p.Str(0, "only", "L[,L...]", "", "print only the records labelled L, without the label column")
	splitDir := p.Str(0, "split", "DIR", "", "append each record to DIR/<label> instead of printing it")
	tree := p.Str(0, "tree", "FILE", "", "labels are paths in an indented taxonomy FILE")
	beamK := p.Int('b', "beam", "K", 3, "with --tree: keep the K best paths per record")
	window := p.Int('W', "window", "N", 0, "show the model N neighbouring records on each side (with --line-buffered, each record waits for N later ones)")
	refFile := p.Str('S', "reference", "FILE", "", "shared context for every question")
	about := p.Str(0, "about", "TEXT", "", "shared context, e.g. what the input is")
	delim := p.Str('d', "delimiter", "DELIM", "", "split records into fields {1}, {2}, … on DELIM")
	mode := cli.Records(p)
	lineBuf := p.Flag(0, "line-buffered", "stream: label and print records as they arrive")
	flush := p.Str(0, "flush", "DUR", "250ms", "with --line-buffered: send a partial request after DUR")
	args := t.Parse()

	var opts jev.Opts
	var root *node
	if *tree != "" {
		if len(args) > 0 || *labelFile != "" {
			p.Usagef("--tree takes its labels from the taxonomy file, not arguments")
		}
		if *lineBuf {
			p.Usagef("--tree can't stream (it asks one round per level)")
		}
		var err error
		if root, err = parseTree(*tree); err != nil {
			t.Fatalf("%v", err)
		}
	} else {
		if *labelFile != "" {
			more, err := cli.LabelsFile(*labelFile)
			if err != nil {
				t.Fatalf("%v", err)
			}
			args = append(args, more...)
		}
		if *other != nil {
			args = append(args, **other+"=none of the other options fits the record")
		}
		var err error
		if opts, err = cli.ParseLabels(args); err != nil {
			p.Usagef("%v", err)
		}
		if len(opts) < 2 {
			p.Usagef("need at least two labels")
		}
	}
	if *beamK < 1 {
		p.Usagef("-b wants a positive number")
	}
	flushDur, err := time.ParseDuration(*flush)
	if err != nil {
		p.Usagef("--flush: %v", err)
	}
	shared, err := cli.SharedState(*refFile, *about)
	if err != nil {
		t.Fatalf("%v", err)
	}
	st := jev.NewState(shared)

	maxField := 0
	if *delim != "" {
		maxField = -1
	}
	if err := cli.CheckPlaceholders(*question, true, maxField); err != nil {
		p.Usagef("-q: %v", err)
	}
	var q string
	switch s := strings.TrimSpace(*question); {
	case s == "":
		q = "Which option best fits `text`?"
	case cli.HasPlaceholder(s):
		q = cli.Rewrite(s)
	default:
		q = "Regarding `text`: " + s
	}
	instr := func(r *rec, bef, aft []string) jev.Obj {
		cr := cli.Rec{Text: r.text, Before: bef, After: aft}
		if *delim != "" {
			cr.Fields = strings.Split(r.text, cli.Unescape(*delim))
		}
		return cli.Instr(cr, q)
	}

	// Output.
	sep := mode().Sep()
	out := t.Output()
	out.SetLineBuffered(*lineBuf)
	onlySet := map[string]bool{}
	if *only != "" {
		for _, l := range strings.Split(*only, ",") {
			onlySet[strings.TrimSpace(l)] = true
		}
	}
	files := map[string]*os.File{}
	counts := map[string]int{}
	var order []string
	failed := 0
	var firstErr error
	write := func(r *rec) {
		label := r.label
		switch {
		case r.failed:
			label = failLabel
		case r.conf < *threshold:
			label = *unsure
		}
		if counts[label] == 0 {
			order = append(order, label)
		}
		counts[label]++
		if *splitDir != "" {
			if r.failed {
				return // counted and reported, but not routed anywhere
			}
			f := files[label]
			if f == nil {
				path := filepath.Join(*splitDir, fileName(label))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("%v", err)
				}
				if f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644); err != nil {
					t.Fatalf("%v", err)
				}
				files[label] = f
			}
			if _, err := f.WriteString(r.text + sep); err != nil {
				t.Fatalf("%v", err)
			}
			return
		}
		if len(onlySet) > 0 && !onlySet[label] {
			return
		}
		var b strings.Builder
		if *scores {
			if r.failed {
				b.WriteString("-\t")
			} else {
				fmt.Fprintf(&b, "%.2f\t", r.conf)
			}
		}
		if len(onlySet) == 0 {
			b.WriteString(label + "\t")
		}
		b.WriteString(r.text)
		b.WriteString(sep)
		out.WriteString(b.String())
	}
	fail := func(r *rec, err error) {
		failed++
		if firstErr == nil && err != nil {
			firstErr = err
		}
		r.failed = true
		write(r)
	}

	var runErr error
	if root != nil {
		recs := readAll(t, *inputs, mode())
		// A walk cut short (budget, interrupt, auth) leaves records without
		// labels: report and exit before writing anything.
		if runErr = walkTree(t, root, recs, st, instr, *beamK, *window); runErr != nil {
			t.Exit(t.Finish(runErr))
		}
		for _, r := range recs {
			if r.ok {
				write(r)
			} else {
				fail(r, nil)
			}
		}
	} else {
		mk := func(r *rec, bef, aft []string) *jev.Item {
			return jev.NewItem(st, jev.Choice(instr(r, bef, aft), opts), r)
		}
		emit := func(res jev.Result) {
			r := res.Item.Tag.(*rec)
			if res.Err != nil {
				fail(r, res.Err)
				return
			}
			r.label, r.conf, r.ok = res.Answer.Choice, res.Answer.Confidence, true
			write(r)
		}
		ctx, stop := context.WithCancel(t.Ctx())
		defer stop()
		if *lineBuf {
			in := make(chan *jev.Item, 64)
			go func() {
				defer close(in)
				err := produce(*inputs, mode(), *window, func(r *rec, bef, aft []string) bool {
					select {
					case in <- mk(r, bef, aft):
						return true
					case <-ctx.Done():
						return false
					}
				})
				if err != nil {
					t.Warnf("%v", err)
				}
			}()
			runErr = t.StreamItems(ctx, in, flushDur, emit)
		} else {
			var items []*jev.Item
			err := produce(*inputs, mode(), *window, func(r *rec, bef, aft []string) bool {
				items = append(items, mk(r, bef, aft))
				return true
			})
			if err != nil {
				t.Fatalf("%v", err)
			}
			runErr = t.RunItems(ctx, items, emit)
		}
	}

	for _, f := range files {
		if err := f.Close(); err != nil {
			t.Warnf("%v", err)
		}
	}
	if *splitDir != "" {
		for _, l := range order {
			t.Warnf("%s\t%d", l, counts[l])
		}
	}
	if runErr != nil {
		t.Exit(t.Finish(runErr))
	}
	if failed > 0 {
		if firstErr != nil {
			t.Warnf("%d record(s) failed: %v", failed, firstErr)
		} else {
			t.Warnf("%d record(s) failed", failed)
		}
		t.Exit(cli.ExitError)
	}
	t.Exit(cli.ExitYes)
}

// fileName maps a label to a relative path under --split (tree paths nest).
func fileName(label string) string {
	var parts []string
	for _, s := range strings.Split(label, "/") {
		s = strings.TrimSpace(s)
		if s == "" || s == "." || s == ".." {
			s = "_"
		}
		if runtime.GOOS == "windows" {
			s = windowsSafe(s)
		}
		parts = append(parts, s)
	}
	return filepath.Join(parts...)
}

// windowsSafe percent-encodes characters Windows forbids in file names
// (the unsure label "?" becomes "%3F").
func windowsSafe(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || strings.ContainsRune(`<>:"\|?*%`, r) {
			fmt.Fprintf(&b, "%%%02X", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// produce reads records from the inputs (stdin if none) and calls send with
// each record and its -W neighbours, stopping when send returns false.
func produce(inputs []string, m cli.RecMode, window int, send func(r *rec, bef, aft []string) bool) error {
	if len(inputs) == 0 {
		inputs = []string{"-"}
	}
	num := 0
	for _, name := range inputs {
		f, err := cli.Open(name)
		if err != nil {
			return err
		}
		var hist []string
		var pend []*rec
		ok := true
		flushOne := func() bool {
			r := pend[0]
			aft := make([]string, 0, window)
			for _, x := range pend[1:] {
				aft = append(aft, x.text)
			}
			ok := send(r, append([]string(nil), hist...), aft)
			hist = append(hist, r.text)
			if len(hist) > window {
				hist = hist[1:]
			}
			pend = pend[1:]
			return ok
		}
		err = cli.Scan(f, m, func(text string) bool {
			num++
			pend = append(pend, &rec{num: num, text: text})
			if len(pend) > window {
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

func readAll(t *cli.Tool, inputs []string, m cli.RecMode) []*rec {
	var recs []*rec
	if err := produce(inputs, m, 0, func(r *rec, _, _ []string) bool {
		recs = append(recs, r)
		return true
	}); err != nil {
		t.Fatalf("%v", err)
	}
	return recs
}

// ---- --tree ----

type node struct {
	name     string
	desc     string
	children []*node
}

// parseTree reads an indented taxonomy: one "name" or "name=description" per
// line, children indented deeper than their parent. '#' lines are comments.
func parseTree(path string) (*node, error) {
	text, err := cli.ReadInput(path)
	if err != nil {
		return nil, err
	}
	root := &node{}
	type frame struct {
		indent int
		n      *node
	}
	stack := []frame{{-1, root}}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := 0
		for _, c := range line[:len(line)-len(trimmed)] {
			if c == '\t' {
				indent += 4
			} else {
				indent++
			}
		}
		name, desc, _ := strings.Cut(trimmed, "=")
		n := &node{name: strings.TrimSpace(name), desc: strings.TrimSpace(desc)}
		if n.name == "" || strings.Contains(n.name, "/") {
			return nil, fmt.Errorf("%s:%d: bad name %q (empty or contains '/')", path, i+1, n.name)
		}
		for stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		parent := stack[len(stack)-1].n
		for _, c := range parent.children {
			if c.name == n.name {
				return nil, fmt.Errorf("%s:%d: duplicate name %q under the same parent", path, i+1, n.name)
			}
		}
		parent.children = append(parent.children, n)
		if len(parent.children) > 255 {
			return nil, fmt.Errorf("%s:%d: more than 255 children under one node", path, i+1)
		}
		stack = append(stack, frame{indent, n})
	}
	if len(root.children) == 0 {
		return nil, fmt.Errorf("%s: empty taxonomy", path)
	}
	return root, nil
}

// options describes a node's children for a Choice: each child's description
// and, for inner nodes, the names beneath it, so the model can see what a
// branch contains before committing to it.
func (n *node) options() jev.Opts {
	opts := make(jev.Opts, len(n.children))
	for i, c := range n.children {
		var names []string
		for _, g := range c.children {
			names = append(names, g.name)
			if len(names) == 20 {
				names = append(names, "…")
				break
			}
		}
		var d any
		switch {
		case c.desc != "" && names != nil:
			d = jev.Obj{{K: "description", V: c.desc}, {K: "includes", V: names}}
		case c.desc != "":
			d = c.desc
		case names != nil:
			d = jev.Obj{{K: "includes", V: names}}
		}
		opts[i] = jev.Opt{Key: c.name, Desc: d}
	}
	return opts
}

type path struct {
	nodes []*node // from the first level down; the root is implicit
	logp  float64 // log-probability of the model-chosen edges
	edges int     // model-chosen edges (single-child descents don't count)
}

func (p path) last(root *node) *node {
	if len(p.nodes) == 0 {
		return root
	}
	return p.nodes[len(p.nodes)-1]
}

// score is the geometric mean of the model-chosen edge probabilities, so
// shallow and deep leaves compare fairly and forced steps don't inflate it.
func (p path) score() float64 {
	switch {
	case len(p.nodes) == 0:
		return 0
	case p.edges == 0:
		return 1 // every step was forced
	}
	return math.Exp(p.logp / float64(p.edges))
}

func (p path) extend(c *node, prob float64) path {
	return path{nodes: append(append([]*node(nil), p.nodes...), c),
		logp: p.logp + math.Log(max(prob, 1e-9)), edges: p.edges + 1}
}

// descend follows single-child chains without asking the model; those steps
// are not model choices, so they don't count as edges.
func (p path) descend(root *node) path {
	for n := p.last(root); len(n.children) == 1; n = p.last(root) {
		p = path{nodes: append(append([]*node(nil), p.nodes...), n.children[0]), logp: p.logp, edges: p.edges}
	}
	return p
}

type step struct {
	r *rec
	i int // index into r.beam
}

// walkTree runs one round of Choices per level: every open path of every
// record asks which child fits, and each record keeps its K best paths.
func walkTree(t *cli.Tool, root *node, recs []*rec, st *jev.State,
	instr func(*rec, []string, []string) jev.Obj, k, window int) error {
	e := t.Engine()
	texts := make([]string, len(recs))
	for i, r := range recs {
		texts[i] = r.text
		r.beam = []path{path{}.descend(root)}
		r.ok = true
	}
	neighbours := func(i int) ([]string, []string) {
		if window == 0 {
			return nil, nil
		}
		return texts[max(0, i-window):i], texts[i+1 : min(len(texts), i+1+window)]
	}
	for round := 0; ; round++ {
		var items []*jev.Item
		for i, r := range recs {
			if !r.ok {
				continue
			}
			bef, aft := neighbours(i)
			for j, pth := range r.beam {
				n := pth.last(root)
				if len(n.children) == 0 {
					continue
				}
				items = append(items, jev.NewItem(st, jev.Choice(instr(r, bef, aft), n.options()), step{r, j}))
			}
		}
		if len(items) == 0 {
			break
		}
		answers := map[step]jev.Answer{}
		emit := func(res jev.Result) {
			s := res.Item.Tag.(step)
			if res.Err != nil {
				if s.r.ok {
					t.Warnf("record %d: %v", s.r.num, res.Err)
				}
				s.r.ok = false
				return
			}
			answers[s] = res.Answer
		}
		var err error
		if round == 0 {
			if t.Quoting() {
				t.Warnf("note: the quote covers the first level; each deeper level costs up to %d× as much", k)
			}
			err = t.RunItems(t.Ctx(), items, emit)
		} else {
			e.Plan(items)
			err = e.RunAll(t.Ctx(), items, emit)
		}
		if err != nil {
			return err
		}
		for _, r := range recs {
			if !r.ok {
				continue
			}
			var cand []path
			for j, pth := range r.beam {
				n := pth.last(root)
				if len(n.children) == 0 {
					cand = append(cand, pth)
					continue
				}
				a := answers[step{r, j}]
				for _, c := range n.children {
					if pr := a.Probabilities[c.name]; pr > 0 {
						cand = append(cand, pth.extend(c, pr).descend(root))
					}
				}
			}
			if len(cand) == 0 {
				r.ok = false
				continue
			}
			sort.SliceStable(cand, func(a, b int) bool { return cand[a].score() > cand[b].score() })
			if len(cand) > k {
				cand = cand[:k]
			}
			r.beam = cand
		}
	}
	for _, r := range recs {
		if !r.ok || len(r.beam) == 0 {
			r.ok = false
			continue
		}
		best := r.beam[0]
		names := make([]string, len(best.nodes))
		for i, n := range best.nodes {
			names[i] = n.name
		}
		r.label, r.conf = strings.Join(names, "/"), best.score()
	}
	return nil
}
