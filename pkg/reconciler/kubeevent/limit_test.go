// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package kubeevent_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/kubeevent"
)

type countingRecorder struct{ n atomic.Int64 }

func (c *countingRecorder) Eventf(runtime.Object, runtime.Object, string, string, string, string, ...interface{}) {
	c.n.Add(1)
}

// TestLimited (#1682): the burst passes, the rest is dropped and counted,
// never queued; qps 0 and a nil recorder pass through.
//
// Covers PERF-EVENTS-01.
func TestLimited(t *testing.T) {
	next := &countingRecorder{}
	rec := kubeevent.Limited(next, "test-limited", 1, 5)
	before := testutil.ToFloat64(kubeevent.EventsDroppedTotal.WithLabelValues("test-limited"))
	for i := 0; i < 20; i++ {
		rec.Eventf(&corev1.Pod{}, nil, corev1.EventTypeNormal, "R", "A", "n %d", i)
	}
	assert.EqualValues(t, 5, next.n.Load(), "the burst passes")
	assert.InDelta(t, 15, testutil.ToFloat64(kubeevent.EventsDroppedTotal.WithLabelValues("test-limited"))-before, 0.5, "the rest is dropped and counted")

	assert.Same(t, next, kubeevent.Limited(next, "x", 0, 5), "qps 0: no limit")
	assert.Nil(t, kubeevent.Limited(nil, "x", 1, 5))
}

// slowSink is an events.EventSink whose writes take latency, like an API
// server under load, and that records the most writes in flight at once.
type slowSink struct {
	latency        time.Duration
	inFlight, peak atomic.Int64
	writes         atomic.Int64
}

func (s *slowSink) write(e *eventsv1.Event) (*eventsv1.Event, error) {
	n := s.inFlight.Add(1)
	for p := s.peak.Load(); n > p && !s.peak.CompareAndSwap(p, n); p = s.peak.Load() {
	}
	time.Sleep(s.latency)
	s.inFlight.Add(-1)
	s.writes.Add(1)
	return e, nil
}
func (s *slowSink) Create(_ context.Context, e *eventsv1.Event) (*eventsv1.Event, error) {
	return s.write(e)
}
func (s *slowSink) Update(_ context.Context, e *eventsv1.Event) (*eventsv1.Event, error) {
	return s.write(e)
}
func (s *slowSink) Patch(_ context.Context, e *eventsv1.Event, _ []byte) (*eventsv1.Event, error) {
	return s.write(e)
}

// burst emits the Events of a ChaosTokenRotation-sized burst through rec:
// 1,000 Bundles changing state at once, two Events each, from 40 reconcile
// workers. It returns the slowest Eventf call.
func burst(rec events.EventRecorder) time.Duration {
	var wg sync.WaitGroup
	var slowest atomic.Int64
	work := make(chan int)
	for w := 0; w < 40; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				obj := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("b-%d", i), Namespace: "ns", UID: "u"}}
				start := time.Now()
				rec.Eventf(obj, nil, corev1.EventTypeNormal, "Superseded", "Supersede", "bundle %d superseded", i)
				rec.Eventf(obj, nil, corev1.EventTypeWarning, "PromotionFailed", "Promote", "bundle %d failed", i)
				if d := int64(time.Since(start)); d > slowest.Load() {
					slowest.Store(d)
				}
			}
		}()
	}
	for i := 0; i < 1000; i++ {
		work <- i
	}
	close(work)
	wg.Wait()
	return time.Duration(slowest.Load())
}

// TestLimited_BurstShape (#1682): through client-go's real events.k8s.io
// broadcaster and a sink whose writes take 100ms, an unlimited recorder has
// hundreds of writes (and their goroutines) in flight at once; the limited
// one (the defaults) stays near its burst, drops the rest, and no Eventf
// call waits.
//
// Covers PERF-EVENTS-01.
func TestLimited_BurstShape(t *testing.T) {
	run := func(limit bool) (peak, writes int64, slowest time.Duration) {
		sink := &slowSink{latency: 100 * time.Millisecond}
		b := events.NewBroadcaster(sink)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		require.NoError(t, b.StartRecordingToSinkWithContext(ctx))
		defer b.Shutdown()
		var rec events.EventRecorder = b.NewRecorder(scheme.Scheme, "kardinal-test")
		if limit {
			rec = kubeevent.Limited(rec, "test-burst", kubeevent.DefaultQPS, kubeevent.DefaultBurst)
		}
		slowest = burst(rec)
		require.Eventually(t, func() bool { return sink.inFlight.Load() == 0 && sink.writes.Load() > 0 }, 10*time.Second, 20*time.Millisecond)
		time.Sleep(300 * time.Millisecond) // stragglers
		return sink.peak.Load(), sink.writes.Load(), slowest
	}
	peak, writes, _ := run(false)
	t.Logf("unlimited: %d writes, %d in flight at peak", writes, peak)
	assert.Greater(t, peak, int64(300), "the unlimited broadcaster writes hundreds at once (the #1682 burst)")

	peak, writes, slowest := run(true)
	t.Logf("limited: %d writes, %d in flight at peak, slowest Eventf %s", writes, peak, slowest)
	assert.LessOrEqual(t, peak, int64(kubeevent.DefaultBurst+10), "in flight stays near --event-burst")
	assert.Less(t, writes, int64(2000), "the rest is dropped")
	assert.Less(t, slowest, 50*time.Millisecond, "a reconcile never waits on an Event")
}
