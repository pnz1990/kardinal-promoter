// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package rollbackpolicy implements the RollbackPolicyReconciler, which monitors
// PromotionStep.status.consecutiveHealthFailures and triggers auto-rollback by
// writing status.shouldRollback to the RollbackPolicy CRD and creating a rollback
// Bundle when the failure threshold is exceeded.
//
// Architecture: the reconciler reads PromotionStep status (cross-CRD read is OK)
// but only writes its own CRD status (RollbackPolicy.status.*). It also creates
// a rollback Bundle when triggered — creating a new resource is not the same as
// mutating another CRD's status and is permitted.
//
// Graph-purity: eliminates PS-6 and PS-7 from docs/design/11-graph-purity-tech-debt.md.
// The threshold comparison was previously invisible inside the PromotionStep reconciler;
// now it is written to RollbackPolicy.status.shouldRollback and observable by the Graph.
package rollbackpolicy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/eventfilter"
)

const (
	// defaultFailureThreshold is used when spec.failureThreshold <= 0.
	defaultFailureThreshold = 3

	// requeueInterval is how often to recheck when the PromotionStep is not found.
	requeueInterval = 30 * time.Second

	labelPipeline    = "kardinal.io/pipeline"
	labelEnvironment = "kardinal.io/environment"
	labelBundle      = "kardinal.io/bundle"
)

// Reconciler monitors a RollbackPolicy and triggers auto-rollback when the
// associated PromotionStep's consecutive health failures exceed the threshold.
// It is idempotent and safe to re-run after a crash.
type Reconciler struct {
	client.Client

	// NowFn returns the current time. Overridable for testing.
	NowFn func() time.Time
}

// Reconcile processes one RollbackPolicy event.
// It is idempotent: safe to re-run after a crash at any point.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().
		Str("rollbackpolicy", req.Name).
		Str("namespace", req.Namespace).
		Logger()

	var rp v1alpha1.RollbackPolicy
	if err := r.Get(ctx, req.NamespacedName, &rp); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get rollbackpolicy %s: %w", req.Name, err)
	}

	// Terminal: already triggered and rollback bundle created — no-op.
	if rp.Status.ShouldRollback && rp.Status.RollbackBundleName != nil {
		log.Debug().Msg("rollback already triggered, no-op")
		return ctrl.Result{}, nil
	}

	// Find the PromotionStep(s) of spec.bundleRef in this environment. Steps are
	// per Bundle, so the bundle label is required: matching on pipeline and
	// environment alone would read another Bundle's step (C04-gates-05).
	failures, stepCount, err := r.bundleStepFailures(ctx, &rp)
	if err != nil {
		return ctrl.Result{}, err
	}
	if stepCount == 0 {
		log.Debug().
			Str("pipeline", rp.Spec.PipelineName).
			Str("environment", rp.Spec.Environment).
			Str("bundle", rp.Spec.BundleRef).
			Msg("no PromotionStep found yet for the bundle, requeueing")
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}

	threshold := rp.Spec.FailureThreshold
	if threshold <= 0 {
		threshold = defaultFailureThreshold
	}

	// Write status: always update consecutiveFailures and lastEvaluatedAt.
	now := metav1.NewTime(r.now())
	patch := client.MergeFrom(rp.DeepCopy())
	rp.Status.ConsecutiveFailures = failures
	rp.Status.LastEvaluatedAt = &now

	if failures >= threshold {
		rp.Status.ShouldRollback = true
	}

	if err := r.Status().Patch(ctx, &rp, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch rollbackpolicy status %s: %w", req.Name, err)
	}

	// If threshold exceeded, create rollback Bundle (if not already done).
	if rp.Status.ShouldRollback {
		rbName, err := r.ensureRollbackBundle(ctx, log, &rp)
		if err != nil {
			log.Error().Err(err).Msg("failed to ensure rollback bundle")
			return ctrl.Result{RequeueAfter: requeueInterval}, nil
		}
		if rbName != "" {
			// Record the rollback bundle name on the status.
			patch2 := client.MergeFrom(rp.DeepCopy())
			rp.Status.RollbackBundleName = &rbName
			if patchErr := r.Status().Patch(ctx, &rp, patch2); patchErr != nil {
				return ctrl.Result{}, fmt.Errorf("patch rollbackpolicy bundlename %s: %w", req.Name, patchErr)
			}
		}
		return ctrl.Result{}, nil
	}

	// Not yet triggered — requeue to re-check.
	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// bundleStepFailures returns the highest status.consecutiveHealthFailures across
// the PromotionSteps of spec.bundleRef in spec.environment, and how many such
// steps exist. A multi-region environment has one step per region; any region
// reaching the threshold triggers the rollback.
func (r *Reconciler) bundleStepFailures(ctx context.Context, rp *v1alpha1.RollbackPolicy) (int, int, error) {
	var stepList v1alpha1.PromotionStepList
	if err := r.List(ctx, &stepList,
		client.InNamespace(rp.Namespace),
		client.MatchingLabels{
			labelPipeline:    rp.Spec.PipelineName,
			labelEnvironment: rp.Spec.Environment,
		},
	); err != nil {
		return 0, 0, fmt.Errorf("list promotionsteps for bundle %s: %w", rp.Spec.BundleRef, err)
	}
	maxFailures, count := 0, 0
	for i := range stepList.Items {
		step := &stepList.Items[i]
		if stepBundle(step) != rp.Spec.BundleRef {
			continue
		}
		count++
		if step.Status.ConsecutiveHealthFailures > maxFailures {
			maxFailures = step.Status.ConsecutiveHealthFailures
		}
	}
	return maxFailures, count, nil
}

// stepBundle returns the Bundle a PromotionStep promotes: spec.bundleName, or
// the kardinal.io/bundle label when the spec field is empty. The spec field is
// authoritative; steps created outside the Graph may not carry the label.
func stepBundle(step *v1alpha1.PromotionStep) string {
	if step.Spec.BundleName != "" {
		return step.Spec.BundleName
	}
	return step.Labels[labelBundle]
}

// ensureRollbackBundle creates a rollback Bundle if one doesn't already exist.
// Returns the name of the rollback Bundle (new or existing), or "" if not needed.
//
// The rollback is planned by lifecycle.PlanRollback, the planner shared with
// `kardinal rollback`, the UI and onHealthFailure=rollback: it restores the
// artifacts of the most recent Bundle, other than spec.bundleRef, that was
// Verified in the environment, and never re-promotes the failing image. When
// there is nothing safe to roll back to, or spec.bundleRef is itself a
// rollback (a failing rollback does not start another), no Bundle is created
// (C04-gates-06).
func (r *Reconciler) ensureRollbackBundle(ctx context.Context, log zerolog.Logger,
	rp *v1alpha1.RollbackPolicy) (string, error) {
	// Reuse a rollback of this Bundle created before this planner existed:
	// those have no kardinal.io/rollback-from annotation and recorded the
	// failing Bundle in provenance.rollbackOf.
	var existingBundles v1alpha1.BundleList
	if err := r.List(ctx, &existingBundles, client.InNamespace(rp.Namespace)); err != nil {
		return "", fmt.Errorf("list bundles: %w", err)
	}
	for _, b := range existingBundles.Items {
		if b.Labels[lifecycle.LabelRollback] == "true" &&
			b.Annotations[lifecycle.AnnotationRollbackFrom] == "" &&
			b.Spec.Provenance != nil &&
			b.Spec.Provenance.RollbackOf == rp.Spec.BundleRef {
			log.Debug().
				Str("existing_rollback", b.Name).
				Msg("rollback bundle already exists, reusing")
			return b.Name, nil
		}
	}
	// Reuse a rollback of this Bundle in this environment from either
	// automatic path (this one or onHealthFailure=rollback).
	existing, err := lifecycle.FindRollback(ctx, r.Client, rp.Namespace,
		rp.Spec.PipelineName, rp.Spec.Environment, rp.Spec.BundleRef)
	if err != nil {
		return "", fmt.Errorf("find rollback of bundle %s: %w", rp.Spec.BundleRef, err)
	}
	if existing != "" {
		log.Debug().Str("existing_rollback", existing).Msg("rollback bundle already exists, reusing")
		return existing, nil
	}

	rollbackName := lifecycle.AutoRollbackName(rp.Spec.BundleRef, "policy")
	plan, err := lifecycle.PlanRollback(ctx, r.Client, lifecycle.RollbackRequest{
		Namespace:   rp.Namespace,
		Pipeline:    rp.Spec.PipelineName,
		Environment: rp.Spec.Environment,
		FromBundle:  rp.Spec.BundleRef,
		Actor:       "kardinal-controller (auto-rollback via RollbackPolicy)",
		Name:        rollbackName,
		Reason:      "AutoRollback",
		Now:         r.now(),
		Automatic:   true,
	})
	if err != nil {
		if errors.Is(err, lifecycle.ErrConflict) || errors.Is(err, lifecycle.ErrInvalid) ||
			errors.Is(err, lifecycle.ErrNotFound) {
			log.Warn().Err(err).
				Str("bundleRef", rp.Spec.BundleRef).
				Str("environment", rp.Spec.Environment).
				Msg("auto-rollback: nothing safe to roll back to; no rollback bundle created, human intervention required")
			return "", nil
		}
		return "", fmt.Errorf("plan rollback of bundle %s: %w", rp.Spec.BundleRef, err)
	}

	if err := r.Create(ctx, plan.Bundle); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create rollback bundle: %w", err)
	}

	log.Info().
		Str("rollback_bundle", rollbackName).
		Str("original_bundle", rp.Spec.BundleRef).
		Str("rollback_to", plan.Target.Name).
		Int("failures", rp.Status.ConsecutiveFailures).
		Str("pipeline", rp.Spec.PipelineName).
		Str("environment", rp.Spec.Environment).
		Msg("auto-rollback: created rollback bundle via RollbackPolicy")

	return rollbackName, nil
}

// now returns the current time via NowFn if set (for testing), otherwise time.Now().UTC().
func (r *Reconciler) now() time.Time {
	if r.NowFn != nil {
		return r.NowFn()
	}
	return time.Now().UTC()
}

// SetupWithManager registers the RollbackPolicyReconciler with controller-runtime.
//
// It watches PromotionStep so a threshold crossing is acted on when the step's
// status changes, not only on the 30s requeue. Only spec or annotation changes
// of the RollbackPolicy itself trigger a reconcile: its own status writes do not.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.RollbackPolicy{}, builder.WithPredicates(eventfilter.SpecOrAnnotationChanged)).
		Watches(&v1alpha1.PromotionStep{}, handler.EnqueueRequestsFromMapFunc(r.policiesForStep)).
		Complete(r)
}

// policiesForStep maps a PromotionStep to the RollbackPolicies that monitor its
// Bundle in its environment.
func (r *Reconciler) policiesForStep(ctx context.Context, obj client.Object) []reconcile.Request {
	step, ok := obj.(*v1alpha1.PromotionStep)
	if !ok {
		return nil
	}
	labels := step.GetLabels()
	bundle := stepBundle(step)
	if bundle == "" {
		return nil
	}
	var list v1alpha1.RollbackPolicyList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Str("promotionstep", obj.GetName()).
			Msg("failed to list RollbackPolicies for PromotionStep event; relying on requeue")
		return nil
	}
	var reqs []reconcile.Request
	for _, rp := range list.Items {
		if rp.Spec.BundleRef == bundle &&
			rp.Spec.PipelineName == labels[labelPipeline] &&
			rp.Spec.Environment == labels[labelEnvironment] {
			reqs = append(reqs, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: rp.Name, Namespace: rp.Namespace},
			})
		}
	}
	return reqs
}
