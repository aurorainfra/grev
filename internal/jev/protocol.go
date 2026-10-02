package jev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
)

// Protocol is one decision API's dialect of System One: how a request (one
// state, its questions) goes over the wire, what it is billed for, and how
// large it may be. The tools speak only Question, Answer and State; servers
// that differ (TypeSafe's Jev, Fastino's GLiDE, and whatever comes next)
// differ here.
type Protocol interface {
	Name() string
	// DefaultModel is the model used when none is configured.
	DefaultModel() string
	// Limits bounds a request's body, in this package's token estimates.
	Limits() Limits
	// Base and Per estimate the input tokens a request is billed for: Base
	// once per request, Per for each of its questions, given the estimated
	// size of the state they share.
	Base(state int) int
	Per(state int, it *Item) int
	// Do sends one request and returns its answers.
	Do(ctx context.Context, c *Client, req *Request) (*Response, error)
	// Models lists the decision models the server offers.
	Models(ctx context.Context, c *Client) ([]ModelCard, error)
	// Check makes a free request that fails unless the key is accepted.
	Check(ctx context.Context, c *Client) error
}

// Limits are a request's context budgets, before the packing margin.
type Limits struct {
	CtxTotal  int // the whole request
	CtxStateQ int // the state plus its longest question
}

// The protocols, by the name api.protocol uses.
var (
	TypeSafe Protocol = typeSafe{}
	Fastino  Protocol = fastino{}

	Protocols = map[string]Protocol{"typesafe": TypeSafe, "fastino": Fastino}
)

// ProtocolFor picks the protocol: override (api.protocol) when it names one,
// else Fastino's for a fastino.ai endpoint or a Fastino model id (also
// through a gateway such as OpenRouter), else TypeSafe's.
func ProtocolFor(override, endpoint, model string) Protocol {
	if p, ok := Protocols[strings.ToLower(override)]; ok {
		return p
	}
	if u, err := url.Parse(endpoint); err == nil {
		if h := strings.ToLower(u.Hostname()); h == "fastino.ai" || strings.HasSuffix(h, ".fastino.ai") {
			return Fastino
		}
	}
	if m := strings.ToLower(model); strings.HasPrefix(m, "fastino/") || m == "glide" {
		return Fastino
	}
	return TypeSafe
}

// ---- TypeSafe: the reference dialect (also served by OpenRouter).

type typeSafe struct{}

func (typeSafe) Name() string         { return "typesafe" }
func (typeSafe) DefaultModel() string { return DefaultModel }
func (typeSafe) Limits() Limits       { return Limits{CtxTotal: CtxTotal, CtxStateQ: CtxStateQ} }

// Jev bills the state once per request.
func (typeSafe) Base(state int) int      { return reqOverhead + state }
func (typeSafe) Per(_ int, it *Item) int { return it.est }

func (typeSafe) Do(ctx context.Context, c *Client, req *Request) (*Response, error) {
	return c.Do(ctx, req)
}

func (typeSafe) Models(ctx context.Context, c *Client) ([]ModelCard, error) { return c.Models(ctx) }

// TypeSafe's model list needs the key.
func (typeSafe) Check(ctx context.Context, c *Client) error {
	_, err := c.Models(ctx)
	return err
}

// ---- Fastino's GLiDE (docs.fastino.ai/inference/systemone).

type fastino struct{}

// GLiDE's billing, measured live (2026-10): each question is its own pass
// over the state, billed as state plus question plus framing, and a Choice
// reads a little per option. A Score with three or more levels "thinks":
// about three passes, plus a thought that grows with its uncertainty (100 to
// 900 tokens on 3 to 10 levels), which is the noisiest part; it is quoted on
// the high side. GLiDE's tokenizer counts about 0.8 of this package's estimate.
const (
	fastinoRatio  = 0.8
	fastinoPass   = 47
	fastinoOption = 8   // per Choice option
	fastinoThink  = 3   // passes of a Score over 3+ levels
	fastinoLevel  = 120 // and its thought, per level
	fastinoCtx    = 40_000
)

func (fastino) Name() string         { return "fastino" }
func (fastino) DefaultModel() string { return "fastino/GLiDE" }

// The 40k-token limit is per question (state plus question); there is no
// published limit on the request as a whole, so Jev's packing budget stays.
func (fastino) Limits() Limits { return Limits{CtxTotal: CtxTotal, CtxStateQ: fastinoCtx} }

func (fastino) Base(int) int { return 0 }

func (fastino) Per(state int, it *Item) int {
	pass := int(math.Round(fastinoRatio*float64(state+it.est))) + fastinoPass
	switch n := levels(it.Q); {
	case it.Q.Type == TypeScore && n >= 3:
		return fastinoThink*pass + fastinoLevel*n
	case it.Q.Type == TypeChoice:
		return pass + fastinoOption*n
	}
	return pass
}

// levels counts a Score's levels or a Choice's options.
func levels(q Question) int {
	switch c := q.Criteria.(type) {
	case []any:
		return len(c)
	case map[string]any:
		return len(c)
	case Opts:
		return len(c)
	}
	return 0
}

// Do adapts the request to GLiDE, which takes instructions only as text and
// Noul criteria only as strings, and maps its answers back: GLiDE's Score
// "score" is the winning level, and the probability-weighted level that Jev
// calls score is "expected_level".
func (fastino) Do(ctx context.Context, c *Client, req *Request) (*Response, error) {
	out := &Request{State: req.State, Model: req.Model, Questions: make(map[string]Question, len(req.Questions))}
	for k, q := range req.Questions {
		q.Instructions = Text(q.Instructions)
		if q.Type == TypeNoul && q.Criteria != nil {
			q.Criteria = noulCriteria(q.Criteria)
		}
		out.Questions[k] = q
	}
	resp, err := c.Do(ctx, out)
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == 404 && strings.Contains(strings.ToLower(req.Model), "gliner") {
			err = fmt.Errorf("%w; GLiNER models answer through chat completions, which grev doesn't speak: use fastino/GLiDE", err)
		}
		return nil, err
	}
	for k, a := range resp.Answers {
		if a.ExpectedLevel != nil {
			a.Score = *a.ExpectedLevel
			resp.Answers[k] = a
		}
	}
	return resp, nil
}

// noulCriteria gives both sides of a Noul's criteria as strings, defaulting
// a missing side the way the API would.
func noulCriteria(c any) any {
	m, ok := c.(map[string]any)
	if !ok {
		return c
	}
	out := map[string]any{}
	for side, def := range map[string]string{"true": "Yes", "false": "No"} {
		if v := m[side]; v != nil {
			out[side] = Text(v)
		} else {
			out[side] = def
		}
	}
	return out
}

// Models lists Fastino's System One models from its public catalog; its
// /v1/models lists chat models, which /v1/systemone can't answer with.
func (fastino) Models(ctx context.Context, c *Client) ([]ModelCard, error) {
	out, err := c.Raw(ctx, "GET", "/v1/base-models", nil)
	if err != nil {
		return nil, err
	}
	var cat struct {
		Models []struct {
			ID          string `json:"id"`
			Description string `json:"description"`
			TaskType    string `json:"task_type"`
			Released    string `json:"release_month"`
			Deprecated  bool   `json:"deprecated"`
		} `json:"models"`
	}
	if err := json.Unmarshal(out, &cat); err != nil {
		return nil, fmt.Errorf("decoding the model catalog: %w", err)
	}
	var cards []ModelCard
	for _, m := range cat.Models {
		if m.TaskType == "systemone" && !m.Deprecated {
			cards = append(cards, ModelCard{Name: m.ID, Description: m.Description, ReleaseDate: m.Released})
		}
	}
	return cards, nil
}

// Check asks for one of the account's training jobs: Fastino's model lists
// are public, so they would accept any key.
func (fastino) Check(ctx context.Context, c *Client) error {
	_, err := c.Raw(ctx, "GET", "/v1/training-jobs?limit=1", nil)
	return err
}

// Text renders structured instructions as text for APIs that take only
// strings: one "key: value" line per member, in order, with the question
// last so it follows what it refers to. Strings are kept as they are;
// nested values are written as JSON.
func Text(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case nil:
		return ""
	case Obj:
		var lines, last []string
		for _, kv := range v {
			l := kv.K + ": " + textValue(kv.V)
			if kv.K == "question" {
				last = append(last, l)
			} else {
				lines = append(lines, l)
			}
		}
		return strings.Join(append(lines, last...), "\n")
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		o := make(Obj, len(keys))
		for i, k := range keys {
			o[i] = KV{k, v[k]}
		}
		return Text(o)
	}
	return textValue(v)
}

// textValue writes one member's value: a string as it is (continuation lines
// indented, so they stay inside the member), anything else as JSON.
func textValue(v any) string {
	if s, ok := v.(string); ok {
		return strings.ReplaceAll(s, "\n", "\n  ")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
