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
	"strconv"
	"strings"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
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
	// SCMProvider is used for startup reconciliation to re-check in-flight PR status.
	// May be nil if startup reconciliation is not needed.
	SCMProvider scm.SCMProvider
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
// informer cache is synced, making it safe to List resources. It runs startup
// reconciliation once: re-checks all WaitingForMerge PromotionSteps to recover
// state from PRs merged during controller downtime.
func (r *Reconciler) Start(ctx context.Context) error {
	if r.SCMProvider == nil {
		return nil
	}
	log := zerolog.Ctx(ctx).With().Str("component", "startup-reconciliation").Logger()

	var psList kardinalv1alpha1.PromotionStepList
	if err := r.List(ctx, &psList); err != nil {
		log.Error().Err(err).Msg("startup reconciliation: failed to list promotionsteps")
		return nil // non-fatal: don't block manager startup
	}

	var inFlight int
	for i := range psList.Items {
		ps := &psList.Items[i]
		if ps.Status.State != "WaitingForMerge" {
			continue
		}
		inFlight++
	}
	log.Info().Int("in_flight_prs", inFlight).Msg("startup reconciliation: re-checking in-flight PRs")

	for i := range psList.Items {
		ps := &psList.Items[i]
		if ps.Status.State != "WaitingForMerge" {
			continue
		}

		prNumStr := ps.Status.Outputs["prNumber"]
		if prNumStr == "" {
			prNumStr = extractPRNumberFromURL(ps.Status.PRURL)
		}
		if prNumStr == "" {
			continue
		}
		prNum, err := strconv.Atoi(prNumStr)
		if err != nil {
			continue
		}
		repo := extractRepoFromURL(ps.Status.PRURL)
		if repo == "" {
			continue
		}

		merged, open, err := r.SCMProvider.GetPRStatus(ctx, repo, prNum)
		if err != nil {
			log.Warn().Err(err).
				Str("promotionstep", ps.Name).
				Msg("startup reconciliation: failed to get PR status")
			continue
		}

		patch := client.MergeFrom(ps.DeepCopy())
		if merged {
			ps.Status.State = "HealthChecking"
			ps.Status.Message = "PR merged during controller downtime (startup reconciliation)"
			if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
				log.Error().Err(patchErr).
					Str("promotionstep", ps.Name).
					Msg("startup reconciliation: patch to HealthChecking failed")
			} else {
				log.Info().
					Str("promotionstep", ps.Name).
					Int("pr", prNum).
					Msg("startup reconciliation: advanced to HealthChecking")
			}
		} else if !open {
			ps.Status.State = "Failed"
			ps.Status.Message = "PR closed without merge (detected during startup reconciliation)"
			if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
				log.Error().Err(patchErr).
					Str("promotionstep", ps.Name).
					Msg("startup reconciliation: patch to Failed failed")
			} else {
				log.Info().
					Str("promotionstep", ps.Name).
					Int("pr", prNum).
					Msg("startup reconciliation: marked Failed (PR closed without merge)")
			}
		}
	}
	return nil
}

// extractPRNumberFromURL parses the PR number from a GitHub PR URL.
func extractPRNumberFromURL(prURL string) string {
	if prURL == "" {
		return ""
	}
	parts := strings.Split(strings.TrimRight(prURL, "/"), "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

// extractRepoFromURL extracts "owner/repo" from a GitHub PR URL.
func extractRepoFromURL(prURL string) string {
	s := strings.TrimPrefix(prURL, "https://")
	s = strings.TrimPrefix(s, "http://")
	idx := strings.Index(s, "/")
	if idx < 0 {
		return ""
	}
	s = s[idx+1:]
	parts := strings.SplitN(s, "/", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "/" + parts[1]
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
