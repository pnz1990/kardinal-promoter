// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/audit"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
)

// Verdict AuditEvent actions (docs/design/19-verdicts.md, section 6.5).
const (
	auditActionVerdictRecorded = "VerdictRecorded"
	auditActionVerdictRevoked  = "VerdictRevoked"
)

// bundleAuthor is the version's author for the one-party rule: the Bundle's
// actor stamp, or the read-side unknown actor with its verified creator.
func bundleAuthor(bundle *kardinalv1alpha1.Bundle) kardinalv1alpha1.Actor {
	return actor.OrUnknown(bundle.Spec.Actor, bundle.Annotations[lifecycle.AnnotationCreatedBy])
}

// healthCheckedAt is when bundle passed its health check in env, or nil.
func healthCheckedAt(bundle *kardinalv1alpha1.Bundle, env string) *time.Time {
	for _, e := range bundle.Status.Environments {
		if e.Name == env && e.HealthCheckedAt != nil {
			t := e.HealthCheckedAt.Time
			return &t
		}
	}
	return nil
}

// tallyVerdicts counts gate's spec.verdicts (lifecycle.TallyVerdicts) for
// bundle: its author, and the deadline from the verified environment's
// health check time, both read from the Bundle's own fields.
func tallyVerdicts(gate *kardinalv1alpha1.PolicyGate, bundle *kardinalv1alpha1.Bundle, now time.Time) lifecycle.VerdictTally {
	p := lifecycle.VerifyPolicyOf(gate)
	var deadline *time.Time
	if p != nil {
		deadline = lifecycle.VerifyDeadline(p, healthCheckedAt(bundle, p.Environment))
	}
	return lifecycle.TallyVerdicts(gate, bundleAuthor(bundle), string(bundle.UID), deadline, now)
}

// untilDeadline is how long until a waiting verify gate's deadline, so the
// gate is evaluated again the moment it times out; 0 when it does not wait
// for one.
func untilDeadline(t lifecycle.VerdictTally, now time.Time) time.Duration {
	if t.Deadline == nil || t.Outcome != lifecycle.VerifyWaiting || !now.Before(*t.Deadline) {
		return 0
	}
	return t.Deadline.Sub(now) + time.Second
}

// recordVerdicts writes the tally's records to status.verdicts when they
// changed, with a VerdictRecorded or VerdictRevoked record in the audit
// outbox, in the same patch, for each verdict that appeared or left. The
// patch carries the resourceVersion the gate was read at, as
// recordApprovals's does, so a stale reconcile audits nothing twice.
//
// Each time a verdict enters status.verdicts is an appearance of its own
// (status.verdictAppearances counts them, and keeps the count after it
// leaves): a verdict that leaves and comes back is recorded again, and its
// second leave revoked again (#2380). The appearance and the Bundle's UID
// name the records, so each name is written once.
func (r *Reconciler) recordVerdicts(ctx context.Context, gate *kardinalv1alpha1.PolicyGate, t lifecycle.VerdictTally,
	bundleUID string) error {
	records := t.Records
	if len(records) == 0 {
		records = nil
	}
	if equality.Semantic.DeepEqual(records, gate.Status.Verdicts) {
		return nil
	}
	old, oldPending, oldAppearances := gate.Status.Verdicts, gate.Status.PendingAuditEvents, gate.Status.VerdictAppearances
	patch := client.MergeFromWithOptions(gate.DeepCopy(), client.MergeFromWithOptimisticLock{})
	gate.Status.Verdicts = records
	appearances := maps.Clone(oldAppearances)
	if appearances == nil {
		appearances = map[string]int32{}
	}
	byName := map[string]kardinalv1alpha1.GateVerdict{}
	for _, v := range gate.Spec.Verdicts {
		byName[v.Name] = v
	}
	enqueue := func(rec kardinalv1alpha1.GateVerdictStatus, action string) {
		if e, ok := r.verdictAuditEntry(gate, rec, byName[rec.Name].Actor, action, bundleUID, appearances[rec.Name]); ok {
			gate.Status.PendingAuditEvents = audit.Enqueue(ctx, auditKind, gate.Status.PendingAuditEvents, e)
		}
	}
	seen := func(list []kardinalv1alpha1.GateVerdictStatus, name string) bool {
		for _, o := range list {
			if o.Name == name {
				return true
			}
		}
		return false
	}
	for _, rec := range records {
		if !seen(old, rec.Name) {
			appearances[rec.Name]++
			enqueue(rec, auditActionVerdictRecorded)
			observability.VerdictsTotal.WithLabelValues(t.Policy.Check, rec.Result, verifierKind(byName[rec.Name])).Inc()
		}
	}
	for _, o := range old {
		if !seen(records, o.Name) {
			enqueue(o, auditActionVerdictRevoked)
		}
	}
	if len(appearances) > 0 {
		gate.Status.VerdictAppearances = appearances
	}
	if err := r.Status().Patch(ctx, gate, patch); err != nil {
		gate.Status.Verdicts, gate.Status.PendingAuditEvents, gate.Status.VerdictAppearances = old, oldPending, oldAppearances
		return fmt.Errorf("record verdicts: %w", err)
	}
	_ = r.flushAudit(ctx, gate)
	return nil
}

// verifierKind is the actor kind of a copied verdict, for the metric.
func verifierKind(v kardinalv1alpha1.GateVerdict) string {
	return lifecycle.VerifierActor(v).Kind
}

// verdictAuditEntry is the outbox entry recording that rec appeared in or
// left the gate's verdicts (its appearance-th time in them), and false for a
// gate a Graph did not create. The name is derived from the gate, the
// action, the Verdict's name, the Bundle's UID and the appearance: a retried
// write of one transition is the same record, and every transition of the
// Bundle's, and of a Bundle created again under its name, has a name of its
// own (#2380).
func (r *Reconciler) verdictAuditEntry(gate *kardinalv1alpha1.PolicyGate, rec kardinalv1alpha1.GateVerdictStatus,
	stamp *kardinalv1alpha1.Actor, action, bundleUID string, appearance int32) (kardinalv1alpha1.PendingAuditEvent, bool) {
	labels := gate.GetLabels()
	if labels[labelPipeline] == "" || labels[labelBundle] == "" || labels[labelEnvironment] == "" || bundleUID == "" {
		return kardinalv1alpha1.PendingAuditEvent{}, false
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", action, rec.Name, bundleUID, appearance)))
	suffix := "-verdict-" + hex.EncodeToString(sum[:])[:12]
	base := gate.Name
	if limit := maxObjectNameLength - len(suffix); len(base) > limit {
		base = base[:limit]
	}
	counted := "counted"
	if !rec.Counted {
		counted = "not counted: " + rec.Reason
	}
	verb := map[string]string{auditActionVerdictRecorded: "recorded", auditActionVerdictRevoked: "revoked"}[action]
	msg := fmt.Sprintf("verdict %s %s by %s %s on gate %s (%s)", rec.Name, rec.Result, rec.Verifier, verb, gate.Name, counted)
	if rec.Summary != "" {
		msg += ": " + rec.Summary
	}
	spec := kardinalv1alpha1.AuditEventSpec{
		BundleName: labels[labelBundle], BundleUID: bundleUID, PipelineName: labels[labelPipeline],
		Environment: labels[labelEnvironment], Action: action, Outcome: "Success", Message: msg,
	}
	if stamp != nil {
		a := *stamp
		spec.Actor = &a
	}
	return audit.Entry(sanitizeGateName(base+suffix), gateAuditLabels(labels, action), spec, metav1.NewTime(r.now())), true
}
