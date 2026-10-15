// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/rs/zerolog"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/audit"
)

// The AuditEvent actions of a pause and a resume (#1939), of the Pipeline or
// of one fleet (environment set).
const (
	AuditActionPipelinePaused  = "PipelinePaused"
	AuditActionPipelineResumed = "PipelineResumed"
)

// reconcilePauseAudit records each pause and resume of p and of its fleets
// as an AuditEvent, whichever client changed spec.paused or
// spec.pausedFleets (CLI, UI, kubectl): the difference from
// status.observedPaused and status.observedPausedFleets, attributed to
// spec.lastChange when it records that very change (who, why, the actor
// stamp), else to an unknown actor. The records go into the outbox in the
// same optimistic-locked patch as the observed state, so a crash or a
// failed create cannot lose them (#1552); their names are fixed per change
// (the Pipeline's generation), so a re-run writes nothing new. The first
// record (status.observedPaused unset, an upgrade) is taken unaudited.
func (r *Reconciler) reconcilePauseAudit(ctx context.Context, log zerolog.Logger, p *kardinalv1alpha1.Pipeline) error {
	paused := p.Spec.Paused
	fleets := slices.Clone(p.Spec.PausedFleets)
	slices.Sort(fleets)
	observedFleets := slices.Clone(p.Status.ObservedPausedFleets)
	slices.Sort(observedFleets)
	if p.Status.ObservedPaused != nil && *p.Status.ObservedPaused == paused && slices.Equal(fleets, observedFleets) {
		return nil
	}
	var entries []kardinalv1alpha1.PendingAuditEvent
	if p.Status.ObservedPaused != nil {
		if *p.Status.ObservedPaused != paused {
			action := kardinalv1alpha1.PipelineChangeResume
			if paused {
				action = kardinalv1alpha1.PipelineChangePause
			}
			entries = append(entries, r.pauseAuditEntry(p, action, ""))
		}
		for _, f := range fleets {
			if !slices.Contains(observedFleets, f) {
				entries = append(entries, r.pauseAuditEntry(p, kardinalv1alpha1.PipelineChangePauseFleet, f))
			}
		}
		for _, f := range observedFleets {
			if !slices.Contains(fleets, f) {
				entries = append(entries, r.pauseAuditEntry(p, kardinalv1alpha1.PipelineChangeResumeFleet, f))
			}
		}
	}
	patch := client.MergeFromWithOptions(p.DeepCopy(), client.MergeFromWithOptimisticLock{})
	p.Status.ObservedPaused = &paused
	p.Status.ObservedPausedFleets = fleets
	for _, e := range entries {
		p.Status.PendingAuditEvents = audit.Enqueue(ctx, auditKind, p.Status.PendingAuditEvents, e)
	}
	if err := r.Status().Patch(ctx, p, patch); err != nil {
		if k8serrors.IsConflict(err) || k8serrors.IsNotFound(err) {
			log.Debug().Err(err).Msg("pipeline changed since it was read; pause not recorded yet")
			return nil // the newer version's reconcile records it
		}
		return fmt.Errorf("record pause state: %w", err)
	}
	if len(entries) > 0 {
		_ = r.flushAudit(ctx, p) // an unwritten record stays in the outbox (reconcileHolds retries it)
	}
	return nil
}

// pauseAuditEntry is the outbox entry of a pause or resume change (action)
// of p, or of its fleet target. Who did it is spec.lastChange when that
// records this change; else the actor is unknown and the message says where
// to look.
func (r *Reconciler) pauseAuditEntry(p *kardinalv1alpha1.Pipeline, action, target string) kardinalv1alpha1.PendingAuditEvent {
	auditAction, verb := AuditActionPipelineResumed, "resumed"
	if action == kardinalv1alpha1.PipelineChangePause || action == kardinalv1alpha1.PipelineChangePauseFleet {
		auditAction, verb = AuditActionPipelinePaused, "paused"
	}
	what := "pipeline " + p.Name
	if target != "" {
		what = "fleet " + target + " of pipeline " + p.Name
	}
	by, why, stamp := changeOf(p, action, target)
	msg := fmt.Sprintf("%s %s %s", orUnknown(by), verb, what)
	if why != "" {
		msg += ": " + why
	}
	if by == "" {
		msg = fmt.Sprintf("%s %s (spec changed without kardinal; the Kubernetes audit log names who)", what, verb)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", action, target, p.Generation)))
	prefix := p.Name
	if len(prefix) > 200 {
		prefix = prefix[:200]
	}
	labels := map[string]string{"kardinal.io/pipeline": p.Name, "kardinal.io/action": auditAction}
	env := "*" // the whole Pipeline
	if target != "" {
		env = target
		labels["kardinal.io/environment"] = target
	}
	return audit.Entry(fmt.Sprintf("%s-pause-%s", prefix, hex.EncodeToString(sum[:])[:12]),
		labels, kardinalv1alpha1.AuditEventSpec{
			BundleName:   "*", // every version of the Pipeline
			PipelineName: p.Name,
			Environment:  env,
			Action:       auditAction,
			Outcome:      "Success",
			Message:      msg,
			Actor:        stamp,
		}, metav1.NewTime(r.now()))
}

// changeOf is who made the change action on target, from spec.lastChange
// when it records that change: the username, the reason and the actor
// stamp (nil without one). An empty by means the change was made without
// a record (kubectl, an older client).
func changeOf(p *kardinalv1alpha1.Pipeline, action, target string) (by, why string, stamp *kardinalv1alpha1.Actor) {
	c := p.Spec.LastChange
	if c == nil || c.Action != action || c.Target != target {
		return "", "", nil
	}
	return c.By, c.Reason, c.Actor
}
