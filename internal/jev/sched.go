package jev

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Published account limits; GREV_RPM / GREV_TPS override them for higher plans.
const (
	DefaultRPM = 1200
	DefaultTPS = 250_000
	DefaultJ   = 4
	maxJ       = 256
)

// Sched gates requests: a concurrency limit (fixed, or adaptive for -Jmax),
// token buckets for the request- and token-rate limits, and a global pause
// after the server throttles us.
type Sched struct {
	mu       sync.Mutex
	changed  chan struct{} // closed and replaced whenever state changes
	adaptive bool
	limit    float64
	inflight int
	paused   time.Time

	rpm, tps     float64
	reqB, tokB   float64
	lastRefill   time.Time
	lastCut      time.Time
	lastThrottle time.Time
	baseline     float64 // lowest recent latency per kTok (seconds)
	ewma         float64
	trend        int // +1 growing, -1 shrinking, 0 flat (for display)
	lastLimitInt int
	slowStart    bool // grow by one per completion until latency rises or we are throttled
	samples      int
}

// ParseJ parses a -J value: a positive integer or "max" ("" is the default).
func ParseJ(v string) (n int, adaptive bool, err error) {
	if v == "" {
		return DefaultJ, false, nil
	}
	if strings.EqualFold(v, "max") {
		return 0, true, nil
	}
	n, err = strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, false, fmt.Errorf("invalid -J %q: want a positive number or 'max'", v)
	}
	return n, false, nil
}

// NewSched builds a scheduler for -J n (or adaptive when adaptive is set).
func NewSched(n int, adaptive bool) *Sched {
	s := &Sched{
		changed:  make(chan struct{}),
		adaptive: adaptive,
		limit:    float64(n),
		rpm:      envFloat("GREV_RPM", DefaultRPM),
		tps:      envFloat("GREV_TPS", DefaultTPS),
	}
	if adaptive {
		s.limit = 2
		s.slowStart = true
	}
	s.reqB, s.tokB = s.reqCap(), s.tps
	s.lastRefill = time.Now()
	s.lastLimitInt = int(s.limit)
	return s
}

// reqCap allows a burst of ten seconds of request quota: the published limit
// is per minute, and a 429 still slows us down if the server disagrees.
func (s *Sched) reqCap() float64 { return math.Max(1, s.rpm/6) }

func (s *Sched) refill(now time.Time) {
	dt := now.Sub(s.lastRefill).Seconds()
	s.lastRefill = now
	s.reqB = math.Min(s.reqCap(), s.reqB+dt*s.rpm/60)
	s.tokB = math.Min(s.tps, s.tokB+dt*s.tps)
}

func (s *Sched) notify() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// Acquire blocks until a request estimated at est tokens may be sent.
func (s *Sched) Acquire(ctx context.Context, est int) error {
	need := math.Min(float64(est), s.tps)
	for {
		s.mu.Lock()
		now := time.Now()
		s.refill(now)
		var wait time.Duration
		switch {
		case now.Before(s.paused):
			wait = s.paused.Sub(now)
		case s.inflight >= int(math.Max(1, math.Floor(s.limit))):
			wait = time.Second // woken early by Release
		case s.reqB < 1:
			wait = time.Duration((1 - s.reqB) / (s.rpm / 60) * float64(time.Second))
		case s.tokB < need:
			wait = time.Duration((need - s.tokB) / s.tps * float64(time.Second))
		default:
			s.reqB--
			s.tokB -= need
			s.inflight++
			s.mu.Unlock()
			return nil
		}
		ch := s.changed
		s.mu.Unlock()
		t := time.NewTimer(wait + time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-ch:
			t.Stop()
		case <-t.C:
		}
	}
}

// Release reports a finished request: its latency and actual token count.
func (s *Sched) Release(lat time.Duration, tokens int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding := s.inflight >= int(math.Floor(s.limit))
	s.inflight--
	if s.adaptive && lat > 0 {
		s.adapt(lat, tokens, binding)
	}
	s.notify()
}

// Controller tuning. Latency is noisy (±30% request to request), so the
// gradient only reacts beyond a tolerance band, as in Netflix's gradient2.
const (
	warmup    = 2   // ignore the first samples: they include connection setup
	tolerance = 1.5 // latency may grow to 1.5× the baseline before we back off
)

// adapt is the -Jmax controller: slow start (+1 per completion, so the limit
// doubles every round trip) until latency leaves the tolerance band or the
// server throttles us, then a gradient controller (PI-like) on latency per
// kTok against a slowly forgetting minimum: grow ~√L while latency is flat,
// shrink in proportion when it rises.
func (s *Sched) adapt(lat time.Duration, tokens int, binding bool) {
	s.samples++
	per := lat.Seconds() / (1 + float64(tokens)/1000)
	if s.samples <= warmup {
		if s.slowStart && binding {
			s.setLimit(s.limit + 1)
		}
		return
	}
	if s.baseline == 0 || per < s.baseline {
		s.baseline = per
	} else {
		s.baseline += (per - s.baseline) * 0.01
	}
	if s.ewma == 0 {
		s.ewma = per
	}
	s.ewma = 0.8*s.ewma + 0.2*per
	grad := math.Max(0.5, math.Min(1, tolerance*s.baseline/s.ewma))
	if debugSched {
		fmt.Fprintf(os.Stderr, "sched: lat=%v tok=%d per=%.4f base=%.4f ewma=%.4f grad=%.2f limit=%.1f binding=%v slow=%v\n",
			lat.Round(time.Millisecond), tokens, per, s.baseline, s.ewma, grad, s.limit, binding, s.slowStart)
	}
	switch {
	case s.slowStart && grad < 1:
		s.slowStart = false
		s.setLimit(s.limit * grad)
	case s.slowStart:
		if binding {
			s.setLimit(s.limit + 1)
		}
	case binding || grad < 1:
		target := s.limit*grad + math.Sqrt(s.limit)
		next := 0.8*s.limit + 0.2*target
		// Right after a 429 the server has told us where its limit is:
		// don't grow back into it, only shrink.
		if next < s.limit || time.Since(s.lastThrottle) > throttleCooldown {
			s.setLimit(next)
		}
	}
}

// throttleCooldown is how long -Jmax refrains from growing after a 429, and
// cutEvery how often a burst of 429s may halve the limit again.
const (
	throttleCooldown = time.Second
	cutEvery         = 250 * time.Millisecond
)

// Throttle reports a 429/529: pause everyone for wait and, for -Jmax, halve
// the limit (at most once per cutEvery, so one burst doesn't collapse it).
func (s *Sched) Throttle(wait time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if until := now.Add(wait); until.After(s.paused) {
		s.paused = until
	}
	s.lastThrottle = now
	if s.adaptive && now.Sub(s.lastCut) > cutEvery {
		s.lastCut = now
		s.slowStart = false
		s.setLimit(s.limit / 2)
	}
	s.notify()
}

func (s *Sched) setLimit(l float64) {
	l = math.Max(1, math.Min(maxJ, l))
	switch li := int(l); {
	case li > s.lastLimitInt:
		s.trend = 1
	case li < s.lastLimitInt:
		s.trend = -1
	}
	s.lastLimitInt = int(l)
	s.limit = l
}

// State reports the current limit, whether it is adaptive, its recent trend
// and the number of requests in flight.
func (s *Sched) State() (limit int, adaptive bool, trend int, inflight int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int(math.Max(1, math.Floor(s.limit))), s.adaptive, s.trend, s.inflight
}

// SetRates replaces the rate ceilings (e.g. from config); 0 keeps a value.
// Environment overrides (GREV_RPM, GREV_TPS) still win.
func (s *Sched) SetRates(rpm, tps float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rpm > 0 && os.Getenv("GREV_RPM") == "" {
		s.rpm = rpm
		s.reqB = s.reqCap()
	}
	if tps > 0 && os.Getenv("GREV_TPS") == "" {
		s.tps = tps
		s.tokB = tps
	}
}

// Rates returns the request and token rate ceilings in use.
func (s *Sched) Rates() (rpm, tps float64) { return s.rpm, s.tps }

var debugSched = os.Getenv("GREV_DEBUG") == "sched"

func envFloat(name string, def float64) float64 {
	if v := os.Getenv(name); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return def
}
