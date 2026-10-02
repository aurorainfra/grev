package jev

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProtocolFor(t *testing.T) {
	for _, c := range []struct {
		override, endpoint, model, want string
	}{
		{"", "https://api.typesafe.ai", "jev-1.13.0", "typesafe"},
		{"", "https://api.fastino.ai/", "", "fastino"},
		{"", "https://fastino.ai", "", "fastino"},
		{"", "https://notfastino.ai", "", "typesafe"},
		{"", "https://openrouter.ai/api", "fastino/GLiDE", "fastino"},
		{"", "https://openrouter.ai/api", "typesafe/jev-1.13", "typesafe"},
		{"", "http://127.0.0.1:8080", "glide", "fastino"},
		{"typesafe", "https://api.fastino.ai", "fastino/GLiDE", "typesafe"},
		{"FASTINO", "https://gateway.example", "my-model", "fastino"},
		{"auto", "https://api.typesafe.ai", "", "typesafe"},
	} {
		if got := ProtocolFor(c.override, c.endpoint, c.model).Name(); got != c.want {
			t.Errorf("ProtocolFor(%q, %q, %q) = %s, want %s", c.override, c.endpoint, c.model, got, c.want)
		}
	}
	t.Setenv("TYPESAFE_DEFAULT_MODEL", "")
	if m := ModelFor("", Fastino); m != "fastino/GLiDE" {
		t.Errorf("Fastino's default model: %q", m)
	}
}

func TestText(t *testing.T) {
	for _, c := range []struct {
		in   any
		want string
	}{
		{"Is it vegan?", "Is it vegan?"},
		{nil, ""},
		{Obj{{"before", []string{"a", "b"}}, {"question", "Is `text` vegan?"}, {"text", "tofu\nrice"}, {"f1", 3}},
			"before: [\"a\",\"b\"]\ntext: tofu\n  rice\nf1: 3\nquestion: Is `text` vegan?"},
		{map[string]any{"b": "2", "a": "1"}, "a: 1\nb: 2"},
		{[]any{"x", 1}, `["x",1]`},
	} {
		if got := Text(c.in); got != c.want {
			t.Errorf("Text(%v) =\n%q\nwant\n%q", c.in, got, c.want)
		}
	}
}

func TestNoulCriteria(t *testing.T) {
	got := noulCriteria(map[string]any{"true": "matches", "false": nil})
	b, _ := json.Marshal(got)
	if string(b) != `{"false":"No","true":"matches"}` {
		t.Errorf("one side: %s", b)
	}
	b, _ = json.Marshal(noulCriteria(map[string]any{"true": Obj{{"k", "v"}}, "false": "no"}))
	if string(b) != `{"false":"no","true":"k: v"}` {
		t.Errorf("structured side: %s", b)
	}
}

// TestFastinoDo: the wire request has text instructions and string Noul
// criteria; expected_level comes back as Score; a GLiNER model's 404 says why.
func TestFastinoDo(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &sent)
		if strings.Contains(string(b), "GLiNER") {
			w.WriteHeader(404)
			w.Write([]byte(`{"code":"model_not_found","message":"No inference provider is configured"}`))
			return
		}
		w.Write([]byte(`{"model":"glide","answers":{"n":{"type":"noul","noul":0.9},"s":{"type":"score","score":2,"expected_level":1.6}},"usage":{"input_tokens":300,"output_tokens":50}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Key: "k", HTTP: srv.Client()}
	req := &Request{State: "s", Model: "fastino/GLiDE", Questions: map[string]Question{
		"n": Noul(Obj{{"text", "tofu"}, {"question", "Is `text` vegan?"}}, "vegan", nil),
		"s": Score("How spicy?", []any{"mild", "medium", "hot"}),
	}}
	resp, err := Fastino.Do(context.Background(), c, req)
	if err != nil {
		t.Fatal(err)
	}
	q := sent["questions"].(map[string]any)["n"].(map[string]any)
	if q["instructions"] != "text: tofu\nquestion: Is `text` vegan?" {
		t.Errorf("instructions on the wire: %#v", q["instructions"])
	}
	if cr := q["criteria"].(map[string]any); cr["true"] != "vegan" || cr["false"] != "No" {
		t.Errorf("criteria on the wire: %#v", cr)
	}
	if _, isObj := req.Questions["n"].Instructions.(Obj); !isObj {
		t.Error("the caller's request was modified")
	}
	if a := resp.Answers["s"]; a.Score != 1.6 {
		t.Errorf("Score should be expected_level 1.6, got %v", a.Score)
	}
	req.Model = "fastino/GLiNER-2.5-Decide"
	if _, err := Fastino.Do(context.Background(), c, req); err == nil || !strings.Contains(err.Error(), "use fastino/GLiDE") {
		t.Errorf("GLiNER hint: %v", err)
	}
}

func TestBill(t *testing.T) {
	st := NewState(strings.Repeat("word ", 2000))
	noul := NewItem(st, Noul("Is it?", nil, nil), nil)
	score := NewItem(st, Score("How much?", []any{"a", "b", "c", "d", "e"}), nil)
	choice := NewItem(st, Choice("Which?", Opts{{Key: "x"}, {Key: "y"}, {Key: "z"}}), nil)
	if TypeSafe.Base(st.est)+TypeSafe.Per(st.est, noul) != reqOverhead+st.est+noul.est {
		t.Error("typesafe bills the state once")
	}
	pass := Fastino.Per(st.est, noul)
	if Fastino.Base(st.est) != 0 || pass < int(0.75*float64(st.est)) {
		t.Errorf("fastino bills the state per question: %d for a %d-token state", pass, st.est)
	}
	if s := Fastino.Per(st.est, score); s < 3*pass {
		t.Errorf("a Score over 5 levels thinks: %d vs a pass of %d", s, pass)
	}
	if two := NewItem(st, Score("How much?", []any{"a", "b"}), nil); Fastino.Per(st.est, two) > pass+10 {
		t.Error("a two-level Score is one pass")
	}
	if Fastino.Per(st.est, choice) <= pass {
		t.Error("Choice options cost something")
	}
}

// TestPackingByBody: on Fastino, many questions about a large state still
// share one request (the body limit counts the state once), while the quote
// counts the state once per question.
func TestPackingByBody(t *testing.T) {
	st := NewState(strings.Repeat("word ", 8000))
	var items []*Item
	for i := 0; i < 20; i++ {
		items = append(items, NewItem(st, Noul("Is it?", nil, nil), i))
	}
	e := &Engine{Proto: Fastino, MaxQ: DefaultMaxQ, Stats: NewStats()}
	q := e.Plan(items)
	if q.Requests != 1 || q.EstTokens < 20*int(0.75*float64(st.est)) {
		t.Fatalf("plan %+v for a %d-token state", q, st.est)
	}
	e.Proto = TypeSafe
	if q2 := e.Plan(items); q2.Requests != 1 || q2.EstTokens > st.est+20*items[0].est+reqOverhead {
		t.Fatalf("typesafe plan %+v", q2)
	}
}
