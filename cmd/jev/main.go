// Command jev is the plumbing and admin tool of the grev family: manage the
// API key, list models, ask one-off questions, and send raw requests.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/aurorainfra/grev/internal/cli"
	"github.com/aurorainfra/grev/internal/config"
	"github.com/aurorainfra/grev/internal/jev"
)

func main() {
	t := cli.New("jev")
	t.TolerateBadConfig = true // so `jev config edit` can fix a broken file
	p := t.P
	p.Commands = []string{"config", "key", "spend", "models", "ask", "raw"}
	p.Synopsis = []string{
		"config list [--show-origin] | get NAME | set NAME VALUE | unset NAME | edit | path",
		"key set | import | status | path | rm",
		"spend [--days N]",
		"models",
		"ask [-S FILE | --state TEXT] -q SPEC... [FILE]",
		"raw [FILE]",
	}
	p.About = `Plumbing and admin for the grev tools (TypeSafe System One / Jev).

  config      read and edit ~/.grevconfig (git-config style; see grevconfig(5))
  key set     store an API key in ~/.grevconfig (read from the terminal, or stdin)
  key import  store the key currently given by TYPESAFE_API_KEY or
              TYPESAFE_API_KEY_FILE in ~/.grevconfig, so you can drop them
  key status  show which key source is in use, masked, and check it works
  key path    print the config file that key set writes to
  key rm      remove api.key / api.keyCommand from the config
  spend       show today's and this month's spend against the caps, by tool
  models      list the models your key can use
  ask         ask SPEC questions about FILE or stdin, print the answers
  raw         POST a request JSON (FILE or stdin) as-is, print the response JSON`
	p.Notes = `SPEC:  name: question            yes/no (Noul)
       name: question [a|b|c]    pick one option (Choice); options may be a=description
       name: question <lo|mid|hi> rate on ordered levels (Score), lowest first

Config names are section.key or section.subsection.key, e.g. api.model,
defaults.progress, limits.daily, tool.grev.about, model.jev-1.14.0.price.

The key lives in ~/.grevconfig (api.key, mode 0600, or api.keyCommand for a
password manager). For CI, containers and services, TYPESAFE_API_KEY,
TYPESAFE_API_KEY_FILE and $CREDENTIALS_DIRECTORY/typesafe_api_key take
precedence, in that order.`
	p.ExitStatus = `0 ok, 1 key check failed or config key not found, 2 error,
4 declined or over a spend limit, 130 interrupted.`
	p.Examples = []string{
		`jev key set`,
		`jev config set api.keyCommand 'pass show typesafe/api'`,
		`jev config set limits.daily 5`,
		`jev config list --show-origin`,
		`jev spend`,
		`echo 'I was charged twice' | jev ask -q 'refund: asks for a refund' -q 'dept: team? [billing|tech]'`,
	}
	stateFile := p.Str('S', "state-file", "FILE", "", "ask: read the state from FILE")
	stateText := p.Str(0, "state", "TEXT", "", "ask: use TEXT as the state")
	specs := p.List('q', "question", "SPEC", "ask: a question (repeatable)")
	asJSON := p.Flag(0, "json", "ask: print the raw answers as JSON")
	noCheck := p.Flag(0, "no-check", "key set/status: don't call the API to verify the key")
	showOrigin := p.Flag(0, "show-origin", "config list: show file:line of every value")
	showSecret := p.Flag(0, "show-secret", "config list/get: show api.key unmasked")
	days := p.Int(0, "days", "N", 7, "spend: show the last N days (default 7)")
	args := t.Parse()
	if len(args) == 0 {
		p.Usagef("missing command")
	}
	switch args[0] {
	case "config":
		configCmd(t, args[1:], *showOrigin, *showSecret)
	case "key":
		if len(args) != 2 {
			p.Usagef("usage: jev key set|import|status|path|rm")
		}
		keyCmd(t, args[1], !*noCheck)
	case "spend":
		spend(t, *days)
	case "models":
		models(t)
	case "ask":
		ask(t, args[1:], *stateFile, *stateText, *specs, *asJSON)
	case "raw":
		raw(t, args[1:])
	default:
		p.Usagef("unknown command %q", args[0])
	}
	t.Exit(cli.ExitYes)
}

func configCmd(t *cli.Tool, args []string, showOrigin, showSecret bool) {
	if len(args) == 0 {
		t.P.Usagef("usage: jev config list|get|set|unset|edit|path")
	}
	cfg := t.Config()
	mask := func(v config.Value) string {
		if v.Section == "api" && v.Key == "key" && !showSecret {
			return jev.Key{Value: v.Raw}.Masked()
		}
		return v.Raw
	}
	want := func(n int) {
		if len(args) != n {
			t.P.Usagef("usage: jev config %s", map[string]string{
				"get": "get NAME", "set": "set NAME VALUE", "unset": "unset NAME"}[args[0]])
		}
	}
	name := func() (string, string, string) {
		sec, sub, key, err := config.SplitName(args[1])
		if err != nil {
			t.P.Usagef("%v", err)
		}
		if _, ok := config.Lookup(sec, sub, key); !ok {
			t.Warnf("warning: %s is not a known config key (see grevconfig(5))", args[1])
		}
		return sec, sub, key
	}
	switch args[0] {
	case "list":
		for _, v := range cfg.Values {
			if showOrigin {
				fmt.Fprintf(t.Output(), "%s\t%s=%s\n", v.Origin(), v.Name(), mask(v))
			} else {
				fmt.Fprintf(t.Output(), "%s=%s\n", v.Name(), mask(v))
			}
		}
	case "get":
		want(2)
		sec, sub, key := name()
		v, ok := cfg.Get(sec, sub, key)
		if !ok {
			t.Exit(cli.ExitNo)
		}
		fmt.Fprintln(t.Output(), mask(v))
	case "set":
		want(3)
		sec, sub, key := name()
		if k, ok := config.Lookup(sec, sub, key); ok {
			if err := config.CheckValue(k, args[2]); err != nil {
				t.Fatalf("%s: %v", args[1], err)
			}
		}
		path := config.WritePath()
		if err := config.Set(path, sec, sub, key, args[2]); err != nil {
			t.Fatalf("%v", err)
		}
		if sec == "api" && config.Norm(key) == "key" {
			os.Chmod(path, 0o600)
		}
	case "unset":
		want(2)
		sec, sub, key := name()
		if err := config.Unset(config.WritePath(), sec, sub, key); err != nil {
			t.Warnf("%v", err)
			t.Exit(cli.ExitNo)
		}
	case "path":
		fmt.Fprintln(t.Output(), config.WritePath())
	case "edit":
		editor := os.Getenv("VISUAL")
		if editor == "" {
			editor = os.Getenv("EDITOR")
		}
		if editor == "" {
			editor = "vi"
			if runtime.GOOS == "windows" {
				editor = "notepad"
			}
		}
		cmd := exec.Command(editor, config.WritePath())
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v", editor, err)
		}
		if _, err := config.Load(); err != nil {
			t.Fatalf("the config now has an error: %v", err)
		}
	default:
		t.P.Usagef("unknown config command %q", args[0])
	}
}

func keyCmd(t *cli.Tool, sub string, check bool) {
	path := config.WritePath()
	switch sub {
	case "path":
		fmt.Println(path)
	case "rm":
		removed := false
		for _, k := range []string{"key", "keyCommand"} {
			if config.Unset(path, "api", "", k) == nil {
				removed = true
				fmt.Fprintf(os.Stderr, "removed api.%s from %s\n", k, path)
			}
		}
		if !removed {
			t.Warnf("no key is set in %s", path)
			t.Exit(cli.ExitNo)
		}
	case "set":
		key := readKey(t)
		storeKey(t, check, path, key)
	case "import":
		k, err := jev.LoadKey(jev.KeyConfig{})
		if err != nil {
			t.Fatalf("nothing to import: %v", err)
		}
		fmt.Fprintf(os.Stderr, "importing %s from %s\n", k.Masked(), k.Source)
		storeKey(t, check, path, k.Value)
		fmt.Fprintf(os.Stderr, "you can now unset %s", k.Source)
		if k.Path != "" {
			fmt.Fprintf(os.Stderr, " and delete %s", k.Path)
		}
		fmt.Fprintln(os.Stderr)
	case "status":
		k, err := jev.LoadKey(t.KeyConfig())
		if err != nil {
			t.Fatalf("%v", err)
		}
		fmt.Printf("source: %s\n", k.Source)
		if k.Path != "" {
			fmt.Printf("file:   %s\n", k.Path)
		}
		fmt.Printf("key:    %s (%d chars)\n", k.Masked(), len(k.Value))
		if k.Warn != "" {
			fmt.Printf("warning: %s\n", k.Warn)
		}
		if check {
			n, err := verify(t, k.Value)
			if err != nil {
				fmt.Printf("check:  FAILED: %v\n", err)
				t.Exit(cli.ExitNo)
			}
			fmt.Printf("check:  ok (%d models available)\n", n)
		}
	default:
		t.P.Usagef("unknown key command %q", sub)
	}
}

// readKey reads a key from the terminal (hidden) or from stdin.
func readKey(t *cli.Tool) string {
	var raw string
	var err error
	if cli.IsTerminal(os.Stdin) {
		fmt.Fprint(os.Stderr, "TypeSafe API key (input hidden): ")
		raw, err = cli.ReadSecret(os.Stdin)
		fmt.Fprintln(os.Stderr)
	} else {
		raw, err = cli.ReadLineFrom(os.Stdin)
		if err == io.EOF {
			err = nil
		}
	}
	if err != nil {
		t.Fatalf("reading key: %v", err)
	}
	key, err := jev.CleanKey(raw)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return key
}

// storeKey checks key against the API and stores it as api.key in the
// config, mode 0600, replacing any other key setting.
func storeKey(t *cli.Tool, check bool, path, key string) {
	if check {
		if _, err := verify(t, key); err != nil {
			t.Fatalf("key check failed: %v (use --no-check to store it anyway)", err)
		}
	}
	config.Unset(path, "api", "", "keyCommand")
	if err := config.Set(path, "api", "", "key", key); err != nil {
		t.Fatalf("%v", err)
	}
	os.Chmod(path, 0o600)
	fmt.Fprintf(os.Stderr, "stored key %s as api.key in %s (mode 0600)\n", jev.Key{Value: key}.Masked(), path)
	if os.Getenv("TYPESAFE_API_KEY") != "" || os.Getenv("TYPESAFE_API_KEY_FILE") != "" {
		fmt.Fprintln(os.Stderr, "note: TYPESAFE_API_KEY or TYPESAFE_API_KEY_FILE is set and still takes precedence")
	}
}

func verify(t *cli.Tool, key string) (int, error) {
	c := jev.NewClient(key, cli.UserAgent("jev"))
	if os.Getenv("TYPESAFE_BASE_URL") == "" {
		if ep := t.Config().Str("api", "", "endpoint"); ep != "" {
			c.BaseURL = strings.TrimRight(ep, "/")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ms, err := c.Models(ctx)
	return len(ms), err
}

func spend(t *cli.Tool, days int) {
	path := cli.LedgerPath()
	if path == "" {
		t.Fatalf("the spend ledger is turned off (GREV_LEDGER is empty)")
	}
	entries, err := cli.ReadLedger(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("%v", err)
	}
	cfg := t.Config()
	capOf := func(k string) string {
		v, ok := cfg.Get("limits", "", k)
		if !ok || strings.EqualFold(v.Raw, "off") {
			return "no cap"
		}
		return "cap $" + v.Raw
	}
	now := time.Now()
	day, month := now.Format("2006-01-02"), now.Format("2006-01")
	var dayCost, monCost float64
	byTool := map[string]float64{}
	byDay := map[string]float64{}
	for _, e := range entries {
		if e.Day == day {
			dayCost += e.Cost
		}
		if strings.HasPrefix(e.Day, month) {
			monCost += e.Cost
			byTool[e.Tool] += e.Cost
		}
		byDay[e.Day] += e.Cost
	}
	out := t.Output()
	fmt.Fprintf(out, "today       %s  $%.4f  (%s)\n", day, dayCost, capOf("daily"))
	fmt.Fprintf(out, "this month  %s     $%.4f  (%s)\n", month, monCost, capOf("monthly"))
	if len(byTool) > 0 {
		tools := make([]string, 0, len(byTool))
		for k := range byTool {
			tools = append(tools, k)
		}
		sort.Slice(tools, func(i, j int) bool { return byTool[tools[i]] > byTool[tools[j]] })
		fmt.Fprintln(out, "\nthis month by tool:")
		for _, k := range tools {
			fmt.Fprintf(out, "  %-8s $%.4f\n", k, byTool[k])
		}
	}
	fmt.Fprintf(out, "\nlast %d days:\n", days)
	for i := days - 1; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		fmt.Fprintf(out, "  %s  $%.4f\n", d, byDay[d])
	}
	fmt.Fprintf(out, "\nledger: %s\n", path)
}

func models(t *cli.Tool) {
	e := t.Engine()
	ms, err := e.Client.Models(t.Ctx())
	if err != nil {
		t.Exit(t.Finish(err))
	}
	for _, m := range ms {
		fmt.Fprintf(t.Out, "%-14s %-10.10s  %s\n", m.Name, m.ReleaseDate, m.Description)
	}
}

func ask(t *cli.Tool, args []string, stateFile, stateText string, specs []string, asJSON bool) {
	if len(specs) == 0 {
		t.P.Usagef("ask needs at least one -q SPEC")
	}
	var state string
	switch {
	case stateText != "":
		state = stateText
	case stateFile != "":
		s, err := cli.ReadInput(stateFile)
		if err != nil {
			t.Fatalf("%v", err)
		}
		state = s
	default:
		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		s, err := cli.ReadInput(name)
		if err != nil {
			t.Fatalf("%v", err)
		}
		state = s
	}
	var names []string
	var qs []jev.Question
	seen := map[string]bool{}
	for i, s := range specs {
		sp, err := cli.ParseSpec(s, i+1)
		if err != nil {
			t.P.Usagef("-q: %v", err)
		}
		if seen[sp.Name] {
			t.P.Usagef("-q: duplicate question name %q", sp.Name)
		}
		seen[sp.Name] = true
		names = append(names, sp.Name)
		qs = append(qs, sp.Q)
	}
	ans := t.Ask(state, names, qs)
	if asJSON {
		ordered := jev.Obj{}
		for _, n := range names {
			ordered = append(ordered, jev.KV{K: n, V: ans[n]})
		}
		b, _ := json.MarshalIndent(ordered, "", "  ")
		fmt.Fprintln(t.Out, string(b))
		return
	}
	w := 0
	for _, n := range names {
		w = max(w, len(n))
	}
	for _, n := range names {
		fmt.Fprintf(t.Out, "%-*s  %s\n", w, n, describe(ans[n]))
	}
}

// describe renders an answer on one line: the value, then the distribution.
func describe(a jev.Answer) string {
	switch a.Type {
	case jev.TypeNoul:
		return fmt.Sprintf("noul    %.2f", a.Noul)
	case jev.TypeChoice:
		return fmt.Sprintf("choice  %s (conf %.2f)  %s", a.Choice, a.Confidence, dist(a.Probabilities, nil))
	case jev.TypeScore:
		return fmt.Sprintf("score   %.2f (conf %.2f)  %s", a.Score, a.Confidence, dist(a.Probabilities, a.Legend))
	}
	return a.Type
}

func dist(p map[string]float64, legend map[string]string) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	if legend != nil {
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	} else {
		sort.Slice(keys, func(i, j int) bool { return p[keys[i]] > p[keys[j]] })
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		label := k
		if l, ok := legend[k]; ok {
			label = k + ":" + cli.Clip(l, 18)
		}
		parts[i] = fmt.Sprintf("%s %.2f", label, p[k])
	}
	return strings.Join(parts, " · ")
}

func raw(t *cli.Tool, args []string) {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	body, err := cli.ReadInput(name)
	if err != nil {
		t.Fatalf("%v", err)
	}
	var req map[string]any
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("request is not a JSON object: %v", err)
	}
	e := t.Engine()
	// Send the body as written: re-encoding it would sort every object's
	// keys, and key order is part of what the model reads. Only a missing
	// "model" is spliced in, at the front.
	b := []byte(strings.TrimSpace(body))
	if _, ok := req["model"]; !ok {
		m, _ := json.Marshal(e.Model)
		rest := strings.TrimSpace(string(b[1:]))
		sep := ","
		if strings.HasPrefix(rest, "}") {
			sep = ""
		}
		b = []byte(`{"model":` + string(m) + sep + rest)
	}
	nq := 0
	if qs, ok := req["questions"].(map[string]any); ok {
		nq = len(qs)
	}
	t.Confirm(jev.Quote{Questions: nq, Requests: 1, EstTokens: jev.EstRequest(b)})
	out, err := e.Client.Raw(t.Ctx(), "POST", "/v1/systemone", b)
	if err != nil {
		t.Exit(t.Finish(err))
	}
	var resp jev.Response
	if json.Unmarshal(out, &resp) == nil {
		e.Stats.Record(resp.Model, resp.Usage, nq)
	}
	t.Out.Write(out)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		t.Out.WriteString("\n")
	}
}
