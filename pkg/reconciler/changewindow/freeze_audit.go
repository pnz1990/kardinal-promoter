// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package changewindow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/labelvalue"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/kubeevent"
)

// The AuditEvents of a freeze's start and lift (#1939).
const (
	AuditActionFreezeStarted = "FreezeStarted"
	AuditActionFreezeLifted  = "FreezeLifted"
)

// FreezeAuditFinalizer keeps a freezing ChangeWindow until its
// FreezeLifted AuditEvent is written, however it is deleted (kardinal
// unfreeze, the UI, kubectl).
const FreezeAuditFinalizer = "kardinal.io/freeze-audit"

// conditionFreezeAudited is True once the freeze's FreezeStarted AuditEvent
// is written.
const conditionFreezeAudited = "FreezeAudited"

// freezeAuditRetry is how soon a failed freeze audit write is retried, and
// freezeAuditGrace how long a deleted freeze waits for its FreezeLifted
// record before the finalizer comes off without it (a Warning Event says
// so): an audit write never holds a window for good.
const (
	freezeAuditRetry = 10 * time.Second
	freezeAuditGrace = 2 * time.Minute
)

// ReasonFreezeAuditFailed is the Warning Event of a freeze audit record that
// could not be written.
const ReasonFreezeAuditFailed = "FreezeAuditFailed"

// startFreezeAudit records a freeze's start as an AuditEvent naming who froze
// (spec.freeze.createdBy and its actor stamp), once (the FreezeAudited
// condition), and keeps the audit finalizer on the window so its lift is
// recorded too, however it is deleted. It runs after the window's status is
// written and never fails the reconcile: a failure is a Warning Event and a
// retry (the returned delay, 0 for none).
func (r *Reconciler) startFreezeAudit(ctx context.Context, cw *kardinalv1alpha1.ChangeWindow) time.Duration {
	if cw.Spec.Freeze == nil {
		return 0
	}
	log := zerolog.Ctx(ctx)
	retry := time.Duration(0)
	if !controllerutil.ContainsFinalizer(cw, FreezeAuditFinalizer) {
		patch := client.MergeFrom(cw.DeepCopy())
		controllerutil.AddFinalizer(cw, FreezeAuditFinalizer)
		if err := r.Patch(ctx, cw, patch); err != nil {
			log.Warn().Err(err).Str("changewindow", cw.Name).Msg("could not add the freeze audit finalizer; will retry")
			retry = freezeAuditRetry
		}
	}
	if meta.IsStatusConditionTrue(cw.Status.Conditions, conditionFreezeAudited) {
		return retry
	}
	if err := r.writeFreezeAudit(ctx, cw, AuditActionFreezeStarted); err != nil {
		r.auditFailed(ctx, cw, err)
		return freezeAuditRetry
	}
	patch := client.MergeFrom(cw.DeepCopy())
	meta.SetStatusCondition(&cw.Status.Conditions, metav1.Condition{Type: conditionFreezeAudited,
		Status: metav1.ConditionTrue, Reason: "Recorded", Message: "the FreezeStarted AuditEvent is written",
		ObservedGeneration: cw.Generation, LastTransitionTime: metav1.NewTime(r.now())})
	if err := r.Status().Patch(ctx, cw, patch); err != nil && !apierrors.IsNotFound(err) {
		// The record is written; its name is fixed, so writing it again is a no-op.
		log.Warn().Err(err).Str("changewindow", cw.Name).Msg("could not record the freeze audit; will retry")
		return freezeAuditRetry
	}
	return retry
}

// finishFreezeAudit records a deleted freeze's lift as an AuditEvent naming
// who lifted it (spec.freeze.lifted, which kardinal unfreeze and the UI write
// just before the delete), else an unknown actor, and then removes the audit
// finalizer. A write that keeps failing does not hold the window: after
// freezeAuditGrace the finalizer comes off without the record, and a Warning
// Event says so. The finalizer comes off a window that is no longer a freeze
// too.
func (r *Reconciler) finishFreezeAudit(ctx context.Context, cw *kardinalv1alpha1.ChangeWindow) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(cw, FreezeAuditFinalizer) {
		return ctrl.Result{}, nil
	}
	if cw.Spec.Freeze != nil {
		if err := r.writeFreezeAudit(ctx, cw, AuditActionFreezeLifted); err != nil {
			r.auditFailed(ctx, cw, err)
			if r.now().Sub(cw.DeletionTimestamp.Time) < freezeAuditGrace {
				return ctrl.Result{RequeueAfter: freezeAuditRetry}, nil
			}
			kubeevent.Emit(r.Recorder, cw, corev1.EventTypeWarning, ReasonFreezeAuditFailed, "Audit",
				fmt.Sprintf("the FreezeLifted AuditEvent of %s could not be written for %s; the freeze is deleted without it",
					cw.Name, freezeAuditGrace))
		}
	}
	patch := client.MergeFrom(cw.DeepCopy())
	controllerutil.RemoveFinalizer(cw, FreezeAuditFinalizer)
	if err := r.Patch(ctx, cw, patch); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("remove the freeze audit finalizer: %w", err))
	}
	return ctrl.Result{}, nil
}

// auditFailed logs and reports a freeze audit write that failed.
func (r *Reconciler) auditFailed(ctx context.Context, cw *kardinalv1alpha1.ChangeWindow, err error) {
	zerolog.Ctx(ctx).Warn().Err(err).Str("changewindow", cw.Name).Msg("could not write the freeze AuditEvent; will retry")
	kubeevent.Emit(r.Recorder, cw, corev1.EventTypeWarning, ReasonFreezeAuditFailed, "Audit", err.Error())
}

// writeFreezeAudit writes the FreezeStarted or FreezeLifted AuditEvent of
// freeze cw, in its one namespace, or in AuditNamespace for a freeze of
// several or every namespace.
func (r *Reconciler) writeFreezeAudit(ctx context.Context, cw *kardinalv1alpha1.ChangeWindow, action string) error {
	f := cw.Spec.Freeze
	by, stamp, verb := f.CreatedBy, f.Actor, "froze"
	if action == AuditActionFreezeLifted {
		by, stamp, verb = "", nil, "lifted"
		if f.Lifted != nil {
			by, stamp = f.Lifted.By, f.Lifted.Actor
		}
	}
	if stamp != nil && stamp.Principal != by {
		stamp = nil // a stamp of someone other than who the record names is never recorded
	}
	msg := fmt.Sprintf("%s %s %s (%s)", orUnknown(by), verb, cw.Name, FreezeScope(f))
	if action == AuditActionFreezeStarted && cw.Spec.Reason != "" {
		msg += ": " + cw.Spec.Reason
	}
	if by == "" && action == AuditActionFreezeLifted {
		msg = fmt.Sprintf("freeze %s (%s) lifted: deleted without kardinal unfreeze; the Kubernetes audit log names who",
			cw.Name, FreezeScope(f))
	}
	ns := r.auditNamespace()
	if len(f.Namespaces) == 1 && !strings.HasSuffix(f.Namespaces[0], "*") {
		ns = f.Namespaces[0]
	}
	suffix := "freeze-started"
	if action == AuditActionFreezeLifted {
		suffix = "freeze-lifted"
	}
	now := metav1.NewTime(r.now())
	if action == AuditActionFreezeStarted && !cw.CreationTimestamp.IsZero() {
		// When it froze, also for a freeze first recorded after an upgrade.
		now = cw.CreationTimestamp
	}
	ae := &kardinalv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s", string(cw.UID), suffix), Namespace: ns,
			Labels: map[string]string{"kardinal.io/changewindow": labelvalue.Fit(cw.Name), "kardinal.io/action": action},
			// spec.timestamp has one-second resolution; the annotation orders
			// records within a second (lifecycle.AnnotationCreatedAt; the
			// package cannot import lifecycle).
			Annotations: map[string]string{"kardinal.io/created-at": now.UTC().Format(time.RFC3339Nano)}},
		Spec: kardinalv1alpha1.AuditEventSpec{
			Timestamp:   now,
			Environment: "*", // a freeze holds environments, not one
			Action:      action,
			Outcome:     "Success",
			Message:     msg,
			Actor:       stamp,
		},
	}
	// Named after the window's UID: writing it again is a no-op.
	err := r.Create(ctx, ae)
	if (apierrors.IsNotFound(err) || apierrors.IsForbidden(err)) && ns != r.auditNamespace() {
		// The frozen namespace is gone or terminating, or this controller may
		// not write there (namespace mode): the controller's own namespace.
		ae.Namespace = r.auditNamespace()
		err = r.Create(ctx, ae)
	}
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("write the %s AuditEvent of %s: %w", action, cw.Name, err)
	}
	return nil
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

// auditNamespace is where freeze records go that cannot go to the frozen
// namespace: AuditNamespace (the controller's own, or the watched one in
// namespace mode), else kardinal-system.
func (r *Reconciler) auditNamespace() string {
	if r.AuditNamespace != "" {
		return r.AuditNamespace
	}
	return "kardinal-system"
}
