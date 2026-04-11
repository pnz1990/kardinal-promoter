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
	"fmt"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

const (
	// defaultFailureThreshold is used when spec.failureThreshold <= 0.
	defaultFailureThreshold = 3

	// requeueInterval is how often to recheck when the PromotionStep is not found.
	requeueInterval = 30 * time.Second
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

	// Find the associated PromotionStep by pipeline+environment labels.
	var stepList v1alpha1.PromotionStepList
	if err := r.List(ctx, &stepList,
		client.InNamespace(req.Namespace),
		client.MatchingLabels{
			"kardinal.io/pipeline":    rp.Spec.PipelineName,
			"kardinal.io/environment": rp.Spec.Environment,
		},
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("list promotionsteps: %w", err)
	}

	if len(stepList.Items) == 0 {
		log.Debug().
			Str("pipeline", rp.Spec.PipelineName).
			Str("environment", rp.Spec.Environment).
			Msg("no PromotionStep found yet, requeueing")
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}

	// Use the first matching PromotionStep (there should be at most one per env/pipeline).
	step := &stepList.Items[0]
	failures := step.Status.ConsecutiveHealthFailures

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

// ensureRollbackBundle creates a rollback Bundle if one doesn't already exist.
// Returns the name of the rollback Bundle (new or existing), or "" if not needed.
func (r *Reconciler) ensureRollbackBundle(ctx context.Context, log zerolog.Logger,
	rp *v1alpha1.RollbackPolicy) (string, error) {
	// Check if a rollback Bundle already exists for this bundle.
	var existingBundles v1alpha1.BundleList
	if err := r.List(ctx, &existingBundles, client.InNamespace(rp.Namespace)); err != nil {
		return "", fmt.Errorf("list bundles: %w", err)
	}
	for _, b := range existingBundles.Items {
		if b.Labels["kardinal.io/rollback"] == "true" &&
			b.Spec.Provenance != nil &&
			b.Spec.Provenance.RollbackOf == rp.Spec.BundleRef {
			log.Debug().
				Str("existing_rollback", b.Name).
				Msg("rollback bundle already exists, reusing")
			return b.Name, nil
		}
	}

	// Load the original Bundle to copy its spec.
	var originalBundle v1alpha1.Bundle
	if err := r.Get(ctx, client.ObjectKey{Name: rp.Spec.BundleRef, Namespace: rp.Namespace},
		&originalBundle); err != nil {
		if apierrors.IsNotFound(err) {
			log.Warn().Str("bundleRef", rp.Spec.BundleRef).Msg("original bundle not found, cannot create rollback")
			return "", nil
		}
		return "", fmt.Errorf("get bundle %s: %w", rp.Spec.BundleRef, err)
	}

	// Create the rollback Bundle.
	now := r.now()
	rollbackName := fmt.Sprintf("%s-rollback-%d", originalBundle.Spec.Pipeline, now.Unix()%100000)
	rollbackBundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rollbackName,
			Namespace: rp.Namespace,
			Labels: map[string]string{
				"kardinal.io/pipeline": originalBundle.Spec.Pipeline,
				"kardinal.io/rollback": "true",
			},
		},
		Spec: v1alpha1.BundleSpec{
			Type:     originalBundle.Spec.Type,
			Pipeline: originalBundle.Spec.Pipeline,
			Images:   originalBundle.Spec.Images,
			Provenance: &v1alpha1.BundleProvenance{
				RollbackOf: originalBundle.Name,
				Timestamp:  metav1.NewTime(now),
				Author:     "kardinal-controller (auto-rollback via RollbackPolicy)",
			},
		},
	}

	if err := r.Create(ctx, rollbackBundle); err != nil {
		return "", fmt.Errorf("create rollback bundle: %w", err)
	}

	log.Info().
		Str("rollback_bundle", rollbackName).
		Str("original_bundle", originalBundle.Name).
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
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.RollbackPolicy{}).
		Complete(r)
}
