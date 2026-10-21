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

// frozen is a clock that never moves: the buckets never refill, so what
// passes is exactly each burst however slowly a loaded host runs the test.
func frozen() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }

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
	rec := kubeevent.LimitedAt(next, "test-limited", kubeevent.Limit{QPS: 1, Burst: 5}, kubeevent.Limit{QPS: 1, Burst: 2}, frozen)
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
	rec = kubeevent.LimitedAt(next, "test-limited-one", kubeevent.Limit{QPS: 1, Burst: 1}, kubeevent.Limit{}, frozen)
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
	rec := kubeevent.LimitedAt(next, "test-flood",
		kubeevent.Limit{QPS: kubeevent.DefaultQPS, Burst: kubeevent.DefaultBurst},
		kubeevent.Limit{QPS: kubeevent.DefaultWarningQPS, Burst: kubeevent.DefaultWarningBurst}, frozen)
	w0 := dropped("test-flood", "Warning")
	for i := 0; i < 5000; i++ {
		rec.Eventf(&corev1.Pod{}, nil, corev1.EventTypeNormal, "Superseded", "Supersede", "bundle %d", i)
	}
	require.EqualValues(t, kubeevent.DefaultBurst, next.normal.Load(), "the Normal bucket is empty")
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
	rec := kubeevent.LimitedAt(next, "test-tenants", kubeevent.Limit{QPS: kubeevent.DefaultQPS, Burst: kubeevent.DefaultBurst},
		kubeevent.Limit{QPS: kubeevent.DefaultWarningQPS, Burst: kubeevent.DefaultWarningBurst}, frozen)
	for i := 0; i < 1000; i++ {
		rec.Eventf(pod("tenant-a", fmt.Sprintf("step-%d", i)), nil, corev1.EventTypeWarning, "PromotionFailed", "Promote", "step %d failed", i)
	}
	require.EqualValues(t, kubeevent.DefaultWarningBurst, next.warning.Load(), "tenant A's burst is capped at its own bucket")
	rec.Eventf(pod("tenant-b", "pipeline"), nil, corev1.EventTypeWarning, "HoldBundleMissing", "CheckHold", "hold of prod")
	assert.EqualValues(t, kubeevent.DefaultWarningBurst+1, next.warning.Load(), "tenant B's Warning is written")
}

// TestLimited_RepeatsPayTokens: a repeat of the same Event on an object
// whose resourceVersion changes (a status write between them, the usual
// case) is a new Event to client-go's broadcaster, which keys on the
// object reference including resourceVersion, so it must pay a token like
// any other: of 500 such repeats exactly each bucket's burst reaches the
// broadcaster, and through the real broadcaster each is one write, none
// merged into another's series. It counts what reaches the broadcaster and
// what the sink writes, not how many writes are in flight at once:
// client-go v0.38 records Events one at a time, v0.37 each in a goroutine
// of its own (#1819).
//
// Covers PERF-EVENTS-01.
func TestLimited_RepeatsPayTokens(t *testing.T) {
	sink := &slowSink{gate: make(chan struct{})}
	b := events.NewBroadcaster(sink)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, b.StartRecordingToSinkWithContext(ctx))
	defer b.Shutdown()
	broadcast := &counting{EventRecorder: b.NewRecorder(scheme.Scheme, "kardinal-test")}
	rec := kubeevent.LimitedAt(broadcast, "test-repeats",
		kubeevent.Limit{QPS: 1, Burst: 5}, kubeevent.Limit{QPS: 1, Burst: 5}, frozen)
	for i := 0; i < 500; i++ {
		obj := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "gate", UID: "u", ResourceVersion: fmt.Sprint(i + 1)}}
		rec.Eventf(obj, nil, corev1.EventTypeWarning, "Blocked", "Evaluate", "gate %s blocking", "weekend")
		rec.Eventf(obj, nil, corev1.EventTypeNormal, "Evaluated", "Evaluate", "gate evaluated")
	}
	// The clock does not move, so exactly each bucket's burst gets through.
	assert.EqualValues(t, 5+5, broadcast.n.Load(), "each bucket's burst reaches the broadcaster, the repeats included")
	sink.settle(t)
	require.Eventually(t, func() bool { return sink.writes.Load() == 5+5 }, 30*time.Second, time.Millisecond,
		"each is one write, none merged into a series")
	assert.LessOrEqual(t, sink.peak.Load(), int64(5+5))
}

// counting is an events.EventRecorder that counts the Events it passes on.
// The recorder is embedded, so it keeps compiling when client-go adds a
// method to the interface.
type counting struct {
	events.EventRecorder
	n atomic.Int64
}

func (c *counting) Eventf(regarding, related runtime.Object, eventtype, reason, action, note string, args ...interface{}) {
	c.n.Add(1)
	c.EventRecorder.Eventf(regarding, related, eventtype, reason, action, note, args...)
}

// TestLimitValidate: main refuses a negative or non-finite rate and a burst
// below 1 at startup.
//
// Covers PERF-EVENTS-01.
func TestLimitValidate(t *testing.T) {
	for _, l := range []kubeevent.Limit{{QPS: 0, Burst: 1}, {QPS: 20, Burst: 100}, {QPS: 0.5, Burst: 1}} {
		assert.NoError(t, l.Validate("event-qps"), "%+v", l)
	}
	for _, l := range []kubeevent.Limit{{QPS: -1, Burst: 10}, {QPS: math.NaN(), Burst: 10}, {QPS: math.Inf(1), Burst: 10}} {
		err := l.Validate("event-qps")
		require.Error(t, err, "%+v", l)
		assert.Contains(t, err.Error(), "--event-qps")
	}
	for flag, burst := range map[string]string{"event-qps": "--event-burst", "event-warning-qps": "--event-warning-burst"} {
		for _, l := range []kubeevent.Limit{{QPS: 20, Burst: 0}, {QPS: 0, Burst: -1}} {
			err := l.Validate(flag)
			require.Error(t, err, "%+v", l)
			assert.Contains(t, err.Error(), burst, "the error names the burst flag")
		}
	}
}

// slowSink is an events.EventSink whose writes wait for gate, like an API
// server under load, and that records the most writes in flight at once.
type slowSink struct {
	// gate holds every write until it is closed, so all the writes the
	// broadcaster starts are in flight at once.
	gate           chan struct{}
	inFlight, peak atomic.Int64
	// started counts the writes begun, writes those finished. A write counts
	// as finished before it leaves inFlight, so inFlight 0 means writes is
	// final: a write preempted between the two can no longer be missed (#1794).
	started, writes atomic.Int64
}

func (s *slowSink) write(e *eventsv1.Event) (*eventsv1.Event, error) {
	s.started.Add(1)
	n := s.inFlight.Add(1)
	for p := s.peak.Load(); n > p && !s.peak.CompareAndSwap(p, n); p = s.peak.Load() {
	}
	<-s.gate
	s.writes.Add(1)
	s.inFlight.Add(-1)
	return e, nil
}

// settle releases the held writes and waits until every write started has
// finished: a count, polled, never a sleep.
func (s *slowSink) settle(t *testing.T) {
	t.Helper()
	close(s.gate)
	require.Eventually(t, func() bool { return s.inFlight.Load() == 0 && s.writes.Load() == s.started.Load() },
		30*time.Second, time.Millisecond, "every write started finishes")
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
// workers.
func burst(rec events.EventRecorder) {
	var wg sync.WaitGroup
	work := make(chan int)
	for w := 0; w < 40; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				obj := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("b-%d", i), Namespace: "ns", UID: "u"}}
				rec.Eventf(obj, nil, corev1.EventTypeNormal, "Superseded", "Supersede", "bundle %d superseded", i)
				rec.Eventf(obj, nil, corev1.EventTypeWarning, "PromotionFailed", "Promote", "bundle %d failed", i)
			}
		}()
	}
	for i := 0; i < 1000; i++ {
		work <- i
	}
	close(work)
	wg.Wait()
}

// TestLimited_BurstShape (#1682): through client-go's real events.k8s.io
// broadcaster and a sink that holds every write until the burst is over, an
// unlimited recorder hands all 2,000 Events of the burst to the broadcaster,
// which writes hundreds; the limited one (the defaults, on a clock that does
// not move, so the buckets do not refill) passes exactly its two bursts on
// (the burst sends a Normal and a Warning per Bundle, all in one namespace)
// and drops the rest, and they are all the sink writes. The burst returns
// while every write is still held: no Eventf call waits on a write. Counts,
// not wall-clock time, so host load cannot fail it; and what reaches the
// broadcaster and what is written, not how many writes are in flight at
// once, which client-go v0.38 makes one (it records Events one at a time;
// v0.37 starts a goroutine for each, #1819).
//
// Covers PERF-EVENTS-01.
func TestLimited_BurstShape(t *testing.T) {
	run := func(limit bool, wantWrites func(int64) bool) (forwarded, peak, writes int64) {
		sink := &slowSink{gate: make(chan struct{})}
		b := events.NewBroadcaster(sink)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		require.NoError(t, b.StartRecordingToSinkWithContext(ctx))
		defer b.Shutdown()
		broadcast := &counting{EventRecorder: b.NewRecorder(scheme.Scheme, "kardinal-test")}
		var rec events.EventRecorder = broadcast
		if limit {
			rec = kubeevent.LimitedAt(rec, "test-burst", kubeevent.Limit{QPS: kubeevent.DefaultQPS, Burst: kubeevent.DefaultBurst},
				kubeevent.Limit{QPS: kubeevent.DefaultWarningQPS, Burst: kubeevent.DefaultWarningBurst}, frozen)
		}
		done := make(chan struct{})
		go func() { burst(rec); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Minute): // a failure guard, not a measure
			close(sink.gate)
			t.Fatal("the burst did not return while the writes were held: an Eventf call waits on a write")
		}
		assert.Zero(t, sink.writes.Load(), "every write is still held when the burst returns")
		forwarded = broadcast.n.Load()
		sink.settle(t)
		// The broadcaster hands Events to its sink asynchronously.
		require.Eventually(t, func() bool { return wantWrites(sink.writes.Load()) }, 30*time.Second, time.Millisecond,
			"%d writes so far", sink.writes.Load())
		return forwarded, sink.peak.Load(), sink.writes.Load()
	}
	forwarded, peak, writes := run(false, func(w int64) bool { return w > 300 })
	t.Logf("unlimited: %d Events to the broadcaster, %d writes, %d in flight at peak", forwarded, writes, peak)
	assert.EqualValues(t, 2000, forwarded, "the unlimited recorder hands the whole burst to the broadcaster")
	assert.Greater(t, writes, int64(300), "which writes hundreds (the #1682 burst)")

	bursts := int64(kubeevent.DefaultBurst + kubeevent.DefaultWarningBurst)
	forwarded, peak, writes = run(true, func(w int64) bool { return w >= bursts })
	t.Logf("limited: %d Events to the broadcaster, %d writes, %d in flight at peak", forwarded, writes, peak)
	assert.Equal(t, bursts, forwarded, "exactly the two bursts reach the broadcaster, the rest is dropped")
	assert.Equal(t, bursts, writes, "and are all it writes")
	assert.LessOrEqual(t, peak, bursts)
}
