package jev

import (
	"context"
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

// Request framing, calibrated live against reported usage (jev-1.13.0); the
// per-character estimate is in text.go.
const (
	reqOverhead = 262 // fixed per-request framing
	qOverhead   = 7   // per-question framing
)

// State is a request state shared by the items that point to it. Items with
// the same *State can share a request; a new *State starts a new request.
type State struct {
	V   any
	est int
}

// NewState wraps v (string, object or array) as a request state. Its
// strings are cleaned of escape sequences first (see Clean).
func NewState(v any) *State {
	v = sanitize(v)
	return &State{V: v, est: estJSON(v)}
}

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
	q.Instructions, q.Criteria = sanitize(q.Instructions), sanitize(q.Criteria)
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

// ErrStopped wraps the failure of a request that stopped the run (see
// Engine.OnFail and StopOnFail).
var ErrStopped = errors.New("run stopped")

// FailAction is what to do with a request that failed for good.
type FailAction int

const (
	FailSkip  FailAction = iota // its questions fail; the run goes on
	FailRetry                   // send it again
	FailStop                    // stop the run, sending nothing more
)

// Engine packs items into requests and runs them through the scheduler.
type Engine struct {
	Client  *Client
	Proto   Protocol // how requests go over the wire and are billed
	Model   string
	Sched   *Sched
	Stats   *Stats
	MaxQ    int
	MaxCost float64 // USD per run; 0 = unlimited

	// Budget, if set, returns the USD still allowed by limits outside this
	// run (daily/monthly caps), or a negative number for "no limit". It is
	// consulted before every request.
	Budget func() float64

	// OnFail, if set, decides what happens to a request that failed after
	// the client's retries (auth failures and oversize requests aside). Calls
	// are serialized, and no new request starts while one runs, so it may
	// ask the user. A FailRetry answer also covers the requests that failed
	// while it was deciding.
	OnFail func(err error, questions int) FailAction

	// StopOnFail turns FailSkip into FailStop, for callers that can't use a
	// run with missing answers: better to stop than to pay for the rest.
	StopOnFail bool

	hold      sync.RWMutex // held while OnFail decides
	lastAct   FailAction
	lastActAt time.Time
}

// NewEngine wires a client and scheduler with fresh stats.
func NewEngine(c *Client, model string, s *Sched) *Engine {
	e := &Engine{Client: c, Proto: ProtocolFor("", c.BaseURL, model), Model: model, Sched: s, Stats: NewStats(), MaxQ: DefaultMaxQ}
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

// batch is one request in the making. est is the size of its body (the
// state once, plus the questions), which the context limits apply to; bill
// is the input tokens it will be billed for, which quotes, budgets and rate
// limits count. They differ where the state is billed per question.
type batch struct {
	seq   int
	state *State
	items []*Item
	est   int
	bill  int
}

func (b *batch) add(it *Item, p Protocol) {
	if len(b.items) == 0 {
		b.est = reqOverhead + b.state.est
		b.bill = p.Base(b.state.est)
	}
	b.items = append(b.items, it)
	b.est += it.est
	b.bill += p.Per(b.state.est, it)
}

// fits reports whether it can join b within budget. An item that cannot fit
// even alone is reported by tooBig.
func (e *Engine) fits(b *batch, it *Item) bool {
	if b.state != it.State || len(b.items) >= e.MaxQ {
		return false
	}
	return b.est+it.est <= int(float64(e.proto().Limits().CtxTotal)*safety)
}

func (e *Engine) tooBig(it *Item) bool {
	return reqOverhead+it.State.est+it.est > int(float64(e.proto().Limits().CtxStateQ)*safety)
}

// proto is the engine's protocol; an Engine built without one speaks
// TypeSafe's.
func (e *Engine) proto() Protocol {
	if e.Proto == nil {
		return TypeSafe
	}
	return e.Proto
}

// SizedState is a placeholder state of an estimated size, for quoting rounds
// whose state isn't built yet.
func SizedState(est int) *State { return &State{est: est} }

// Est is the input tokens n questions like probe, asked in one request about
// st, would be billed for: for quoting rounds not yet built.
func (e *Engine) Est(st *State, probe *Item, n int) int {
	if n == 0 {
		return e.proto().Base(st.est)
	}
	return e.proto().Base(st.est) + n*e.proto().Per(st.est, probe)
}

// Bill is the input tokens one request asking items about st would be billed for.
func (e *Engine) Bill(st *State, items ...*Item) int {
	n := e.proto().Base(st.est)
	for _, it := range items {
		n += e.proto().Per(st.est, it)
	}
	return n
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
		if e.tooBig(it) {
			// pack sends the current batch before an oversize item.
			if cur != nil {
				q.Requests++
				q.EstTokens += cur.bill
				cur = nil
			}
			q.TooBig++
			continue
		}
		if cur == nil || !e.fits(cur, it) {
			if cur != nil {
				q.Requests++
				q.EstTokens += cur.bill
			}
			cur = &batch{state: it.State}
		}
		cur.add(it, e.proto())
		q.Questions++
	}
	if cur != nil {
		q.Requests++
		q.EstTokens += cur.bill
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
			if e.MaxCost > 0 && e.Stats.committedCost(e.Model, b.bill) > e.MaxCost {
				budgetHit = true
				stopPacking()
				break
			}
			if e.Budget != nil {
				if rem := e.Budget(); rem >= 0 && e.Stats.pendingCost(e.Model, b.bill) > rem {
					budgetHit = true
					stopPacking()
					break
				}
			}
			e.hold.RLock() // wait out an OnFail decision
			e.hold.RUnlock()
			e.Stats.queued(1)
			if err := e.Sched.Acquire(ctx, b.bill); err != nil {
				e.Stats.queued(-1)
				stopPacking()
				break
			}
			e.Stats.queued(-1)
			e.Stats.start(b.bill)
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
			if cur != nil && (e.tooBig(it) || !e.fits(cur, it)) {
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
			cur.add(it, e.proto())
			if e.tooBig(it) && !send() {
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
	if len(b.items) == 1 && e.tooBig(b.items[0]) {
		e.Sched.Release(0, 0)
		e.Stats.finish(1, b.bill, 1)
		return failAll(b.items, fmt.Errorf("%w: record too large for one request (≈%d tokens; limit %d)",
			ErrTooLarge, reqOverhead+b.state.est+b.items[0].est, e.proto().Limits().CtxStateQ)), nil
	}

	req := &Request{State: b.state.V, Model: e.Model, Questions: make(map[string]Question, len(b.items))}
	for i, it := range b.items {
		req.Questions["q"+strconv.Itoa(i)] = it.Q
	}
	t0 := time.Now()
	resp, err := e.proto().Do(ctx, e.Client, req)
	lat := time.Since(t0)
	if err != nil {
		e.Sched.Release(0, 0)
		var ae *APIError
		if errors.As(err, &ae) {
			switch {
			case ae.Status == 401 || ae.Status == 403 || ae.Status == 404:
				e.Stats.finish(len(b.items), b.bill, len(b.items))
				return failAll(b.items, err), err
			case IsOverLimit(ae) && len(b.items) > 1:
				e.Stats.finish(0, b.bill, 0) // the rejected request
				return e.split(ctx, b)
			case IsOverLimit(ae):
				err = fmt.Errorf("%w (≈%d tokens estimated): %w", ErrTooLarge, b.est, ae)
			}
		}
		if ctx.Err() == nil && !errors.Is(err, ErrTooLarge) {
			switch e.onFail(ctx, err, len(b.items)) {
			case FailRetry:
				e.Stats.finish(0, b.bill, 0) // the failed request
				if err := e.Sched.Acquire(ctx, b.bill); err != nil {
					e.Stats.finish(len(b.items), 0, len(b.items))
					return failAll(b.items, context.Cause(ctx)), nil
				}
				e.Stats.start(b.bill)
				return e.runBatch(ctx, b)
			case FailStop:
				err = fmt.Errorf("%w: %w", ErrStopped, err)
				e.Stats.finish(len(b.items), b.bill, len(b.items))
				return failAll(b.items, err), err
			}
		}
		if ctx.Err() != nil {
			err = context.Cause(ctx)
		}
		e.Stats.finish(len(b.items), b.bill, len(b.items))
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
	e.Stats.finishOK(len(b.items), b.bill, resp.Usage.InputTokens, cost, known, model, missing)
	return res, nil
}

// onFail applies OnFail and StopOnFail to a request that has just failed.
func (e *Engine) onFail(ctx context.Context, err error, n int) FailAction {
	failedAt := time.Now()
	e.hold.Lock()
	defer e.hold.Unlock()
	act := FailSkip
	switch {
	case ctx.Err() != nil:
		return FailSkip // the run is ending anyway
	case e.lastAct == FailRetry && e.lastActAt.After(failedAt):
		act = FailRetry // it failed while the user was answering "retry"
	case e.OnFail != nil:
		act = e.OnFail(err, n)
		e.lastAct, e.lastActAt = act, time.Now()
	}
	if act == FailSkip && e.StopOnFail {
		act = FailStop
	}
	return act
}

// split halves a batch the API rejected as too large and runs both halves.
func (e *Engine) split(ctx context.Context, b *batch) ([]Result, error) {
	mid := len(b.items) / 2
	var res []Result
	for i, part := range [][]*Item{b.items[:mid], b.items[mid:]} {
		h := &batch{seq: b.seq, state: b.state}
		for _, it := range part {
			h.add(it, e.proto())
		}
		if err := e.Sched.Acquire(ctx, h.bill); err != nil {
			rest := b.items[mid*i:]
			return append(res, failAll(rest, err)...), nil
		}
		e.Stats.start(h.bill)
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

// ErrTooLarge marks a question whose request alone the API rejected as over
// the model's context; the caller has to send less (a smaller window).
var ErrTooLarge = errors.New("over the model's context")

// IsOverLimit reports whether err is the API rejecting a request as too
// large: 400 {"error_type":"max_tokens_exceeded"} on jev-1.13, or 413/422
// with context-length wording.
func IsOverLimit(err error) bool {
	if errors.Is(err, ErrTooLarge) {
		return true
	}
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	words := []string{"max_tokens", "context"} // a 400 can be any bad request
	switch ae.Status {
	case 400:
	case 413, 422:
		words = append(words, "token", "too long", "too large", "length", "limit")
	default:
		return false
	}
	b := strings.ToLower(ae.Body)
	for _, w := range words {
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
