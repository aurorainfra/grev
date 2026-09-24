package jev

import (
	"sync"
	"time"
)

// Stats is the live accounting of a run, read by the progress overlay.
type Stats struct {
	mu sync.Mutex
	s  Snap

	// OnCost, if set, is called after every request that reported usage
	// (e.g. to record spend in the ledger). Set it before the run starts.
	OnCost func(model string, cost float64, tokens int)
}

// Snap is a consistent copy of Stats.
type Snap struct {
	Start time.Time

	// Plan (zero when unknown, e.g. streaming).
	PlanQ, PlanReqs, PlanEst int

	DoneQ, FailedQ           int
	Queued, Inflight, Reqs   int
	Retries, Throttles       int
	Tokens                   int // actual input tokens
	EstDone, EstSeen, EstFly int // estimated tokens: completed OK, started, in flight
	Cost                     float64
	PriceUnknown             bool
	Model                    string
}

// NewStats starts the clock.
func NewStats() *Stats { return &Stats{s: Snap{Start: time.Now()}} }

// Snapshot returns a copy of the current numbers.
func (st *Stats) Snapshot() Snap {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.s
}

// Ratio is actual/estimated tokens over completed requests (1 before any).
func (s Snap) Ratio() float64 {
	if s.EstDone == 0 || s.Tokens == 0 {
		return 1
	}
	return float64(s.Tokens) / float64(s.EstDone)
}

// Projected is the expected total cost of the planned run, or -1 when the
// total is unknown.
func (s Snap) Projected() float64 {
	if s.PlanEst == 0 {
		return -1
	}
	p, _ := PriceOf(s.Model)
	remaining := float64(s.PlanEst-s.EstDone-s.EstFailed()) * s.Ratio()
	if remaining < 0 {
		remaining = 0
	}
	return s.Cost + remaining*p.In/1e6
}

// EstFailed is the estimate of requests that finished without usage.
func (s Snap) EstFailed() int { return s.EstSeen - s.EstDone - s.EstFly }

func (st *Stats) plan(q Quote) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.s.PlanQ += q.Questions + q.TooBig
	st.s.PlanReqs += q.Requests
	st.s.PlanEst += q.EstTokens
}

// AddPlan records work planned outside Engine.Plan (e.g. multi-round tools).
func (st *Stats) AddPlan(q Quote) { st.plan(q) }

func (st *Stats) queued(d int) {
	st.mu.Lock()
	st.s.Queued += d
	st.mu.Unlock()
}

func (st *Stats) start(est int) {
	st.mu.Lock()
	st.s.Inflight++
	st.s.EstSeen += est
	st.s.EstFly += est
	st.mu.Unlock()
}

func (st *Stats) finish(n, est, failed int) {
	st.mu.Lock()
	st.s.Inflight--
	st.s.Reqs++
	st.s.EstFly -= est
	st.s.DoneQ += n
	st.s.FailedQ += failed
	st.mu.Unlock()
}

func (st *Stats) finishOK(n, est, tokens int, cost float64, known bool, model string, missing int) {
	st.mu.Lock()
	st.s.Inflight--
	st.s.Reqs++
	st.s.EstFly -= est
	st.s.EstDone += est
	st.s.DoneQ += n
	st.s.FailedQ += missing
	st.s.Tokens += tokens
	st.s.Cost += cost
	st.s.Model = model
	if !known {
		st.s.PriceUnknown = true
	}
	st.mu.Unlock()
	if st.OnCost != nil {
		st.OnCost(model, cost, tokens)
	}
}

func (st *Stats) retry(throttled bool) {
	st.mu.Lock()
	st.s.Retries++
	if throttled {
		st.s.Throttles++
	}
	st.mu.Unlock()
}

// pendingCost is the projected cost of requests in flight plus one more
// request estimated at est tokens.
func (st *Stats) pendingCost(model string, est int) float64 {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.s.Model != "" {
		model = st.s.Model
	}
	p, _ := PriceOf(model)
	return float64(st.s.EstFly+est) * st.s.Ratio() * p.In / 1e6
}

// committedCost is spend so far plus the projected cost of requests in flight
// and of one more request estimated at est tokens.
func (st *Stats) committedCost(model string, est int) float64 {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.s.Model != "" {
		model = st.s.Model
	}
	p, _ := PriceOf(model)
	return st.s.Cost + float64(st.s.EstFly+est)*st.s.Ratio()*p.In/1e6
}

// Record accounts a request sent outside Run (e.g. jev raw).
func (st *Stats) Record(model string, u Usage, questions int) {
	cost, known := Cost(model, u)
	st.mu.Lock()
	defer st.mu.Unlock()
	st.s.Reqs++
	st.s.DoneQ += questions
	st.s.Tokens += u.InputTokens
	st.s.Cost += cost
	if model != "" {
		st.s.Model = model
	}
	if !known {
		st.s.PriceUnknown = true
	}
	if st.OnCost != nil {
		defer st.OnCost(model, cost, u.InputTokens)
	}
}
