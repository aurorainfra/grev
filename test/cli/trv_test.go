package clitest

import (
	"strings"
	"testing"

	"github.com/aurorainfra/grev/internal/jevtest"
)

func TestTrvActions(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	// Translate only where the model says yes (the fake says yes on lines with "yes").
	expect(t, run(t, env, "yes: 'hi'\nno: 'hi'\n", "trv", "'", `"`, "q"), 0, "yes: \"hi\"\nno: 'hi'\n")
	// Squeeze runs of a repeated character; single commas are never asked about.
	expect(t, run(t, env, "yes a,,b,c\nno c,,d\n", "trv", "-s", ",", "q"), 0, "yes a,b,c\nno c,,d\n")
	// Delete runs; classes and ranges in SET.
	expect(t, run(t, env, "yes x!!!\nno y!!!\n", "trv", "-d", "!", "q"), 0, "yes x\nno y!!!\n")
	expect(t, run(t, env, "yes a1b22\nno c3\n", "trv", "-d", "[:digit:]", "q"), 0, "yes ab\nno c3\n")
	expect(t, run(t, env, "yes abc\n", "trv", "a-c", "A-C", "q"), 0, "yes ABC\n")
	// SET2 shorter than SET1 repeats its last character, as in tr.
	expect(t, run(t, env, "yes abc\n", "trv", "abc", "X", "q"), 0, "yes XXX\n")
	// Regex replace with group expansion.
	expect(t, run(t, env, "yes color\nno color\n", "trv", "-e", `(colo)r`, "-r", "${1}ur", "q"), 0, "yes colour\nno color\n")
	// Regex delete.
	expect(t, run(t, env, "yes quote (sic) end\nno x (sic)\n", "trv", "-d", "-e", ` ?\(sic\)`, "q"), 0, "yes quote end\nno x (sic)\n")
}

func TestTrvChoices(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	// The fake picks the option named in the line; with none, confidence 0 keeps it.
	in := "Street: Main St.\nSaint: St. Mary\nnothing St. here\n"
	expect(t, run(t, env, in, "trv", "-e", `St\.`, "-o", "Saint|Street", "q"), 0,
		"Street: Main Street\nSaint: Saint Mary\nnothing St. here\n")
	// "keep" wins → unchanged, exit 1.
	expect(t, run(t, env, "keep St. as is\n", "trv", "-e", `St\.`, "-o", "Saint|Street", "q"), 1, "keep St. as is\n")
	usageError(t, env, in, "trv", "-e", `St\.`, "-o", "Saint||Street", "q")
	usageError(t, env, in, "trv", "-e", `St\.`, "-o", "keep|Street", "q")
}

func TestTrvVerbatimAndExit(t *testing.T) {
	srv, env := fake(t, jevtest.Options{})
	// Bytes outside the chosen occurrences are copied exactly (CRLF, no final newline).
	expect(t, run(t, env, "yes a,,b\r\nno c,,d\r\ntail", "trv", "-s", ",", "q"), 0, "yes a,b\r\nno c,,d\r\ntail")
	// Nothing chosen → unchanged, exit 1.
	expect(t, run(t, env, "no a,,b\n", "trv", "-s", ",", "q"), 1, "no a,,b\n")
	// No candidates at all → unchanged, exit 1, no requests.
	before := srv.Requests()
	expect(t, run(t, env, "yes a,b\n", "trv", "-s", ",", "q"), 1, "yes a,b\n")
	if srv.Requests() != before {
		t.Fatal("no candidates should mean no requests")
	}
	// --trace narrates decisions on stderr; the output is unaffected.
	r := run(t, env, "yes a,,b\nno c,,d\n", "trv", "--trace", "-s", ",", "q")
	if r.Code != 0 || r.Stdout != "yes a,b\nno c,,d\n" || !strings.Contains(r.Stderr, "1:6 ⟦,,⟧ p=0.95 → changed") ||
		!strings.Contains(r.Stderr, "→ kept") {
		t.Fatalf("--trace: %+v", r)
	}
	// -i FILE instead of stdin.
	f := writeFile(t, t.TempDir(), "in.txt", "yes x!!\n")
	expect(t, run(t, env, "", "trv", "-i", f, "-s", "!", "q"), 0, "yes x!\n")
}

func TestTrvUsageAndSafeguards(t *testing.T) {
	_, env := fake(t, jevtest.Options{})
	in := "yes a,,b\n"
	usageError(t, env, in, "trv")
	usageError(t, env, in, "trv", "q")                         // translate needs SET1 SET2
	usageError(t, env, in, "trv", "-d", "-s", ",", "q")        // one of -d / -s
	usageError(t, env, in, "trv", "-s", "-e", ",,", "q")       // no squeeze with -e
	usageError(t, env, in, "trv", "-e", ",,", "q")             // -e needs an action
	usageError(t, env, in, "trv", "-r", "x", ",", "q")         // -r needs -e
	usageError(t, env, in, "trv", "-d", "[:nope:]", "q")       // bad class
	usageError(t, env, in, "trv", "-d", "z-a", "q")            // reversed range
	usageError(t, env, in, "trv", "-e", "(", "-d", "q")        // bad regex
	usageError(t, env, in, "trv", "-s", ",", "Is {1} a typo?") // no fields in trv
	declineQuote(t, jevtest.Options{}, in, "trv", "-s", ",", "q")
	overBudget(t, jevtest.Options{}, in, "trv", "-s", ",", "q")
}
