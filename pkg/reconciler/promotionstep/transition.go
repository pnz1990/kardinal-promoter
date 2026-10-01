// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/kubeevent"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
)

// transition moves ps to state with message and patches its status against
// base, the copy taken before this reconcile edited ps.Status. It is the one
// place a PromotionStep changes state, so every transition gets the same side
// effects (C03-promotionstep-14, -15, -28):
//
//   - status.steps entries of the phases being left are closed (E2E-12);
//   - an AuditEvent is written for start, success, failure and rollback;
//   - the step counter and age metrics are recorded for terminal states;
//   - a Kubernetes Event is emitted.
//
// The side effects run only when the state actually changes, so re-running a
// reconcile that already transitioned is a plain status patch.
func (r *Reconciler) transition(ctx context.Context, base, ps *v1alpha1.PromotionStep, state, message string) error {
	return r.transitionAudit(ctx, base, ps, state, message, "")
}

// transitionAudit is transition with an explicit audit action for the new
// state, for example PromotionSuperseded instead of PromotionFailed.
func (r *Reconciler) transitionAudit(ctx context.Context, base, ps *v1alpha1.PromotionStep,
	state, message, auditAction string) error {
	changed, err := r.patchState(ctx, base, ps, state, message)
	if err != nil || !changed {
		return err
	}
	r.recordTransition(ctx, ps, state, message, auditAction)
	return nil
}

// cancelUnstarted fails a step that never left Pending because its Bundle was
// superseded. The step did no work, so unlike transition it writes no
// AuditEvent and records no step metrics: kardinal audit summary counts
// neither a started nor a superseded promotion (E2E-R20). Only the Kubernetes
// Event is emitted.
func (r *Reconciler) cancelUnstarted(ctx context.Context, base, ps *v1alpha1.PromotionStep, message string) error {
	changed, err := r.patchState(ctx, base, ps, StateFailed, message)
	if err != nil || !changed {
		return err
	}
	kubeevent.Emit(r.Recorder, ps, corev1.EventTypeNormal, "Superseded", "Cancel",
		fmt.Sprintf("env %s: %s", ps.Spec.Environment, message))
	return nil
}

// patchState sets state and message on ps and patches its status against
// base. It reports whether the state changed; a step deleted while
// reconciling reports no change and no error.
func (r *Reconciler) patchState(ctx context.Context, base, ps *v1alpha1.PromotionStep,
	state, message string) (bool, error) {
	ps.Status.State = state
	ps.Status.Message = message
	changed := base.Status.State != state
	if changed {
		closeStepStatuses(ps, state)
		ps.Status.RetryCount = 0
	}
	if err := r.Status().Patch(ctx, ps, client.MergeFrom(base)); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted while reconciling: nothing left to transition.
			return false, nil
		}
		return false, fmt.Errorf("patch state %s: %w", state, err)
	}
	return changed, nil
}

// recordTransition writes the audit record, metrics and Event for a state change.
func (r *Reconciler) recordTransition(ctx context.Context, ps *v1alpha1.PromotionStep, state, message, auditAction string) {
	env := ps.Spec.Environment
	eventType, reason, eventAction, note := corev1.EventTypeNormal, state, "Promote", ""
	switch state {
	case StatePromoting:
		note = fmt.Sprintf("env %s: promotion started: %s", env, message)
		writeAuditEvent(ctx, r.Client, ps, AuditActionPromotionStarted, AuditOutcomePending, message)
	case StateWaitingForMerge:
		note = fmt.Sprintf("env %s: PR opened, waiting for merge: %s", env, ps.Status.PRURL)
	case StateHealthChecking:
		eventAction = "CheckHealth"
		note = fmt.Sprintf("env %s: change delivered, running health check", env)
	case StateVerified:
		eventAction = "Verify"
		note = fmt.Sprintf("env %s: step completed successfully", env)
		writeAuditEvent(ctx, r.Client, ps, AuditActionPromotionSucceeded, AuditOutcomeSuccess, message)
		observability.StepsTotal.WithLabelValues("PromotionStep", "succeeded").Inc()
		observability.PromotionStepAgeSeconds.Observe(time.Since(ps.CreationTimestamp.Time).Seconds())
	case StateFailed, StateAbortedByAlarm:
		eventType = corev1.EventTypeWarning
		note = fmt.Sprintf("env %s: step failed: %s", env, message)
		action := auditAction
		if action == "" {
			action = AuditActionPromotionFailed
		}
		writeAuditEvent(ctx, r.Client, ps, action, AuditOutcomeFailure, message)
		observability.StepsTotal.WithLabelValues("PromotionStep", "failed").Inc()
		observability.PromotionStepAgeSeconds.Observe(time.Since(ps.CreationTimestamp.Time).Seconds())
	case StateRollingBack:
		eventType, eventAction = corev1.EventTypeWarning, "Rollback"
		note = fmt.Sprintf("env %s: %s", env, message)
		writeAuditEvent(ctx, r.Client, ps, AuditActionRollbackStarted, AuditOutcomePending, message)
	default:
		return
	}
	kubeevent.Emit(r.Recorder, ps, eventType, reason, eventAction, note)
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
// engine's steps.
func closeStepStatuses(ps *v1alpha1.PromotionStep, state string) {
	now := time.Now()
	steps := ps.Status.Steps
	complete := func(s *v1alpha1.StepStatus) {
		if s.State != v1alpha1.StepExecutionCompleted {
			closeStep(s, v1alpha1.StepExecutionCompleted, time.Time{}, now, true)
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
				return
			}
			complete(&steps[i])
		}
	case StateVerified:
		for i := range steps {
			complete(&steps[i])
		}
	case StateFailed, StateAbortedByAlarm, StateRollingBack:
		for i := range steps {
			switch steps[i].State {
			case v1alpha1.StepExecutionFailed:
				return
			case v1alpha1.StepExecutionCompleted:
				continue
			}
			closeStep(&steps[i], v1alpha1.StepExecutionFailed, time.Time{}, now, true)
			steps[i].Message = ps.Status.Message
			return
		}
	}
}
