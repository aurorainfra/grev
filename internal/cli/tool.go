// Package cli is the shared runtime of the grev tools: option parsing, the
// common flags, ~/.grevconfig defaults, credentials, the engine, safeguards
// (quotes, budgets, the spend ledger), the progress overlay, stdout
// coordination and exit codes.
package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/aurorainfra/grev/internal/config"
	"github.com/aurorainfra/grev/internal/jev"
)

// Version is the toolset version, set at build time with
// -ldflags "-X github.com/aurorainfra/grev/internal/cli.Version=v1.2.3".
var Version = ""

// version falls back to the module version for `go install` builds.
func version() string {
	if Version != "" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

// ProjectURL is where grev comes from. The User-Agent carries it, and it is
// grev's app identity on OpenRouter, so it must not change.
const ProjectURL = "https://github.com/aurorainfra/grev"

// UserAgent identifies the tool to the API, e.g.
// "grev/0.1.0 (pickv; linux/amd64; +https://github.com/aurorainfra/grev)".
func UserAgent(tool string) string {
	return fmt.Sprintf("grev/%s (%s; %s/%s; +%s)",
		strings.TrimPrefix(version(), "v"), tool, runtime.GOOS, runtime.GOARCH, ProjectURL)
}

// Attribution returns OpenRouter's app-attribution headers
// (openrouter.ai/docs/app-attribution). They name the whole family as one
// app, grev; the User-Agent says which tool. Other endpoints ignore them.
func Attribution() http.Header {
	return http.Header{
		"Http-Referer":            {ProjectURL},
		"X-Openrouter-Title":      {"grev"},
		"X-Openrouter-Categories": {"programming-app"},
	}
}

// NewClient returns an API client for key: the endpoint from TYPESAFE_BASE_URL
// or api.endpoint, the tool's User-Agent, and the attribution headers unless
// api.attribution is false.
func NewClient(cfg *config.Config, key, tool string) *jev.Client {
	c := jev.NewClient(key, UserAgent(tool))
	if os.Getenv("TYPESAFE_BASE_URL") == "" {
		if ep := cfg.Str("api", "", "endpoint"); ep != "" {
			c.BaseURL = strings.TrimRight(ep, "/")
		}
	}
	if v, ok := cfg.Get("api", "", "attribution"); !ok || !isFalse(v.Raw) {
		c.Header = Attribution()
	}
	return c
}

// isFalse reports whether s is a boolean false. Anything unparsable counts as
// the default, true; Check reports it.
func isFalse(s string) bool {
	b, err := config.Bool(s)
	return err == nil && !b
}

// DefaultConfirmAbove is the built-in safeguard: runs quoted above this many
// USD ask first (or refuse without a terminal).
const DefaultConfirmAbove = 1.00

// Exit codes shared by the whole family.
const (
	ExitYes       = 0   // yes / match / ok
	ExitNo        = 1   // no / none
	ExitError     = 2   // error
	ExitUncertain = 3   // only uncertain answers
	ExitDeclined  = 4   // declined at a prompt, or over a budget or cap
	ExitInterrupt = 130 // interrupted
)

// Tool is one invocation of a grev tool.
type Tool struct {
	Name string
	P    *Parser
	Out  *Out

	// TolerateBadConfig lets a tool run with a broken config file (grev-settings
	// config, so it can be fixed). Set before Parse.
	TolerateBadConfig bool

	progress     string // always | never | auto
	quote        bool
	confirmAbove float64 // USD; < 0 = never ask
	retryAlways  bool    // "retry without asking again" was the answer to a failed request
	j            string
	model        string
	maxCost      float64 // USD; 0 = no per-run cap
	help         bool
	version      bool

	manPage    *string // --help-man[=PAGE]
	completion string  // --help-completion=SHELL

	cfg     *config.Config
	origins map[string]string // option long name → where its value came from
	ledger  *Ledger

	eng    *Engine
	prog   *Progress
	ctx    context.Context
	cancel context.CancelFunc
	sigs   atomic.Int32
}

// Engine is jev.Engine plus the tool it belongs to.
type Engine = jev.Engine

// New creates a tool and registers the common options. Register the tool's
// own options on t.P afterwards, then call t.Parse.
func New(name string) *Tool {
	t := &Tool{Name: name, P: NewParser(name), progress: "never", confirmAbove: DefaultConfirmAbove,
		origins: map[string]string{}}
	p := t.P
	p.Short = toolShort(name)
	p.OptFunc('p', "progress", "WHEN", "always", "show live progress, cost and projection on stderr (WHEN: always, never, auto)",
		func(s string) error {
			switch strings.ToLower(s) {
			case "always", "true", "yes", "on", "1":
				t.progress = "always"
			case "never", "false", "no", "off", "0":
				t.progress = "never"
			case "auto":
				t.progress = "auto"
			default:
				return fmt.Errorf("want always, never or auto, got %q", s)
			}
			return nil
		})
	p.Func('Q', "quote", "", "always show the quote (requests, tokens, cost) and ask before sending anything",
		func(s string) error { t.quote = s != "false"; return nil })
	p.Func(0, "confirm-above", "USD", fmt.Sprintf("ask first when a run is quoted above USD; 'off' never asks (default %.2f)", DefaultConfirmAbove),
		func(s string) error { return parseUSD(s, &t.confirmAbove, -1) })
	p.Func('J', "jobs", "N|max", "parallel requests: a number, or 'max' to adapt to the server (default 4)",
		func(s string) error { t.j = s; return nil })
	p.Func('M', "model", "MODEL", "model id (default: api.model, $TYPESAFE_DEFAULT_MODEL, or "+jev.DefaultModel+")",
		func(s string) error { t.model = s; return nil })
	p.Func(0, "max-cost", "USD", "per-run budget: refuse or stop once spend would exceed USD; 'off' for none",
		func(s string) error { return parseUSD(s, &t.maxCost, 0) })
	p.Func(0, "help", "", "show this help", func(string) error { t.help = true; return nil })
	p.Func(0, "version", "", "show version", func(string) error { t.version = true; return nil })
	p.markCommon()
	return t
}

// parseUSD parses an amount, or "off" (stored as off).
func parseUSD(s string, dst *float64, off float64) error {
	if strings.EqualFold(strings.TrimSpace(s), "off") {
		*dst = off
		return nil
	}
	f, err := config.Float(s)
	if err != nil || f < 0 {
		return fmt.Errorf("want an amount in USD or 'off', got %q", s)
	}
	if f == 0 && off == 0 {
		*dst = 0 // 0 means "no cap" for --max-cost
		return nil
	}
	*dst = f
	return nil
}

// Parse loads ~/.grevconfig, applies its defaults, then parses os.Args,
// handling --help, --version and usage errors. -h also means help unless the
// tool gave it another meaning (grev: no filenames).
func (t *Tool) Parse() []string {
	if t.P.findShort('h') == nil {
		t.P.Func('h', "", "", "", func(string) error { t.help = true; return nil })
	}
	// Hidden generators for man pages and shell completions (used by make).
	t.P.OptFunc(0, "help-man", "PAGE", "", "", func(s string) error { t.manPage = &s; return nil })
	t.P.Func(0, "help-completion", "SHELL", "", func(s string) error { t.completion = s; return nil })
	if docsOnly(os.Args[1:]) {
		t.cfg = &config.Config{} // help and docs don't depend on (or fail on) the config
	} else {
		t.loadConfig()
	}
	pos, err := t.P.Parse(os.Args[1:])
	if t.manPage != nil {
		if err := t.writeMan(os.Stdout, *t.manPage); err != nil {
			t.P.Usagef("%v", err)
		}
		os.Exit(0)
	}
	if t.completion != "" {
		if err := t.P.Completion(os.Stdout, t.completion); err != nil {
			t.P.Usagef("%v", err)
		}
		os.Exit(0)
	}
	if t.help {
		t.P.Help(os.Stdout)
		os.Exit(0)
	}
	if t.version {
		fmt.Printf("%s %s (grev tools)\n", t.Name, version())
		os.Exit(0)
	}
	if err != nil {
		t.P.Usagef("%v", err)
	}
	for _, name := range []string{"progress", "quote", "confirm-above", "max-cost", "jobs"} {
		if t.P.Seen(name) {
			t.origins[name] = "--" + name
		}
	}
	return pos
}

// docsOnly reports whether args ask only for help, version, a man page or a
// completion script.
func docsOnly(args []string) bool {
	for _, a := range args {
		if a == "--" {
			break
		}
		name, _, _ := strings.Cut(a, "=")
		switch name {
		case "--help", "--version", "--help-man", "--help-completion":
			return true
		}
	}
	return false
}

// loadConfig reads the config files and applies [defaults] and
// [tool "<name>"] as option defaults.
func (t *Tool) loadConfig() {
	cfg, err := config.Load()
	if err != nil {
		if t.TolerateBadConfig {
			t.Warnf("warning: %v (config ignored)", err)
			t.cfg = &config.Config{}
			return
		}
		t.Fatalf("%v", err)
	}
	t.cfg = cfg
	for _, w := range cfg.Check() {
		t.Warnf("warning: %s", w)
	}
	apply := func(v config.Value, allowed func(string) bool) {
		if v.Key == "model" || v.Key == "help" || v.Key == "version" || v.Key == "helpman" {
			t.Warnf("warning: %s: %s can't be set here; use [api]", v.Origin(), v.Name())
			return
		}
		if allowed != nil && !allowed(v.Key) {
			return // unknown [defaults] keys are reported by Check
		}
		ok, err := t.P.ApplyDefault(v.Key, v.Raw)
		switch {
		case err != nil:
			t.Fatalf("%s: %s: %v", v.Origin(), v.Name(), err)
		case !ok:
			t.Warnf("warning: %s: %s has no option --%s", v.Origin(), t.Name, v.Key)
		default:
			for _, o := range t.P.opts {
				if o.long != "" && config.Norm(o.long) == v.Key {
					t.origins[o.long] = v.Origin()
				}
			}
		}
	}
	common := func(k string) bool {
		switch k {
		case "progress", "jobs", "maxcost", "confirmabove", "quote":
			return true
		}
		return false
	}
	for _, v := range cfg.Section("defaults", "") {
		apply(v, common)
	}
	for _, v := range cfg.Section("tool", t.Name) {
		apply(v, nil)
	}
}

// Config is the loaded config (empty if none).
func (t *Tool) Config() *config.Config { return t.cfg }

// from is " (origin)" for values that didn't come from the command line.
func (t *Tool) from(long string) string {
	if o := t.origin(long); !strings.HasPrefix(o, "--") {
		return " (" + o + ")"
	}
	return ""
}

// origin says where an option's value came from, for messages.
func (t *Tool) origin(long string) string {
	if o, ok := t.origins[long]; ok {
		return o
	}
	return "built-in default"
}

// Progress reports whether the overlay is on.
func (t *Tool) Progress() bool {
	return t.progress == "always" || t.progress == "auto" && IsTerminal(os.Stderr)
}

// Quoting reports whether -Q was given.
func (t *Tool) Quoting() bool { return t.quote }

// Model is the model the tool will use: -M, TYPESAFE_DEFAULT_MODEL,
// api.model, then the pinned default.
func (t *Tool) Model() string {
	if t.model == "" && os.Getenv("TYPESAFE_DEFAULT_MODEL") == "" {
		if m := t.cfg.Str("api", "", "model"); m != "" {
			return m
		}
	}
	return jev.Model(t.model)
}

// KeyConfig is the api.key / api.keyCommand setting.
func (t *Tool) KeyConfig() jev.KeyConfig { return KeyConfigFrom(t.cfg) }

// KeyConfigFrom reads api.key (which wins) or api.keyCommand from cfg.
func KeyConfigFrom(cfg *config.Config) jev.KeyConfig {
	if v, ok := cfg.Get("api", "", "key"); ok {
		return jev.KeyConfig{Key: v.Raw, Name: "api.key", Origin: v.Origin(), ConfigFile: v.File}
	}
	if v, ok := cfg.Get("api", "", "keyCommand"); ok {
		return jev.KeyConfig{Command: v.Raw, Name: "api.keyCommand", Origin: v.Origin(), ConfigFile: v.File}
	}
	return jev.KeyConfig{}
}

// Ctx is cancelled on the first interrupt.
func (t *Tool) Ctx() context.Context {
	if t.ctx == nil {
		t.ctx, t.cancel = context.WithCancel(context.Background())
		ch := make(chan os.Signal, 2)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		go func() {
			for range ch {
				if t.sigs.Add(1) > 1 {
					t.stopProgress(false)
					os.Exit(ExitInterrupt)
				}
				t.cancel()
			}
		}()
	}
	return t.ctx
}

// Engine loads credentials and builds the engine, scheduler, safeguards and
// overlay.
func (t *Tool) Engine() *Engine {
	if t.eng != nil {
		return t.eng
	}
	n, adaptive, err := jev.ParseJ(t.j)
	if err != nil {
		t.P.Usagef("%v", err)
	}
	key, err := jev.LoadKey(t.KeyConfig())
	if err != nil {
		t.Fatalf("%v", err)
	}
	if key.Warn != "" {
		t.Warnf("warning: %s", key.Warn)
	}
	c := NewClient(t.cfg, key.Value, t.Name)
	for _, sub := range t.cfg.Subsections("model") {
		if v, ok := t.cfg.Get("model", sub, "price"); ok {
			if f, err := config.Float(v.Raw); err == nil {
				jev.SetPrice(sub, jev.Price{In: f})
			}
		}
	}
	sched := jev.NewSched(n, adaptive)
	sched.SetRates(t.cfgFloat("limits", "rpm"), t.cfgFloat("limits", "tps"))
	t.eng = jev.NewEngine(c, t.Model(), sched)
	if q := int(t.cfgFloat("limits", "questionsPerRequest")); q > 0 && os.Getenv("GREV_MAX_Q") == "" {
		t.eng.MaxQ = q
	}
	t.eng.MaxCost = t.maxCost
	t.eng.OnFail = t.onFail

	t.ledger = OpenLedger(t.Name, t.cfgFloat("limits", "daily"), t.cfgFloat("limits", "monthly"))
	if t.ledger != nil {
		l := t.ledger
		t.eng.Stats.OnCost = func(_ string, cost float64, tokens int) { l.Add(cost, tokens) }
		if l.Capped() {
			t.eng.Budget = l.Remaining
		}
	}
	if _, known := jev.PriceOf(t.Model()); !known && (t.maxCost > 0 || t.ledger.Capped()) {
		t.Fatalf("spend limits need the price of %s: add [model %q] price = USD-per-Mtok to ~/.grevconfig, or set GREV_PRICE_PER_MTOK",
			t.Model(), t.Model())
	}
	t.Ctx()
	if t.Progress() {
		t.prog = startProgress(t.Name, t.eng)
	}
	if t.Out == nil {
		t.Out = newOut(t.prog)
	} else {
		t.Out.setProgress(t.prog)
	}
	t.Out.onError = t.cancelOnOutputError
	return t.eng
}

func (t *Tool) cfgFloat(section, key string) float64 {
	v, ok := t.cfg.Get(section, "", key)
	if !ok || strings.EqualFold(v.Raw, "off") {
		return 0
	}
	f, err := config.Float(v.Raw)
	if err != nil {
		return 0 // reported by Check
	}
	return f
}

// cancelOnOutputError stops the run when stdout is gone (e.g. a closed
// pipe), so nothing more is spent on answers nobody reads.
func (t *Tool) cancelOnOutputError(error) {
	if t.cancel != nil {
		t.cancel()
	}
}

// Output returns the stdout writer, creating it if the engine isn't needed.
func (t *Tool) Output() *Out {
	if t.Out == nil {
		t.Out = newOut(t.prog)
	}
	return t.Out
}

// Streaming rejects -Q for tools running in streaming mode.
func (t *Tool) Streaming() {
	if t.quote {
		t.P.Usagef("-Q needs the whole input up front; it can't be combined with streaming")
	}
}

// Confirm applies the safeguards to a planned run, in order: the per-run
// budget and the daily/monthly caps refuse outright; then -Q, or a quote
// above --confirm-above, asks on the terminal (and refuses without one). An
// explicit --max-cost covering the quote counts as the answer. It returns
// only if the run may proceed.
func (t *Tool) Confirm(q jev.Quote) {
	e := t.Engine()
	if q.TooBig > 0 {
		t.Warnf("warning: %d record(s) are too large for a single request and will fail", q.TooBig)
	}
	p, known := jev.PriceOf(e.Model)
	if !known {
		p, _ = jev.PriceOf(jev.DefaultModel) // estimate with the default model's price
	}
	cost := float64(q.EstTokens) * p.In / 1e6
	refuse := func(format string, a ...any) {
		t.pauseProgress(func() { printQuote(os.Stderr, t.Name, e, q) })
		t.Warnf(format, a...)
		t.Exit(ExitDeclined)
	}
	if t.maxCost > 0 && cost > t.maxCost {
		refuse("quote %s exceeds --max-cost %s%s", fmtCost(cost), fmtCost(t.maxCost), t.from("max-cost"))
	}
	if rem := t.ledger.remaining(); rem >= 0 && cost > rem {
		refuse("quote %s exceeds what the spend caps allow (%s left: %s)", fmtCost(cost), fmtCost(rem), t.ledger.Describe())
	}
	ask := t.quote || (t.confirmAbove >= 0 && cost > t.confirmAbove)
	if !t.quote && t.P.Seen("max-cost") && t.maxCost >= cost {
		ask = false // an explicit budget is an answer
	}
	if !ask {
		return
	}
	decision, msg := -1, ""
	t.pauseProgress(func() {
		printQuote(os.Stderr, t.Name, e, q)
		if !known {
			fmt.Fprintf(os.Stderr, "%*sprice of %s unknown; estimated at %s's price\n", len(t.Name)+8, "", e.Model, jev.DefaultModel)
		}
		ok, err := askYes(t.Ctx(), "Proceed? [y/N] ")
		switch {
		case err != nil && t.quote:
			decision, msg = ExitError, fmt.Sprintf("-Q needs a terminal to confirm (%v); use --max-cost for unattended runs", err)
		case err != nil:
			decision, msg = ExitDeclined, fmt.Sprintf("quote %s is above --confirm-above %s (%s) and there is no terminal to ask; "+
				"pass --max-cost=%.2f to accept it, or --confirm-above=off", fmtCost(cost), fmtCost(t.confirmAbove), t.origin("confirm-above"), roundUp(cost))
		case !ok:
			decision = ExitDeclined
		}
	})
	if decision >= 0 {
		if msg != "" {
			t.Warnf("%s", msg)
		}
		t.Exit(decision)
	}
}

func roundUp(c float64) float64 {
	return float64(int(c*100)+1) / 100
}

func (l *Ledger) remaining() float64 {
	if l == nil {
		return -1
	}
	return l.Remaining()
}

// Finish maps a run error to an exit code (0 if err is nil), printing it.
// Runs cut short also report what was already spent.
func (t *Tool) Finish(err error) int {
	switch {
	case err == nil:
		return ExitYes
	case errors.Is(err, jev.ErrBudget):
		var why []string
		if t.maxCost > 0 {
			why = append(why, "per-run budget "+fmtCost(t.maxCost)+" ("+t.origin("max-cost")+")")
		}
		if t.ledger.Capped() {
			why = append(why, t.ledger.Describe())
		}
		t.Warnf("stopped: spend limit reached: %s%s", strings.Join(why, "; "), t.spent())
		return ExitDeclined
	case errors.Is(err, jev.ErrStopped):
		t.Warnf("%v%s", err, t.spent())
		return ExitError
	case errors.Is(err, context.Canceled) && t.sigs.Load() > 0:
		t.Warnf("interrupted%s", t.spent())
		return ExitInterrupt
	case errors.Is(err, context.Canceled) && t.Out != nil && t.Out.Err() != nil:
		return ExitError // stdout went away; nothing useful to say on stderr
	default:
		t.Warnf("%v", err)
		return ExitError
	}
}

// onFail is the engine's OnFail. On a big run (quoted, or already spent, at
// --confirm-above or more; at $1 when that is off) it asks on the terminal
// what to do with a request that failed for good, rather than lose the run.
// Small runs, and runs without a terminal, let the questions fail as always.
func (t *Tool) onFail(err error, n int) jev.FailAction {
	if t.retryAlways {
		return jev.FailRetry
	}
	s := t.eng.Stats.Snapshot()
	threshold := t.confirmAbove
	if threshold < 0 {
		threshold = DefaultConfirmAbove
	}
	if max(s.Projected(), s.Cost) < threshold {
		return jev.FailSkip
	}
	skip := !t.eng.StopOnFail
	opts, keys := "Retry it [r], retry without asking again [a], or stop [q]?", "[R/a/q]"
	if skip {
		opts, keys = fmt.Sprintf("Retry it [r], retry without asking again [a], skip its %d questions [s], or stop [q]?", n), "[R/a/s/q]"
	}
	act := jev.FailSkip
	t.pauseProgress(func() {
		shown := false
		for {
			a, ok, err2 := ask(t.Ctx(), func() {
				if !shown {
					shown = true
					fmt.Fprintf(os.Stderr, "%s: a request failed: %v\n", t.Name, err)
					done := commas(s.DoneQ)
					if s.PlanQ > 0 {
						done += " of " + commas(s.PlanQ)
					}
					fmt.Fprintf(os.Stderr, "%s: %s questions answered so far, %s spent; the request held %d\n", t.Name, done, fmtCost(s.Cost), n)
				}
				fmt.Fprintf(os.Stderr, "%s %s ", opts, keys)
			})
			switch {
			case err2 != nil:
				return // no terminal: as before
			case !ok:
				act = jev.FailStop
			case a == "" || a == "r" || a == "retry" || a == "y" || a == "yes":
				act = jev.FailRetry
			case a == "a" || a == "always":
				act, t.retryAlways = jev.FailRetry, true
			case skip && (a == "s" || a == "skip"):
				act = jev.FailSkip
			case a == "q" || a == "quit" || a == "stop" || a == "n" || a == "no":
				act = jev.FailStop
			default:
				continue
			}
			return
		}
	})
	return act
}

// spent describes the spend so far for messages about cut-short runs (the
// -p summary already says it).
func (t *Tool) spent() string {
	if t.eng == nil || t.prog != nil {
		return ""
	}
	s := t.eng.Stats.Snapshot()
	return fmt.Sprintf(" after %s requests, %s spent", commas(s.Reqs), fmtCost(s.Cost))
}

// Ledger is the spend ledger (nil if turned off).
func (t *Tool) Ledger() *Ledger { return t.ledger }

// Exit flushes output, closes the overlay (printing the -p summary) and exits.
func (t *Tool) Exit(code int) {
	if t.Out != nil {
		t.Out.Flush()
	}
	if err := t.ledger.Err(); err != nil {
		t.Warnf("warning: spend ledger: %v", err)
	}
	t.stopProgress(true)
	os.Exit(code)
}

// Fatalf prints an error and exits with status 2.
func (t *Tool) Fatalf(format string, args ...any) {
	t.Warnf(format, args...)
	t.Exit(ExitError)
}

// Warnf prints "tool: message" on stderr without disturbing the overlay.
func (t *Tool) Warnf(format string, args ...any) {
	msg := fmt.Sprintf("%s: %s\n", t.Name, fmt.Sprintf(format, args...))
	t.pauseProgress(func() { os.Stderr.WriteString(msg) })
}

func (t *Tool) pauseProgress(fn func()) {
	if t.prog != nil {
		t.prog.pause(fn)
		return
	}
	fn()
}

func (t *Tool) stopProgress(summary bool) {
	if t.prog != nil {
		t.prog.stop(summary)
		t.prog = nil
	}
}

// Ask runs one request: a whole-input state with a few named questions. It
// applies -Q/--max-cost and exits on errors, so callers get only answers.
func (t *Tool) Ask(state any, names []string, qs []jev.Question) map[string]jev.Answer {
	e := t.Engine()
	st := jev.NewState(state)
	items := make([]*jev.Item, len(qs))
	for i, q := range qs {
		items[i] = jev.NewItem(st, q, names[i])
	}
	q := e.Plan(items)
	if q.TooBig > 0 {
		t.Fatalf("input is too large for one request (≈%s tokens; the state plus a question must fit in %s)",
			fmtTokens(st.Est()), fmtTokens(jev.CtxStateQ))
	}
	t.Confirm(q)
	out := map[string]jev.Answer{}
	var firstErr error
	err := e.RunAll(t.Ctx(), items, func(r jev.Result) {
		if r.Err != nil {
			if firstErr == nil {
				firstErr = r.Err
			}
			return
		}
		out[r.Item.Tag.(string)] = r.Answer
	})
	if err == nil {
		err = firstErr
	}
	if err != nil {
		t.Exit(t.Finish(err))
	}
	return out
}

// RunItems plans items, applies -Q/--max-cost, and runs them, calling emit
// once per item in input order. It returns the engine's run error.
func (t *Tool) RunItems(ctx context.Context, items []*jev.Item, emit func(jev.Result)) error {
	e := t.Engine()
	q := e.Plan(items)
	if q.Questions+q.TooBig > 0 {
		t.Confirm(q)
	}
	return e.RunAll(ctx, items, emit)
}

// StreamItems runs items as they arrive on in (streaming mode: no totals, no
// -Q), sending partial requests after flush.
func (t *Tool) StreamItems(ctx context.Context, in <-chan *jev.Item, flush time.Duration, emit func(jev.Result)) error {
	t.Streaming()
	return t.Engine().Run(ctx, in, flush, emit)
}
