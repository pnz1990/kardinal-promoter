// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package kubeevent

import (
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

func init() { ctrlmetrics.Registry.MustRegister(EventsDroppedTotal) }

// Defaults of --event-qps and --event-burst (#1682).
const (
	DefaultQPS   = 20
	DefaultBurst = 100
)

// EventsDroppedTotal counts the Events a Limited recorder dropped over its
// rate, by recorder name.
var EventsDroppedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "kardinal_events_dropped_total",
	Help: "Kubernetes Events not written because the recorder was over --event-qps / --event-burst.",
}, []string{"recorder"})

// Limited bounds how fast rec writes Events: at most qps a second, in bursts
// of at most burst; an Event over the limit is dropped and counted in
// kardinal_events_dropped_total, never queued, so a reconcile never waits.
//
// client-go's events.k8s.io broadcaster (behind controller-runtime's
// GetEventRecorder) starts one goroutine and one API write per Event, with
// no bound and no knob: when hundreds of Bundles and steps change state at
// once it held 2,135 goroutines and dialed hundreds of TLS connections to
// the API server (#1682). Its isomorphic-Event aggregation still applies to
// the Events that pass. Events are best effort: every transition is also in
// the object's status and in an AuditEvent. qps <= 0 returns rec unlimited.
func Limited(rec events.EventRecorder, name string, qps float64, burst int) events.EventRecorder {
	if rec == nil || qps <= 0 {
		return rec
	}
	if burst < 1 {
		burst = 1
	}
	return &limited{next: rec, lim: rate.NewLimiter(rate.Limit(qps), burst), dropped: EventsDroppedTotal.WithLabelValues(name)}
}

type limited struct {
	next    events.EventRecorder
	lim     *rate.Limiter
	dropped prometheus.Counter
}

// Eventf writes the Event if the rate allows it, else drops it.
func (l *limited) Eventf(regarding, related runtime.Object, eventtype, reason, action, note string, args ...interface{}) {
	if !l.lim.Allow() {
		l.dropped.Inc()
		return
	}
	l.next.Eventf(regarding, related, eventtype, reason, action, note, args...)
}
