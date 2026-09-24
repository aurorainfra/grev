//go:build eval

// Package eval measures how grev should phrase and pack its questions, on
// labelled fixtures, against the live API. Run with `make eval`; it prints a
// table per variant: accuracy at 0.5, best threshold and its accuracy, AUC,
// mean P(yes) for positives/negatives, and tokens.
package eval

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aurorainfra/grev/internal/cli"
	"github.com/aurorainfra/grev/internal/config"
	"github.com/aurorainfra/grev/internal/jev"
)

type dataset struct {
	name     string
	query    string // predicate form: "is a vegan meal"
	question string // natural form with {}: "Is {} a vegan dish?"
	about    string // what the records are, as --about would say
	texts    []string
	labels   []bool
}

func load(t *testing.T, path string) dataset {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d := dataset{name: strings.TrimSuffix(filepath.Base(path), ".tsv")}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "# query: "):
			d.query = strings.TrimPrefix(line, "# query: ")
		case strings.HasPrefix(line, "# about: "):
			d.about = strings.TrimPrefix(line, "# about: ")
		case strings.HasPrefix(line, "# question: "):
			d.question = strings.TrimPrefix(line, "# question: ")
		case line == "" || strings.HasPrefix(line, "#"):
		default:
			l, text, _ := strings.Cut(line, "\t")
			d.labels = append(d.labels, l == "1")
			d.texts = append(d.texts, text)
		}
	}
	return d
}

// variant builds the items for one way of asking.
type variant struct {
	name  string
	maxQ  int
	build func(d dataset) []*jev.Item
}

func inQuestion(question func(d dataset) string) func(d dataset) []*jev.Item {
	return inQuestionState(question, false)
}

func inQuestionState(question func(d dataset) string, about bool) func(d dataset) []*jev.Item {
	return func(d dataset) []*jev.Item {
		st := jev.NewState("Each question carries its own text to judge.")
		if about {
			st = jev.NewState(jev.Obj{{K: "about", V: d.about}})
		}
		q := question(d)
		items := make([]*jev.Item, len(d.texts))
		for i, text := range d.texts {
			items[i] = jev.NewItem(st, jev.Noul(cli.Instr(cli.Rec{Text: text}, q), nil, nil), i)
		}
		return items
	}
}

var variants = []variant{
	{"in-question/predicate", 128, inQuestion(func(d dataset) string {
		return "Is it true that `text` " + d.query + "?"
	})},
	{"in-question/statement", 128, inQuestion(func(d dataset) string {
		return "`text` " + d.query + "."
	})},
	{"in-question/natural{}", 128, inQuestion(func(d dataset) string {
		return cli.Rewrite(d.question)
	})},
	{"in-question/predicate+about", 128, inQuestionState(func(d dataset) string {
		return "Is it true that `text` " + d.query + "?"
	}, true)},
	{"in-question/natural{}+about", 128, inQuestionState(func(d dataset) string {
		return cli.Rewrite(d.question)
	}, true)},
	{"in-question/predicate/maxq=8", 8, inQuestion(func(d dataset) string {
		return "Is it true that `text` " + d.query + "?"
	})},
	{"state-array/path", 128, func(d dataset) []*jev.Item {
		st := jev.NewState(jev.Obj{{K: "lines", V: d.texts}})
		items := make([]*jev.Item, len(d.texts))
		for i := range d.texts {
			q := fmt.Sprintf("Is it true that `lines[%d]` %s?", i, d.query)
			items[i] = jev.NewItem(st, jev.Noul(q, nil, nil), i)
		}
		return items
	}},
}

func TestEval(t *testing.T) {
	cfg, _ := config.Load()
	key, err := jev.LoadKey(cli.KeyConfigFrom(cfg))
	if err != nil {
		t.Skipf("no API key: %v", err)
	}
	paths, _ := filepath.Glob("testdata/*.tsv")
	var sets []dataset
	for _, p := range paths {
		sets = append(sets, load(t, p))
	}
	client := jev.NewClient(key.Value, "grev-eval/"+cli.Version)
	var spent float64

	fmt.Printf("\n%-30s %-8s %6s %6s %6s %6s %6s %6s %8s\n",
		"variant", "set", "acc@.5", "bestT", "acc@T", "AUC", "p(+)", "p(-)", "tokens")
	type agg struct{ acc, accT, auc float64 }
	totals := map[string]*agg{}
	for _, v := range variants {
		totals[v.name] = &agg{}
		for _, d := range sets {
			e := jev.NewEngine(client, jev.Model(""), jev.NewSched(8, false))
			e.MaxQ = v.maxQ
			e.MaxCost = 0.05
			items := v.build(d)
			e.Plan(items)
			ps := make([]float64, len(items))
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			err := e.RunAll(ctx, items, func(r jev.Result) {
				if r.Err != nil {
					t.Errorf("%s/%s: %v", v.name, d.name, r.Err)
					return
				}
				ps[r.Item.Tag.(int)] = r.Answer.Noul
			})
			cancel()
			if err != nil {
				t.Fatalf("%s/%s: %v", v.name, d.name, err)
			}
			s := e.Stats.Snapshot()
			spent += s.Cost
			acc := accuracy(ps, d.labels, 0.5)
			bestT, accT := bestThreshold(ps, d.labels)
			a := auc(ps, d.labels)
			pp, pn := means(ps, d.labels)
			fmt.Printf("%-30s %-8s %6.2f %6.2f %6.2f %6.3f %6.2f %6.2f %8d\n",
				v.name, d.name, acc, bestT, accT, a, pp, pn, s.Tokens)
			g := totals[v.name]
			g.acc += acc / float64(len(sets))
			g.accT += accT / float64(len(sets))
			g.auc += a / float64(len(sets))
			for i, p := range ps {
				if (p >= 0.5) != d.labels[i] && testing.Verbose() {
					fmt.Printf("    miss %.2f %v %q\n", p, d.labels[i], d.texts[i])
				}
			}
		}
	}
	fmt.Printf("\n%-30s %6s %6s %6s\n", "variant (mean over sets)", "acc@.5", "acc@T", "AUC")
	names := make([]string, 0, len(totals))
	for n := range totals {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return totals[names[i]].acc > totals[names[j]].acc })
	for _, n := range names {
		g := totals[n]
		fmt.Printf("%-30s %6.3f %6.3f %6.3f\n", n, g.acc, g.accT, g.auc)
	}
	fmt.Printf("\nspent $%s\n", strconv.FormatFloat(spent, 'f', 5, 64))
}

func accuracy(ps []float64, labels []bool, t float64) float64 {
	ok := 0
	for i, p := range ps {
		if (p >= t) == labels[i] {
			ok++
		}
	}
	return float64(ok) / float64(len(ps))
}

func bestThreshold(ps []float64, labels []bool) (float64, float64) {
	best, bestAcc := 0.5, accuracy(ps, labels, 0.5)
	for t := 0.05; t < 0.96; t += 0.05 {
		if a := accuracy(ps, labels, t); a > bestAcc+1e-9 {
			best, bestAcc = t, a
		}
	}
	return best, bestAcc
}

// auc is the probability that a random positive outranks a random negative.
func auc(ps []float64, labels []bool) float64 {
	var pos, neg []float64
	for i, p := range ps {
		if labels[i] {
			pos = append(pos, p)
		} else {
			neg = append(neg, p)
		}
	}
	if len(pos) == 0 || len(neg) == 0 {
		return math.NaN()
	}
	var s float64
	for _, a := range pos {
		for _, b := range neg {
			switch {
			case a > b:
				s++
			case a == b:
				s += 0.5
			}
		}
	}
	return s / float64(len(pos)*len(neg))
}

func means(ps []float64, labels []bool) (pos, neg float64) {
	var np, nn int
	for i, p := range ps {
		if labels[i] {
			pos += p
			np++
		} else {
			neg += p
			nn++
		}
	}
	return pos / float64(max(np, 1)), neg / float64(max(nn, 1))
}
