package jev_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aurorainfra/grev/internal/jev"
	"github.com/aurorainfra/grev/internal/jevtest"
)

// engine builds an engine against srv with the given key and scheduler.
func engine(t *testing.T, srv *jevtest.Server, key string, j int, adaptive bool) *jev.Engine {
	t.Helper()
	t.Setenv("TYPESAFE_BASE_URL", srv.URL)
	t.Setenv("GREV_PRICE_PER_MTOK", "")
	os.Unsetenv("GREV_PRICE_PER_MTOK")
	t.Setenv("GREV_MAX_Q", "")
	c := jev.NewClient(key, "test")
	return jev.NewEngine(c, jev.DefaultModel, jev.NewSched(j, adaptive))
}

// items makes n single-record nouls; record i asks about "item i p=P".
func items(st *jev.State, n int, p func(i int) float64) []*jev.Item {
	out := make([]*jev.Item, n)
	for i := range out {
		instr := jev.Obj{{K: "text", V: fmt.Sprintf("item %d p=%.2f", i, p(i))}, {K: "question", V: "Is `text` good?"}}
		out[i] = jev.NewItem(st, jev.Noul(instr, nil, nil), i)
	}
	return out
}

func collect(t *testing.T, e *jev.Engine, ctx context.Context, its []*jev.Item) ([]jev.Result, error) {
	t.Helper()
	var res []jev.Result
	err := e.RunAll(ctx, its, func(r jev.Result) { res = append(res, r) })
	return res, err
}

func TestEngineOrderUnderRandomLatency(t *testing.T) {
	srv := jevtest.New(t, jevtest.Options{Latency: func() time.Duration {
		return time.Duration(rand.IntN(15)) * time.Millisecond
	}})
	e := engine(t, srv, "k", 8, false)
	e.MaxQ = 3
	st := jev.NewState("shared")
	its := items(st, 200, func(i int) float64 { return float64(i%100) / 100 })
	e.Plan(its)
	res, err := collect(t, e, context.Background(), its)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 200 {
		t.Fatalf("got %d results", len(res))
	}
	for i, r := range res {
		if r.Err != nil || r.Item.Tag.(int) != i {
			t.Fatalf("result %d: tag %v err %v", i, r.Item.Tag, r.Err)
		}
		if want := float64(i%100) / 100; r.Answer.Noul < want-0.001 || r.Answer.Noul > want+0.001 {
			t.Fatalf("result %d: noul %v, want %v", i, r.Answer.Noul, want)
		}
	}
	if got := srv.Count(200); got != 67 {
		t.Errorf("requests = %d, want 67 (200 questions / MaxQ 3)", got)
	}
	s := e.Stats.Snapshot()
	if s.DoneQ != 200 || s.Reqs != 67 || s.FailedQ != 0 || s.Tokens == 0 || s.Cost <= 0 || s.Inflight != 0 {
		t.Errorf("stats: %+v", s)
	}
	if p := s.Projected(); p < s.Cost-1e-12 || p > s.Cost+1e-12 {
		t.Errorf("projection after the run %v != cost %v", p, s.Cost)
	}
}

func TestEngineStreamingFlush(t *testing.T) {
	srv := jevtest.New(t, jevtest.Options{})
	e := engine(t, srv, "k", 4, false)
	st := jev.NewState("shared")
	in := make(chan *jev.Item)
	got := make(chan jev.Result, 10)
	done := make(chan error)
	go func() { done <- e.Run(context.Background(), in, 20*time.Millisecond, func(r jev.Result) { got <- r }) }()
	its := items(st, 3, func(int) float64 { return 0.9 })
	for _, it := range its {
		in <- it
	}
	// Without closing the input, the flush timer must still send the partial batch.
	for i := 0; i < 3; i++ {
		select {
		case r := <-got:
			if r.Item.Tag.(int) != i {
				t.Fatalf("out of order: %v", r.Item.Tag)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("partial batch was not flushed")
		}
	}
	close(in)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestEngineSplitsOn422(t *testing.T) {
	srv := jevtest.New(t, jevtest.Options{MaxBody: 1500})
	e := engine(t, srv, "k", 2, false)
	e.MaxQ = 50
	its := items(jev.NewState("s"), 40, func(i int) float64 { return 0.95 })
	res, err := collect(t, e, context.Background(), its)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range res {
		if r.Err != nil || r.Item.Tag.(int) != i {
			t.Fatalf("result %d: %v %v", i, r.Item.Tag, r.Err)
		}
	}
	if len(res) != 40 || srv.Count(422) == 0 || srv.Answered() != 40 {
		t.Fatalf("results %d, 422s %d, answered %d", len(res), srv.Count(422), srv.Answered())
	}
	if s := e.Stats.Snapshot(); s.FailedQ != 0 || s.DoneQ != 40 || s.Inflight != 0 {
		t.Fatalf("stats after split: %+v", s)
	}
}

func TestEngine422NotSplitWhenMalformed(t *testing.T) {
	// A 422 that is not about size fails the batch without splitting.
	srv := jevtest.New(t, jevtest.Options{})
	e := engine(t, srv, "k", 1, false)
	e.Model = "" // the fake rejects requests without a model
	res, err := collect(t, e, context.Background(), items(jev.NewState("s"), 4, func(int) float64 { return 0.9 }))
	if err != nil {
		t.Fatal(err)
	}
	var ae *jev.APIError
	if len(res) != 4 || !errors.As(res[0].Err, &ae) || ae.Status != 422 {
		t.Fatalf("want per-item 422 errors, got %v", res)
	}
	if srv.Requests() != 1 {
		t.Fatalf("malformed 422 should not be split: %d requests", srv.Requests())
	}
}

func TestEngineRetries500(t *testing.T) {
	srv := jevtest.New(t, jevtest.Options{FailFirst: 2})
	e := engine(t, srv, "k", 1, false)
	res, err := collect(t, e, context.Background(), items(jev.NewState("s"), 2, func(int) float64 { return 0.9 }))
	if err != nil || len(res) != 2 || res[0].Err != nil || res[1].Err != nil {
		t.Fatalf("err %v, results %v", err, res)
	}
	if srv.Count(500) != 2 || e.Stats.Snapshot().Retries != 2 || e.Stats.Snapshot().Throttles != 0 {
		t.Fatalf("500s %d, stats %+v", srv.Count(500), e.Stats.Snapshot())
	}
}

func TestEngineHonors429RetryAfter(t *testing.T) {
	srv := jevtest.New(t, jevtest.Options{Capacity: 1, RetryAfterMS: 40, Latency: func() time.Duration { return 5 * time.Millisecond }})
	e := engine(t, srv, "k", 3, false)
	var its []*jev.Item
	for i := 0; i < 3; i++ { // three states → three concurrent requests
		its = append(its, items(jev.NewState(fmt.Sprint("s", i)), 1, func(int) float64 { return 0.9 })...)
	}
	t0 := time.Now()
	res, err := collect(t, e, context.Background(), its)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Err != nil {
			t.Fatalf("request failed despite retries: %v", r.Err)
		}
	}
	s := e.Stats.Snapshot()
	if srv.Count(429) == 0 || s.Throttles == 0 {
		t.Fatalf("expected throttling: 429s %d, stats %+v", srv.Count(429), s)
	}
	if time.Since(t0) < 40*time.Millisecond {
		t.Fatalf("finished in %v: Retry-After was not honored", time.Since(t0))
	}
}

func TestEngineFatal401(t *testing.T) {
	srv := jevtest.New(t, jevtest.Options{Key: "right", Latency: func() time.Duration { return 5 * time.Millisecond }})
	e := engine(t, srv, "wrong", 2, false)
	e.MaxQ = 1
	res, err := collect(t, e, context.Background(), items(jev.NewState("s"), 50, func(int) float64 { return 0.9 }))
	var ae *jev.APIError
	if !errors.As(err, &ae) || ae.Status != 401 {
		t.Fatalf("want a fatal 401, got %v", err)
	}
	if srv.Requests() > 6 {
		t.Fatalf("a fatal error should stop the run: %d requests sent", srv.Requests())
	}
	for _, r := range res {
		if r.Err == nil {
			t.Fatalf("item %v succeeded with a bad key", r.Item.Tag)
		}
	}
}

func TestEngineBudget(t *testing.T) {
	srv := jevtest.New(t, jevtest.Options{})
	e := engine(t, srv, "k", 1, false)
	e.MaxQ = 1
	e.MaxCost = 6e-5 // a handful of ~1.4e-5 requests
	res, err := collect(t, e, context.Background(), items(jev.NewState("s"), 30, func(int) float64 { return 0.9 }))
	if !errors.Is(err, jev.ErrBudget) {
		t.Fatalf("want ErrBudget, got %v", err)
	}
	n := srv.Requests()
	if n < 1 || n >= 30 || len(res) != n {
		t.Fatalf("requests %d, results %d", n, len(res))
	}
	if s := e.Stats.Snapshot(); s.Cost > e.MaxCost {
		t.Fatalf("spent %v over budget %v", s.Cost, e.MaxCost)
	}

	// A budget smaller than one request sends nothing.
	srv2 := jevtest.New(t, jevtest.Options{})
	e2 := engine(t, srv2, "k", 1, false)
	e2.MaxCost = 1e-12
	res, err = collect(t, e2, context.Background(), items(jev.NewState("s"), 3, func(int) float64 { return 0.9 }))
	if !errors.Is(err, jev.ErrBudget) || srv2.Requests() != 0 || len(res) != 0 {
		t.Fatalf("tiny budget: err %v, requests %d, results %d", err, srv2.Requests(), len(res))
	}
}

func TestEngineCancel(t *testing.T) {
	srv := jevtest.New(t, jevtest.Options{Latency: func() time.Duration { return 20 * time.Millisecond }})
	e := engine(t, srv, "k", 1, false)
	e.MaxQ = 1
	ctx, cancel := context.WithCancel(context.Background())
	var n atomic.Int32
	err := e.RunAll(ctx, items(jev.NewState("s"), 100, func(int) float64 { return 0.9 }), func(jev.Result) {
		if n.Add(1) == 3 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if srv.Requests() > 10 {
		t.Fatalf("cancel should stop sending: %d requests", srv.Requests())
	}
}

func TestEngineTooBigItemFailsLocally(t *testing.T) {
	srv := jevtest.New(t, jevtest.Options{})
	e := engine(t, srv, "k", 1, false)
	huge := jev.NewState(string(make([]byte, 120_000)))
	its := []*jev.Item{
		jev.NewItem(jev.NewState("small"), jev.Noul("q yes", nil, nil), 0),
		jev.NewItem(huge, jev.Noul("q", nil, nil), 1),
		jev.NewItem(jev.NewState("small2"), jev.Noul("q yes", nil, nil), 2),
	}
	res, err := collect(t, e, context.Background(), its)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 || res[0].Err != nil || res[1].Err == nil || res[2].Err != nil {
		t.Fatalf("results: %+v", res)
	}
	if srv.Requests() != 2 {
		t.Fatalf("the oversize item must not be sent: %d requests", srv.Requests())
	}
}

// TestJmaxConverges drives the adaptive scheduler against a server that
// accepts K requests at a time: it should grow towards K, survive the 429s
// without failing requests, and not collapse to 1.
func TestJmaxConverges(t *testing.T) {
	const K = 6
	t.Setenv("GREV_RPM", "1000000")
	t.Setenv("GREV_TPS", "1000000000")
	srv := jevtest.New(t, jevtest.Options{
		Capacity: K, RetryAfterMS: 20,
		Latency: func() time.Duration { return 15 * time.Millisecond },
	})
	e := engine(t, srv, "k", 0, true)
	e.MaxQ = 1
	var mu sync.Mutex
	peak, final := 0, 0
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
				l, adaptive, _, _ := e.Sched.State()
				if !adaptive {
					panic("not adaptive")
				}
				mu.Lock()
				peak = max(peak, l)
				mu.Unlock()
			}
		}
	}()
	t0 := time.Now()
	res, err := collect(t, e, context.Background(), items(jev.NewState("s"), 400, func(int) float64 { return 0.9 }))
	close(stop)
	el := time.Since(t0)
	if err != nil {
		t.Fatal(err)
	}
	failed := 0
	for _, r := range res {
		if r.Err != nil {
			failed++
		}
	}
	final, _, _, _ = e.Sched.State()
	mu.Lock()
	defer mu.Unlock()
	t.Logf("400 requests in %v; peak limit %d, final %d, server peak in-flight %d, 429s %d",
		el, peak, final, srv.PeakInflight(), srv.Count(429))
	if failed > 0 {
		t.Fatalf("%d requests failed under throttling", failed)
	}
	if peak < K/2+1 {
		t.Errorf("limit never grew towards capacity %d (peak %d)", K, peak)
	}
	if final < 2 {
		t.Errorf("limit collapsed to %d", final)
	}
	// Sequential would take 400 × 15ms = 6s; two at a time 3s.
	if el > 3*time.Second {
		t.Errorf("too slow: %v", el)
	}
}
