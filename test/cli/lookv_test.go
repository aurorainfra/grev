package clitest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aurorainfra/grev/internal/jevtest"
)

// numbered builds n lines "line NNNN yes|no" where yes(i) decides (0-based).
func numbered(n int, yes func(i int) bool) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		ans := "no"
		if yes(i) {
			ans = "yes"
		}
		fmt.Fprintf(&b, "line %04d %s\n", i+1, ans)
	}
	return b.String()
}

func TestLookvFindsFlip(t *testing.T) {
	in := numbered(1000, func(i int) bool { return i >= 636 })
	srv, env := fake(t, jevtest.Options{})
	expect(t, run(t, env, in, "lookv", "q"), 0, "line 0637 yes\n")
	// k-ary search: a handful of rounds, not a linear scan.
	if r := srv.Requests(); r > 4 {
		t.Fatalf("expected ≤ 4 requests, got %d", r)
	}
	if a := srv.Answered(); a > 4*16 {
		t.Fatalf("expected ≤ 64 questions, got %d", a)
	}
	expect(t, run(t, env, in, "lookv", "-n", "-B1", "-A1", "q"), 0,
		"636-line 0636 no\n637:line 0637 yes\n638-line 0638 yes\n")
	expect(t, run(t, env, in, "lookv", "-s", "q"), 0, "0.95\tline 0637 yes\n")
	// Fewer probes per round means more rounds, same answer.
	before := srv.Requests()
	expect(t, run(t, env, in, "lookv", "-k", "2", "q"), 0, "line 0637 yes\n")
	if r := srv.Requests() - before; r < 6 {
		t.Fatalf("-k 2 should take more rounds, took %d", r)
	}
}

func TestLookvEdges(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	// Inverted: yes … yes, then no.
	in := numbered(300, func(i int) bool { return i < 123 })
	expect(t, run(t, env, in, "lookv", "-v", "q"), 0, "line 0124 no\n")
	// Never flips.
	expect(t, run(t, env, numbered(300, func(int) bool { return false }), "lookv", "q"), 1, "")
	// Already yes at the first record.
	r := run(t, env, numbered(50, func(int) bool { return true }), "lookv", "q")
	if r.Code != 0 || r.Stdout != "line 0001 yes\n" || !strings.Contains(r.Stderr, "already yes at the first record") {
		t.Fatalf("yes from the start: %+v", r)
	}
	// Empty input, and tiny inputs.
	expect(t, run(t, env, "", "lookv", "q"), 1, "")
	expect(t, run(t, env, "only no\n", "lookv", "q"), 1, "")
	expect(t, run(t, env, "a no\nb yes\n", "lookv", "q"), 0, "b yes\n")
	// Paragraph records.
	expect(t, run(t, env, "p one\nno\n\np two\nyes\n\np three\nyes\n", "lookv", "--para", "q"), 0, "p two\nyes\n\n")
}

func TestLookvNonMonotonic(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	// A yes island at 471..481 that the first round hits, then no again,
	// then yes for good from 801.
	in := numbered(1000, func(i int) bool { return i >= 470 && i <= 480 || i >= 800 })
	r := run(t, env, in, "lookv", "q")
	if r.Code != 0 || r.Stdout != "line 0471 yes\n" || !strings.Contains(r.Stderr, "not monotonic") {
		t.Fatalf("non-monotonic: %+v", r)
	}
}

func TestLookvBandAndVerify(t *testing.T) {
	srv, env := fake(t, jevtest.Options{})
	// The record before the flip is a coin toss: exit 3 with --band.
	in := numbered(200, func(i int) bool { return i >= 101 })
	in = strings.Replace(in, "line 0101 no", "line 0101 maybe", 1)
	expect(t, run(t, env, in, "lookv", "q"), 0, "line 0101 maybe\n") // 0.5 ≥ -t 0.5
	expect(t, run(t, env, in, "lookv", "--band", "0.3:0.7", "q"), 3, "line 0102 yes\n")
	// --verify agrees on a clean flip and costs one more request.
	clean := numbered(200, func(i int) bool { return i >= 150 })
	before := srv.Requests()
	expect(t, run(t, env, clean, "lookv", "q"), 0, "line 0151 yes\n")
	plain := srv.Requests() - before
	before = srv.Requests()
	expect(t, run(t, env, clean, "lookv", "--verify", "q"), 0, "line 0151 yes\n")
	if got := srv.Requests() - before; got != plain+1 {
		t.Fatalf("--verify: %d requests, want %d", got, plain+1)
	}
	// --trace narrates the rounds on stderr.
	r := run(t, env, clean, "lookv", "--trace", "q")
	if r.Code != 0 || !strings.Contains(r.Stderr, "round 1: asked") || !strings.Contains(r.Stderr, "→ flip in (") {
		t.Fatalf("--trace: %+v", r)
	}
}

func TestLookvUsageAndSafeguards(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	in := numbered(100, func(i int) bool { return i >= 50 })
	usageError(t, env, in, "lookv")
	usageError(t, env, in, "lookv", "-k", "0", "q")
	usageError(t, env, in, "lookv", "Is {1} yes?")
	usageError(t, env, in, "lookv", "a", "b", "c")
	declineQuote(t, jevtest.Options{}, in, "lookv", "q")
	overBudget(t, jevtest.Options{}, in, "lookv", "q")
	// Fields with -d.
	expect(t, run(t, env, "a\tno\nb\tyes\n", "lookv", "-d", `\t`, "Is {2} a yes?"), 0, "b\tyes\n")
}
