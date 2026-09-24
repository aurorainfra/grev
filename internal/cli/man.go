package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aurorainfra/grev/internal/config"
)

// Man pages are generated from the same metadata as --help, so the two never
// drift: `TOOL --help-man` writes TOOL(1); `jev --help-man=config` writes
// grevconfig(5) from the config schema and `jev --help-man=tools` writes the
// grev-tools(7) overview.

// manDate is SOURCE_DATE_EPOCH (for reproducible builds) or today, UTC.
func manDate() string {
	if s := os.Getenv("SOURCE_DATE_EPOCH"); s != "" {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.Unix(n, 0).UTC().Format("2006-01-02")
		}
	}
	return time.Now().UTC().Format("2006-01-02")
}

// writeMan writes the requested page: "" is the tool's own page.
func (t *Tool) writeMan(w io.Writer, page string) error {
	date, ver := manDate(), version()
	switch page {
	case "":
		t.P.Man(w, date, ver)
	case "config":
		configMan(w, date, ver)
	case "tools":
		toolsMan(w, date, ver)
	default:
		return fmt.Errorf("--help-man: unknown page %q (want config or tools)", page)
	}
	return nil
}

// ---- roff helpers ----

// esc escapes prose for roff: backslashes, non-ASCII (as \[uXXXX]), and
// dashes in option-like words (-t, --max-cost) as \- so they copy as ASCII.
// Identifier-like words (limits.daily, unwrap(1), camelCase, --flags) are
// marked \% so they are never hyphenated. `backticked` spans become bold.
func esc(s string) string {
	var b strings.Builder
	bold := false
	optWord := false
	wordStart := true
	for i, w := 0, 0; i < len(s); i += w {
		r, width := utf8.DecodeRuneInString(s[i:])
		w = width
		if wordStart && r != ' ' && r != '\t' && r != '\n' {
			end := strings.IndexAny(s[i:], " \t\n")
			if end < 0 {
				end = len(s) - i
			}
			if identifier(s[i : i+end]) {
				b.WriteString(`\%`)
			}
			wordStart = false
		}
		switch {
		case r == '`':
			if bold {
				b.WriteString(`\fR`)
			} else {
				b.WriteString(`\fB`)
			}
			bold = !bold
			continue
		case r == ' ' || r == '\t' || r == '\n':
			optWord = false
			wordStart = true
		case r == '-' && !optWord:
			prev := byte(' ')
			if i > 0 {
				prev = s[i-1]
			}
			if strings.IndexByte(" \t\n(['\"/|=,`", prev) >= 0 {
				optWord = true
			}
		}
		b.WriteString(escRune(r, optWord))
	}
	if bold {
		b.WriteString(`\fR`)
	}
	return b.String()
}

// identifier reports whether a word looks like code rather than prose.
func identifier(word string) bool {
	word = strings.TrimRight(strings.TrimLeft(word, "(['\"`"), ".,;:)]'\"`")
	if strings.HasPrefix(word, "-") || strings.ContainsAny(word, "._/()$~=\\") {
		return true
	}
	for i := 1; i < len(word); i++ {
		if word[i] >= 'A' && word[i] <= 'Z' && word[i-1] >= 'a' && word[i-1] <= 'z' {
			return true // camelCase
		}
	}
	return false
}

// escCode escapes code (commands, synopsis, option names): every dash is \-.
func escCode(s string) string {
	var b strings.Builder
	for _, r := range s {
		b.WriteString(escRune(r, true))
	}
	return b.String()
}

func escRune(r rune, minus bool) string {
	switch {
	case r == '\\':
		return `\e`
	case r == '-' && minus:
		return `\-`
	case r >= 0x80:
		return fmt.Sprintf(`\[u%04X]`, r)
	}
	return string(r)
}

// macroArg quotes an already-escaped string as one macro argument.
func macroArg(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\(dq`) + `"` }

// safe protects a line that would otherwise start a roff request.
func safe(line string) string {
	if strings.HasPrefix(line, ".") || strings.HasPrefix(line, "'") {
		return `\&` + line
	}
	return line
}

// text writes prose: blank lines separate paragraphs; runs of indented lines
// are kept verbatim (no-fill), everything else is filled.
func text(w io.Writer, s string, code func(string) string) {
	s = strings.Trim(s, "\n")
	if s == "" {
		return
	}
	for i, block := range splitParas(s) {
		if i > 0 {
			fmt.Fprintln(w, ".PP")
		}
		lines := strings.Split(block, "\n")
		if table(lines) {
			fmt.Fprintln(w, ".RS 4\n.nf")
			for _, l := range lines {
				fmt.Fprintln(w, safe(code(l)))
			}
			fmt.Fprintln(w, ".fi\n.RE")
			continue
		}
		for j := 0; j < len(lines); {
			if indented(lines[j]) {
				k := j
				for k < len(lines) && indented(lines[k]) {
					k++
				}
				fmt.Fprintln(w, ".RS 4\n.nf")
				pre := commonIndent(lines[j:k])
				for _, l := range lines[j:k] {
					fmt.Fprintln(w, safe(code(l[pre:])))
				}
				fmt.Fprintln(w, ".fi\n.RE")
				j = k
				continue
			}
			fmt.Fprintln(w, safe(esc(strings.TrimSpace(lines[j]))))
			j++
		}
	}
}

func splitParas(s string) []string {
	var out []string
	var cur []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) == "" {
			if len(cur) > 0 {
				out = append(out, strings.Join(cur, "\n"))
				cur = nil
			}
			continue
		}
		cur = append(cur, l)
	}
	if len(cur) > 0 {
		out = append(out, strings.Join(cur, "\n"))
	}
	return out
}

// table reports whether a block is a label-and-columns table, like
// "SPEC:  name: question    yes/no" followed by indented rows: it is kept
// verbatim so the columns stay aligned.
func table(lines []string) bool {
	if len(lines) < 2 || indented(lines[0]) || !strings.Contains(strings.TrimSpace(lines[0]), "  ") {
		return false
	}
	for _, l := range lines[1:] {
		if indented(l) {
			return true
		}
	}
	return false
}

func indented(l string) bool { return strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t") }

func commonIndent(lines []string) int {
	n := -1
	for _, l := range lines {
		i := len(l) - len(strings.TrimLeft(l, " \t"))
		if n < 0 || i < n {
			n = i
		}
	}
	return max(n, 0)
}

func th(w io.Writer, title string, section int, date, ver, generator string) {
	fmt.Fprintf(w, ".\\\" Generated by %s; do not edit.\n", generator)
	fmt.Fprintf(w, ".TH %s %d %s \"grev tools %s\" \"grev tools manual\"\n",
		escCode(strings.ToUpper(title)), section, date, escCode(ver))
}

// optTag is the .TP tag of an option: \fB\-C\fR, \fB\-\-context\fR=\fIN\fR.
func optTag(o *opt) string {
	var parts []string
	if o.short != 0 {
		s := `\fB\-` + escCode(string(o.short)) + `\fR`
		if o.long == "" && o.argName != "" {
			s += ` \fI` + escCode(o.argName) + `\fR`
		}
		parts = append(parts, s)
	}
	if o.long != "" {
		s := `\fB\-\-` + escCode(o.long) + `\fR`
		switch {
		case o.argName == "":
		case o.optional:
			s += `[=\fI` + escCode(o.argName) + `\fR]`
		default:
			s += `=\fI` + escCode(o.argName) + `\fR`
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

// Man writes the tool's section 1 page.
func (p *Parser) Man(w io.Writer, date, ver string) {
	th(w, p.Prog, 1, date, ver, p.Prog+" --help-man")
	fmt.Fprintln(w, ".SH NAME")
	short := p.Short
	if short == "" {
		short = "a grev tool"
	}
	fmt.Fprintf(w, "%s \\- %s\n", escCode(p.Prog), esc(short))

	fmt.Fprintln(w, ".SH SYNOPSIS\n.nf")
	for _, s := range p.Synopsis {
		fmt.Fprintf(w, "\\fB%s\\fR %s\n", escCode(p.Prog), escCode(s))
	}
	fmt.Fprintln(w, ".fi")

	if p.About != "" {
		fmt.Fprintln(w, ".SH DESCRIPTION")
		text(w, p.About, esc)
	}
	optSection := func(title string, common bool) {
		first := true
		for _, o := range p.opts {
			if o.common != common || o.help == "" {
				continue
			}
			if first {
				fmt.Fprintf(w, ".SH %s\n", title)
				first = false
			}
			fmt.Fprintf(w, ".TP\n%s\n%s\n", optTag(o), safe(esc(o.help)))
		}
	}
	optSection("OPTIONS", false)
	optSection("COMMON OPTIONS", true)
	fmt.Fprintln(w, ".PP")
	text(w, "Boolean options can be switched off with --no-NAME (e.g. --no-progress). "+
		"Defaults for the common options, and for any long option of a single tool, can be set in "+
		"~/.grevconfig; see grevconfig(5). A command-line option always wins over the config.", esc)

	if p.Notes != "" {
		fmt.Fprintln(w, ".SH NOTES")
		text(w, p.Notes, esc)
	}
	fmt.Fprintln(w, ".SH EXIT STATUS")
	if p.ExitStatus != "" {
		text(w, p.ExitStatus, esc)
		fmt.Fprintln(w, ".PP")
	}
	text(w, "Every tool exits with 130 when interrupted; a run cut short reports what it had already spent.", esc)

	if len(p.Examples) > 0 {
		fmt.Fprintln(w, ".SH EXAMPLES\n.RS 4\n.nf")
		for _, ex := range p.Examples {
			for _, l := range strings.Split(ex, "\n") {
				fmt.Fprintln(w, safe(escCode(l)))
			}
		}
		fmt.Fprintln(w, ".fi\n.RE")
	}
	manEnvironment(w)
	manFiles(w)
	manSeeAlso(w, p.Prog)
}

func tp(w io.Writer, tag, body string) {
	fmt.Fprintf(w, ".TP\n%s\n", tag)
	text(w, body, esc)
}

func manEnvironment(w io.Writer) {
	fmt.Fprintln(w, ".SH ENVIRONMENT")
	for _, e := range [][2]string{
		{"TYPESAFE_API_KEY", "The API key. Takes precedence over key files and the config."},
		{"TYPESAFE_API_KEY_FILE", "A file holding the API key (the Docker/12-factor *_FILE convention)."},
		{"CREDENTIALS_DIRECTORY", "systemd credentials directory; typesafe_api_key in it is used as the key."},
		{"TYPESAFE_BASE_URL", "API root, overriding api.endpoint (default https://api.typesafe.ai)."},
		{"TYPESAFE_DEFAULT_MODEL", "Model id, overriding api.model; -M overrides both."},
		{"GREV_CONFIG", "Read only this config file instead of the usual ones; set but empty, read none."},
		{"GREV_LEDGER", "Path of the spend ledger; set but empty, turns the ledger (and the spend caps) off."},
		{"GREV_RPM, GREV_TPS", "Request and token rate ceilings, overriding limits.rpm and limits.tps."},
		{"GREV_MAX_Q", "Most questions packed into one request, overriding limits.questionsPerRequest."},
		{"GREV_PRICE_PER_MTOK", "Price in USD per million input tokens, overriding the built-in table and [model] prices."},
		{"GREV_DEBUG", "sched traces the adaptive concurrency controller (-J max) on stderr."},
	} {
		tp(w, `\fB`+escCode(e[0])+`\fR`, e[1])
	}
}

func manFiles(w io.Writer) {
	fmt.Fprintln(w, ".SH FILES")
	tp(w, `\fI~/.grevconfig\fR`, "Per-user configuration (git-config style); see grevconfig(5).")
	tp(w, `\fI$XDG_CONFIG_HOME/grev/config\fR`, "Alternative location, read before ~/.grevconfig (%AppData%\\grev\\config on Windows).")
	tp(w, `\fI$XDG_STATE_HOME/grev/spend\fR`, "The spend ledger behind jev spend and the limits.daily/limits.monthly caps "+
		"(~/.local/state/grev/spend by default; %LocalAppData%\\grev\\spend on Windows).")
}

func manSeeAlso(w io.Writer, self string) {
	fmt.Fprintln(w, ".SH SEE ALSO")
	var refs []string
	for _, r := range []string{"grevconfig(5)", "grev-tools(7)"} {
		if !strings.HasPrefix(r, self+"(") {
			refs = append(refs, r)
		}
	}
	for _, t := range Tools {
		if t.Name != self {
			refs = append(refs, t.Name+"(1)")
		}
	}
	for i, r := range refs {
		name, sec, _ := strings.Cut(r, "(")
		sep := ","
		if i == len(refs)-1 {
			sep = ""
		}
		fmt.Fprintf(w, ".BR \\%%%s (%s%s\n", escCode(name), sec, sep)
	}
}

// ---- grevconfig(5) ----

func configMan(w io.Writer, date, ver string) {
	th(w, "grevconfig", 5, date, ver, "jev --help-man=config")
	fmt.Fprintln(w, ".SH NAME\ngrevconfig \\- configuration file of the grev tools")
	fmt.Fprintln(w, ".SH SYNOPSIS\n.nf\n\\fI~/.grevconfig\\fR\n\\fI$XDG_CONFIG_HOME/grev/config\\fR\n.fi")
	fmt.Fprintln(w, ".SH DESCRIPTION")
	text(w, `The grev tools read their API key, endpoint, model, default options and spend limits from a
git-config style file. It is optional: without it, the tools use environment variables and built-in defaults.
Edit it by hand, or with `+"`jev config set NAME VALUE`"+`, which keeps comments and layout.`, esc)
	fmt.Fprintln(w, ".SS Syntax")
	text(w, `The file consists of sections. A section starts with a header in brackets, [section], or
[section "subsection"] for sections that take a name. Each following line is key = value.
Lines starting with # or ; are comments, as is anything after an unquoted # or ; on a line.
Values may be double-quoted to keep leading or trailing spaces, # or ;, and understand the
escapes \", \\, \n and \t. A backslash at the end of a line continues the value on the next line.
A key with no = is a boolean true; booleans accept true/false, yes/no, on/off and 1/0.

Section and key names are case-insensitive and ignore dashes and underscores: maxCost, max-cost
and maxcost are the same key. Subsection names are case-sensitive. When a key appears more
than once, the last value wins.`, esc)
	fmt.Fprintln(w, ".SS Locations")
	text(w, `$XDG_CONFIG_HOME/grev/config (default ~/.config/grev/config; %AppData%\grev\config on Windows)
is read first, then ~/.grevconfig, whose values win. GREV_CONFIG=PATH reads only PATH; set
but empty, no config is read at all. jev config set writes to ~/.grevconfig unless only the
XDG file exists. There is deliberately no per-project config file: a cloned repository could
otherwise redirect the API endpoint and collect your key.`, esc)
	fmt.Fprintln(w, ".SS Precedence")
	text(w, `A command-line option wins over an environment variable, which wins over the config
([tool "NAME"] over [defaults]), which wins over the built-in default.`, esc)
	fmt.Fprintln(w, ".SS Includes")
	text(w, `[include] path = FILE reads FILE at that point, relative to the including file, with ~
expanded; includes nest up to 10 levels and missing files are skipped. This keeps secrets
such as api.key out of a config you share or keep in a dotfiles repository.`, esc)

	fmt.Fprintln(w, ".SH KEYS")
	var section string
	for _, k := range config.Schema {
		hdr := "[" + k.Section + "]"
		if k.Sub != "" {
			hdr = "[" + k.Section + ` "` + k.Sub + `"]`
		}
		if hdr != section {
			fmt.Fprintf(w, ".SS %s\n", macroArg(escCode(hdr)))
			section = hdr
		}
		tag := `\fB` + escCode(k.Name) + `\fR = \fI` + escCode(k.Type) + `\fR`
		body := k.Doc
		if k.Default != "" {
			body += " Default: " + k.Default + "."
		}
		tp(w, tag, body)
	}

	fmt.Fprintln(w, ".SH SAFEGUARDS")
	text(w, `Before a run whose size is known up front, each tool quotes it: requests, input tokens and
cost. Then, in order:`, esc)
	for _, b := range []string{
		"A quote above the per-run budget (defaults.maxCost, --max-cost) is refused (exit status 4); nothing is sent.",
		"A quote above what the daily or monthly cap (limits.daily, limits.monthly) still allows is refused.",
		"A quote above defaults.confirmAbove (--confirm-above; built-in 1.00 USD), or any quote with -Q, is shown " +
			"and needs a yes on the terminal. Without a terminal the run is refused, with a hint to pass " +
			"--max-cost or --confirm-above=off. An explicit --max-cost that covers the quote counts as the answer.",
		"While running, every request is checked against the per-run budget and the caps, so streaming " +
			"(--line-buffered) and multi-round tools stop at the limit too (exit status 4).",
	} {
		fmt.Fprintln(w, ".IP \\(bu 2")
		fmt.Fprintln(w, safe(esc(b)))
	}
	fmt.Fprintln(w, ".PP")
	text(w, `Spend is always recorded in the ledger ($XDG_STATE_HOME/grev/spend), per day and tool,
so jev spend works from the first run; the caps are only enforced when set. Several tools
running at once can overshoot a cap by the requests they already have in flight. Limits need
the model's price: for a model the built-in table doesn't know, add [model "ID"] price.`, esc)

	manEnvironment(w)
	fmt.Fprintln(w, ".SH EXAMPLES\n.RS 4\n.nf")
	for _, l := range strings.Split(`# ~/.grevconfig
[api]
	key = tsk-...                  ; written by jev key set (mode 0600); or keyCommand = pass show typesafe

[defaults]
	progress = auto                ; the -p overlay whenever stderr is a terminal
	jobs = max                     ; adapt parallelism to the server
	confirmAbove = 0.25            ; ask before runs quoted above $0.25

[limits]
	daily = 5
	monthly = 50

[tool "grev"]
	about = application logs       ; grev --about 'application logs' by default

[include]
	path = ~/.grevconfig.local`, "\n") {
		fmt.Fprintln(w, safe(escCode(l)))
	}
	fmt.Fprintln(w, ".fi\n.RE")
	fmt.Fprintln(w, ".PP")
	text(w, "The same settings from the command line:", esc)
	fmt.Fprintln(w, ".RS 4\n.nf")
	for _, l := range []string{
		"jev key set",
		"jev config set defaults.progress auto",
		"jev config set limits.daily 5",
		"jev config set tool.grev.about 'application logs'",
		"jev config list \\-\\-show-origin",
	} {
		fmt.Fprintln(w, safe(escCode(strings.ReplaceAll(l, `\-`, "-"))))
	}
	fmt.Fprintln(w, ".fi\n.RE")
	manSeeAlso(w, "grevconfig")
}

// ---- grev-tools(7) ----

func toolsMan(w io.Writer, date, ver string) {
	th(w, "grev-tools", 7, date, ver, "jev --help-man=tools")
	fmt.Fprintln(w, ".SH NAME\ngrev\\-tools \\- Unix filters that ask a model instead of matching patterns")
	fmt.Fprintln(w, ".SH DESCRIPTION")
	text(w, `The grev tools are small Unix filters built on TypeSafe's System One models (Jev), which
return typed answers with calibrated probabilities instead of generated text. Because the model
can't write text, the tools behave like classic filters: they select, reorder, split, route or
annotate their input, and their output is the input, verbatim. Labels and probabilities appear only
in explicit, tab-separated columns.

For example, printf 'steak\nboiled carrots\n' | grev 'is vegan meal' prints boiled carrots.`, esc)
	fmt.Fprintln(w, ".SH TOOLS")
	for _, t := range Tools {
		tp(w, `\fB`+escCode(t.Name)+`\fR(1)`, t.Short+" (like "+t.Like+").")
	}
	fmt.Fprintln(w, ".SH HOW THE TOOLS ASK")
	for _, b := range []string{
		"Independent judgments put each record inside its own question and keep only shared context " +
			"(--about, -S) in the state: grev, tagv, rank, uniqv. On labelled sets this beat listing all " +
			"records together, and naming the domain with --about was the biggest accuracy gain.",
		"Relational judgments (does this line continue that one? where does a new topic start? which line " +
			"answers?) send a line-numbered document instead: unwrap, seg, pickv.",
		"A Choice is relative and always crowns a winner, so pickv and seek pair it with a way to say " +
			"\"nothing here\": an existence question in pickv, a (none) option at every level in seek.",
		"Code does what the model is bad at: counting (grev -c), negation (-v is 1 minus P(yes), never a " +
			"negated question), sorting and arithmetic (rank, probev columns for awk), and structure " +
			"(unwrap never asks about list markers, fences or blank lines).",
		"Questions are packed many to a request (up to 128) and requests run in parallel (-J, or -J max to " +
			"adapt to the server); results always come out in input order.",
	} {
		fmt.Fprintln(w, ".IP \\(bu 2")
		fmt.Fprintln(w, safe(esc(b)))
	}
	fmt.Fprintln(w, ".SH COMMON OPTIONS")
	text(w, `Every tool takes -p/--progress[=WHEN] (live progress, spend and projected cost on stderr),
-Q/--quote (show the quote and ask first), --confirm-above=USD, --max-cost=USD, -J N|max,
and -M MODEL; see any tool's page for details, and grevconfig(5) for setting defaults.`, esc)
	fmt.Fprintln(w, ".SH COST")
	text(w, `Input tokens cost 0.042 USD per million with jev-1.13; output is free. A request carries
about 260 tokens of framing, plus about 7 per question, plus the text itself: grev over a
4,300-line source file is about 200k tokens, under a cent. jev spend shows what was spent
today and this month.`, esc)
	fmt.Fprintln(w, ".SH EXIT STATUS")
	for _, e := range [][2]string{
		{"0", "yes, a match, or success"},
		{"1", "no, or nothing matched"},
		{"2", "an error"},
		{"3", "only uncertain answers (for tools with a confidence threshold or --band)"},
		{"4", "declined at a prompt, or refused or stopped by a budget or spend cap"},
		{"130", "interrupted"},
	} {
		tp(w, `\fB`+e[0]+`\fR`, e[1]+".")
	}
	manFiles(w)
	manSeeAlso(w, "grev-tools")
}
