// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/audit"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/kubeevent"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
)

// transition moves ps to state with message and patches its status against
// base, the copy taken before this reconcile edited ps.Status. It is the one
// place a PromotionStep changes state, so every transition gets the same side
// effects (C03-promotionstep-14, -15, -28):
//
//   - status.steps entries of the phases being left are closed (E2E-12);
//   - an AuditEvent is recorded for start, success, failure and rollback,
//     and for the success of a rollback Bundle (auditEntries), in the step's
//     status outbox in the same patch as the state (#1552);
//   - the step counter and age metrics are recorded for terminal states;
//   - a Kubernetes Event is emitted.
//
// The side effects run only when the state actually changes, so re-running a
// reconcile that already transitioned is a plain status patch.
func (r *Reconciler) transition(ctx context.Context, base, ps *v1alpha1.PromotionStep, state, message string) error {
	_, err := r.transitionClosing(ctx, base, ps, state, message, "", nil)
	return err
}

// transitionAudit is transition with an explicit audit action for the new
// state, for example PromotionSuperseded instead of PromotionFailed.
func (r *Reconciler) transitionAudit(ctx context.Context, base, ps *v1alpha1.PromotionStep,
	state, message, auditAction string) error {
	_, err := r.transitionClosing(ctx, base, ps, state, message, auditAction, nil)
	return err
}

// transitionClosing is transitionAudit for a reconcile that closed steps
// before the transition (the step engine's): closed is observed with the
// steps the transition closes, once the status patch succeeds. An empty
// auditAction is the default for the state. It reports whether the state
// changed, so a caller records its own metrics only for a transition that
// was patched.
func (r *Reconciler) transitionClosing(ctx context.Context, base, ps *v1alpha1.PromotionStep,
	state, message, auditAction string, closed stepObservations) (bool, error) {
	var entries []v1alpha1.PendingAuditEvent
	if base.Status.State != state {
		var err error
		if entries, err = r.auditEntries(ctx, ps, state, message, auditAction); err != nil {
			return false, err
		}
	}
	changed, err := r.patchState(ctx, base, ps, state, message, closed, entries...)
	if err != nil || !changed {
		return false, err
	}
	// The records are in the step's status now, so a failed create only
	// delays them: the status patch that stored them wakes the reconciler
	// (auditPending), and the next reconcile writes them first.
	_ = r.flushAudit(ctx, ps)
	r.recordTransition(ps, state, message)
	return true, nil
}

// cancelUnstarted fails a step that never left Pending because its Bundle was
// superseded or rejected (eventReason Superseded or Rejected). The step did no work, so unlike transition it writes no
// AuditEvent and records no step metrics: kardinal audit summary counts
// neither a started nor a superseded promotion (E2E-R20). Only the Kubernetes
// Event is emitted.
func (r *Reconciler) cancelUnstarted(ctx context.Context, base, ps *v1alpha1.PromotionStep, message, eventReason string) error {
	changed, err := r.patchState(ctx, base, ps, StateFailed, message, nil)
	if err != nil || !changed {
		return err
	}
	kubeevent.Emit(r.Recorder, ps, corev1.EventTypeNormal, eventReason, "Cancel",
		fmt.Sprintf("env %s: %s", ps.Spec.Environment, message))
	return nil
}

// patchState sets state and message on ps and patches its status against
// base. It reports whether the state changed; a step deleted while
// reconciling, or changed since base was read (a stale cache), reports no
// change and no error. The steps closed before the
// call (closed) and by the state change are observed in
// kardinal_step_duration_seconds only after the patch succeeds.
//
// The audit records of the state change (entries) are stored in
// status.pendingAuditEvents in the same patch, so the state cannot change
// without them (#1552); flushAudit writes them.
func (r *Reconciler) patchState(ctx context.Context, base, ps *v1alpha1.PromotionStep,
	state, message string, closed stepObservations, entries ...v1alpha1.PendingAuditEvent) (bool, error) {
	ps.Status.State = state
	ps.Status.Message = message
	changed := base.Status.State != state
	if changed {
		closed = append(closed, closeStepStatuses(ps, state)...)
		ps.Status.RetryCount, ps.Status.GitCredentialRetries = 0, 0
		for _, e := range entries {
			ps.Status.PendingAuditEvents = audit.Enqueue(ctx, auditKind, ps.Status.PendingAuditEvents, e)
		}
	}
	// Locked on the resourceVersion base was read at: a reconcile that read
	// the step from a stale cache would otherwise repeat a transition a newer
	// reconcile already wrote, with a second Event, AuditEvent and metric.
	if err := r.Status().Patch(ctx, ps, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted while reconciling: nothing left to transition.
			return false, nil
		}
		if apierrors.IsConflict(err) {
			// Changed since it was read: the newer version's watch event
			// reconciles it again, from what is stored.
			zerolog.Ctx(ctx).Debug().Str("step", ps.Name).Str("state", state).
				Msg("step changed since it was read; state not written")
			return false, nil
		}
		return false, fmt.Errorf("patch state %s: %w", state, err)
	}
	closed.record()
	return changed, nil
}

// auditEntries are the audit records of ps's change to state, for
// patchState to store with it: start, success, failure and rollback, and the
// success of a rollback Bundle. An empty auditAction is the default for the
// state. It fails only when the Bundle cannot be read to tell whether ps
// promotes a rollback, so the transition is retried with its records rather
// than patched without one.
func (r *Reconciler) auditEntries(ctx context.Context, ps *v1alpha1.PromotionStep,
	state, message, auditAction string) ([]v1alpha1.PendingAuditEvent, error) {
	at := r.now()
	var out []v1alpha1.PendingAuditEvent
	add := func(action, outcome, msg string) {
		if e, ok := auditEntry(ps, action, outcome, msg, at); ok {
			out = append(out, e)
		}
	}
	switch state {
	case StatePromoting:
		add(AuditActionPromotionStarted, AuditOutcomePending, message)
	case StateVerified:
		add(AuditActionPromotionSucceeded, AuditOutcomeSuccess, message)
		msg, ok, err := r.rollbackSucceededMessage(ctx, ps, message)
		if err != nil {
			return nil, err
		}
		if ok {
			add(AuditActionRollbackSucceeded, AuditOutcomeSuccess, msg)
		}
	case StateFailed, StateAbortedByAlarm:
		action := auditAction
		if action == "" {
			action = AuditActionPromotionFailed
		}
		add(action, AuditOutcomeFailure, message)
	case StateRollingBack:
		add(AuditActionRollbackStarted, AuditOutcomePending, message)
	}
	return out, nil
}

// recordTransition records the metrics and Event for a state change.
func (r *Reconciler) recordTransition(ps *v1alpha1.PromotionStep, state, message string) {
	env := ps.Spec.Environment
	eventType, reason, eventAction, note := corev1.EventTypeNormal, state, "Promote", ""
	switch state {
	case StatePromoting:
		note = fmt.Sprintf("env %s: promotion started: %s", env, message)
	case StateWaitingForMerge:
		note = fmt.Sprintf("env %s: PR opened, waiting for merge: %s", env, ps.Status.PRURL)
	case StateHealthChecking:
		eventAction = "CheckHealth"
		note = fmt.Sprintf("env %s: change delivered, running health check", env)
	case StateVerifying:
		eventAction = "RunVerification"
		note = fmt.Sprintf("env %s: health check passed, verifying (post-deploy hooks and analyses)", env)
	case StateVerified:
		eventAction = "Verify"
		note = fmt.Sprintf("env %s: step completed successfully", env)
		observability.StepsTotal.WithLabelValues("PromotionStep", "succeeded").Inc()
		observability.PromotionStepAgeSeconds.Observe(time.Since(ps.CreationTimestamp.Time).Seconds())
	case StateFailed, StateAbortedByAlarm:
		eventType = corev1.EventTypeWarning
		note = fmt.Sprintf("env %s: step failed: %s", env, message)
		observability.StepsTotal.WithLabelValues("PromotionStep", "failed").Inc()
		observability.PromotionStepAgeSeconds.Observe(time.Since(ps.CreationTimestamp.Time).Seconds())
	case StateRollingBack:
		eventType, eventAction = corev1.EventTypeWarning, "Rollback"
		note = fmt.Sprintf("env %s: %s", env, message)
	default:
		return
	}
	kubeevent.Emit(r.Recorder, ps, eventType, reason, eventAction, note)
}

// rollbackSucceededMessage is the message of the RollbackSucceeded record
// when ps, which is reaching Verified, promoted a rollback Bundle: the
// environment runs the restored artifacts and passed its health check. It
// reports false for any other Bundle and for a Bundle that is gone. The
// record is named {ps.Name}-rollback-succeeded, so a step writes at most
// one, also when a reconcile is repeated after a controller restart.
func (r *Reconciler) rollbackSucceededMessage(ctx context.Context, ps *v1alpha1.PromotionStep,
	message string) (string, bool, error) {
	name := ps.Spec.BundleName
	if name == "" {
		name = ps.Labels["kardinal.io/bundle"]
	}
	if name == "" {
		return "", false, nil
	}
	var bundle v1alpha1.Bundle
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ps.Namespace}, &bundle); err != nil {
		if apierrors.IsNotFound(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read Bundle %s for the RollbackSucceeded AuditEvent: %w", name, err)
	}
	if !isRollbackBundle(&bundle) {
		return "", false, nil
	}
	detail := ""
	if bundle.Spec.Provenance != nil && bundle.Spec.Provenance.RollbackOf != "" {
		detail = " (artifacts of " + bundle.Spec.Provenance.RollbackOf
		if from := bundle.Annotations[lifecycle.AnnotationRollbackFrom]; from != "" {
			detail += ", rolled back from " + from
		}
		detail += ")"
	}
	return fmt.Sprintf("rollback Bundle %s%s verified in %s: %s", name, detail, ps.Spec.Environment, message), true, nil
}

// closeStepStatuses brings status.steps in line with the state being entered
// (E2E-12). The step engine updates the entries only while it runs; the
// wait-for-merge and health-check phases are driven by the state machine, so
// their entries are closed here.
//
//   - Entering HealthChecking: every entry before health-check is Completed
//     (wait-for-merge finished when the PR merged) and health-check is
//     InProgress, even if the engine's placeholder step marked it Completed.
//   - Entering Verified: every entry that is not Completed becomes Completed.
//   - Entering a failure state: the first entry that is not Completed is Failed
//     with the message, unless one already failed.
//
// Each entry closed here goes through closeStep, so wait-for-merge and the
// health check are observed in kardinal_step_duration_seconds like the
// engine's steps; the samples are returned for the caller to record after
// its status patch.
func closeStepStatuses(ps *v1alpha1.PromotionStep, state string) stepObservations {
	now := time.Now()
	steps := ps.Status.Steps
	var closed stepObservations
	complete := func(s *v1alpha1.StepStatus) {
		if s.State != v1alpha1.StepExecutionCompleted {
			closed = append(closed, closeStep(s, v1alpha1.StepExecutionCompleted, time.Time{}, now, true)...)
		}
	}
	switch state {
	case StateHealthChecking:
		for i := range steps {
			if steps[i].Name == healthCheckStep {
				// The step engine runs a placeholder health-check step and marks
				// it Completed; the real check only starts now.
				if steps[i].State != v1alpha1.StepExecutionInProgress {
					started := metav1.NewTime(now)
					steps[i].State = v1alpha1.StepExecutionInProgress
					steps[i].StartedAt = &started
					steps[i].CompletedAt = nil
					steps[i].DurationMs = 0
				}
				return closed
			}
			complete(&steps[i])
		}
	case StateVerifying, StateVerified:
		for i := range steps {
			complete(&steps[i])
		}
	case StateFailed, StateAbortedByAlarm, StateRollingBack:
		for i := range steps {
			switch steps[i].State {
			case v1alpha1.StepExecutionFailed:
				return closed
			case v1alpha1.StepExecutionCompleted:
				continue
			}
			closed = closeStep(&steps[i], v1alpha1.StepExecutionFailed, time.Time{}, now, true)
			steps[i].Message = ps.Status.Message
			return closed
		}
	}
	return closed
}
