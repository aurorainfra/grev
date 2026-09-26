// Package jev is a small client for TypeSafe's System One API (Jev models):
// request/answer types, credentials, pricing, request packing, scheduling and
// a parallel engine that returns answers in submission order.
package jev

import (
	"bytes"
	"encoding/json"
)

// Question types.
const (
	TypeNoul   = "noul"
	TypeChoice = "choice"
	TypeScore  = "score"
)

// Question is one typed question. Instructions and criteria may be strings,
// objects or arrays; the API treats them as structure, not just text.
type Question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Opt is one Choice option; Desc may be nil when the key says it all.
type Opt struct {
	Key  string
	Desc any
}

// Opts marshals as a JSON object that keeps option order (Go maps don't).
type Opts []Opt

func (o Opts) MarshalJSON() ([]byte, error) {
	obj := make(Obj, len(o))
	for i, opt := range o {
		obj[i] = KV{opt.Key, opt.Desc}
	}
	return obj.MarshalJSON()
}

// KV is one member of an ordered JSON object.
type KV struct {
	K string
	V any
}

// Obj marshals as a JSON object with members in the given order, so the
// model reads structured instructions the way they were written.
type Obj []KV

func (o Obj) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, kv := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(kv.K)
		if err != nil {
			return nil, err
		}
		v, err := json.Marshal(kv.V)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// Noul builds a yes/no question. yes/no are optional criteria (nil to omit).
func Noul(instructions, yes, no any) Question {
	q := Question{Type: TypeNoul, Instructions: instructions}
	if yes != nil || no != nil {
		q.Criteria = map[string]any{"true": yes, "false": no}
	}
	return q
}

// Choice builds a question picking one of opts (max 255).
func Choice(instructions any, opts Opts) Question {
	return Question{Type: TypeChoice, Instructions: instructions, Criteria: opts}
}

// Score builds a question rating along ordered levels (2..10), lowest first.
func Score(instructions any, levels []any) Question {
	return Question{Type: TypeScore, Instructions: instructions, Criteria: levels}
}

// Request is the body of POST /v1/systemone.
type Request struct {
	State     any                 `json:"state"`
	Model     string              `json:"model"`
	Questions map[string]Question `json:"questions"`
}

// Answer is one typed answer; which fields are set depends on Type.
type Answer struct {
	Type          string             `json:"type"`
	Noul          float64            `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         float64            `json:"score,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Legend        map[string]string  `json:"legend,omitempty"`
}

// MarshalJSON emits only the fields that belong to the answer's type, so a
// legitimate 0 (a firm "no") is never dropped.
func (a Answer) MarshalJSON() ([]byte, error) {
	m := map[string]any{"type": a.Type}
	switch a.Type {
	case TypeNoul:
		m["noul"] = a.Noul
	case TypeChoice:
		m["choice"] = a.Choice
		m["confidence"] = a.Confidence
		m["probabilities"] = a.Probabilities
	case TypeScore:
		m["score"] = a.Score
		m["confidence"] = a.Confidence
		m["probabilities"] = a.Probabilities
		m["legend"] = a.Legend
	}
	return json.Marshal(m)
}

// Usage is the token accounting of one request.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	// Cost is the charge reported by the API in USD, when the provider sends
	// one (OpenRouter and other gateways do; TypeSafe does not). It is nil
	// otherwise, and the local price table is used instead.
	Cost *float64 `json:"cost,omitempty"`
}

// Response is the body returned by POST /v1/systemone.
type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
}

// ModelCard is one entry of GET /v1/models.
type ModelCard struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReleaseDate string `json:"release_date"`
}
