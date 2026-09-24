package jev

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestClean(t *testing.T) {
	for in, want := range map[string]string{
		"plain text\twith tab\n":                            "plain text\twith tab\n",
		"\x1b[1;31mERROR\x1b[0m disk full":                  "ERROR disk full",
		"\x1b[38;5;208m2026-01-02\x1b[m ok\r\n":             "2026-01-02 ok\n",
		"\x1b]0;window title\x07after":                      "after",
		"\x1b]8;;https://x.example\x1b\\link\x1b]8;;\x1b\\": "link",
		"bell\x07 nul\x00 del\x7f bs\x08":                   "bell nul del bs",
		"\x1b(Bcharset \x1bMreverse":                        "charset reverse",
		"trailing esc \x1b":                                 "trailing esc ",
		"unterminated \x1b[12":                              "unterminated ",
		"héllo ✓ 日本":                                        "héllo ✓ 日本",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEstTextClasses(t *testing.T) {
	letters, digits := EstText(strings.Repeat("abcdefghij", 100)), EstText(strings.Repeat("1234567890", 100))
	if digits < 4*letters {
		t.Errorf("digits (%d) should cost far more than letters (%d)", digits, letters)
	}
	// Colour codes are stripped before the model sees the text, so they cost nothing.
	line := "2026-03-04T12:00:01.123Z INFO  proving sector 1234 took 12.5s"
	if a, b := EstText("\x1b[2m"+line+"\x1b[0m"), EstText(line); a != b {
		t.Errorf("ANSI should not count: %d vs %d", a, b)
	}
	// Logs are dense (measured 1.4–2.2 bytes/token), prose light (4.8).
	if bpt := float64(len(line)) / float64(EstText(line)); bpt < 1.2 || bpt > 2.3 {
		t.Errorf("log line: %.2f bytes/token", bpt)
	}
	prose := "The quick brown fox jumps over the lazy dog, and then it naps in the warm afternoon sun."
	if bpt := float64(len(prose)) / float64(EstText(prose)); bpt < 3.8 || bpt > 6 {
		t.Errorf("prose: %.2f bytes/token", bpt)
	}
}

func TestSanitize(t *testing.T) {
	in := Obj{
		{K: "text", V: "\x1b[31mred\x1b[0m"},
		{K: "list", V: []string{"a\x1b[1m!"}},
		{K: "any", V: []any{"b\x07", 3}},
		{K: "map", V: map[string]any{"k": "c\x00"}},
	}
	st := NewState(in)
	b, _ := json.Marshal(st.V)
	if want := `{"text":"red","list":["a!"],"any":["b",3],"map":{"k":"c"}}`; string(b) != want {
		t.Errorf("state: %s", b)
	}
	if in[0].V != "\x1b[31mred\x1b[0m" {
		t.Error("the caller's value must not be modified")
	}
	it := NewItem(st, Choice(Obj{{K: "q", V: "pick\x1b[0m"}}, Opts{{Key: "a\x1b", Desc: "x\x1b[1m"}, {Key: "b"}}), nil)
	b, _ = json.Marshal(it.Q)
	if want := `{"type":"choice","instructions":{"q":"pick"},"criteria":{"a\u001b":"x","b":null}}`; string(b) != want {
		t.Errorf("question: %s", b)
	}
}

func TestIsOverLimit(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{&APIError{Status: 400, Body: `{"error_type":"max_tokens_exceeded"}`}, true},
		{&APIError{Status: 400, Body: `{"detail":"invalid json"}`}, false},
		{&APIError{Status: 400, Body: `{"detail":"options limit is 255"}`}, false},
		{&APIError{Status: 413, Body: `payload too large`}, true},
		{&APIError{Status: 422, Body: `request exceeds the context length`}, true},
		{&APIError{Status: 422, Body: `{"detail":[{"msg":"field required"}]}`}, false},
		{&APIError{Status: 429, Body: `too many tokens per second`}, false},
		{fmt.Errorf("item 3: %w", ErrTooLarge), true},
		{fmt.Errorf("wrapped: %w", &APIError{Status: 400, Body: `{"error_type":"max_tokens_exceeded"}`}), true},
		{nil, false},
	} {
		if got := IsOverLimit(c.err); got != c.want {
			t.Errorf("IsOverLimit(%v) = %v", c.err, got)
		}
	}
}

func TestFit(t *testing.T) {
	s := strings.Repeat("abc 123 ", 1000)
	n := Fit(s, 500)
	if n == 0 || n == len(s) || EstText(s[:n]) > 501 || EstText(s[:n+8]) <= 500 {
		t.Fatalf("Fit = %d: %d tokens, next %d", n, EstText(s[:n]), EstText(s[:min(len(s), n+8)]))
	}
	if Fit("short", 100) != 5 || Fit("\x1b[31m", 0) != 5 {
		t.Fatal("Fit on short or escape-only input")
	}
	if got := Tokens("\x1b[1mab\x1b[0m"); got != 2*tokPerLetter {
		t.Fatalf("Tokens with escapes = %v", got)
	}
}
