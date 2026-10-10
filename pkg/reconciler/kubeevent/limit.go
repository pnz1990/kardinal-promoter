// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package kubeevent

import (
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

func init() { ctrlmetrics.Registry.MustRegister(EventsDroppedTotal) }

// Defaults of --event-qps / --event-burst (Normal Events) and
// --event-warning-qps / --event-warning-burst (Warning Events) (#1682).
const (
	DefaultQPS          = 20
	DefaultBurst        = 100
	DefaultWarningQPS   = 5
	DefaultWarningBurst = 50
)

// EventsDroppedTotal counts the Events a Limited recorder dropped over its
// rate, by recorder name and Event type (Normal, Warning).
var EventsDroppedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "kardinal_events_dropped_total",
	Help: "Kubernetes Events not written because the recorder was over its rate (--event-qps/--event-burst for Normal, --event-warning-qps/--event-warning-burst for Warning).",
}, []string{"recorder", "type"})

// Limit is the rate of one Event type: at most QPS a second in bursts of at
// most Burst. QPS <= 0 is no limit.
type Limit struct {
	QPS   float64
	Burst int
}

func (l Limit) limiter() *rate.Limiter {
	if l.QPS <= 0 {
		return nil
	}
	return rate.NewLimiter(rate.Limit(l.QPS), max(l.Burst, 1))
}

// Limited bounds how fast rec writes Events. Normal and Warning Events have
// separate buckets, so a flood of Normal transitions never starves a
// one-shot Warning (HoldBundleMissing, NotificationDropped). An Event over
// its bucket is dropped and counted in kardinal_events_dropped_total, never
// queued, so a reconcile never waits.
//
// client-go's events.k8s.io broadcaster (behind controller-runtime's
// GetEventRecorder) starts one goroutine and one API write per Event, with
// no bound and no knob: when hundreds of Bundles and steps change state at
// once it held 2,135 goroutines and dialed hundreds of TLS connections to
// the API server (#1682). Its isomorphic-Event aggregation still applies to
// the Events that pass. Events are best effort: the durable record of every
// transition is the object's status and its AuditEvent. With neither type
// limited, rec is returned as is.
func Limited(rec events.EventRecorder, name string, normal, warning Limit) events.EventRecorder {
	if rec == nil || (normal.QPS <= 0 && warning.QPS <= 0) {
		return rec
	}
	return &limited{next: rec,
		normal: normal.limiter(), warning: warning.limiter(),
		droppedNormal:  EventsDroppedTotal.WithLabelValues(name, corev1.EventTypeNormal),
		droppedWarning: EventsDroppedTotal.WithLabelValues(name, corev1.EventTypeWarning)}
}

type limited struct {
	next                          events.EventRecorder
	normal, warning               *rate.Limiter // nil: no limit
	droppedNormal, droppedWarning prometheus.Counter
}

// Eventf writes the Event if its type's bucket allows it, else drops it.
// Any type other than Warning counts as Normal.
func (l *limited) Eventf(regarding, related runtime.Object, eventtype, reason, action, note string, args ...interface{}) {
	lim, dropped := l.normal, l.droppedNormal
	if eventtype == corev1.EventTypeWarning {
		lim, dropped = l.warning, l.droppedWarning
	}
	if lim != nil && !lim.Allow() {
		dropped.Inc()
		return
	}
	l.next.Eventf(regarding, related, eventtype, reason, action, note, args...)
}
