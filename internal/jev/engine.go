package jev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Context budgets of a request (jev-1.13): the whole request, and the state
// plus its longest question. We pack to a safety margin below them.
const (
	CtxTotal  = 64_000
	CtxStateQ = 32_000
	safety    = 0.85
	// DefaultMaxQ caps questions per request. Smaller requests parallelize
	// better; the eval harness is where this number should come from.
	DefaultMaxQ = 128
)

// Token estimation, calibrated live against reported usage.
const (
	reqOverhead = 262 // fixed per-request framing (measured on jev-1.13.0)
	qOverhead   = 7   // per-question framing (measured)
	bytesPerTok = 3.6
)

func estJSON(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return int(float64(len(b))/bytesPerTok) + 1
}

// EstText estimates the tokens of a plain string.
func EstText(s string) int { return int(float64(len(s))/bytesPerTok) + 1 }

// State is a request state shared by the items that point to it. Items with
// the same *State can share a request; a new *State starts a new request.
type State struct {
	V   any
	est int
}

// NewState wraps v (string, object or array) as a request state.
func NewState(v any) *State { return &State{V: v, est: estJSON(v)} }

// Est is the estimated token count of the state.
func (s *State) Est() int { return s.est }

// Item is one question to ask, plus caller data returned with its answer.
type Item struct {
	State *State
	Q     Question
	Tag   any
	est   int
}

// NewItem builds an item and estimates its size.
func NewItem(st *State, q Question, tag any) *Item {
	return &Item{State: st, Q: q, Tag: tag, est: qOverhead + estJSON(q)}
}

// Est is the estimated token count of the question.
func (it *Item) Est() int { return it.est }

// Result is the outcome for one item.
type Result struct {
	Item   *Item
	Answer Answer
	Err    error
}

// ErrBudget is returned when --max-cost stops a run.
var ErrBudget = errors.New("cost budget reached")

// Engine packs items into requests and runs them through the scheduler.
type Engine struct {
	Client  *Client
	Model   string
	Sched   *Sched
	Stats   *Stats
	MaxQ    int
	MaxCost float64 // USD per run; 0 = unlimited

	// Budget, if set, returns the USD still allowed by limits outside this
	// run (daily/monthly caps), or a negative number for "no limit". It is
	// consulted before every request.
	Budget func() float64
}

// NewEngine wires a client and scheduler with fresh stats.
func NewEngine(c *Client, model string, s *Sched) *Engine {
	e := &Engine{Client: c, Model: model, Sched: s, Stats: NewStats(), MaxQ: DefaultMaxQ}
	e.Stats.s.Model = model
	if v, err := strconv.Atoi(os.Getenv("GREV_MAX_Q")); err == nil && v > 0 {
		e.MaxQ = v
	}
	c.OnRetry = func(status int, throttled bool, wait time.Duration) {
		e.Stats.retry(throttled)
		if throttled {
			s.Throttle(wait)
		}
	}
	return e
}

type batch struct {
	seq   int
	state *State
	items []*Item
	est   int
}

func (b *batch) add(it *Item) {
	if len(b.items) == 0 {
		b.est = reqOverhead + b.state.est
	}
	b.items = append(b.items, it)
	b.est += it.est
}

// fits reports whether it can join b within budget. An item that cannot fit
// even alone is reported by tooBig.
func (e *Engine) fits(b *batch, it *Item) bool {
	if b.state != it.State || len(b.items) >= e.MaxQ {
		return false
	}
	return b.est+it.est <= int(CtxTotal*safety)
}

func tooBig(it *Item) bool {
	return reqOverhead+it.State.est+it.est > int(CtxStateQ*safety)
}

// Quote is the plan for a known set of items.
type Quote struct {
	Questions int
	Requests  int
	EstTokens int
	TooBig    int
}

// Plan packs items exactly as Run will and returns the quote. It also records
// the totals in Stats so progress can show a bar and a projection.
func (e *Engine) Plan(items []*Item) Quote {
	var q Quote
	var cur *batch
	for _, it := range items {
		if tooBig(it) {
			// pack sends the current batch before an oversize item.
			if cur != nil {
				q.Requests++
				q.EstTokens += cur.est
				cur = nil
			}
			q.TooBig++
			continue
		}
		if cur == nil || !e.fits(cur, it) {
			if cur != nil {
				q.Requests++
				q.EstTokens += cur.est
			}
			cur = &batch{state: it.State}
		}
		cur.add(it)
		q.Questions++
	}
	if cur != nil {
		q.Requests++
		q.EstTokens += cur.est
	}
	e.Stats.plan(q)
	return q
}

// Run reads items from in, packs consecutive items into requests, runs them
// in parallel and calls emit once per item, in input order. With flush > 0
// (streaming), a partial request is sent once its oldest item has waited that
// long. Run returns the first fatal error (auth, cancellation) or ErrBudget;
// items never sent are not emitted.
func (e *Engine) Run(ctx context.Context, in <-chan *Item, flush time.Duration, emit func(Result)) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	packCtx, stopPacking := context.WithCancel(ctx)
	defer stopPacking()

	batches := make(chan *batch, 4)
	go e.pack(packCtx, in, flush, batches)

	type done struct {
		seq int
		res []Result
	}
	results := make(chan done, 16)
	var wg sync.WaitGroup
	var budgetHit bool
	go func() {
		defer func() { wg.Wait(); close(results) }()
		for b := range batches {
			if e.MaxCost > 0 && e.Stats.committedCost(e.Model, b.est) > e.MaxCost {
				budgetHit = true
				stopPacking()
				break
			}
			if e.Budget != nil {
				if rem := e.Budget(); rem >= 0 && e.Stats.pendingCost(e.Model, b.est) > rem {
					budgetHit = true
					stopPacking()
					break
				}
			}
			e.Stats.queued(1)
			if err := e.Sched.Acquire(ctx, b.est); err != nil {
				e.Stats.queued(-1)
				stopPacking()
				break
			}
			e.Stats.queued(-1)
			e.Stats.start(b.est)
			wg.Add(1)
			go func(b *batch) {
				defer wg.Done()
				res, fatal := e.runBatch(ctx, b)
				if fatal != nil {
					cancel(fatal)
				}
				results <- done{b.seq, res}
			}(b)
		}
		for range batches { // let the packer exit
		}
	}()

	// Reorder and emit.
	pending := map[int][]Result{}
	next := 0
	for d := range results {
		pending[d.seq] = d.res
		for {
			r, ok := pending[next]
			if !ok {
				break
			}
			delete(pending, next)
			next++
			for _, x := range r {
				emit(x)
			}
		}
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if budgetHit {
		return ErrBudget
	}
	return nil
}

// pack groups items into batches; an oversize item becomes a one-item batch
// that fails locally with a clear error.
func (e *Engine) pack(ctx context.Context, in <-chan *Item, flush time.Duration, out chan<- *batch) {
	defer close(out)
	seq := 0
	var cur *batch
	var timer <-chan time.Time
	send := func() bool {
		if cur == nil || len(cur.items) == 0 {
			return true
		}
		cur.seq = seq
		seq++
		select {
		case out <- cur:
		case <-ctx.Done():
			return false
		}
		cur, timer = nil, nil
		return true
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer:
			if !send() {
				return
			}
		case it, ok := <-in:
			if !ok {
				send()
				return
			}
			if cur != nil && (tooBig(it) || !e.fits(cur, it)) {
				if !send() {
					return
				}
			}
			if cur == nil {
				cur = &batch{state: it.State}
				if flush > 0 {
					timer = time.After(flush)
				}
			}
			cur.add(it)
			if tooBig(it) && !send() {
				return
			}
		}
	}
}

func failAll(items []*Item, err error) []Result {
	res := make([]Result, len(items))
	for i, it := range items {
		res[i] = Result{Item: it, Err: err}
	}
	return res
}

// runBatch sends one request for which a scheduler slot is already held,
// splitting it on context-length errors, and returns per-item results. A
// non-nil error is fatal for the whole run.
func (e *Engine) runBatch(ctx context.Context, b *batch) ([]Result, error) {
	if len(b.items) == 1 && tooBig(b.items[0]) {
		e.Sched.Release(0, 0)
		e.Stats.finish(1, b.est, 1)
		return failAll(b.items, fmt.Errorf("record too large for one request (≈%d tokens; limit %d)",
			reqOverhead+b.state.est+b.items[0].est, CtxStateQ)), nil
	}

	req := &Request{State: b.state.V, Model: e.Model, Questions: make(map[string]Question, len(b.items))}
	for i, it := range b.items {
		req.Questions["q"+strconv.Itoa(i)] = it.Q
	}
	t0 := time.Now()
	resp, err := e.Client.Do(ctx, req)
	lat := time.Since(t0)
	if err != nil {
		e.Sched.Release(0, 0)
		var ae *APIError
		if errors.As(err, &ae) {
			switch {
			case ae.Status == 401 || ae.Status == 403 || ae.Status == 404:
				e.Stats.finish(len(b.items), b.est, len(b.items))
				return failAll(b.items, err), err
			case ae.Status == 422 && len(b.items) > 1 && overLimit(ae.Body):
				e.Stats.finish(0, b.est, 0) // the rejected request
				return e.split(ctx, b)
			}
		}
		if ctx.Err() != nil {
			err = context.Cause(ctx)
		}
		e.Stats.finish(len(b.items), b.est, len(b.items))
		return failAll(b.items, err), nil
	}
	e.Sched.Release(lat, resp.Usage.InputTokens)
	model := resp.Model
	if model == "" {
		model = e.Model
	}
	cost, known := Cost(model, resp.Usage)
	res := make([]Result, len(b.items))
	missing := 0
	for i, it := range b.items {
		a, ok := resp.Answers["q"+strconv.Itoa(i)]
		if !ok {
			missing++
			res[i] = Result{Item: it, Err: errors.New("answer missing from response")}
			continue
		}
		res[i] = Result{Item: it, Answer: a}
	}
	e.Stats.finishOK(len(b.items), b.est, resp.Usage.InputTokens, cost, known, model, missing)
	return res, nil
}

// split halves a batch the API rejected as too large and runs both halves.
func (e *Engine) split(ctx context.Context, b *batch) ([]Result, error) {
	mid := len(b.items) / 2
	var res []Result
	for i, part := range [][]*Item{b.items[:mid], b.items[mid:]} {
		h := &batch{seq: b.seq, state: b.state}
		for _, it := range part {
			h.add(it)
		}
		if err := e.Sched.Acquire(ctx, h.est); err != nil {
			rest := b.items[mid*i:]
			return append(res, failAll(rest, err)...), nil
		}
		e.Stats.start(h.est)
		r, fatal := e.runBatch(ctx, h)
		res = append(res, r...)
		if fatal != nil {
			if i == 0 {
				res = append(res, failAll(b.items[mid:], fatal)...)
			}
			return res, fatal
		}
	}
	return res, nil
}

func overLimit(body string) bool {
	b := strings.ToLower(body)
	for _, w := range []string{"token", "context", "too long", "too large", "length", "limit"} {
		if strings.Contains(b, w) {
			return true
		}
	}
	return false
}

// RunAll is Run over a slice (no streaming).
func (e *Engine) RunAll(ctx context.Context, items []*Item, emit func(Result)) error {
	in := make(chan *Item)
	go func() {
		defer close(in)
		for _, it := range items {
			select {
			case in <- it:
			case <-ctx.Done():
				return
			}
		}
	}()
	return e.Run(ctx, in, 0, emit)
}

// EstRequest estimates the input tokens of a raw request body.
func EstRequest(body []byte) int { return reqOverhead + EstText(string(body)) }
