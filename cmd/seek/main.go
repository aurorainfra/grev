// Command seek is find(1)/cd by description: it walks a directory tree one
// level at a time, asking at each level which entry leads to what QUERY
// describes, and prints the best matching path.
//
//	cd "$(seek -d 'stripe webhook handlers')"
package main

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/aurorainfra/grev/internal/cli"
	"github.com/aurorainfra/grev/internal/jev"
)

const (
	hereKey   = "."
	noneKey   = "(none)"
	maxRounds = 64
	maxNodes  = 300_000
	maxOpts   = 255
)

type node struct {
	name  string // display name; may be "a/b/c" after chain compression
	rel   string // path relative to the root ("" for the root)
	abs   bool   // from a path list: the path was absolute (/etc/…)
	dir   bool
	kids  []*node
	files int // files in the subtree
}

type cand struct {
	n     *node
	logp  float64
	edges int
	done  bool
}

// score is the geometric mean of the edge probabilities on the path, so
// shallow and deep answers compare fairly.
func (c cand) score() float64 {
	if c.edges == 0 {
		return 1
	}
	return math.Exp(c.logp / float64(c.edges))
}

type opts struct {
	all, dirsOnly, filesOnly bool
	sample, peek             int
	root                     string // filesystem root, or "" for a path list
	backslash                bool   // the path list used \ separators (Windows): print them back that way
}

// Budgets, in estimated tokens per Choice, for the file heads (--peek) and
// the sampled names of subdirectories, so the question stays well inside the
// model's 32k-token context however many entries a directory holds.
const (
	peekBudget  = 10_000
	namesBudget = 10_000
)

func main() {
	t := cli.New("seek")
	p := t.P
	p.Synopsis = []string{"[OPTIONS] QUERY [DIR]", "[OPTIONS] QUERY - < PATHS"}
	p.About = `Find the file or directory under DIR (default .) that QUERY describes, by
walking the tree: at each level the model picks which entry leads there, keeping
the -b best paths (beam search). A path's score is the geometric mean of its
step probabilities. With '-' the tree is built from a list of paths on stdin,
e.g. from git ls-files. Hidden entries, .git and node_modules are skipped unless
-a is given.

While walking, the model sees names only (plus the first lines of files with
--peek). When the query is about what code or text does rather than what it is
called, add --verify: the best candidates are re-checked against their content
in one more request, and ranked by that answer.

Chains of directories that each hold a single directory are one step (a/b/c/),
except with -d, where every directory on the way can be the answer.

Each level is one request, so a search costs about one request per directory
level. -Q quotes the first request only; later levels are similar in size.
Directories with more than 254 entries are judged entry by entry instead (one
yes/no question each).`
	p.ExitStatus = `0 if a path scored at least -t, 1 if none did, 2 on error,
4 if declined at -Q or over --max-cost.`
	p.Examples = []string{
		`cd "$(seek -d 'stripe webhook handlers')"`,
		`$EDITOR "$(git ls-files | seek - 'where retries are configured')"`,
		`seek -m3 -s --verify 'the HTTP server implementation' "$(go env GOROOT)/src"`,
	}
	beamW := p.Int('b', "beam", "K", 3, "keep the K best paths at each level (default 3)")
	maxOut := p.Int('m', "max-count", "N", 1, "print the N best paths (default 1)")
	scores := p.Flag('s', "scores", "prefix each path with its score")
	threshold := p.Float('t', "threshold", "S", 0.15, "print only paths scoring at least S (default 0.15; 0.5 with --verify)")
	dirsOnly := p.Flag('d', "dirs", "find directories only")
	filesOnly := p.Flag('f', "files", "find files only")
	all := p.Flag('a', "all", "include hidden entries, .git and node_modules")
	sample := p.Int(0, "sample", "N", 8, "describe each directory by N names found beneath it (default 8)")
	peek := p.Int(0, "peek", "N", 0, "show the model the first N lines of candidate files")
	verify := p.Flag(0, "verify", "re-check the best paths against their content and re-rank")
	about := p.Str(0, "about", "TEXT", "", "context about the tree, e.g. what the project is")
	args := t.Parse()
	if len(args) < 1 || len(args) > 2 {
		p.Usagef("want QUERY [DIR|-]")
	}
	if *dirsOnly && *filesOnly {
		p.Usagef("-d and -f are exclusive")
	}
	if *beamW < 1 || *maxOut < 1 {
		p.Usagef("-b and -m must be at least 1")
	}
	if *verify && !p.Seen("threshold") {
		*threshold = 0.5
	}
	query := args[0]
	dir := "."
	if len(args) == 2 {
		dir = args[1]
	}
	o := opts{all: *all, dirsOnly: *dirsOnly, filesOnly: *filesOnly, sample: *sample, peek: *peek}

	var root *node
	var err error
	if dir == "-" {
		root, o.backslash, err = fromList(os.Stdin)
	} else {
		o.root = dir
		root, err = fromDir(dir, *all)
	}
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !*dirsOnly {
		compress(root)
	}
	if len(root.kids) == 0 {
		t.Fatalf("nothing to search in %s", dir)
	}

	e := t.Engine()
	sv := jev.Obj{{K: "looking_for", V: query}}
	if *about != "" {
		sv = append(sv, jev.KV{K: "about", V: *about})
	}
	state := jev.NewState(sv)
	beam := []cand{{n: root}}
	var finished []cand
	confirmed := false
	run := func(items []*jev.Item, emit func(jev.Result)) {
		var runErr, firstErr error
		collect := func(r jev.Result) {
			if r.Err != nil {
				if firstErr == nil {
					firstErr = r.Err
				}
				return
			}
			emit(r)
		}
		if !confirmed {
			confirmed = true
			runErr = t.RunItems(t.Ctx(), items, collect)
		} else {
			e.Plan(items)
			runErr = e.RunAll(t.Ctx(), items, collect)
		}
		if runErr == nil {
			runErr = firstErr
		}
		if runErr != nil {
			t.Exit(t.Finish(runErr))
		}
	}

	for round := 0; round < maxRounds; round++ {
		var open []int
		for i, c := range beam {
			if !c.done {
				open = append(open, i)
			}
		}
		if len(open) == 0 {
			break
		}
		// Probabilities per open beam entry, per option key. Choice answers
		// are a distribution; for directories too big for one Choice, each
		// entry gets its own yes/no probability (raw: normalizing independent
		// answers would dilute the right entry among hundreds of others).
		probs := make(map[int]map[string]float64)
		raw := make(map[int]bool)
		kidsOf := make(map[int]map[string]*node)
		var items []*jev.Item
		type tag struct {
			beam int
			key  string // set for per-entry Nouls (big directories)
		}
		for _, bi := range open {
			c := beam[bi]
			keys, kids, question := o.options(c.n, c.n == root)
			kidsOf[bi] = kids
			switch {
			case len(keys) == 0:
				probs[bi] = map[string]float64{}
			case len(keys) <= maxOpts:
				items = append(items, jev.NewItem(state, jev.Choice(question, keys), tag{beam: bi}))
			default:
				raw[bi] = true
				for _, k := range keys {
					var q jev.Obj
					switch k.Key {
					case noneKey:
						continue // computed in code: 1 − the best entry
					case hereKey:
						q = jev.Obj{
							{K: "directory", V: displayDir(c.n)},
							{K: "question", V: "Is `directory` itself what `looking_for` describes?"},
						}
					default:
						q = jev.Obj{
							{K: "directory", V: displayDir(c.n)},
							{K: "entry", V: k.Key},
							{K: "entry_details", V: k.Desc},
							{K: "question", V: "Is `entry` in `directory` what `looking_for` describes, or does it lead there?"},
						}
					}
					items = append(items, jev.NewItem(state, jev.Noul(q, nil, nil), tag{beam: bi, key: k.Key}))
				}
			}
		}
		if len(items) > 0 {
			run(items, func(r jev.Result) {
				tg := r.Item.Tag.(tag)
				if probs[tg.beam] == nil {
					probs[tg.beam] = map[string]float64{}
				}
				if tg.key == "" {
					for k, v := range r.Answer.Probabilities {
						probs[tg.beam][k] = v
					}
				} else {
					probs[tg.beam][tg.key] = r.Answer.Noul
				}
			})
		}

		var next []cand
		for i, c := range beam {
			if c.done {
				next = append(next, c)
				continue
			}
			ps := probs[i]
			if len(ps) == 0 { // nothing below: the entry is its own answer
				c.done = true
				next = append(next, c)
				continue
			}
			if raw[i] {
				best := 0.0
				for _, v := range ps {
					best = max(best, v)
				}
				ps[noneKey] = 1 - best // for the trace; "none" is a dead end anyway
			} else {
				normalize(ps)
			}
			if debug {
				fmt.Fprintf(os.Stderr, "seek: %s (score %.2f):", displayDir(c.n), c.score())
				for _, kv := range top(ps, 6) {
					fmt.Fprintf(os.Stderr, " %s=%.2f", kv.k, kv.v)
				}
				fmt.Fprintln(os.Stderr)
			}
			for key, pv := range ps {
				if pv < 0.005 {
					continue
				}
				nc := cand{n: c.n, logp: c.logp + math.Log(pv), edges: c.edges + 1}
				if key == noneKey {
					continue // dead end
				}
				if key == hereKey {
					nc.done = true
				} else if kid := kidsOf[i][key]; kid != nil {
					nc.n = kid
					nc.done = !kid.dir
				} else {
					continue
				}
				next = append(next, nc)
			}
		}
		sort.SliceStable(next, func(a, b int) bool { return next[a].score() > next[b].score() })
		for _, c := range next {
			if c.done && !contains(finished, c) {
				finished = append(finished, c)
			}
		}
		if len(next) > *beamW {
			next = next[:*beamW]
		}
		beam = next
	}
	for _, c := range beam { // rounds exhausted: keep what we have
		if !contains(finished, c) {
			finished = append(finished, c)
		}
	}
	sort.SliceStable(finished, func(a, b int) bool { return finished[a].score() > finished[b].score() })
	finished = filter(finished, o)

	type result struct {
		c     cand
		score float64
	}
	var results []result
	for _, c := range finished {
		results = append(results, result{c, c.score()})
	}
	if *verify && len(results) > 0 {
		n := min(len(results), max(*maxOut, 2**beamW))
		results = results[:n]
		var items []*jev.Item
		for i, r := range results {
			q := jev.Obj{{K: "path", V: displayDir(r.c.n)}}
			if r.c.n.dir {
				q = append(q, jev.KV{K: "contains", V: sampleNames(r.c.n, max(o.sample, 12), namesBudget)})
			} else {
				q = append(q, jev.KV{K: "head", V: o.head(r.c.n, max(o.peek, 40), 8000)})
			}
			q = append(q, jev.KV{K: "question", V: "Is `path` what `looking_for` describes, or does it implement or contain it?"})
			items = append(items, jev.NewItem(state, jev.Noul(q, nil, nil), i))
		}
		run(items, func(r jev.Result) {
			results[r.Item.Tag.(int)].score = r.Answer.Noul
		})
		sort.SliceStable(results, func(a, b int) bool { return results[a].score > results[b].score })
	}

	out := t.Output()
	printed := 0
	for _, r := range results {
		if printed >= *maxOut || r.score < *threshold {
			break
		}
		if *scores {
			fmt.Fprintf(out, "%.2f\t", r.score)
		}
		fmt.Fprintln(out, o.outPath(r.c.n))
		printed++
	}
	if printed == 0 {
		if len(results) > 0 {
			t.Warnf("best match %s scored %.2f, below -t %.2f", o.outPath(results[0].c.n), results[0].score, *threshold)
		}
		t.Exit(cli.ExitNo)
	}
	t.Exit(cli.ExitYes)
}

var debug = slices.Contains(strings.Split(os.Getenv("GREV_DEBUG"), ","), "seek")

type kv struct {
	k string
	v float64
}

func top(ps map[string]float64, n int) []kv {
	var out []kv
	for k, v := range ps {
		out = append(out, kv{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].v > out[j].v })
	return out[:min(n, len(out))]
}

func contains(cs []cand, c cand) bool {
	for _, x := range cs {
		if x.n == c.n && x.done == c.done {
			return true
		}
	}
	return false
}

// filter drops answers of the wrong kind (-d/-f) and keeps the best-scoring
// candidate per path.
func filter(cs []cand, o opts) []cand {
	seen := map[*node]bool{}
	var out []cand
	for _, c := range cs {
		if seen[c.n] || (o.dirsOnly && !c.n.dir) || (o.filesOnly && c.n.dir) {
			continue
		}
		seen[c.n] = true
		out = append(out, c)
	}
	return out
}

func normalize(ps map[string]float64) {
	sum := 0.0
	for _, v := range ps {
		sum += v
	}
	if sum <= 0 {
		return
	}
	for k, v := range ps {
		ps[k] = v / sum
	}
}

// options builds the Choice options for directory n: its eligible entries
// plus "." for the directory itself (not at the root, not with -f).
func (o opts) options(n *node, isRoot bool) (jev.Opts, map[string]*node, jev.Obj) {
	var eligible []*node
	for _, k := range n.kids {
		switch {
		case o.dirsOnly && !k.dir:
		case o.filesOnly && k.dir && k.files == 0:
		default:
			eligible = append(eligible, k)
		}
	}
	kids := map[string]*node{}
	var keys jev.Opts
	if len(eligible) == 0 {
		return nil, kids, nil
	}
	per := min(o.sample, max(2, 3000/len(eligible)))
	files := 0
	for _, k := range eligible {
		if !k.dir {
			files++
		}
	}
	dirs := len(eligible) - files
	peekTok := 0 // per file
	if o.peek > 0 && files > 0 {
		peekTok = peekBudget / files
	}
	for _, k := range eligible {
		key := k.name
		if k.dir {
			key += "/"
		}
		kids[key] = k
		var desc any
		if k.dir {
			desc = jev.Obj{{K: "directory_containing", V: sampleNames(k, per, namesBudget/dirs)}}
		} else if peekTok >= 10 {
			desc = jev.Obj{{K: "file_starting_with", V: o.head(k, o.peek, peekTok)}}
		}
		keys = append(keys, jev.Opt{Key: key, Desc: desc})
	}
	if !isRoot && !o.filesOnly {
		keys = append(keys, jev.Opt{Key: hereKey, Desc: "this directory itself is what is looked for"})
	}
	// Choice is relative: without a way to say "nothing here", the mass of a
	// wrong turn (or of a query the tree can't answer) lands on the least bad
	// entry.
	keys = append(keys, jev.Opt{Key: noneKey, Desc: "nothing in this directory matches"})
	q := jev.Obj{
		{K: "directory", V: displayDir(n)},
		{K: "question", V: "Which entry of `directory` is, or leads to, what `looking_for` describes?"},
	}
	return keys, kids, q
}

// path is n's path as the user gave it: relative to the root, or absolute
// when a path list had absolute paths.
func (n *node) path() string {
	if n.abs {
		return "/" + n.rel
	}
	return n.rel
}

func displayDir(n *node) string {
	if n.rel == "" {
		return "./"
	}
	if n.dir {
		return n.path() + "/"
	}
	return n.path()
}

func (o opts) outPath(n *node) string {
	if o.root == "" {
		if n.rel == "" {
			return "."
		}
		if o.backslash {
			return filepath.FromSlash(n.path())
		}
		return n.path()
	}
	return filepath.Join(o.root, filepath.FromSlash(n.rel))
}

// head returns the first n lines of file node k, each clipped to 120
// characters and all together to about maxBytes.
func (o opts) head(k *node, n, maxTok int) []string {
	f, err := os.Open(o.outPath(k))
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	size := 0.0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for len(lines) < n && size < float64(maxTok) && sc.Scan() {
		l := cli.Clip(sc.Text(), 120)
		if left := float64(maxTok) - size; jev.Tokens(l) > left {
			l = l[:jev.Fit(l, int(left))] + "…"
		}
		lines = append(lines, l)
		size += jev.Tokens(l) + 3 // quotes and comma
	}
	return lines
}

// sampleNames lists up to n names (about maxTok tokens) beneath directory d,
// breadth first, as paths relative to d (directories end in "/").
func sampleNames(d *node, n, maxTok int) []string {
	var out []string
	size := 0.0
	type item struct {
		n      *node
		prefix string
	}
	queue := []item{{d, ""}}
	for len(queue) > 0 && len(out) < n {
		it := queue[0]
		queue = queue[1:]
		for _, k := range it.n.kids {
			name := it.prefix + k.name
			if k.dir {
				name += "/"
			}
			if len(out) >= n || len(out) > 0 && size+jev.Tokens(name) > float64(maxTok) {
				return out
			}
			if k.dir {
				queue = append(queue, item{k, name})
			}
			out = append(out, name)
			size += jev.Tokens(name) + 3
		}
	}
	return out
}

// fromDir walks root, skipping hidden entries, .git and node_modules unless
// all is set.
func fromDir(root string, all bool) (*node, error) {
	fi, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}
	count := 0
	var walk func(n *node, dir string)
	walk = func(n *node, dir string) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, de := range ents {
			name := de.Name()
			if !all && (strings.HasPrefix(name, ".") || name == "node_modules") {
				continue
			}
			if count >= maxNodes {
				return
			}
			count++
			isDir := de.IsDir()
			if de.Type()&os.ModeSymlink != 0 {
				if st, err := os.Stat(filepath.Join(dir, name)); err == nil && st.IsDir() {
					continue // don't follow directory symlinks (cycles)
				}
			}
			k := &node{name: name, rel: path.Join(n.rel, name), dir: isDir}
			if isDir {
				walk(k, filepath.Join(dir, name))
			} else {
				k.files = 1
			}
			n.files += k.files
			n.kids = append(n.kids, k)
		}
	}
	r := &node{dir: true}
	walk(r, root)
	return r, nil
}

// fromList builds a tree from newline-separated paths. On Windows, paths
// with \ separators (e.g. from `dir /s /b`) are read as such, and backslash
// reports it so results print the same way.
func fromList(f *os.File) (_ *node, backslash bool, _ error) {
	root := &node{dir: true}
	index := map[string]*node{"": root}
	var get func(rel string) *node
	get = func(rel string) *node {
		if n, ok := index[rel]; ok {
			return n
		}
		parent := get(path.Dir(rel))
		if parent == root && path.Dir(rel) == "." {
			parent = root
		}
		n := &node{name: path.Base(rel), rel: rel, dir: true, abs: parent.abs}
		parent.dir = true
		parent.kids = append(parent.kids, n)
		index[rel] = n
		return n
	}
	index["."] = root
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if runtime.GOOS == "windows" && strings.Contains(line, `\`) {
			line = filepath.ToSlash(line)
			backslash = true
		}
		isDir := strings.HasSuffix(line, "/")
		rel := path.Clean(strings.TrimPrefix(line, "./"))
		if line == "" || rel == "." || rel == "/" || strings.HasPrefix(rel, "../") || rel == ".." {
			continue
		}
		// Absolute paths share the tree with relative ones (without their
		// leading "/"), but print back as they came.
		abs := strings.HasPrefix(rel, "/")
		rel = strings.TrimPrefix(rel, "/")
		if abs {
			top := strings.SplitN(rel, "/", 2)[0]
			if _, seen := index[top]; !seen {
				index[top] = &node{name: top, rel: top, dir: true, abs: true}
				root.kids = append(root.kids, index[top])
			}
		}
		n := get(rel)
		if !isDir && len(n.kids) == 0 {
			n.dir = false
		}
	}
	if err := sc.Err(); err != nil {
		return nil, false, err
	}
	var count func(n *node) int
	count = func(n *node) int {
		if len(n.kids) > 0 {
			n.dir = true
		}
		if !n.dir {
			n.files = 1
			return 1
		}
		n.files = 0
		sort.Slice(n.kids, func(i, j int) bool { return n.kids[i].name < n.kids[j].name })
		for _, k := range n.kids {
			n.files += count(k)
		}
		return n.files
	}
	count(root)
	return root, backslash, nil
}

// compress merges chains of directories that hold a single directory, so
// a/b/c/ is one step instead of three.
func compress(n *node) {
	for _, k := range n.kids {
		for k.dir && len(k.kids) == 1 && k.kids[0].dir {
			only := k.kids[0]
			k.name += "/" + only.name
			k.rel = only.rel
			k.kids = only.kids
		}
		compress(k)
	}
}
