// Package jevtest is a fake of TypeSafe's System One API for offline tests:
// POST /v1/systemone answered by a pluggable oracle, GET /v1/models, and
// knobs to simulate latency, capacity (429), context-length errors (422),
// transient failures (500) and bad keys (401).
package jevtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurorainfra/grev/internal/jev"
)

// Oracle answers one question about a state. Choice criteria arrive as
// jev.Opts (in request order), Score criteria as []any, Noul criteria as a map.
type Oracle func(state any, q jev.Question) jev.Answer

// Options configure a fake server. The zero value accepts any key, answers
// with DefaultOracle instantly, and never throttles.
type Options struct {
	Key           string               // required bearer key; "" accepts any
	Oracle        Oracle               // nil means DefaultOracle
	Latency       func() time.Duration // per accepted request; nil means none
	Capacity      int                  // >0: requests beyond this many in flight get 429
	RetryAfterMS  int                  // 429 retry-after-ms header (default 50 when RetryAfterSec is 0)
	RetryAfterSec int                  // 429 Retry-After header in seconds, used if set
	MaxBody       int                  // >0: request bodies larger than this get 400 max_tokens_exceeded, as jev-1.13 answers
	FailFirst     int                  // the first N POSTs answer 500
	Model         string               // model reported in responses (default jev-1.13.0)
}

// Req is one logged POST.
type Req struct {
	Status    int
	Questions int
	Bytes     int
	Model     string // the request's model field, when it parsed
	UserAgent string
}

// Server is a running fake API.
type Server struct {
	*httptest.Server
	o Options

	mu       sync.Mutex
	posts    int
	inflight int
	peak     int
	log      []Req
}

// New starts a fake server and closes it when the test ends.
func New(tb testing.TB, o Options) *Server {
	if o.Oracle == nil {
		o.Oracle = DefaultOracle
	}
	if o.Model == "" {
		o.Model = "jev-1.13.0"
	}
	if o.RetryAfterMS == 0 && o.RetryAfterSec == 0 {
		o.RetryAfterMS = 50
	}
	s := &Server{o: o}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	if tb != nil {
		tb.Cleanup(s.Close)
	}
	return s
}

// Requests is the number of POSTs received, whatever their outcome.
func (s *Server) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.posts
}

// Log returns every POST so far.
func (s *Server) Log() []Req {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Req(nil), s.log...)
}

// Count returns how many POSTs ended with status.
func (s *Server) Count(status int) int {
	n := 0
	for _, r := range s.Log() {
		if r.Status == status {
			n++
		}
	}
	return n
}

// Answered is the number of questions answered with 200.
func (s *Server) Answered() int {
	n := 0
	for _, r := range s.Log() {
		if r.Status == http.StatusOK {
			n += r.Questions
		}
	}
	return n
}

// PeakInflight is the highest number of accepted requests in flight at once.
func (s *Server) PeakInflight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peak
}

func (s *Server) record(r Req) {
	s.mu.Lock()
	s.log = append(s.log, r)
	s.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-typesafe-request-id", "req_test_"+strconv.Itoa(status))
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if s.o.Key != "" && r.Header.Get("Authorization") != "Bearer "+s.o.Key {
		if r.Method == http.MethodPost {
			s.mu.Lock()
			s.posts++
			s.mu.Unlock()
			s.record(Req{Status: 401})
		}
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "invalid API key"})
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		writeJSON(w, http.StatusOK, map[string]any{"models": []map[string]string{
			{"name": "jev-latest", "description": "fake latest", "release_date": "2026-09-10T00:00:00Z"},
			{"name": "jev-preview", "description": "fake preview", "release_date": "2026-09-10T00:00:00Z"},
		}})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/systemone":
		s.systemOne(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "not found"})
	}
}

type wireQuestion struct {
	Type         string          `json:"type"`
	Instructions any             `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria"`
}

type wireRequest struct {
	State     any                     `json:"state"`
	Model     string                  `json:"model"`
	Questions map[string]wireQuestion `json:"questions"`
}

func (s *Server) systemOne(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.posts++
	n := s.posts
	s.mu.Unlock()

	if n <= s.o.FailFirst {
		s.record(Req{Status: 500, Bytes: len(body)})
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "injected failure"})
		return
	}
	var req wireRequest
	if err := json.Unmarshal(body, &req); err != nil || req.Questions == nil || req.Model == "" {
		s.record(Req{Status: 422, Bytes: len(body)})
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": fmt.Sprintf("invalid request: %v", err)})
		return
	}
	if s.o.MaxBody > 0 && len(body) > s.o.MaxBody {
		s.record(Req{Status: 400, Questions: len(req.Questions), Bytes: len(body), Model: req.Model})
		writeJSON(w, http.StatusBadRequest, map[string]any{"error_type": "max_tokens_exceeded"})
		return
	}
	s.mu.Lock()
	if s.o.Capacity > 0 && s.inflight >= s.o.Capacity {
		s.mu.Unlock()
		s.record(Req{Status: 429, Questions: len(req.Questions), Bytes: len(body), Model: req.Model})
		if s.o.RetryAfterSec > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(s.o.RetryAfterSec))
		} else {
			w.Header().Set("retry-after-ms", strconv.Itoa(s.o.RetryAfterMS))
		}
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"detail": "rate limited"})
		return
	}
	s.inflight++
	if s.inflight > s.peak {
		s.peak = s.inflight
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inflight--
		s.mu.Unlock()
	}()
	if s.o.Latency != nil {
		time.Sleep(s.o.Latency())
	}

	answers := make(map[string]jev.Answer, len(req.Questions))
	for id, wq := range req.Questions {
		q := jev.Question{Type: wq.Type, Instructions: wq.Instructions}
		switch wq.Type {
		case jev.TypeChoice:
			q.Criteria = orderedOpts(wq.Criteria)
		case jev.TypeScore:
			var levels []any
			json.Unmarshal(wq.Criteria, &levels)
			q.Criteria = levels
		default:
			if len(wq.Criteria) > 0 && string(wq.Criteria) != "null" {
				var m map[string]any
				json.Unmarshal(wq.Criteria, &m)
				q.Criteria = m
			}
		}
		answers[id] = s.o.Oracle(req.State, q)
	}
	s.record(Req{Status: 200, Questions: len(req.Questions), Bytes: len(body), Model: req.Model, UserAgent: r.UserAgent()})
	writeJSON(w, http.StatusOK, map[string]any{
		"model":   s.o.Model,
		"answers": answers,
		"usage":   map[string]int{"input_tokens": len(body)/4 + 260, "output_tokens": 20 * len(req.Questions)},
	})
}

// orderedOpts decodes a JSON object keeping member order.
func orderedOpts(raw json.RawMessage) jev.Opts {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var opts jev.Opts
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return opts
		}
		key, _ := t.(string)
		var v any
		if err := dec.Decode(&v); err != nil {
			return opts
		}
		opts = append(opts, jev.Opt{Key: key, Desc: v})
	}
	return opts
}

var (
	pRe     = regexp.MustCompile(`p=([0-9]*\.?[0-9]+)`)
	levelRe = regexp.MustCompile(`level=(\d+)`)
)

// Subject is the text a question is about: the record in structured
// instructions ("text" plus fields), else the state's "document" or "input"
// member, else the state itself.
func Subject(state any, q jev.Question) string {
	if m, ok := q.Instructions.(map[string]any); ok {
		if t, ok := m["text"].(string); ok {
			var b strings.Builder
			b.WriteString(t)
			for i := 1; ; i++ {
				f, ok := m["f"+strconv.Itoa(i)].(string)
				if !ok {
					break
				}
				b.WriteString(" " + f)
			}
			return b.String()
		}
	}
	switch st := state.(type) {
	case string:
		return st
	case map[string]any:
		for _, k := range []string{"document", "input", "text"} {
			if v, ok := st[k].(string); ok {
				return v
			}
		}
	}
	b, _ := json.Marshal(state)
	return string(b)
}

// DefaultOracle answers deterministically from markers in the subject:
//
//	Noul:   "p=0.73" → 0.73; else "yes" → 0.95, "maybe" → 0.5, otherwise 0.05
//	Choice: the first option whose key appears in the subject (0.9), else a
//	        uniform distribution over the options
//	Score:  "level=N" → level N; else the middle level
func DefaultOracle(state any, q jev.Question) jev.Answer {
	subj := Subject(state, q)
	low := strings.ToLower(subj)
	switch q.Type {
	case jev.TypeChoice:
		opts, _ := q.Criteria.(jev.Opts)
		if len(opts) == 0 {
			return jev.Answer{Type: jev.TypeChoice}
		}
		probs := map[string]float64{}
		pick := -1
		for i, o := range opts {
			if pick < 0 && strings.Contains(low, strings.ToLower(o.Key)) {
				pick = i
			}
		}
		if pick < 0 {
			for _, o := range opts {
				probs[o.Key] = 1 / float64(len(opts))
			}
			return jev.Answer{Type: jev.TypeChoice, Choice: opts[0].Key, Probabilities: probs, Confidence: 0}
		}
		rest := 0.1 / float64(max(1, len(opts)-1))
		for i, o := range opts {
			if i == pick {
				probs[o.Key] = 0.9
			} else {
				probs[o.Key] = rest
			}
		}
		if len(opts) == 1 {
			probs[opts[0].Key] = 1
		}
		return jev.Answer{Type: jev.TypeChoice, Choice: opts[pick].Key, Probabilities: probs, Confidence: 0.8}
	case jev.TypeScore:
		levels, _ := q.Criteria.([]any)
		n := max(1, len(levels))
		lvl := (n - 1) / 2
		if m := levelRe.FindStringSubmatch(low); m != nil {
			if v, err := strconv.Atoi(m[1]); err == nil && v < n {
				lvl = v
			}
		}
		probs := map[string]float64{}
		legend := map[string]string{}
		for i := 0; i < n; i++ {
			k := strconv.Itoa(i)
			probs[k] = 0
			if i < len(levels) {
				legend[k] = fmt.Sprint(levels[i])
			}
		}
		probs[strconv.Itoa(lvl)] = 1
		return jev.Answer{Type: jev.TypeScore, Score: float64(lvl), Probabilities: probs, Legend: legend, Confidence: 1}
	default:
		p := 0.05
		switch {
		case pRe.MatchString(low):
			p, _ = strconv.ParseFloat(pRe.FindStringSubmatch(low)[1], 64)
		case strings.Contains(low, "yes"):
			p = 0.95
		case strings.Contains(low, "maybe"):
			p = 0.5
		}
		return jev.Answer{Type: jev.TypeNoul, Noul: p}
	}
}
