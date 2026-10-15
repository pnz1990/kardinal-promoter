// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package changewindow

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
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

// reconcileFreezeAudit records a freeze's start and lift as AuditEvents with
// who did it: FreezeStarted names spec.freeze.createdBy and its actor stamp,
// FreezeLifted spec.freeze.lifted (kardinal unfreeze and the UI write it
// just before the delete), else an unknown actor. A finalizer holds the
// window until FreezeLifted is written. The records are named after the
// window's UID, so a re-run writes nothing new. It reports whether the
// window is being deleted (the reconcile then ends).
func (r *Reconciler) reconcileFreezeAudit(ctx context.Context, cw *kardinalv1alpha1.ChangeWindow) (bool, error) {
	if cw.Spec.Freeze == nil {
		return false, nil
	}
	if !cw.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(cw, FreezeAuditFinalizer) {
			return true, nil
		}
		if err := r.writeFreezeAudit(ctx, cw, AuditActionFreezeLifted); err != nil {
			return true, err
		}
		patch := client.MergeFrom(cw.DeepCopy())
		controllerutil.RemoveFinalizer(cw, FreezeAuditFinalizer)
		if err := r.Patch(ctx, cw, patch); err != nil {
			return true, client.IgnoreNotFound(fmt.Errorf("remove the freeze audit finalizer: %w", err))
		}
		return true, nil
	}
	if !controllerutil.ContainsFinalizer(cw, FreezeAuditFinalizer) {
		patch := client.MergeFrom(cw.DeepCopy())
		controllerutil.AddFinalizer(cw, FreezeAuditFinalizer)
		if err := r.Patch(ctx, cw, patch); err != nil {
			return false, fmt.Errorf("add the freeze audit finalizer: %w", err)
		}
	}
	if meta.IsStatusConditionTrue(cw.Status.Conditions, conditionFreezeAudited) {
		return false, nil
	}
	if err := r.writeFreezeAudit(ctx, cw, AuditActionFreezeStarted); err != nil {
		return false, err
	}
	patch := client.MergeFrom(cw.DeepCopy())
	meta.SetStatusCondition(&cw.Status.Conditions, metav1.Condition{Type: conditionFreezeAudited,
		Status: metav1.ConditionTrue, Reason: "Recorded", Message: "the FreezeStarted AuditEvent is written",
		ObservedGeneration: cw.Generation, LastTransitionTime: metav1.NewTime(r.now())})
	if err := r.Status().Patch(ctx, cw, patch); err != nil {
		return false, client.IgnoreNotFound(fmt.Errorf("record the freeze audit: %w", err))
	}
	return false, nil
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
	msg := fmt.Sprintf("%s %s %s (%s)", orUnknown(by), verb, cw.Name, FreezeScope(f))
	if action == AuditActionFreezeStarted && cw.Spec.Reason != "" {
		msg += ": " + cw.Spec.Reason
	}
	if by == "" && action == AuditActionFreezeLifted {
		msg = fmt.Sprintf("freeze %s (%s) lifted: deleted without kardinal unfreeze; the Kubernetes audit log names who",
			cw.Name, FreezeScope(f))
	}
	ns := r.AuditNamespace
	if len(f.Namespaces) == 1 && !strings.HasSuffix(f.Namespaces[0], "*") {
		ns = f.Namespaces[0]
	}
	if ns == "" {
		ns = "kardinal-system"
	}
	suffix := "freeze-started"
	if action == AuditActionFreezeLifted {
		suffix = "freeze-lifted"
	}
	now := metav1.NewTime(r.now())
	ae := &kardinalv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s", string(cw.UID), suffix), Namespace: ns,
			Labels: map[string]string{"kardinal.io/changewindow": cw.Name, "kardinal.io/action": action},
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
	if err := r.Create(ctx, ae); err != nil && !apierrors.IsAlreadyExists(err) {
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
