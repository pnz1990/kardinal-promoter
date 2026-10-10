// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package kubeevent_test

import (
	"context"
	"fmt"
	"math"
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

type countingRecorder struct{ normal, warning atomic.Int64 }

func (c *countingRecorder) Eventf(_, _ runtime.Object, eventtype, _, _, _ string, _ ...interface{}) {
	if eventtype == corev1.EventTypeWarning {
		c.warning.Add(1)
	} else {
		c.normal.Add(1)
	}
}

func dropped(name, typ string) float64 {
	return testutil.ToFloat64(kubeevent.EventsDroppedTotal.WithLabelValues(name, typ))
}

// TestLimited (#1682): each type's burst passes, the rest is dropped and
// counted by type, never queued; no limit on either type and a nil
// recorder pass through.
//
// Covers PERF-EVENTS-01.
func TestLimited(t *testing.T) {
	next := &countingRecorder{}
	rec := kubeevent.Limited(next, "test-limited", kubeevent.Limit{QPS: 1, Burst: 5}, kubeevent.Limit{QPS: 1, Burst: 2})
	n0, w0 := dropped("test-limited", "Normal"), dropped("test-limited", "Warning")
	for i := 0; i < 20; i++ {
		rec.Eventf(&corev1.Pod{}, nil, corev1.EventTypeNormal, "R", "A", "n %d", i)
		rec.Eventf(&corev1.Pod{}, nil, corev1.EventTypeWarning, "W", "A", "w %d", i)
	}
	assert.EqualValues(t, 5, next.normal.Load(), "the Normal burst passes")
	assert.EqualValues(t, 2, next.warning.Load(), "the Warning burst passes")
	assert.InDelta(t, 15, dropped("test-limited", "Normal")-n0, 0.5)
	assert.InDelta(t, 18, dropped("test-limited", "Warning")-w0, 0.5)

	assert.Same(t, next, kubeevent.Limited(next, "x", kubeevent.Limit{}, kubeevent.Limit{}), "no limit: rec as is")
	assert.Nil(t, kubeevent.Limited(nil, "x", kubeevent.Limit{QPS: 1, Burst: 5}, kubeevent.Limit{QPS: 1, Burst: 5}))

	// One type unlimited: only the other is bounded.
	next = &countingRecorder{}
	rec = kubeevent.Limited(next, "test-limited-one", kubeevent.Limit{QPS: 1, Burst: 1}, kubeevent.Limit{})
	for i := 0; i < 10; i++ {
		rec.Eventf(&corev1.Pod{}, nil, corev1.EventTypeNormal, "R", "A", "n %d", i)
		rec.Eventf(&corev1.Pod{}, nil, corev1.EventTypeWarning, "W", "A", "w %d", i)
	}
	assert.EqualValues(t, 1, next.normal.Load())
	assert.EqualValues(t, 10, next.warning.Load())
}

// TestLimited_WarningAfterNormalFlood: a flood of Normal Events that empties
// their bucket never drops a one-shot Warning such as HoldBundleMissing or
// NotificationDropped, with the defaults.
//
// Covers PERF-EVENTS-01.
func TestLimited_WarningAfterNormalFlood(t *testing.T) {
	next := &countingRecorder{}
	rec := kubeevent.Limited(next, "test-flood",
		kubeevent.Limit{QPS: kubeevent.DefaultQPS, Burst: kubeevent.DefaultBurst},
		kubeevent.Limit{QPS: kubeevent.DefaultWarningQPS, Burst: kubeevent.DefaultWarningBurst})
	w0 := dropped("test-flood", "Warning")
	for i := 0; i < 5000; i++ {
		rec.Eventf(&corev1.Pod{}, nil, corev1.EventTypeNormal, "Superseded", "Supersede", "bundle %d", i)
	}
	require.Less(t, next.normal.Load(), int64(5000), "the Normal bucket is empty")
	rec.Eventf(&corev1.Pod{}, nil, corev1.EventTypeWarning, "HoldBundleMissing", "CheckHold", "hold of prod")
	assert.EqualValues(t, 1, next.warning.Load(), "the Warning is written")
	assert.Zero(t, dropped("test-flood", "Warning")-w0)
}

func pod(ns, name string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
}

// TestLimited_WarningsPerNamespace: one tenant's burst of Warnings empties
// only its own namespace's bucket; another tenant's Warning is still
// written.
//
// Covers PERF-EVENTS-01.
func TestLimited_WarningsPerNamespace(t *testing.T) {
	next := &countingRecorder{}
	rec := kubeevent.Limited(next, "test-tenants", kubeevent.Limit{QPS: kubeevent.DefaultQPS, Burst: kubeevent.DefaultBurst},
		kubeevent.Limit{QPS: kubeevent.DefaultWarningQPS, Burst: kubeevent.DefaultWarningBurst})
	for i := 0; i < 1000; i++ {
		rec.Eventf(pod("tenant-a", fmt.Sprintf("step-%d", i)), nil, corev1.EventTypeWarning, "PromotionFailed", "Promote", "step %d failed", i)
	}
	require.EqualValues(t, kubeevent.DefaultWarningBurst, next.warning.Load(), "tenant A's burst is capped at its own bucket")
	rec.Eventf(pod("tenant-b", "pipeline"), nil, corev1.EventTypeWarning, "HoldBundleMissing", "CheckHold", "hold of prod")
	assert.EqualValues(t, kubeevent.DefaultWarningBurst+1, next.warning.Load(), "tenant B's Warning is written")
}

// TestLimited_Dedupe: an Event repeating one just written (same object,
// type, reason and message) passes without a token, so repeats the
// broadcaster folds into a series do not use up the bucket.
//
// Covers PERF-EVENTS-01.
func TestLimited_Dedupe(t *testing.T) {
	next := &countingRecorder{}
	rec := kubeevent.Limited(next, "test-dedupe", kubeevent.Limit{QPS: 1, Burst: 2}, kubeevent.Limit{QPS: 1, Burst: 2})
	for i := 0; i < 50; i++ {
		rec.Eventf(pod("ns", "gate"), nil, corev1.EventTypeWarning, "Blocked", "Evaluate", "gate %s blocking", "weekend")
	}
	assert.EqualValues(t, 50, next.warning.Load(), "repeats pass (the broadcaster aggregates them)")
	rec.Eventf(pod("ns", "gate"), nil, corev1.EventTypeWarning, "Blocked", "Evaluate", "gate %s blocking", "freeze")
	assert.EqualValues(t, 51, next.warning.Load(), "a new message takes the second token")
	rec.Eventf(pod("ns", "gate"), nil, corev1.EventTypeWarning, "Blocked", "Evaluate", "gate %s blocking", "hours")
	assert.EqualValues(t, 51, next.warning.Load(), "the bucket is empty for new messages")
}

// TestLimitValidate: main refuses a negative or non-finite rate and a burst
// below 1 at startup.
//
// Covers PERF-EVENTS-01.
func TestLimitValidate(t *testing.T) {
	for _, l := range []kubeevent.Limit{{QPS: 0, Burst: 1}, {QPS: 20, Burst: 100}, {QPS: 0.5, Burst: 1}} {
		assert.NoError(t, l.Validate("event-qps"), "%+v", l)
	}
	for _, l := range []kubeevent.Limit{{QPS: -1, Burst: 10}, {QPS: math.NaN(), Burst: 10}, {QPS: math.Inf(1), Burst: 10}, {QPS: 20, Burst: 0}, {QPS: 0, Burst: -1}} {
		assert.Error(t, l.Validate("event-qps"), "%+v", l)
	}
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
// one (the defaults) stays near its bursts (the burst sends a Normal and a
// Warning per Bundle), drops the rest, and no Eventf
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
			rec = kubeevent.Limited(rec, "test-burst", kubeevent.Limit{QPS: kubeevent.DefaultQPS, Burst: kubeevent.DefaultBurst},
				kubeevent.Limit{QPS: kubeevent.DefaultWarningQPS, Burst: kubeevent.DefaultWarningBurst})
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
	assert.LessOrEqual(t, peak, int64(kubeevent.DefaultBurst+kubeevent.DefaultWarningBurst+10), "in flight stays near the two bursts")
	assert.Less(t, writes, int64(2000), "the rest is dropped")
	assert.Less(t, slowest, 50*time.Millisecond, "a reconcile never waits on an Event")
}
