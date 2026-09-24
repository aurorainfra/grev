package config

import (
	"fmt"
	"strings"
)

// Key documents one config key. Sub is "" for keys of a plain section, or a
// placeholder such as "NAME" for sections that take a subsection.
type Key struct {
	Section string
	Sub     string
	Name    string // canonical spelling (camelCase)
	Type    string // string, path, command, bool, usd, number, enum, option
	Default string
	Doc     string
}

// Schema lists every known key. It drives validation, `jev config` help and
// the grevconfig(5) man page.
var Schema = []Key{
	{"api", "", "key", "string", "", "The API key. `jev key set` stores it here and keeps the file mode 0600."},
	{"api", "", "keyCommand", "command", "", "Instead of api.key: run this command and use its standard output as the key (sh -c; cmd /C on Windows), e.g. `pass show typesafe/api`."},
	{"api", "", "endpoint", "string", "https://api.typesafe.ai", "API root. TYPESAFE_BASE_URL overrides it."},
	{"api", "", "model", "string", "jev-1.13.0", "Model id. -M and TYPESAFE_DEFAULT_MODEL override it."},

	{"defaults", "", "progress", "enum", "never", "Show the -p overlay: always, never, or auto (when stderr is a terminal)."},
	{"defaults", "", "jobs", "option", "4", "Parallel requests: a number or max, as -J."},
	{"defaults", "", "maxCost", "usd", "off", "Per-run budget in USD, as --max-cost."},
	{"defaults", "", "confirmAbove", "usd", "1.00", "Ask before runs quoted above this many USD, as --confirm-above; 0 always asks, off never."},
	{"defaults", "", "quote", "bool", "false", "Always show the quote and ask, as -Q."},

	{"tool", "NAME", "OPTION", "option", "", "Default for any long option of tool NAME, e.g. [tool \"grev\"] about = application logs."},

	{"limits", "", "daily", "usd", "off", "Spend cap per calendar day across all tools, tracked in the local spend ledger."},
	{"limits", "", "monthly", "usd", "off", "Spend cap per calendar month across all tools."},
	{"limits", "", "rpm", "number", "1200", "Request rate ceiling (requests per minute). GREV_RPM overrides it."},
	{"limits", "", "tps", "number", "250000", "Token rate ceiling (tokens per second). GREV_TPS overrides it."},
	{"limits", "", "questionsPerRequest", "number", "128", "Most questions packed into one request. GREV_MAX_Q overrides it."},

	{"model", "ID", "price", "usd", "", "Price in USD per million input tokens, for models the built-in table doesn't know."},

	{"include", "", "path", "path", "", "Read another config file here (relative to this file); may repeat."},
}

// Lookup finds the schema entry for section[.sub].key.
func Lookup(section, sub, key string) (Key, bool) {
	section, key = Norm(section), Norm(key)
	for _, k := range Schema {
		if Norm(k.Section) != section {
			continue
		}
		if (k.Sub == "") != (sub == "") {
			continue
		}
		if k.Name == "OPTION" || Norm(k.Name) == key {
			return k, true
		}
	}
	return Key{}, false
}

// Check validates values against the schema and returns one warning per
// problem, each prefixed with the value's file:line. Tool option names are
// checked by the tools themselves.
func (c *Config) Check() []string {
	if c == nil {
		return nil
	}
	var warns []string
	have := map[string]Value{}
	for _, v := range c.Values {
		k, ok := Lookup(v.Section, v.Sub, v.Key)
		if !ok {
			warns = append(warns, fmt.Sprintf("%s: unknown config key %s", v.Origin(), v.Name()))
			continue
		}
		if err := checkType(k, v.Raw); err != nil {
			warns = append(warns, fmt.Sprintf("%s: %s: %v", v.Origin(), v.Name(), err))
		}
		if v.Section == "api" && (v.Key == "key" || v.Key == "keycommand") {
			have[v.Key] = v
		}
	}
	if len(have) > 1 {
		var names []string
		for _, v := range have {
			names = append(names, v.Name()+" ("+v.Origin()+")")
		}
		warns = append(warns, "both api.key and api.keyCommand are set: "+strings.Join(names, ", "))
	}
	return warns
}

func checkType(k Key, raw string) error {
	switch k.Type {
	case "bool":
		_, err := Bool(raw)
		return err
	case "usd", "number":
		if strings.EqualFold(raw, "off") && k.Type == "usd" {
			return nil
		}
		f, err := Float(raw)
		if err == nil && f < 0 {
			return fmt.Errorf("must not be negative")
		}
		return err
	case "enum":
		switch strings.ToLower(raw) {
		case "always", "never", "auto", "true", "false":
			return nil
		}
		return fmt.Errorf("want always, never or auto, got %q", raw)
	}
	return nil
}

// CheckValue validates a value for key k (used by `jev config set`).
func CheckValue(k Key, raw string) error { return checkType(k, raw) }
