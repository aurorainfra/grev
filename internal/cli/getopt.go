package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Parser is a small GNU-style option parser: bundled short flags (-vnc),
// attached values (-C3, -Jmax), --long and --long=value, options anywhere
// before "--", and "-" as an ordinary argument.
type Parser struct {
	Prog       string
	Short      string   // one-line description (man NAME section)
	Synopsis   []string // usage lines, without the program name
	About      string   // description; indented lines are kept as is
	Notes      string   // free text printed after the options
	ExitStatus string   // exit status description (without the "Exit status:" label)
	Examples   []string // shell examples; continuation lines are indented
	Commands   []string // subcommands offered by shell completion (jev)
	opts       []*opt
	seen       map[*opt]bool
}

type opt struct {
	short    byte
	long     string
	argName  string // "" for boolean flags
	optional bool   // long-only optional argument (--other[=NAME])
	optDef   string
	help     string
	common   bool
	repeat   bool // may be given more than once (List)
	set      func(string) error
}

// NewParser starts a parser for prog.
func NewParser(prog string) *Parser { return &Parser{Prog: prog, seen: map[*opt]bool{}} }

func (p *Parser) add(o *opt) {
	for _, x := range p.opts {
		if (o.short != 0 && x.short == o.short) || (o.long != "" && x.long == o.long) {
			panic(fmt.Sprintf("duplicate option -%c/--%s", o.short, o.long))
		}
	}
	p.opts = append(p.opts, o)
}

// Flag registers a boolean option. Boolean options also accept --no-LONG,
// and "false" from config defaults.
func (p *Parser) Flag(short byte, long, help string) *bool {
	v := new(bool)
	p.add(&opt{short: short, long: long, help: help, set: func(s string) error { *v = s != "false"; return nil }})
	return v
}

// OptFunc registers an option whose value is optional on the long form
// (--long[=VALUE], like git's --color[=WHEN]); the short form and a bare
// --long use def, --no-long passes "false".
func (p *Parser) OptFunc(short byte, long, argName, def, help string, fn func(string) error) {
	p.add(&opt{short: short, long: long, argName: argName, optional: true, optDef: def, help: help, set: fn})
}

// Str registers a string option.
func (p *Parser) Str(short byte, long, argName, def, help string) *string {
	v := &def
	p.add(&opt{short: short, long: long, argName: argName, help: help, set: func(s string) error { *v = s; return nil }})
	return v
}

// OptStr registers a long option with an optional value: --long uses def,
// --long=v uses v. The result is nil until the option is given.
func (p *Parser) OptStr(long, argName, def, help string) **string {
	v := new(*string)
	p.add(&opt{long: long, argName: argName, optional: true, optDef: def, help: help,
		set: func(s string) error { *v = &s; return nil }})
	return v
}

// List registers a repeatable string option.
func (p *Parser) List(short byte, long, argName, help string) *[]string {
	v := new([]string)
	p.add(&opt{short: short, long: long, argName: argName, help: help, repeat: true,
		set: func(s string) error { *v = append(*v, s); return nil }})
	return v
}

// Int registers an integer option.
func (p *Parser) Int(short byte, long, argName string, def int, help string) *int {
	v := &def
	p.add(&opt{short: short, long: long, argName: argName, help: help, set: func(s string) error {
		n, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("want an integer, got %q", s)
		}
		*v = n
		return nil
	}})
	return v
}

// Float registers a float option.
func (p *Parser) Float(short byte, long, argName string, def float64, help string) *float64 {
	v := &def
	p.add(&opt{short: short, long: long, argName: argName, help: help, set: func(s string) error {
		f, err := strconv.ParseFloat(strings.TrimPrefix(s, "$"), 64)
		if err != nil {
			return fmt.Errorf("want a number, got %q", s)
		}
		*v = f
		return nil
	}})
	return v
}

// Func registers an option handled by fn (argName "" makes it boolean).
func (p *Parser) Func(short byte, long, argName, help string, fn func(string) error) {
	p.add(&opt{short: short, long: long, argName: argName, help: help, set: fn})
}

// ApplyDefault sets the option whose long name matches name (ignoring case
// and dashes, so maxCost matches --max-cost) as if it had been given, but
// without marking it seen, so explicit arguments parsed later win. Booleans
// take git-style boolean values. It reports whether the option exists.
func (p *Parser) ApplyDefault(name, value string) (bool, error) {
	norm := func(s string) string {
		return strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(s, "-", ""), "_", ""))
	}
	for _, o := range p.opts {
		if o.long == "" || norm(o.long) != norm(name) {
			continue
		}
		if o.argName == "" {
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "true", "yes", "on", "1", "":
				value = "true"
			case "false", "no", "off", "0":
				value = "false"
			default:
				return true, fmt.Errorf("--%s wants a boolean, got %q", o.long, value)
			}
		}
		if err := o.set(value); err != nil {
			return true, fmt.Errorf("--%s: %w", o.long, err)
		}
		return true, nil
	}
	return false, nil
}

// Seen reports whether the option with this long name was given.
func (p *Parser) Seen(long string) bool {
	for o := range p.seen {
		if o.long == long {
			return true
		}
	}
	return false
}

func (p *Parser) name(o *opt) string {
	if o.long != "" {
		return "--" + o.long
	}
	return "-" + string(o.short)
}

// Parse parses args (without the program name) and returns the positionals.
func (p *Parser) Parse(args []string) ([]string, error) {
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return append(pos, args[i+1:]...), nil
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a[2:], "=")
			o := p.findLong(name)
			if o == nil {
				if base, ok := strings.CutPrefix(name, "no-"); ok && !hasVal {
					if b := p.findLong(base); b != nil && (b.argName == "" || b.optional) {
						if err := p.apply(b, "false"); err != nil {
							return nil, err
						}
						continue
					}
				}
				return nil, fmt.Errorf("unknown option --%s", name)
			}
			switch {
			case o.argName == "":
				if hasVal {
					return nil, fmt.Errorf("option --%s takes no value", name)
				}
			case hasVal:
			case o.optional:
				val = o.optDef
			case i+1 < len(args):
				i++
				val = args[i]
			default:
				return nil, fmt.Errorf("option --%s needs a value", name)
			}
			if err := p.apply(o, val); err != nil {
				return nil, err
			}
		case len(a) > 1 && a[0] == '-':
			for j := 1; j < len(a); j++ {
				o := p.findShort(a[j])
				if o == nil {
					return nil, fmt.Errorf("unknown option -%c", a[j])
				}
				if o.argName == "" || o.optional {
					if err := p.apply(o, o.optDef); err != nil {
						return nil, err
					}
					continue
				}
				val := a[j+1:]
				if val == "" {
					if i+1 >= len(args) {
						return nil, fmt.Errorf("option -%c needs a value", a[j])
					}
					i++
					val = args[i]
				}
				if err := p.apply(o, val); err != nil {
					return nil, err
				}
				break
			}
		default:
			pos = append(pos, a)
		}
	}
	return pos, nil
}

func (p *Parser) apply(o *opt, val string) error {
	p.seen[o] = true
	if err := o.set(val); err != nil {
		return fmt.Errorf("%s: %w", p.name(o), err)
	}
	return nil
}

func (p *Parser) findLong(name string) *opt {
	for _, o := range p.opts {
		if o.long == name {
			return o
		}
	}
	return nil
}

func (p *Parser) findShort(c byte) *opt {
	for _, o := range p.opts {
		if o.short == c {
			return o
		}
	}
	return nil
}

// Help writes the usage text.
func (p *Parser) Help(w io.Writer) {
	for i, s := range p.Synopsis {
		if i == 0 {
			fmt.Fprintf(w, "usage: %s %s\n", p.Prog, s)
		} else {
			fmt.Fprintf(w, "       %s %s\n", p.Prog, s)
		}
	}
	if p.About != "" {
		fmt.Fprintf(w, "\n%s\n", strings.TrimSpace(p.About))
	}
	section := func(title string, common bool) {
		first := true
		for _, o := range p.opts {
			if o.common != common || o.help == "" {
				continue
			}
			if first {
				fmt.Fprintf(w, "\n%s:\n", title)
				first = false
			}
			var flag string
			switch {
			case o.short != 0 && o.long != "":
				flag = fmt.Sprintf("-%c, --%s", o.short, o.long)
			case o.short != 0:
				flag = fmt.Sprintf("-%c", o.short)
			default:
				flag = "    --" + o.long
			}
			if o.argName != "" {
				if o.optional {
					flag += "[=" + o.argName + "]"
				} else if o.long != "" {
					flag += "=" + o.argName
				} else {
					flag += " " + o.argName
				}
			}
			if len(flag) > 24 {
				fmt.Fprintf(w, "  %s\n  %-24s  %s\n", flag, "", o.help)
			} else {
				fmt.Fprintf(w, "  %-24s  %s\n", flag, o.help)
			}
		}
	}
	section("Options", false)
	section("Common options", true)
	if p.Notes != "" {
		fmt.Fprintf(w, "\n%s\n", strings.TrimSpace(p.Notes))
	}
	if p.ExitStatus != "" {
		fmt.Fprintf(w, "\nExit status: %s\n", strings.TrimSpace(p.ExitStatus))
	}
	if len(p.Examples) > 0 {
		fmt.Fprintf(w, "\nExamples:\n")
		for _, ex := range p.Examples {
			for _, line := range strings.Split(ex, "\n") {
				fmt.Fprintf(w, "  %s\n", line)
			}
		}
	}
}

// markCommon flags every option registered so far as a common one.
func (p *Parser) markCommon() {
	for _, o := range p.opts {
		o.common = true
	}
}

// Usagef prints an error plus a hint and exits with status 2.
func (p *Parser) Usagef(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", p.Prog, fmt.Sprintf(format, args...))
	fmt.Fprintf(os.Stderr, "Try '%s --help' for more information.\n", p.Prog)
	os.Exit(ExitError)
}
