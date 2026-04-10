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

	// Requeue immediately to advance to Promoting
	return ctrl.Result{Requeue: true}, nil
}

// supersedeSiblings finds and marks Promoting bundles for the same Pipeline as Superseded.
// It is idempotent: already-superseded bundles are skipped.
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

// SetupWithManager registers the BundleReconciler with the controller-runtime Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kardinalv1alpha1.Bundle{}).
		Complete(r)
}
