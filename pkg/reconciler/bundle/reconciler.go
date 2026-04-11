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

// Package bundle implements the BundleReconciler which watches Bundle objects,
// sets status.phase = Available on newly-created Bundles, triggers the
// Pipeline-to-Graph translation, and handles Bundle supersession.
package bundle

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// BundleTranslator is the interface the BundleReconciler uses to translate a
// Bundle+Pipeline into a kro Graph. Abstracted as an interface for testability.
type BundleTranslator interface {
	Translate(ctx context.Context, pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle) (string, error)
}

// Reconciler watches Bundle objects, sets Available phase, triggers translation,
// and manages Bundle supersession.
type Reconciler struct {
	client.Client
	// Translator creates the kro Graph for a Bundle+Pipeline pair.
	// May be nil in test environments where translation is not needed.
	Translator BundleTranslator
}

// Reconcile is called whenever a Bundle is created, updated, or deleted.
//
// State machine:
//   - Phase = "" (new Bundle): supersede older Promoting bundles, set to Available
//   - Phase = "Available" (ready to promote): look up Pipeline, call Translator,
//     set phase to Promoting
//   - All other phases: idempotent no-op
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().
		Str("bundle", req.Name).
		Str("namespace", req.Namespace).
		Logger()

	var b kardinalv1alpha1.Bundle
	if err := r.Get(ctx, req.NamespacedName, &b); err != nil {
		if apierrors.IsNotFound(err) {
			log.Debug().Msg("bundle not found, likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get bundle: %w", err)
	}

	switch b.Status.Phase {
	case "":
		return r.handleNew(ctx, log, &b)
	case "Available":
		return r.handleAvailable(ctx, log, &b)
	default:
		log.Debug().Str("phase", b.Status.Phase).Msg("bundle phase already advanced, skipping")
		return ctrl.Result{}, nil
	}
}

// handleNew sets the phase to Available on a newly-created Bundle.
// It also supersedes any older Bundles in Promoting state for the same Pipeline.
func (r *Reconciler) handleNew(ctx context.Context, log zerolog.Logger,
	b *kardinalv1alpha1.Bundle) (ctrl.Result, error) {
	// Supersession: mark older Promoting bundles for the same Pipeline as Superseded.
	if supersErr := r.supersedeSiblings(ctx, log, b); supersErr != nil {
		// Non-fatal: log and continue. The new bundle must still become Available.
		log.Warn().Err(supersErr).Msg("failed to supersede sibling bundles (non-fatal)")
	}

	patch := client.MergeFrom(b.DeepCopy())
	b.Status.Phase = "Available"

	if err := r.Status().Patch(ctx, b, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch bundle status Available: %w", err)
	}

	log.Info().
		Str("phase", "Available").
		Str("type", b.Spec.Type).
		Str("pipeline", b.Spec.Pipeline).
		Msg("bundle phase set to Available")

	// Requeue immediately to advance to Promoting.
	// Use RequeueAfter instead of Requeue (Requeue is deprecated).
	return ctrl.Result{RequeueAfter: time.Millisecond}, nil
}

// supersedeSiblings finds and marks Promoting bundles for the same Pipeline as Superseded.
// It is idempotent: already-superseded bundles are skipped.
// Type-aware: image bundles only supersede image bundles; config bundles only supersede
// config bundles. This allows image and config promotions to coexist independently.
func (r *Reconciler) supersedeSiblings(ctx context.Context, log zerolog.Logger,
	newBundle *kardinalv1alpha1.Bundle) error {
	var bundles kardinalv1alpha1.BundleList
	if err := r.List(ctx, &bundles,
		client.InNamespace(newBundle.Namespace),
	); err != nil {
		return fmt.Errorf("list bundles: %w", err)
	}

	for i := range bundles.Items {
		sibling := &bundles.Items[i]
		// Skip self and bundles targeting a different Pipeline.
		if sibling.Name == newBundle.Name {
			continue
		}
		if sibling.Spec.Pipeline != newBundle.Spec.Pipeline {
			continue
		}
		// Only supersede bundles of the same type (image vs config independence).
		if sibling.Spec.Type != newBundle.Spec.Type {
			continue
		}
		// Only supersede bundles that are actively promoting.
		if sibling.Status.Phase != "Promoting" && sibling.Status.Phase != "Available" {
			continue
		}

		patch := client.MergeFrom(sibling.DeepCopy())
		sibling.Status.Phase = "Superseded"
		if patchErr := r.Status().Patch(ctx, sibling, patch); patchErr != nil {
			log.Error().Err(patchErr).Str("sibling", sibling.Name).Msg("failed to supersede bundle")
			return fmt.Errorf("supersede bundle %s: %w", sibling.Name, patchErr)
		}

		log.Info().
			Str("superseded", sibling.Name).
			Str("by", newBundle.Name).
			Msg("bundle superseded")
	}
	return nil
}

// handleAvailable triggers Graph creation and advances phase to Promoting.
func (r *Reconciler) handleAvailable(ctx context.Context, log zerolog.Logger,
	b *kardinalv1alpha1.Bundle) (ctrl.Result, error) {
	if r.Translator == nil {
		// No translator configured (test mode / early stage).
		log.Debug().Msg("translator not configured, skipping graph creation")
		return ctrl.Result{}, nil
	}

	// Look up the Pipeline
	var pipeline kardinalv1alpha1.Pipeline
	if err := r.Get(ctx, client.ObjectKey{
		Name:      b.Spec.Pipeline,
		Namespace: b.Namespace,
	}, &pipeline); err != nil {
		if apierrors.IsNotFound(err) {
			log.Warn().
				Str("pipeline", b.Spec.Pipeline).
				Msg("pipeline not found for bundle")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get pipeline %s: %w", b.Spec.Pipeline, err)
	}

	// If the Pipeline is paused, do not start a new promotion.
	// Requeue after a short interval to re-check the pause state.
	if pipeline.Spec.Paused {
		log.Info().
			Str("pipeline", pipeline.Name).
			Msg("pipeline is paused — holding bundle at Available until resumed")
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	// Translate Pipeline+Bundle into a Graph
	graphName, err := r.Translator.Translate(ctx, &pipeline, b)
	if err != nil {
		log.Error().Err(err).Msg("failed to translate bundle to graph")
		// Patch bundle to Failed
		patch := client.MergeFrom(b.DeepCopy())
		b.Status.Phase = "Failed"
		if patchErr := r.Status().Patch(ctx, b, patch); patchErr != nil {
			log.Error().Err(patchErr).Msg("failed to patch bundle status to Failed")
		}
		return ctrl.Result{}, fmt.Errorf("translate bundle %s: %w", b.Name, err)
	}

	// Advance to Promoting
	patch := client.MergeFrom(b.DeepCopy())
	b.Status.Phase = "Promoting"
	_ = graphName // GraphRef will be stored in BundleStatus in a future stage

	if err := r.Status().Patch(ctx, b, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch bundle status Promoting: %w", err)
	}

	log.Info().
		Str("phase", "Promoting").
		Str("graph", graphName).
		Msg("bundle advancing to Promoting")

	return ctrl.Result{}, nil
}

// Start implements manager.Runnable. It is called by controller-runtime after the
// informer cache is synced. With the PRStatus CRD architecture, startup reconciliation
// of PR merge state is no longer needed here: the PRStatusReconciler polls GitHub and
// updates PRStatus.status.merged, and the PromotionStep reconciler reads that CRD.
//
// This method is retained as a no-op Runnable to satisfy the manager.Runnable interface
// without breaking the existing SetupWithManager call.
//
// Eliminates BU-3 (docs/design/11-graph-purity-tech-debt.md).
func (r *Reconciler) Start(ctx context.Context) error {
	log := zerolog.Ctx(ctx).With().Str("component", "startup-reconciliation").Logger()
	log.Info().Msg("startup reconciliation: PRStatus CRD architecture active — no polling required")
	return nil
}

// SetupWithManager registers the BundleReconciler with the controller-runtime Manager.
// It also registers the reconciler as a Runnable so that Start() is called after
// cache sync to perform startup reconciliation.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.Add(r); err != nil {
		return fmt.Errorf("add reconciler as runnable: %w", err)
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&kardinalv1alpha1.Bundle{}).
		Complete(r)
}
