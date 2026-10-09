// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// The AuditEvent actions of environment holds (#1528).
const (
	AuditActionHoldCreated  = "HoldCreated"
	AuditActionHoldReleased = "HoldReleased"
)

// holdKey identifies one hold: the same environment held again on another
// Bundle, or at another time, is another hold.
func holdKey(h *kardinalv1alpha1.EnvironmentHold) string {
	at := ""
	if h.CreatedAt != nil {
		at = h.CreatedAt.UTC().Format(time.RFC3339)
	}
	return h.Environment + "|" + h.Bundle + "|" + at
}

// reconcileHolds keeps spec.holds and its record in status:
//   - a hold whose expiresAt has passed is removed from spec.holds (the
//     controller is the only writer that knows the time; the update starts
//     a new reconcile);
//   - every hold added since status.observedHolds gets a HoldCreated
//     AuditEvent and every hold removed a HoldReleased one, whichever client
//     changed spec.holds (CLI, UI, kubectl). The names are fixed per hold, so
//     a reconcile that runs again after a crash writes nothing new;
//   - status.observedHolds is set to spec.holds.
//
// It returns when to come back for the next expiry (0: none), and whether it
// updated the spec (the caller then stops: the update is reconciled anew).
func (r *Reconciler) reconcileHolds(ctx context.Context, log zerolog.Logger, p *kardinalv1alpha1.Pipeline) (time.Duration, bool, error) {
	now := r.now()
	var kept []kardinalv1alpha1.EnvironmentHold
	expired := false
	var next time.Duration
	for _, h := range p.Spec.Holds {
		if h.ExpiresAt != nil && !now.Before(h.ExpiresAt.Time) {
			expired = true
			log.Info().Str("env", h.Environment).Str("bundle", h.Bundle).Msg("hold expired — removing it")
			continue
		}
		if h.ExpiresAt != nil {
			if d := h.ExpiresAt.Sub(now); next == 0 || d < next {
				next = d
			}
		}
		kept = append(kept, h)
	}
	if expired {
		p.Spec.Holds = kept
		if err := r.Update(ctx, p); err != nil {
			return 0, false, fmt.Errorf("remove expired holds: %w", err)
		}
		return 0, true, nil
	}

	observed := map[string]bool{}
	for i := range p.Status.ObservedHolds {
		observed[holdKey(&p.Status.ObservedHolds[i])] = true
	}
	current := map[string]bool{}
	for i := range p.Spec.Holds {
		h := &p.Spec.Holds[i]
		current[holdKey(h)] = true
		if !observed[holdKey(h)] {
			r.writeHoldAudit(ctx, p, h, AuditActionHoldCreated, fmt.Sprintf(
				"%s held %s on rollback %s: %s%s", orUnknown(h.CreatedBy), h.Environment, h.Bundle, h.Reason, expiresNote(h)))
		}
	}
	for i := range p.Status.ObservedHolds {
		h := &p.Status.ObservedHolds[i]
		if current[holdKey(h)] {
			continue
		}
		why := "released (spec.holds entry removed; the Kubernetes audit log, or the UI access log for a UI release, names who)"
		if h.ExpiresAt != nil && !now.Before(h.ExpiresAt.Time) {
			why = "expired at " + h.ExpiresAt.UTC().Format(time.RFC3339) + ", removed by the controller"
		}
		r.writeHoldAudit(ctx, p, h, AuditActionHoldReleased, fmt.Sprintf(
			"hold of %s on rollback %s (held by %s: %s) %s", h.Environment, h.Bundle, orUnknown(h.CreatedBy), h.Reason, why))
	}
	if !equality.Semantic.DeepEqual(p.Status.ObservedHolds, p.Spec.Holds) {
		patch := client.MergeFrom(p.DeepCopy())
		p.Status.ObservedHolds = append([]kardinalv1alpha1.EnvironmentHold(nil), p.Spec.Holds...)
		if err := r.Status().Patch(ctx, p, patch); err != nil {
			return 0, false, fmt.Errorf("patch observed holds: %w", err)
		}
	}
	return next, false, nil
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func expiresNote(h *kardinalv1alpha1.EnvironmentHold) string {
	if h.ExpiresAt == nil {
		return ""
	}
	return " (expires " + h.ExpiresAt.UTC().Format(time.RFC3339) + ")"
}

// writeHoldAudit creates the AuditEvent of a hold change. Its name is fixed
// per hold and action, so writing it again is a no-op. Errors are logged:
// the audit record must not block the Pipeline.
func (r *Reconciler) writeHoldAudit(ctx context.Context, p *kardinalv1alpha1.Pipeline, h *kardinalv1alpha1.EnvironmentHold, action, msg string) {
	sum := sha256.Sum256([]byte(holdKey(h)))
	suffix := "created"
	if action == AuditActionHoldReleased {
		suffix = "released"
	}
	prefix := p.Name
	if len(prefix) > 200 {
		prefix = prefix[:200]
	}
	now := metav1.NewTime(r.now())
	ae := &kardinalv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-hold-%s-%s", prefix, hex.EncodeToString(sum[:])[:10], suffix),
			Namespace: p.Namespace,
			Labels: map[string]string{
				"kardinal.io/pipeline":    p.Name,
				"kardinal.io/bundle":      h.Bundle,
				"kardinal.io/environment": h.Environment,
				"kardinal.io/action":      action,
			},
		},
		Spec: kardinalv1alpha1.AuditEventSpec{
			Timestamp:    now,
			BundleName:   h.Bundle,
			PipelineName: p.Name,
			Environment:  h.Environment,
			Action:       action,
			Outcome:      "Success",
			Message:      msg,
		},
	}
	lifecycle.StampCreatedAt(ae, now.Time)
	err := r.Create(ctx, ae)
	switch {
	case client.IgnoreAlreadyExists(err) == nil:
	case k8serrors.HasStatusCause(err, corev1.NamespaceTerminatingCause):
	default:
		zerolog.Ctx(ctx).Error().Err(err).Str("auditEvent", ae.Name).Str("action", action).Msg("failed to write AuditEvent")
	}
}
