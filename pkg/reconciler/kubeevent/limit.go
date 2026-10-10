// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package kubeevent

import (
	"container/list"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

func init() { ctrlmetrics.Registry.MustRegister(EventsDroppedTotal) }

// Defaults of --event-qps / --event-burst (Normal Events, one bucket for the
// controller) and --event-warning-qps / --event-warning-burst (Warning
// Events, one bucket per namespace) (#1682).
const (
	DefaultQPS          = 20
	DefaultBurst        = 100
	DefaultWarningQPS   = 5
	DefaultWarningBurst = 50
)

// WarningNamespaces is how many namespaces' Warning buckets a Limited
// recorder keeps, least recently used out first, so its memory is bounded
// whatever the number of namespaces. A namespace whose bucket was evicted
// starts again with a full one.
const WarningNamespaces = 1024

// EventsDroppedTotal counts the Events a Limited recorder dropped over its
// rate, by recorder name and Event type (Normal, Warning).
var EventsDroppedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "kardinal_events_dropped_total",
	Help: "Kubernetes Events not written because the recorder was over its rate (--event-qps/--event-burst for Normal, --event-warning-qps/--event-warning-burst per namespace for Warning).",
}, []string{"recorder", "type"})

// Limit is the rate of one Event type: at most QPS a second in bursts of at
// most Burst. QPS 0 is no limit.
type Limit struct {
	QPS   float64
	Burst int
}

// burstFlag names the burst flag of each rate flag.
var burstFlag = map[string]string{"event-qps": "event-burst", "event-warning-qps": "event-warning-burst"}

// Validate refuses a limit main must not start with: a negative or
// non-finite rate, or a burst below 1. flag names the rate's flag
// (event-qps or event-warning-qps); the error names the flag at fault.
func (l Limit) Validate(flag string) error {
	if math.IsNaN(l.QPS) || math.IsInf(l.QPS, 0) || l.QPS < 0 {
		return fmt.Errorf("--%s %v: want a finite rate of 0 (no limit) or more", flag, l.QPS)
	}
	if l.Burst < 1 {
		return fmt.Errorf("--%s %d: want 1 or more", burstFlag[flag], l.Burst)
	}
	return nil
}

func (l Limit) limiter() *rate.Limiter {
	if l.QPS <= 0 {
		return nil
	}
	return rate.NewLimiter(rate.Limit(l.QPS), l.Burst)
}

// Limited bounds how fast rec writes Events, and never blocks: an Event over
// its bucket is dropped and counted in kardinal_events_dropped_total, never
// queued, so a reconcile never waits.
//
//   - Normal Events share one bucket (normal).
//   - Warning Events have a bucket per namespace of the regarding object
//     (warning each, at most WarningNamespaces kept), so neither a flood of
//     Normal transitions nor one tenant's burst of Warnings drops another
//     namespace's one-shot Warning (HoldBundleMissing, NotificationDropped).
//   - Every Event pays a token, repeats included: the broadcaster keys its
//     aggregation on the regarding object's resourceVersion and more, so a
//     repeat it would write anew must not pass free.
//
// client-go's events.k8s.io broadcaster (behind controller-runtime's
// GetEventRecorder) starts one goroutine and one API write per Event, with
// no bound and no knob: when hundreds of Bundles and steps change state at
// once it held 2,135 goroutines and dialed hundreds of TLS connections to
// the API server (#1682). Events are best effort; docs/installation.md
// (Kubernetes Events) lists the durable record of each Warning. With neither
// type limited, rec is returned as is.
func Limited(rec events.EventRecorder, name string, normal, warning Limit) events.EventRecorder {
	if rec == nil || (normal.QPS <= 0 && warning.QPS <= 0) {
		return rec
	}
	return &limited{next: rec, normal: normal.limiter(), warning: warning,
		warnings: newLRU[*rate.Limiter](WarningNamespaces), now: time.Now,
		droppedNormal:  EventsDroppedTotal.WithLabelValues(name, corev1.EventTypeNormal),
		droppedWarning: EventsDroppedTotal.WithLabelValues(name, corev1.EventTypeWarning)}
}

type limited struct {
	next    events.EventRecorder
	normal  *rate.Limiter // nil: no limit
	warning Limit         // per namespace

	mu       sync.Mutex
	warnings *lru[*rate.Limiter] // namespace -> Warning bucket
	now      func() time.Time

	droppedNormal, droppedWarning prometheus.Counter
}

// Eventf writes the Event if its bucket allows it, else drops it. Any type
// but Warning counts as Normal.
func (l *limited) Eventf(regarding, related runtime.Object, eventtype, reason, action, note string, args ...interface{}) {
	if !l.allow(regarding, eventtype) {
		if eventtype == corev1.EventTypeWarning {
			l.droppedWarning.Inc()
		} else {
			l.droppedNormal.Inc()
		}
		return
	}
	l.next.Eventf(regarding, related, eventtype, reason, action, note, args...)
}

func (l *limited) allow(regarding runtime.Object, eventtype string) bool {
	lim := l.normal
	l.mu.Lock()
	defer l.mu.Unlock()
	if eventtype == corev1.EventTypeWarning {
		lim = nil
		if l.warning.QPS > 0 {
			// The regarding object's namespace; a cluster-scoped object
			// (the shard gate's Namespace Warnings) shares the "" bucket.
			ns := ""
			if m, err := meta.Accessor(regarding); err == nil {
				ns = m.GetNamespace()
			}
			var ok bool
			if lim, ok = l.warnings.get(ns); !ok {
				lim = l.warning.limiter()
				l.warnings.put(ns, lim)
			}
		}
	}
	return lim == nil || lim.AllowN(l.now(), 1)
}

// lru is a map of at most size entries that evicts the least recently used.
// Not safe for concurrent use: limited holds its mutex.
type lru[V any] struct {
	size  int
	order *list.List // front: most recent; values are *entry[V]
	items map[string]*list.Element
}

type entry[V any] struct {
	key string
	val V
}

func newLRU[V any](size int) *lru[V] {
	return &lru[V]{size: size, order: list.New(), items: map[string]*list.Element{}}
}

func (c *lru[V]) get(key string) (V, bool) {
	if e, ok := c.items[key]; ok {
		c.order.MoveToFront(e)
		return e.Value.(*entry[V]).val, true
	}
	var zero V
	return zero, false
}

func (c *lru[V]) put(key string, val V) {
	if e, ok := c.items[key]; ok {
		e.Value.(*entry[V]).val = val
		c.order.MoveToFront(e)
		return
	}
	c.items[key] = c.order.PushFront(&entry[V]{key: key, val: val})
	if c.order.Len() > c.size {
		old := c.order.Back()
		c.order.Remove(old)
		delete(c.items, old.Value.(*entry[V]).key)
	}
}

func (c *lru[V]) len() int { return c.order.Len() }
