package jev

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const secret = "tsk-SECRET-0123456789abcdefwxyz"

// clearKeyEnv unsets every credential source for the duration of the test.
func clearKeyEnv(t *testing.T) string {
	t.Helper()
	for _, k := range []string{"TYPESAFE_API_KEY", "TYPESAFE_API_KEY_FILE", "CREDENTIALS_DIRECTORY"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	return home
}

func writeKey(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	os.Chmod(path, mode)
}

func TestLoadKeyOrder(t *testing.T) {
	home := clearKeyEnv(t)
	sysd := filepath.Join(home, "creds")
	envFile := filepath.Join(home, "env.key")
	writeKey(t, filepath.Join(sysd, "typesafe_api_key"), "sysd-key-2222", 0o644)
	writeKey(t, envFile, "  envfile-key-3333 \n", 0o600)
	conf := filepath.Join(home, ".grevconfig")
	writeKey(t, conf, "[api]\n\tkey = x\n", 0o600)
	kc := KeyConfig{Key: "config-key-1111", Name: "api.key", Origin: "~/.grevconfig:2", ConfigFile: conf}

	if _, err := LoadKey(KeyConfig{}); !errors.Is(err, ErrNoKey) {
		t.Fatalf("want ErrNoKey, got %v", err)
	}
	k, err := LoadKey(kc)
	if err != nil || k.Value != "config-key-1111" || k.Source != "api.key (~/.grevconfig:2)" || k.Warn != "" {
		t.Fatalf("config: %+v %v", k, err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", sysd)
	k, err = LoadKey(kc)
	if err != nil || k.Value != "sysd-key-2222" || k.Source != SrcSystemd || k.Warn != "" {
		t.Fatalf("systemd beats config (no perm check): %+v %v", k, err)
	}
	t.Setenv("TYPESAFE_API_KEY_FILE", envFile)
	k, err = LoadKey(kc)
	if err != nil || k.Value != "envfile-key-3333" || k.Source != SrcEnvFile {
		t.Fatalf("env file: %+v %v", k, err)
	}
	os.Unsetenv("TYPESAFE_API_KEY_FILE")
	t.Setenv("TYPESAFE_API_KEY", "env-key-5555")
	k, err = LoadKey(kc)
	if err != nil || k.Value != "env-key-5555" || k.Source != SrcEnv || k.Path != "" {
		t.Fatalf("env: %+v %v", k, err)
	}
	t.Setenv("TYPESAFE_API_KEY_FILE", envFile)
	if _, err := LoadKey(kc); err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("env + env file should conflict, got %v", err)
	}
	// ~ in TYPESAFE_API_KEY_FILE.
	os.Unsetenv("TYPESAFE_API_KEY")
	writeKey(t, filepath.Join(home, "k"), "tilde-key-7777", 0o600)
	t.Setenv("TYPESAFE_API_KEY_FILE", "~/k")
	if k, err := LoadKey(KeyConfig{}); err != nil || k.Value != "tilde-key-7777" {
		t.Fatalf("tilde: %+v %v", k, err)
	}
}

func TestLoadKeyConfigPermsAndCommand(t *testing.T) {
	home := clearKeyEnv(t)
	conf := filepath.Join(home, ".grevconfig")
	writeKey(t, conf, "[api]\n\tkey = x\n", 0o644)
	k, err := LoadKey(KeyConfig{Key: "inline-key-8888", Name: "api.key", Origin: "~/.grevconfig:2", ConfigFile: conf})
	if err != nil || k.Value != "inline-key-8888" {
		t.Fatalf("inline key: %+v %v", k, err)
	}
	if runtime.GOOS != "windows" && !strings.Contains(k.Warn, "chmod 600") {
		t.Fatalf("a config holding the key with loose perms should warn: %q", k.Warn)
	}
	if strings.Contains(k.Warn, "inline-key-8888") {
		t.Fatal("warning leaks the key")
	}
	os.Chmod(conf, 0o600)
	if k, _ := LoadKey(KeyConfig{Key: "inline-key-8888", ConfigFile: conf}); k.Warn != "" {
		t.Fatalf("0600 config should not warn: %q", k.Warn)
	}
	if _, err := LoadKey(KeyConfig{Key: "bad key", Name: "api.key", Origin: "x:1"}); err == nil || strings.Contains(err.Error(), "bad key") {
		t.Fatalf("bad inline key: %v", err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("sh-specific commands")
	}
	k, err = LoadKey(KeyConfig{Command: "echo cmd-key-9999", Name: "api.keyCommand", Origin: "x:1"})
	if err != nil || k.Value != "cmd-key-9999" || k.Source != "api.keyCommand (x:1)" {
		t.Fatalf("keyCommand: %+v %v", k, err)
	}
	_, err = LoadKey(KeyConfig{Command: "echo leaked-secret-output; exit 3", Name: "api.keyCommand", Origin: "x:1"})
	if err == nil || strings.Contains(err.Error(), "leaked-secret-output") {
		t.Fatalf("failing keyCommand: %v", err)
	}
}

func TestPermWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no mode bits")
	}
	home := clearKeyEnv(t)
	p := filepath.Join(home, "loose.key")
	writeKey(t, p, secret, 0o644)
	t.Setenv("TYPESAFE_API_KEY_FILE", p)
	k, err := LoadKey(KeyConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(k.Warn, "chmod 600") || strings.Contains(k.Warn, secret) {
		t.Fatalf("warning = %q", k.Warn)
	}
	os.Chmod(p, 0o600)
	if k, _ := LoadKey(KeyConfig{}); k.Warn != "" {
		t.Fatalf("0600 should not warn: %q", k.Warn)
	}
}

func TestCleanKey(t *testing.T) {
	if v, err := CleanKey("  \t" + secret + "\r\n"); err != nil || v != secret {
		t.Fatalf("trim: %q %v", v, err)
	}
	for in, want := range map[string]string{
		"":                 "empty",
		"   \n":            "empty",
		"tsk-abc def":      "whitespace",
		"tsk-abc\x01def":   "control",
		"tsk-abc\x7fdef":   "control",
		"tsk-abcé":         "non-ASCII",
		"tsk-abc\ndef-xyz": "whitespace",
	} {
		_, err := CleanKey(in)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("CleanKey(%q) = %v, want error containing %q", in, err, want)
		}
		if err != nil && strings.TrimSpace(in) != "" && strings.Contains(err.Error(), strings.TrimSpace(in)) {
			t.Errorf("error leaks the key: %v", err)
		}
	}
}

func TestKeyErrorsNeverLeak(t *testing.T) {
	home := clearKeyEnv(t)
	p := filepath.Join(home, "bad.key")
	writeKey(t, p, "tsk-"+secret+" trailing-part", 0o600)
	t.Setenv("TYPESAFE_API_KEY_FILE", p)
	_, err := LoadKey(KeyConfig{})
	if err == nil {
		t.Fatal("key with inner whitespace should fail")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "trailing-part") {
		t.Fatalf("error leaks key content: %v", err)
	}
	os.Unsetenv("TYPESAFE_API_KEY_FILE")
	t.Setenv("TYPESAFE_API_KEY", secret+" x")
	_, err = LoadKey(KeyConfig{})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("env error: %v", err)
	}
	if m := (Key{Value: secret}).Masked(); m != "…wxyz" {
		t.Fatalf("Masked = %q", m)
	}
	if m := (Key{Value: "short"}).Masked(); m != "…" {
		t.Fatalf("short key Masked = %q", m)
	}
}

func TestOrderedMarshal(t *testing.T) {
	b, _ := json.Marshal(Opts{{"z", "last letter"}, {"a", nil}, {"m", map[string]any{"x": 1}}})
	if string(b) != `{"z":"last letter","a":null,"m":{"x":1}}` {
		t.Fatalf("Opts: %s", b)
	}
	b, _ = json.Marshal(Obj{{"text", "t"}, {"question", "q"}})
	if string(b) != `{"text":"t","question":"q"}` {
		t.Fatalf("Obj: %s", b)
	}
	b, _ = json.Marshal(Obj{})
	if string(b) != `{}` {
		t.Fatalf("empty Obj: %s", b)
	}
	q := Choice("which?", Opts{{"b", nil}, {"a", "desc"}})
	b, _ = json.Marshal(q)
	if string(b) != `{"type":"choice","instructions":"which?","criteria":{"b":null,"a":"desc"}}` {
		t.Fatalf("Choice: %s", b)
	}
	b, _ = json.Marshal(Noul("q", nil, nil))
	if string(b) != `{"type":"noul","instructions":"q"}` {
		t.Fatalf("Noul without criteria: %s", b)
	}
	b, _ = json.Marshal(Noul("q", "y", nil))
	if !strings.Contains(string(b), `"criteria":{"false":null,"true":"y"}`) {
		t.Fatalf("Noul with criteria: %s", b)
	}
}

func TestAnswerMarshal(t *testing.T) {
	b, _ := json.Marshal(Answer{Type: TypeNoul, Noul: 0})
	if string(b) != `{"noul":0,"type":"noul"}` {
		t.Fatalf("a firm no must keep noul:0, got %s", b)
	}
	b, _ = json.Marshal(Answer{Type: TypeChoice, Choice: "a", Confidence: 0.5, Probabilities: map[string]float64{"a": 1}})
	var m map[string]any
	json.Unmarshal(b, &m)
	if _, has := m["noul"]; has || m["choice"] != "a" || m["confidence"] != 0.5 {
		t.Fatalf("choice: %s", b)
	}
	b, _ = json.Marshal(Answer{Type: TypeScore, Score: 0, Legend: map[string]string{"0": "low"}})
	json.Unmarshal(b, &m)
	if _, has := m["score"]; !has {
		t.Fatalf("score 0 dropped: %s", b)
	}
}

func TestPricing(t *testing.T) {
	t.Setenv("GREV_PRICE_PER_MTOK", "")
	os.Unsetenv("GREV_PRICE_PER_MTOK")
	for _, m := range []string{"jev-1.13.0", "jev-1.13", "jev-latest", "jev-preview", "jev-1.12"} {
		if p, ok := PriceOf(m); !ok || p.In != 0.042 || p.Out != 0 {
			t.Errorf("PriceOf(%s) = %+v %v", m, p, ok)
		}
	}
	if _, ok := PriceOf("gpt-9"); ok {
		t.Error("unknown model should be unknown")
	}
	c, ok := Cost("jev-1.13.0", Usage{InputTokens: 1_000_000, OutputTokens: 5000})
	if !ok || c < 0.0419 || c > 0.0421 {
		t.Errorf("Cost = %v %v", c, ok)
	}
	t.Setenv("GREV_PRICE_PER_MTOK", "1.5")
	if p, ok := PriceOf("anything"); !ok || p.In != 1.5 {
		t.Errorf("override: %+v %v", p, ok)
	}
}

func TestModelDefault(t *testing.T) {
	t.Setenv("TYPESAFE_DEFAULT_MODEL", "")
	os.Unsetenv("TYPESAFE_DEFAULT_MODEL")
	if Model("") != DefaultModel || Model("x") != "x" {
		t.Fatal("Model defaults")
	}
	t.Setenv("TYPESAFE_DEFAULT_MODEL", "jev-preview")
	if Model("") != "jev-preview" || Model("x") != "x" {
		t.Fatal("TYPESAFE_DEFAULT_MODEL")
	}
}

func TestRetryAfter(t *testing.T) {
	h := http.Header{}
	h.Set("retry-after-ms", "250")
	h.Set("Retry-After", "9")
	if d := retryAfter(h); d != 250*time.Millisecond {
		t.Errorf("ms header should win: %v", d)
	}
	h = http.Header{}
	h.Set("Retry-After", "2")
	if d := retryAfter(h); d != 2*time.Second {
		t.Errorf("seconds: %v", d)
	}
	h.Set("Retry-After", time.Now().Add(3*time.Second).UTC().Format(http.TimeFormat))
	if d := retryAfter(h); d < time.Second || d > 3*time.Second {
		t.Errorf("date: %v", d)
	}
	h.Set("Retry-After", "600")
	if d := retryAfter(h); d != 0 {
		t.Errorf("too long should fall back to backoff: %v", d)
	}
	if d := retryAfter(http.Header{}); d != 0 {
		t.Errorf("none: %v", d)
	}
	for i := 0; i < 10; i++ {
		d := backoff(i)
		if d <= 0 || d > backoffMax {
			t.Errorf("backoff(%d) = %v", i, d)
		}
	}
	if d := backoff(0); d < 375*time.Millisecond || d > 500*time.Millisecond {
		t.Errorf("backoff(0) = %v", d)
	}
}

func TestAPIError(t *testing.T) {
	e := &APIError{Status: 422, Body: `{"detail":[{"loc":["questions"],"msg":"bad"}]}`, RequestID: "req_1"}
	s := e.Error()
	if !strings.Contains(s, "422") || !strings.Contains(s, `"msg":"bad"`) || !strings.Contains(s, "req_1") {
		t.Errorf("422: %s", s)
	}
	s = (&APIError{Status: 401, Body: `{"detail":"invalid key"}`}).Error()
	if !strings.Contains(s, "jev key status") {
		t.Errorf("401 should hint at credentials: %s", s)
	}
	s = (&APIError{Status: 500, Body: strings.Repeat("x", 1000)}).Error()
	if len(s) > 500 {
		t.Errorf("long bodies should be truncated: %d chars", len(s))
	}
}

func TestParseJ(t *testing.T) {
	for in, want := range map[string]struct {
		n        int
		adaptive bool
		bad      bool
	}{
		"": {DefaultJ, false, false}, "8": {8, false, false}, "max": {0, true, false},
		"MAX": {0, true, false}, "0": {bad: true}, "-3": {bad: true}, "x": {bad: true},
	} {
		n, a, err := ParseJ(in)
		if (err != nil) != want.bad || (!want.bad && (n != want.n || a != want.adaptive)) {
			t.Errorf("ParseJ(%q) = %d %v %v", in, n, a, err)
		}
	}
}

func TestPlan(t *testing.T) {
	e := &Engine{Model: DefaultModel, Stats: NewStats(), MaxQ: 3}
	a, b := NewState("state A"), NewState("state B")
	mk := func(st *State, n int) []*Item {
		var out []*Item
		for i := 0; i < n; i++ {
			out = append(out, NewItem(st, Noul("q?", nil, nil), i))
		}
		return out
	}
	q := e.Plan(mk(a, 10))
	if q.Requests != 4 || q.Questions != 10 || q.TooBig != 0 || q.EstTokens <= 0 {
		t.Fatalf("MaxQ packing: %+v", q)
	}
	// A new state always starts a new request, even when the old one had room.
	var items []*Item
	items = append(items, mk(a, 2)...)
	items = append(items, mk(b, 2)...)
	items = append(items, mk(a, 1)...)
	e.MaxQ = 100
	if q := e.Plan(items); q.Requests != 3 {
		t.Fatalf("state boundaries: %+v", q)
	}
	// Token budget: questions of ~2k tokens each, no MaxQ limit.
	big := strings.Repeat("word ", 1600)
	var heavy []*Item
	for i := 0; i < 60; i++ {
		heavy = append(heavy, NewItem(a, Noul(big, nil, nil), i))
	}
	e.MaxQ = 1000
	q = e.Plan(heavy)
	per := heavy[0].Est()
	if q.Requests < 60*per/int(CtxTotal*safety) || q.Requests > 60*per/int(CtxTotal*safety)+2 {
		t.Fatalf("token budget packing: %+v (per item %d)", q, per)
	}
	// An item that can't fit even alone is counted, not packed.
	huge := NewState(strings.Repeat("x", 120_000))
	q = e.Plan([]*Item{NewItem(huge, Noul("q", nil, nil), 0), NewItem(a, Noul("q", nil, nil), 1)})
	if q.TooBig != 1 || q.Requests != 1 || q.Questions != 1 {
		t.Fatalf("TooBig: %+v", q)
	}
	s := e.Stats.Snapshot()
	if s.PlanQ == 0 || s.PlanReqs == 0 || s.PlanEst == 0 {
		t.Fatalf("Plan should record totals in Stats: %+v", s)
	}
}

func TestEstimates(t *testing.T) {
	st := NewState("hello world")
	it := NewItem(st, Noul("Is it true that `text` is a greeting?", nil, nil), nil)
	if st.Est() < 3 || st.Est() > 10 || it.Est() < qOverhead+10 || it.Est() > qOverhead+40 {
		t.Fatalf("state est %d, item est %d", st.Est(), it.Est())
	}
	if EstRequest([]byte("{}")) != reqOverhead+EstText("{}") {
		t.Fatal("EstRequest")
	}
}

func TestStatsProjection(t *testing.T) {
	t.Setenv("GREV_PRICE_PER_MTOK", "")
	os.Unsetenv("GREV_PRICE_PER_MTOK")
	st := NewStats()
	st.s.Model = DefaultModel
	st.plan(Quote{Questions: 100, Requests: 2, EstTokens: 2_000_000})
	s := st.Snapshot()
	if p := s.Projected(); p < 0.0839 || p > 0.0841 {
		t.Fatalf("before any request, projection = plan × price: %v", p)
	}
	// First request comes back with half the estimated tokens.
	st.start(1_000_000)
	st.finishOK(50, 1_000_000, 500_000, 0.021, true, DefaultModel, 0)
	s = st.Snapshot()
	if s.Ratio() != 0.5 {
		t.Fatalf("ratio = %v", s.Ratio())
	}
	if p := s.Projected(); p < 0.0419 || p > 0.0421 {
		t.Fatalf("calibrated projection = %v, want ≈0.042", p)
	}
	st.start(1_000_000)
	st.finishOK(50, 1_000_000, 500_000, 0.021, true, DefaultModel, 0)
	s = st.Snapshot()
	if p := s.Projected(); p < s.Cost-1e-9 || p > s.Cost+1e-9 {
		t.Fatalf("finished projection %v != cost %v", p, s.Cost)
	}
	if s.DoneQ != 100 || s.Reqs != 2 || s.Inflight != 0 || s.EstFly != 0 {
		t.Fatalf("counters: %+v", s)
	}
	if (Snap{}).Projected() != -1 {
		t.Fatal("unknown totals should project -1")
	}
}
