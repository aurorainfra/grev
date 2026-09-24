package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aurorainfra/grev/internal/jev"
)

func TestReadAll(t *testing.T) {
	cases := []struct {
		name string
		mode RecMode
		in   string
		want []string
	}{
		{"lines", Lines, "a\nb\nc\n", []string{"a", "b", "c"}},
		{"no trailing newline", Lines, "a\nb", []string{"a", "b"}},
		{"crlf", Lines, "a\r\nb\r\n", []string{"a", "b"}},
		{"empty lines kept", Lines, "a\n\nb\n", []string{"a", "", "b"}},
		{"empty input", Lines, "", nil},
		{"nul", NUL, "a b\x00c\nd\x00e", []string{"a b", "c\nd", "e"}},
		{"para", Para, "a\nb\n\n\nc\n", []string{"a\nb", "c"}},
		{"para leading blanks", Para, "\n\n  x\ny\n\nz", []string{"  x\ny", "z"}},
		{"para crlf", Para, "a\r\n\r\nb\r\n", []string{"a", "b"}},
		{"para trailing blanks", Para, "a\n\n\n", []string{"a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadAll(strings.NewReader(tc.in), tc.mode)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestScanStops(t *testing.T) {
	var got []string
	err := Scan(strings.NewReader("a\nb\nc\n"), Lines, func(s string) bool {
		got = append(got, s)
		return len(got) < 2
	})
	if err != nil || !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestLongLine(t *testing.T) {
	long := strings.Repeat("x", 1<<20)
	got, err := ReadAll(strings.NewReader(long+"\nshort\n"), Lines)
	if err != nil || len(got) != 2 || len(got[0]) != 1<<20 {
		t.Fatalf("long line: %d records, %v", len(got), err)
	}
}

func TestSep(t *testing.T) {
	if Lines.Sep() != "\n" || NUL.Sep() != "\x00" || Para.Sep() != "\n\n" {
		t.Fatal("unexpected separators")
	}
}

func TestUnescape(t *testing.T) {
	if got := Unescape(`a\tb\nc\0d\\e`); got != "a\tb\nc\x00d\\e" {
		t.Fatalf("got %q", got)
	}
}

func TestTemplate(t *testing.T) {
	cases := []struct{ env, in, want string }{
		{"", "is vegan meal", "Is it true that `text` is vegan meal?"},
		{"", "mentions a timeout.", "Is it true that `text` mentions a timeout?"},
		{"", "  Is {} vegan?  ", "Is `text` vegan?"},
		{"", "Is {1} the same product as {2}?", "Is `f1` the same product as `f2`?"},
		{"", "Does this line mention cats?", "Regarding `text`: Does this line mention cats?"},
		{"statement", "is vegan meal", "`text` is vegan meal."},
		{"about", "is vegan meal", "Regarding `text`: is vegan meal"},
		{"statement", "Is {} vegan?", "Is `text` vegan?"}, // placeholders win over templates
	}
	for _, tc := range cases {
		t.Setenv("GREV_TEMPLATE", tc.env)
		if got := Template(tc.in); got != tc.want {
			t.Errorf("Template(%q) [%s] = %q, want %q", tc.in, tc.env, got, tc.want)
		}
	}
	if HasPlaceholder("no braces") || !HasPlaceholder("x {} y") || !HasPlaceholder("{12}") {
		t.Error("HasPlaceholder")
	}
}

func TestInstrOrder(t *testing.T) {
	o := Instr(Rec{Text: "a\tb", Fields: []string{"a", "b"}, Before: []string{"p"}, After: []string{"n"}}, "Is `f1` like `f2`?")
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"before":["p"],"text":"a\tb","f1":"a","f2":"b","after":["n"],"question":"Is ` + "`f1`" + ` like ` + "`f2`" + `?"}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
	b, _ = json.Marshal(Instr(Rec{Text: "x"}, "q"))
	if string(b) != `{"text":"x","question":"q"}` {
		t.Fatalf("minimal instr: %s", b)
	}
}

func TestSharedState(t *testing.T) {
	s, err := SharedState("", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(string); !ok {
		t.Fatalf("no context should give a plain string state, got %T", s)
	}
	ref := filepath.Join(t.TempDir(), "spec.md")
	os.WriteFile(ref, []byte("the spec"), 0o644)
	s, err = SharedState(ref, "restaurant menu")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s)
	if string(b) != `{"about":"restaurant menu","reference":"the spec"}` {
		t.Fatalf("got %s", b)
	}
	if _, err := SharedState(filepath.Join(t.TempDir(), "missing"), ""); err == nil {
		t.Fatal("missing reference file should fail")
	}
}

func TestParseSpec(t *testing.T) {
	cases := []struct {
		in      string
		n       int
		name    string
		typ     string
		text    string
		options string // JSON of criteria
	}{
		{"urgent: conveys urgency", 1, "urgent", jev.TypeNoul, "conveys urgency", "null"},
		{"Is the sky blue?", 3, "q3", jev.TypeNoul, "Is the sky blue?", "null"},
		{"dept: Which team? [billing=Payments|technical|sales]", 1, "dept", jev.TypeChoice, "Which team?",
			`{"billing":"Payments","technical":null,"sales":null}`},
		{"mood: How angry? <calm | annoyed | furious>", 1, "mood", jev.TypeScore, "How angry?",
			`["calm","annoyed","furious"]`},
		{"note: ratio is 3:2 here", 1, "note", jev.TypeNoul, "ratio is 3:2 here", "null"},
		{"http://x: not a name", 2, "q2", jev.TypeNoul, "http://x: not a name", "null"},
	}
	for _, tc := range cases {
		sp, err := ParseSpec(tc.in, tc.n)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		crit, _ := json.Marshal(sp.Q.Criteria)
		if sp.Name != tc.name || sp.Q.Type != tc.typ || sp.Text != tc.text || string(crit) != tc.options {
			t.Errorf("%q → name=%q type=%q text=%q criteria=%s", tc.in, sp.Name, sp.Q.Type, sp.Text, crit)
		}
	}
	for _, bad := range []string{"x: [a|]", "x: ", "[a|b]", "x: q <" + strings.Repeat("l|", 10) + "l>"} {
		if _, err := ParseSpec(bad, 1); err == nil {
			t.Errorf("ParseSpec(%q) should fail", bad)
		}
	}
}

func TestParseLabels(t *testing.T) {
	opts, err := ParseLabels([]string{"bug=Something is broken", "feature", " question = Asks something "})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(opts)
	if string(b) != `{"bug":"Something is broken","feature":null,"question":"Asks something"}` {
		t.Fatalf("got %s", b)
	}
	if _, err := ParseLabels([]string{"a", "a"}); err == nil {
		t.Fatal("duplicate labels should fail")
	}
	if _, err := ParseLabels([]string{"=x"}); err == nil {
		t.Fatal("empty label should fail")
	}
	many := make([]string, 256)
	for i := range many {
		many[i] = "l" + strings.Repeat("x", i)
	}
	if _, err := ParseLabels(many); err == nil {
		t.Fatal("256 labels should fail")
	}
}

func TestLabelsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "labels.tsv")
	os.WriteFile(p, []byte("# comment\nbug\tSomething broken\r\n\nfeature\n"), 0o644)
	got, err := LabelsFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"bug=Something broken", "feature"}) {
		t.Fatalf("got %q", got)
	}
}

func TestFormatting(t *testing.T) {
	cases := map[string]string{
		fmtCost(0):           "$0",
		fmtCost(0.00002646):  "$0.000026",
		fmtCost(0.0169):      "$0.0169",
		fmtCost(12.345):      "$12.35",
		fmtTokens(999):       "999",
		fmtTokens(2500):      "2.5k",
		fmtTokens(402118):    "402k",
		fmtTokens(1_500_000): "1.5M",
		commas(1234567):      "1,234,567",
		commas(-1000):        "-1,000",
		commas(12):           "12",
		fmtDur(0.25):         "0.2s",
		fmtDur(42):           "42s",
		fmtDur(125):          "2m05s",
		fmtDur(3700):         "1h01m",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	if b := bar(0.5, 10); !strings.HasPrefix(b, "▕█████") || len([]rune(b)) != 12 {
		t.Errorf("bar(0.5) = %q", b)
	}
	if b := bar(1.2, 4); b != "▕████▏" {
		t.Errorf("bar(1.2) = %q", b)
	}
	if got := trunc("héllo world", 6); got != "héllo…" {
		t.Errorf("trunc = %q", got)
	}
}
