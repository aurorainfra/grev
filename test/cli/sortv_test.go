package clitest

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/aurorainfra/grev/internal/jev"
	"github.com/aurorainfra/grev/internal/jevtest"
)

var itemNum = regexp.MustCompile(`item (\d+)`)

func num(v any) int {
	m := itemNum.FindStringSubmatch(fmt.Sprint(v))
	if m == nil {
		return -1
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// numberOracle seeds "item N" at level N/5 (coarse buckets) and answers
// "should a come before b?" by comparing the numbers. bias, if set, answers
// that pair question with bias regardless of the items (pure position bias).
func numberOracle(bias float64) func(any, jev.Question) jev.Answer {
	return func(state any, q jev.Question) jev.Answer {
		ins, _ := q.Instructions.(map[string]any)
		switch q.Type {
		case jev.TypeScore:
			lvl := min(4, num(ins["text"])/5)
			probs := map[string]float64{}
			for i := 0; i < 5; i++ {
				probs[strconv.Itoa(i)] = 0
			}
			probs[strconv.Itoa(lvl)] = 1
			return jev.Answer{Type: jev.TypeScore, Score: float64(lvl), Confidence: 1, Probabilities: probs}
		default:
			if bias > 0 {
				return jev.Answer{Type: jev.TypeNoul, Noul: bias}
			}
			p := 0.05
			if num(ins["a"]) < num(ins["b"]) {
				p = 0.95
			}
			return jev.Answer{Type: jev.TypeNoul, Noul: p}
		}
	}
}

func items(order ...int) string {
	var b strings.Builder
	for _, n := range order {
		fmt.Fprintf(&b, "item %02d\n", n)
	}
	return b.String()
}

func TestSortvSeedsThenRefines(t *testing.T) {
	in := items(13, 2, 19, 7, 0, 11, 4, 16, 9, 1, 18, 6, 14, 3, 10, 17, 8, 12, 5, 15)
	srv, env := fake(t, jevtest.Options{Oracle: numberOracle(0)})
	// Seed only: coarse buckets of 5, input order inside each bucket.
	expect(t, run(t, env, in, "sortv", "--passes", "0", "q"), 0,
		items(2, 0, 4, 1, 3, 7, 9, 6, 8, 5, 13, 11, 14, 10, 12, 19, 16, 18, 17, 15))
	// Enough passes sort it fully.
	before := srv.Requests()
	expect(t, run(t, env, in, "sortv", "--passes", "10", "q"), 0, items(seq(0, 19)...))
	// Early stop: it quits after two passes that change nothing, not after 10.
	if got := srv.Requests() - before; got >= 11 {
		t.Fatalf("expected an early stop, used %d requests", got)
	}
	expect(t, run(t, env, in, "sortv", "--passes", "10", "-r", "-m", "3", "q"), 0, items(19, 18, 17))
	expect(t, run(t, env, items(3, 1, 2), "sortv", "-n", "q"), 0, "2:item 01\n3:item 02\n1:item 03\n")
	expect(t, run(t, env, items(3, 1, 2), "sortv", "-s", "q"), 0, "0.00\titem 01\n0.00\titem 02\n0.00\titem 03\n")
}

func TestSortvCancelsPositionBias(t *testing.T) {
	// A judge that always says "a first" cancels out when asked both ways
	// round, so the seed order stands.
	in := items(13, 2, 19, 7, 0, 11)
	_, env := fake(t, jevtest.Options{Oracle: numberOracle(0.9)})
	expect(t, run(t, env, in, "sortv", "q"), 0, items(2, 0, 7, 13, 11, 19))
}

func TestSortvUsageAndSafeguards(t *testing.T) {
	_, env := fake(t, jevtest.Options{Oracle: numberOracle(0)})
	in := items(3, 1, 2)
	expect(t, run(t, env, "", "sortv", "q"), 1, "")
	expect(t, run(t, env, "item 01\n", "sortv", "q"), 0, "item 01\n")
	usageError(t, env, in, "sortv")
	usageError(t, env, in, "sortv", "--passes", "-1", "q")
	usageError(t, env, in, "sortv", "by {}")
	declineQuote(t, jevtest.Options{Oracle: numberOracle(0)}, in, "sortv", "q")
	overBudget(t, jevtest.Options{Oracle: numberOracle(0)}, in, "sortv", "q")
}

func seq(a, b int) []int {
	var out []int
	for i := a; i <= b; i++ {
		out = append(out, i)
	}
	return out
}
