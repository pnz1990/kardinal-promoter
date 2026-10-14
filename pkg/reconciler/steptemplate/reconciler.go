// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package steptemplate implements the StepTemplate reconciler: it validates
// what the CRD schema cannot (agentrun.TemplateProblem) and writes
// status.ready and status.reason. AgentRuns check the template again before
// each run, so a not-ready template refuses its runs with the same reason.
package steptemplate

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentrun"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/eventfilter"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/shard"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
)

// Reconciler writes StepTemplate status.
type Reconciler struct {
	client.Client
	// RequireDigest refuses images without a digest
	// (--step-template-require-digest).
	RequireDigest bool
}

// Reconcile validates one StepTemplate. It is idempotent: the same spec
// always writes the same status, and an unchanged status is not written.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var t v1alpha1.StepTemplate
	if err := r.Get(ctx, req.NamespacedName, &t); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get steptemplate %s: %w", req.Name, err)
	}
	want := v1alpha1.StepTemplateStatus{Ready: true, ObservedGeneration: t.Generation}
	if why := agentrun.TemplateProblem(&t, r.RequireDigest); why != "" {
		want.Ready, want.Reason = false, why
	}
	if t.Status == want {
		return ctrl.Result{}, nil
	}
	patch := client.MergeFrom(t.DeepCopy())
	t.Status = want
	if err := r.Status().Patch(ctx, &t, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch steptemplate %s status: %w", t.Name, err)
	}
	return ctrl.Result{}, nil
}

// SetupWithManager registers the reconciler.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.StepTemplate{}, builder.WithPredicates(eventfilter.SpecOrAnnotationChanged))
	return shard.Active().Complete(b, tracing.WrapReconciler("steptemplate", r), &v1alpha1.StepTemplateList{})
}
