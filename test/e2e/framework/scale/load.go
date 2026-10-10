// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"
)

// LoadStats is what a load generator did.
type LoadStats struct {
	Created int `json:"created"`
	Errors  int `json:"errors"`
	// FirstError is the first create error, if any.
	FirstError string `json:"firstError,omitempty"`
	// CreateP50 and CreateP99 are the Bundle create call's latency, in ms.
	CreateP50 float64 `json:"createP50ms"`
	CreateP99 float64 `json:"createP99ms"`
	// Seconds is how long the generator ran.
	Seconds float64 `json:"seconds"`
	// Rate is Bundles created a second.
	Rate float64 `json:"rate"`
	// WarmAt is when every Pipeline had more than HistoryLimit Bundles: the
	// number of Bundles kept stops growing there (Sustained only; zero if it
	// never happened).
	WarmAt time.Time `json:"warmAt,omitempty"`
}

// HistoryLimit is the Bundles each fleet Pipeline keeps: the controller's
// default historyLimit (pkg/reconciler/bundle defaultHistoryLimit), which
// the fleet's Pipelines do not set.
const HistoryLimit = 50

type loadRecorder struct {
	mu        sync.Mutex
	latencies []float64
	errors    int
	first     string
	start     time.Time
}

func (l *loadRecorder) record(c Created, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		l.errors++
		if l.first == "" {
			l.first = err.Error()
		}
		return
	}
	l.latencies = append(l.latencies, float64(c.Latency.Microseconds())/1000)
}

func (l *loadRecorder) stats() LoadStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := LoadStats{Created: len(l.latencies), Errors: l.errors, FirstError: l.first, Seconds: time.Since(l.start).Seconds()}
	if s.Seconds > 0 {
		s.Rate = float64(s.Created) / s.Seconds
	}
	v := append([]float64(nil), l.latencies...)
	sort.Float64s(v)
	if n := len(v); n > 0 {
		s.CreateP50, s.CreateP99 = v[n/2], v[min(n-1, n*99/100)]
	}
	return s
}

// Burst creates n Bundles round-robin over pipelines with workers
// concurrent create calls, as a CI system draining a backlog does. Bundle i
// of a pipeline gets Tag(pipeline, i).
func (f *Fleet) Burst(t *testing.T, pipelines []string, n, workers int) LoadStats {
	t.Helper()
	rec := &loadRecorder{start: time.Now()}
	_ = Parallel(workers, n, func(i int) error {
		p := pipelines[i%len(pipelines)]
		c, err := f.CreateBundle(context.Background(), p, Tag(p, i/len(pipelines)+1))
		rec.record(c, err)
		return nil
	})
	s := rec.stats()
	if s.Errors > 0 {
		t.Errorf("burst: %d of %d Bundle creates failed; first: %s", s.Errors, n, s.FirstError)
	}
	return s
}

// Sustained creates Bundles at rate a second for d on random pipelines
// until ctx ends, and returns when it stops. Each create runs on its own
// goroutine, so a slow API server shows as latency, not as a lower rate.
func (f *Fleet) Sustained(ctx context.Context, t *testing.T, pipelines []string, rate float64, d time.Duration) LoadStats {
	t.Helper()
	rec := &loadRecorder{start: time.Now()}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	tick := time.NewTicker(time.Duration(float64(time.Second) / rate))
	defer tick.Stop()
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		next   = map[string]int{}
		full   int // Pipelines with more than HistoryLimit Bundles
		warmAt time.Time
	)
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			s := rec.stats()
			s.WarmAt = warmAt
			if s.Errors > 0 {
				t.Errorf("sustained load: %d of %d Bundle creates failed; first: %s", s.Errors, s.Errors+s.Created, s.FirstError)
			}
			return s
		case <-tick.C:
			p := pipelines[f.rng.Intn(len(pipelines))]
			mu.Lock()
			next[p]++
			i := next[p]
			if i == HistoryLimit+1 {
				if full++; full == len(pipelines) {
					warmAt = time.Now()
				}
			}
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				cctx, ccancel := context.WithTimeout(context.Background(), time.Minute)
				defer ccancel()
				c, err := f.CreateBundle(cctx, p, Tag(p, i))
				rec.record(c, err)
			}()
		}
	}
}
