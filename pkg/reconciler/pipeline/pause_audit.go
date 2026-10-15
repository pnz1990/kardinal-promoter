// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
func (r *Reconciler) reconcilePauseAudit(ctx context.Context, log zerolog.Logger, p *kardinalv1alpha1.Pipeline,
	fresh *kardinalv1alpha1.PipelineChange) error {
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
			entries = append(entries, r.pauseAuditEntry(p, fresh, action, ""))
		}
		for _, f := range fleets {
			if !slices.Contains(observedFleets, f) {
				entries = append(entries, r.pauseAuditEntry(p, fresh, kardinalv1alpha1.PipelineChangePauseFleet, f))
			}
		}
		for _, f := range observedFleets {
			if !slices.Contains(fleets, f) {
				entries = append(entries, r.pauseAuditEntry(p, fresh, kardinalv1alpha1.PipelineChangeResumeFleet, f))
			}
		}
	}
	patch := client.MergeFromWithOptions(p.DeepCopy(), client.MergeFromWithOptimisticLock{})
	p.Status.ObservedPaused = &paused
	p.Status.ObservedPausedFleets = fleets
	p.Status.ObservedLastChange = changeKey(p.Spec.LastChange)
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
func (r *Reconciler) pauseAuditEntry(p *kardinalv1alpha1.Pipeline, fresh *kardinalv1alpha1.PipelineChange,
	action, target string) kardinalv1alpha1.PendingAuditEvent {
	auditAction, verb := AuditActionPipelineResumed, "resumed"
	if action == kardinalv1alpha1.PipelineChangePause || action == kardinalv1alpha1.PipelineChangePauseFleet {
		auditAction, verb = AuditActionPipelinePaused, "paused"
	}
	what := "pipeline " + p.Name
	if target != "" {
		what = "fleet " + target + " of pipeline " + p.Name
	}
	by, why, stamp := changeOf(fresh, action, target)
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

// changeOf is who made the change action on target, from fresh (the
// spec.lastChange not yet consumed, freshChange) when it records that change:
// the username, the reason and the actor stamp. A stamp that names someone
// other than by is dropped (admission refuses one; this is the second line).
// An empty by means the change was made without a record (kubectl, an older
// client), or with a record already consumed by an earlier change.
func changeOf(fresh *kardinalv1alpha1.PipelineChange, action, target string) (by, why string, stamp *kardinalv1alpha1.Actor) {
	if fresh == nil || fresh.Action != action || fresh.Target != target {
		return "", "", nil
	}
	return fresh.By, fresh.Reason, stampOf(fresh.Actor, fresh.By)
}

// stampOf is stamp when it names by, else nil: an AuditEvent never carries a
// stamp of someone other than the user its record names.
func stampOf(stamp *kardinalv1alpha1.Actor, by string) *kardinalv1alpha1.Actor {
	if stamp == nil || stamp.Principal != by {
		return nil
	}
	return stamp
}

// freshChange is p's spec.lastChange when no earlier change consumed it
// (status.observedLastChange), else nil: a record is used for the change it
// was written with, and a later change made without one (kubectl) is not
// attributed to it.
func freshChange(p *kardinalv1alpha1.Pipeline) *kardinalv1alpha1.PipelineChange {
	if p.Spec.LastChange == nil || changeKey(p.Spec.LastChange) == p.Status.ObservedLastChange {
		return nil
	}
	return p.Spec.LastChange
}

// changeKey identifies a spec.lastChange record: a hash of every field.
func changeKey(c *kardinalv1alpha1.PipelineChange) string {
	if c == nil {
		return ""
	}
	b, err := json.Marshal(c)
	if err != nil {
		return "" // unreachable: the type always marshals
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}
