// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package audit is the AuditEvent outbox (#1552). A reconciler that records
// a transition does not create the AuditEvent directly. It stores a
// PendingAuditEvent in its own status, in the same patch as the transition.
// Flush then creates the AuditEvents and the reconciler removes the entries
// that were written. An entry whose create failed stays in status, and a
// later reconcile creates it. Names are fixed when the entry is stored, so a
// create that already succeeded returns AlreadyExists, which counts as
// written.
//
// The two writes cannot both happen atomically. The outbox makes the record
// survive whatever happens between them: an etcd timeout, a 401 from a
// killed leader's token, or a crash.
package audit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

var (
	// WriteFailures counts AuditEvent creates that failed and were kept in
	// the outbox to be retried, by the kind of the writing object.
	WriteFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kardinal_audit_write_failures_total",
		Help: "AuditEvent creates that failed and stay in the writer's status.pendingAuditEvents to be retried.",
	}, []string{"kind"})

	// Dropped counts audit records that were lost: the outbox was full, or
	// the API server rejected the record as invalid.
	Dropped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kardinal_audit_events_dropped_total",
		Help: "Audit records never written: the outbox was full (reason overflow) or the record was invalid (reason invalid).",
	}, []string{"kind", "reason"})
)

func init() {
	ctrlmetrics.Registry.MustRegister(WriteFailures, Dropped)
}

// Entry builds the outbox entry for an AuditEvent named name, with labels
// and spec, created at at. spec.Timestamp is set from at.
func Entry(name string, labels map[string]string, spec v1alpha1.AuditEventSpec, at metav1.Time) v1alpha1.PendingAuditEvent {
	spec.Timestamp = at
	return v1alpha1.PendingAuditEvent{
		Name:      name,
		Labels:    labels,
		Spec:      spec,
		CreatedAt: at.UTC().Format(time.RFC3339Nano),
	}
}

// Enqueue appends e to pending unless an entry with its name is already
// there. When the outbox is full, the oldest entry is dropped, logged and
// counted: the outbox is bounded so a long API outage cannot grow the
// writer's status without limit. kind labels the metric.
func Enqueue(ctx context.Context, kind string, pending []v1alpha1.PendingAuditEvent,
	e v1alpha1.PendingAuditEvent) []v1alpha1.PendingAuditEvent {
	for _, p := range pending {
		if p.Name == e.Name {
			return pending
		}
	}
	pending = append(pending, e)
	for len(pending) > v1alpha1.MaxPendingAuditEvents {
		zerolog.Ctx(ctx).Error().Str("auditEvent", pending[0].Name).Str("kind", kind).
			Msg("audit outbox full; oldest unwritten AuditEvent dropped")
		Dropped.WithLabelValues(kind, "overflow").Inc()
		pending = pending[1:]
	}
	return pending
}

// Flush creates the AuditEvents of pending in namespace and returns the
// entries still unwritten, in order, with the first error. An entry is
// written when the create succeeds or returns AlreadyExists. It is dropped
// without error when the namespace is being deleted (the deletion removes
// the records next) or the API server rejects it as invalid, which no retry
// can fix. Every other error keeps the entry.
func Flush(ctx context.Context, c client.Client, kind, namespace string,
	pending []v1alpha1.PendingAuditEvent) ([]v1alpha1.PendingAuditEvent, error) {
	var (
		remaining []v1alpha1.PendingAuditEvent
		errs      []error
	)
	for i := range pending {
		p := &pending[i]
		err := c.Create(ctx, build(namespace, p))
		switch {
		case client.IgnoreAlreadyExists(err) == nil:
		case apierrors.HasStatusCause(err, corev1.NamespaceTerminatingCause):
			zerolog.Ctx(ctx).Debug().Err(err).Str("auditEvent", p.Name).
				Msg("namespace is being deleted — AuditEvent not written")
		case apierrors.IsInvalid(err) || apierrors.IsBadRequest(err):
			zerolog.Ctx(ctx).Error().Err(err).Str("auditEvent", p.Name).Str("kind", kind).
				Msg("AuditEvent rejected as invalid; dropped")
			Dropped.WithLabelValues(kind, "invalid").Inc()
		default:
			WriteFailures.WithLabelValues(kind).Inc()
			remaining = append(remaining, *p)
			errs = append(errs, fmt.Errorf("create AuditEvent %s: %w", p.Name, err))
		}
	}
	return remaining, errors.Join(errs...)
}

// build is the AuditEvent of outbox entry p.
func build(namespace string, p *v1alpha1.PendingAuditEvent) *v1alpha1.AuditEvent {
	labels := make(map[string]string, len(p.Labels))
	for k, v := range p.Labels {
		labels[k] = v
	}
	ae := &v1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: namespace, Labels: labels},
		Spec:       p.Spec,
	}
	// spec.timestamp is stored with one-second resolution; the annotation
	// orders records within a second (lifecycle.CompareAuditEvents).
	if p.CreatedAt != "" {
		ae.Annotations = map[string]string{lifecycle.AnnotationCreatedAt: p.CreatedAt}
	}
	return ae
}

// Pending reports whether obj's update added outbox entries, for the
// writers' watch predicates: their status patches do not otherwise wake
// them, and an entry whose create failed during the reconcile that stored it
// must be retried.
func Pending(oldPending, newPending []v1alpha1.PendingAuditEvent) bool {
	if len(newPending) == 0 {
		return false
	}
	if len(newPending) != len(oldPending) {
		return true
	}
	for i := range newPending {
		if newPending[i].Name != oldPending[i].Name {
			return true
		}
	}
	return false
}
