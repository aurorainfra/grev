package jev

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Token estimation by character class, fitted on usage reported by
// jev-1.13.0 for prose, code, logs, CSV/JSON, UUIDs, hex, numbers, German
// and Japanese (within ±15% on each). Plain bytes/3.6 was off by 2.5× on
// dense logs, whose digits and punctuation cost far more than letters.
const (
	tokPerLetter   = 0.241
	tokPerDigit    = 1.26
	tokPerPunct    = 0.849
	tokPerSpace    = 0.016
	tokPerNonASCII = 0.993
)

// EstText estimates the tokens the model sees for s (escape sequences and
// control characters, which Clean removes, cost nothing).
func EstText(s string) int { return int(Tokens(s)) + 1 }

// Tokens is EstText before rounding, for running sums over many pieces.
func Tokens(s string) float64 {
	var t float64
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = skipEscape(s, i)
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		t += runeTok(r)
		i += n
	}
	return t
}

// Fit returns the length of the longest prefix of s estimated at no more
// than maxTok tokens, cut at a character boundary.
func Fit(s string, maxTok int) int {
	var t float64
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = skipEscape(s, i)
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		if t += runeTok(r); t > float64(maxTok) {
			return i
		}
		i += n
	}
	return len(s)
}

func runeTok(r rune) float64 {
	switch {
	case r >= 128:
		return tokPerNonASCII
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		return tokPerLetter
	case r >= '0' && r <= '9':
		return tokPerDigit
	case r == ' ' || r == '\n' || r == '\t':
		return tokPerSpace
	case r < 0x20 || r == 0x7f: // removed by Clean
		return 0
	}
	return tokPerPunct
}

func estJSON(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	// JSON escapes (\n, \", é) cost about what the character does;
	// estimating the raw encoding slightly overcounts, which is the safe side.
	return EstText(string(b))
}

// Clean removes what the model shouldn't have to read: ANSI escape
// sequences (colours, cursor movement) and other control characters except
// tab and newline. Tools print the input verbatim; only the model's view is
// cleaned.
func Clean(s string) string {
	dirty := false
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 && c != '\n' && c != '\t' || c == 0x7f {
			dirty = true
			break
		}
	}
	if !dirty {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b: // ESC
			i = skipEscape(s, i)
		case c < 0x20 && c != '\n' && c != '\t' || c == 0x7f:
			i++
		default:
			_, n := utf8.DecodeRuneInString(s[i:])
			b.WriteString(s[i : i+n])
			i += n
		}
	}
	return b.String()
}

// skipEscape returns the index just past the escape sequence at s[i].
func skipEscape(s string, i int) int {
	i++ // ESC
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[': // CSI: parameters and intermediates, then a final byte @..~
		i++
		for i < len(s) && (s[i] >= 0x20 && s[i] <= 0x3f) {
			i++
		}
		if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7e {
			i++
		}
	case ']', 'P', '_', '^': // OSC/DCS/APC/PM: until BEL or ESC \
		i++
		for i < len(s) {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
			i++
		}
	default: // ESC, intermediates (e.g. "(" in ESC ( B), final byte
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		if i < len(s) {
			i++
		}
	}
	return i
}

// sanitize applies Clean to every string inside a state or question value
// (option keys are left alone: answers come back under them).
func sanitize(v any) any {
	switch x := v.(type) {
	case string:
		return Clean(x)
	case Obj:
		out := make(Obj, len(x))
		for i, kv := range x {
			out[i] = KV{kv.K, sanitize(kv.V)}
		}
		return out
	case Opts:
		out := make(Opts, len(x))
		for i, o := range x {
			out[i] = Opt{o.Key, sanitize(o.Desc)}
		}
		return out
	case []string:
		out := make([]string, len(x))
		for i, s := range x {
			out[i] = Clean(s)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = sanitize(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = sanitize(e)
		}
		return out
	}
	return v
}
