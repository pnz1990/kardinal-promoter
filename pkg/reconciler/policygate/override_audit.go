// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// AuditActionGateOverridden is the AuditEvent action written once for each
// override recorded on a gate instance (kardinal override or the UI, #1450).
const AuditActionGateOverridden = "GateOverridden"

// overrideKey identifies one spec.overrides entry: a hash of every field, so
// an edited entry is a new override. It is what status.observedOverrides
// records and the suffix of the AuditEvent name.
func overrideKey(o kardinalv1alpha1.PolicyGateOverride) string {
	created := ""
	if o.CreatedAt != nil {
		created = o.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s",
		o.CreatedBy, created, o.Stage, o.ExpiresAt.UTC().Format(time.RFC3339Nano), o.Reason)))
	return hex.EncodeToString(sum[:])[:12]
}

// auditOverrides writes a GateOverridden AuditEvent for each override of the
// gate instance that status.observedOverrides does not list yet, and then
// records their keys there. The AuditEvent name is derived from the gate and
// the key, so a reconcile repeated after a crash between the two writes finds
// it already there and writes nothing new. An AuditEvent that cannot be
// written is logged and its key is not recorded, so the next reconcile tries
// again; audit never blocks gate evaluation.
//
// Graph-first: the PolicyGate reconciler writes only its own status and the
// append-only AuditEvent, from its own spec.
func (r *Reconciler) auditOverrides(ctx context.Context, gate *kardinalv1alpha1.PolicyGate) error {
	var added []string
	// An override is verified when the controller was already checking this
	// gate (overridesVerifiedSince set) before it first saw the override: it
	// was then created under the identity admission policy.
	verified := gate.Status.OverridesVerifiedSince != nil
	for _, o := range gate.Spec.Overrides {
		key := overrideKey(o)
		if observed(gate, key) || slices.Contains(added, key) || slices.Contains(added, key+unverifiedSuffix) {
			continue
		}
		if err := r.writeOverrideAuditEvent(ctx, gate, o, key, verified); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Str("gate", gate.Name).Str("override", key).
				Msg("failed to write the GateOverridden AuditEvent; retrying on the next reconcile")
			continue
		}
		if !verified {
			key += unverifiedSuffix
		}
		added = append(added, key)
	}
	// A gate evaluated before (an upgrade) gets its verifiedSince stamp now,
	// once; a new gate gets it with its first evaluation (patchStatus).
	if len(added) == 0 && (gate.Status.OverridesVerifiedSince != nil || gate.Status.LastEvaluatedAt == nil) {
		return nil
	}
	patch := client.MergeFrom(gate.DeepCopy())
	gate.Status.ObservedOverrides = append(gate.Status.ObservedOverrides, added...)
	stampVerifiedSince(gate, r.now())
	if err := r.Status().Patch(ctx, gate, patch); err != nil {
		return fmt.Errorf("record observed overrides: %w", err)
	}
	return nil
}

// unverifiedSuffix marks a status.observedOverrides key whose override was
// already on the gate when the controller first checked it (an upgrade).
const unverifiedSuffix = ":unverified"

func observed(gate *kardinalv1alpha1.PolicyGate, key string) bool {
	return slices.Contains(gate.Status.ObservedOverrides, key) ||
		slices.Contains(gate.Status.ObservedOverrides, key+unverifiedSuffix)
}

// overrideVerified reports whether the chart's admission policy checked o's
// createdBy: the controller first saw o on a gate it was already checking
// (status.observedOverrides without the unverified mark). An override not
// observed yet counts as verified once the gate has overridesVerifiedSince.
func overrideVerified(gate *kardinalv1alpha1.PolicyGate, o kardinalv1alpha1.PolicyGateOverride) bool {
	key := overrideKey(o)
	switch {
	case slices.Contains(gate.Status.ObservedOverrides, key):
		return true
	case slices.Contains(gate.Status.ObservedOverrides, key+unverifiedSuffix):
		return false
	}
	return gate.Status.OverridesVerifiedSince != nil
}

// stampVerifiedSince sets status.overridesVerifiedSince to now the first
// time, in memory; the caller's status patch writes it.
func stampVerifiedSince(gate *kardinalv1alpha1.PolicyGate, now time.Time) {
	if gate.Status.OverridesVerifiedSince == nil {
		t := metav1.NewTime(now)
		gate.Status.OverridesVerifiedSince = &t
	}
}

// writeOverrideAuditEvent creates the GateOverridden AuditEvent of override o
// of gate. Its timestamp is when the controller first saw the override, not
// the createdAt the writer supplied. AlreadyExists is success: an earlier
// attempt wrote it.
func (r *Reconciler) writeOverrideAuditEvent(ctx context.Context, gate *kardinalv1alpha1.PolicyGate,
	o kardinalv1alpha1.PolicyGateOverride, key string, verified bool) error {
	labels := gate.GetLabels()
	if labels[labelPipeline] == "" || labels[labelBundle] == "" || labels[labelEnvironment] == "" {
		return nil // not a gate instance a Graph created: nothing to attribute the record to
	}
	at := metav1.NewTime(r.now())
	stage := o.Stage
	if stage == "" {
		stage = "every stage"
	}
	by := o.CreatedBy
	if by == "" {
		by = "(unknown)"
	}
	if !verified {
		by += " (unverified: recorded before kardinal checked override identity)"
	}
	suffix := "-override-" + key
	base := gate.Name
	if limit := maxObjectNameLength - len(suffix); len(base) > limit {
		base = base[:limit]
	}
	ae := &kardinalv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sanitizeGateName(base + suffix),
			Namespace: gate.Namespace,
			Labels:    gateAuditLabels(labels, AuditActionGateOverridden),
		},
		Spec: kardinalv1alpha1.AuditEventSpec{
			Timestamp:    at,
			BundleName:   labels[labelBundle],
			PipelineName: labels[labelPipeline],
			Environment:  labels[labelEnvironment],
			Action:       AuditActionGateOverridden,
			Outcome:      "Success",
			Message: truncateMessage(fmt.Sprintf("gate %s overridden by %s for %s until %s: %s",
				gate.Name, by, stage, o.ExpiresAt.UTC().Format(time.RFC3339), o.Reason)),
		},
	}
	err := r.Create(ctx, ae)
	switch {
	case client.IgnoreAlreadyExists(err) == nil:
		return nil
	case apierrors.HasStatusCause(err, corev1.NamespaceTerminatingCause):
		return nil // the namespace deletion removes the gate next
	default:
		return fmt.Errorf("create AuditEvent %s: %w", ae.Name, err)
	}
}
