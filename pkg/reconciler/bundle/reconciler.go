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
// Pipeline-to-Graph translation, handles Bundle self-supersession (BU-1/BU-4 fix:
// each bundle detects a newer sibling and supersedes itself, avoiding cross-CRD
// mutations), and syncs per-environment promotion evidence from PromotionStep status.
package bundle

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// BundleTranslator is the interface the BundleReconciler uses to translate a
// Bundle+Pipeline into a kro Graph. Abstracted as an interface for testability.
type BundleTranslator interface {
	Translate(ctx context.Context, pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle) (string, error)
}

// GraphChecker checks whether a kro Graph exists by name in a given namespace.
// Abstracted as an interface for testability without a real Kubernetes cluster.
type GraphChecker interface {
	GraphExists(ctx context.Context, namespace, name string) (bool, error)
}

// Reconciler watches Bundle objects, sets Available phase, triggers translation,
// manages Bundle supersession, and syncs evidence from PromotionStep status
// into Bundle.status.environments.
type Reconciler struct {
	client.Client
	// Translator creates the kro Graph for a Bundle+Pipeline pair.
	// May be nil in test environments where translation is not needed.
	Translator BundleTranslator
	// GraphChecker detects whether the Graph CR still exists.
	// When nil, graph recreation is skipped (backward-compatible).
	GraphChecker GraphChecker
}

// Reconcile is called whenever a Bundle is created, updated, or deleted,
// or whenever a PromotionStep for this bundle changes (via the Watch in SetupWithManager).
//
// State machine:
//   - Phase = "" (new Bundle): set to Available; each bundle self-supersedes if a newer same-type bundle exists
//   - Phase = "Available" (ready to promote): check self-supersession; look up Pipeline, call Translator,
//     set phase to Promoting
//   - Phase = "Promoting" | "Verified" | "Failed": sync evidence from PromotionStep status
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
		// Check self-supersession before attempting to promote.
		if superseded, err := r.isSuperseededByNewer(ctx, &b); err != nil {
			log.Warn().Err(err).Msg("failed to check for newer bundle (non-fatal)")
		} else if superseded {
			return r.markSuperseded(ctx, log, &b)
		}
		return r.handleAvailable(ctx, log, &b)
	case "Superseded", "Verified":
		// Terminal or stable states — only sync evidence; the pipeline check in
		// handleAvailable is not needed (the bundle has already been promoted or dropped).
		return r.handleSyncEvidence(ctx, log, &b)
	default:
		// For Promoting, Failed: sync evidence from PromotionStep status.
		// Also check supersession for Promoting phase — a newer bundle of the same
		// type may have started Promoting while this one was in-flight (#281).
		if b.Status.Phase == "Promoting" {
			if superseded, err := r.isSuperseededByNewer(ctx, &b); err != nil {
				log.Warn().Err(err).Msg("failed to check for newer bundle during Promoting (non-fatal)")
			} else if superseded {
				return r.markSuperseded(ctx, log, &b)
			}
		}
		// Also check if the parent pipeline was deleted — self-delete to avoid orphan.
		// This extends the orphan guard from handleAvailable to all active phases.
		if b.Spec.Pipeline != "" {
			var pl kardinalv1alpha1.Pipeline
			if err := r.Get(ctx, client.ObjectKey{
				Name:      b.Spec.Pipeline,
				Namespace: b.Namespace,
			}, &pl); err != nil {
				if apierrors.IsNotFound(err) {
					log.Info().
						Str("pipeline", b.Spec.Pipeline).
						Str("phase", b.Status.Phase).
						Msg("parent pipeline deleted — self-deleting orphaned Bundle")
					if delErr := r.Delete(ctx, &b); delErr != nil && !apierrors.IsNotFound(delErr) {
						return ctrl.Result{}, fmt.Errorf("delete orphaned bundle: %w", delErr)
					}
					return ctrl.Result{}, nil
				}
				// Transient error — still sync evidence on best-effort basis.
				log.Warn().Err(err).Msg("failed to check parent pipeline (non-fatal), continuing sync")
			}
		}
		// For Promoting bundles: ensure the Graph still exists and recreate if needed.
		// Fixes issue #490: manual Graph deletion left bundles stuck in Promoting.
		if b.Status.Phase == "Promoting" {
			if err := r.ensureGraphExists(ctx, log, &b); err != nil {
				log.Error().Err(err).Msg("graph recreation failed — requeuing")
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
		}
		return r.handleSyncEvidence(ctx, log, &b)
	}
}

// ensureGraphExists checks whether the Graph for a Promoting Bundle still exists,
// and recreates it via the Translator if it has been deleted.
//
// This fixes issue #490: manual `kubectl delete graph <name>` left Bundles stuck
// in Promoting indefinitely. Graph CR deletion now triggers re-reconciliation
// (via the Graph Watch in SetupWithManager) and this method handles recreation.
//
// Graph-first: we only write to our own CRD status (GraphRef). The Translator
// is idempotent — if the Graph already exists it returns nil without creating again.
func (r *Reconciler) ensureGraphExists(ctx context.Context, log zerolog.Logger,
	b *kardinalv1alpha1.Bundle) error {
	if r.Translator == nil || r.GraphChecker == nil {
		return nil // no-op in test environments without real translator
	}

	// Compute expected graph name — deterministic from (pipeline, bundle).
	expectedName := b.Status.GraphRef
	if expectedName == "" {
		// Fallback: recompute from known naming convention.
		expectedName = graph.GraphNameFrom(b.Spec.Pipeline, b.Name)
	}

	exists, err := r.GraphChecker.GraphExists(ctx, b.Namespace, expectedName)
	if err != nil {
		// Non-fatal: log and continue. Graph check failure should not block evidence sync.
		log.Warn().Err(err).Str("graph", expectedName).Msg("failed to check graph existence (non-fatal)")
		return nil
	}
	if exists {
		return nil // nothing to do
	}

	// Graph is missing — look up the Pipeline and recreate.
	log.Info().Str("graph", expectedName).Msg("graph deleted externally — recreating")

	var pipeline kardinalv1alpha1.Pipeline
	if err := r.Get(ctx, client.ObjectKey{Name: b.Spec.Pipeline, Namespace: b.Namespace}, &pipeline); err != nil {
		if apierrors.IsNotFound(err) {
			// Pipeline deleted — Bundle orphan guard will handle cleanup on next reconcile.
			return nil
		}
		return fmt.Errorf("ensureGraphExists: get pipeline: %w", err)
	}

	graphName, err := r.Translator.Translate(ctx, &pipeline, b)
	if err != nil {
		return fmt.Errorf("ensureGraphExists: recreate graph: %w", err)
	}

	// Update GraphRef if it changed (shouldn't happen, but keep it current).
	if b.Status.GraphRef != graphName {
		patch := client.MergeFrom(b.DeepCopy())
		b.Status.GraphRef = graphName
		if patchErr := r.Status().Patch(ctx, b, patch); patchErr != nil {
			log.Warn().Err(patchErr).Msg("failed to update GraphRef after recreation (non-fatal)")
		}
	}

	log.Info().Str("graph", graphName).Msg("graph recreated after external deletion")
	return nil
}

// handleNew sets the phase to Available on a newly-created Bundle.
// Supersession of older bundles is no longer done here (BU-1 fix). Each bundle
// is responsible for superseding itself when it detects a newer bundle exists.
func (r *Reconciler) handleNew(ctx context.Context, log zerolog.Logger,
	b *kardinalv1alpha1.Bundle) (ctrl.Result, error) {
	patch := client.MergeFrom(b.DeepCopy())
	b.Status.Phase = "Available"

	if err := r.Status().Patch(ctx, b, patch); err != nil {
		if apierrors.IsNotFound(err) {
			// Bundle was deleted between cache read and patch — treat as a no-op.
			log.Debug().Msg("bundle deleted before Available patch — ignoring")
			return ctrl.Result{}, nil
		}
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

// isSuperseededByNewer returns true if there is a newer Bundle for the same
// pipeline and type that is not yet Superseded or Failed. This implements
// self-supersession: each bundle checks if it should yield to a newer sibling,
// writing only to its own status (BU-1 / BU-4 fix — no cross-CRD mutations).
func (r *Reconciler) isSuperseededByNewer(ctx context.Context, b *kardinalv1alpha1.Bundle) (bool, error) {
	var bundles kardinalv1alpha1.BundleList
	if err := r.List(ctx, &bundles, client.InNamespace(b.Namespace)); err != nil {
		return false, fmt.Errorf("list bundles for supersession check: %w", err)
	}

	for i := range bundles.Items {
		sibling := &bundles.Items[i]
		if sibling.Name == b.Name {
			continue
		}
		if sibling.Spec.Pipeline != b.Spec.Pipeline {
			continue
		}
		// Type-aware: image bundles are only superseded by image bundles, etc.
		if sibling.Spec.Type != b.Spec.Type {
			continue
		}
		// Skip terminal or already-superseded siblings.
		if sibling.Status.Phase == "Superseded" || sibling.Status.Phase == "Failed" || sibling.Status.Phase == "Verified" {
			continue
		}
		// A sibling that is not terminal and was created after us means we are superseded.
		// Tiebreaker: when creation times are equal (same second), the lexicographically
		// greater bundle name "wins" (newer by convention for rapid-fire creation, #289).
		siblingTs := sibling.CreationTimestamp.Time
		bTs := b.CreationTimestamp.Time
		if siblingTs.After(bTs) ||
			(siblingTs.Equal(bTs) && sibling.Name > b.Name) {
			return true, nil
		}
	}
	return false, nil
}

// markSuperseded sets this bundle's status.phase to "Superseded" (self-supersession).
func (r *Reconciler) markSuperseded(ctx context.Context, log zerolog.Logger,
	b *kardinalv1alpha1.Bundle) (ctrl.Result, error) {
	patch := client.MergeFrom(b.DeepCopy())
	b.Status.Phase = "Superseded"
	if err := r.Status().Patch(ctx, b, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch bundle status Superseded: %w", err)
	}
	log.Info().
		Str("pipeline", b.Spec.Pipeline).
		Str("type", b.Spec.Type).
		Msg("bundle superseded by newer bundle (self-supersession)")
	return ctrl.Result{}, nil
}

// handleAvailable triggers Graph creation and advances phase to Promoting.
func (r *Reconciler) handleAvailable(ctx context.Context, log zerolog.Logger,
	b *kardinalv1alpha1.Bundle) (ctrl.Result, error) {
	// Look up the Pipeline FIRST (before translator check) so that orphaned bundles
	// are cleaned up even when the translator is not configured (#270).
	var pipeline kardinalv1alpha1.Pipeline
	if err := r.Get(ctx, client.ObjectKey{
		Name:      b.Spec.Pipeline,
		Namespace: b.Namespace,
	}, &pipeline); err != nil {
		if apierrors.IsNotFound(err) {
			// Pipeline has been deleted. Self-delete this Bundle to avoid orphaned
			// resources (#270). This mirrors the PromotionStep orphan guard (#248):
			// we delete our OWN resource (Bundle) when its parent is gone.
			// Graph-first: no cross-CRD mutation — we only delete the Bundle itself.
			log.Info().
				Str("pipeline", b.Spec.Pipeline).
				Msg("parent pipeline not found — self-deleting orphaned Bundle")
			if delErr := r.Delete(ctx, b); delErr != nil && !apierrors.IsNotFound(delErr) {
				return ctrl.Result{}, fmt.Errorf("delete orphaned bundle: %w", delErr)
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get pipeline %s: %w", b.Spec.Pipeline, err)
	}

	if r.Translator == nil {
		// No translator configured (test mode / early stage).
		log.Debug().Msg("translator not configured, skipping graph creation")
		return ctrl.Result{}, nil
	}

	// Translate Pipeline+Bundle into a Graph
	// Note: Pipeline.Spec.Paused enforcement is handled by the freeze PolicyGate
	// (created by `kardinal pause`) which blocks all Graph nodes. The reconciler
	// does not check Spec.Paused directly — that would be a Graph-invisible
	// business rule (PS-2 / BU-2). The freeze gate IS visible to the Graph.
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
	b.Status.GraphRef = graphName // store for recreation detection (#490)

	if err := r.Status().Patch(ctx, b, patch); err != nil {
		if apierrors.IsNotFound(err) {
			log.Debug().Msg("bundle deleted before Promoting patch — ignoring")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("patch bundle status Promoting: %w", err)
	}

	log.Info().
		Str("phase", "Promoting").
		Str("graph", graphName).
		Msg("bundle advancing to Promoting")

	return ctrl.Result{}, nil
}

// handleSyncEvidence reads all PromotionSteps for this Bundle and merges their
// per-environment state into Bundle.status.environments. This is the Graph-first
// replacement for the PromotionStep reconciler's copyEvidenceToBundle (PS-9):
// the Bundle reconciler writes to its own CRD status, not a foreign one.
//
// Graph-purity: the PromotionStep reconciler no longer writes to Bundle.status.
// The Bundle reconciler is triggered by PromotionStep changes via the Watch added
// in SetupWithManager, so evidence is still propagated promptly.
func (r *Reconciler) handleSyncEvidence(ctx context.Context, log zerolog.Logger,
	b *kardinalv1alpha1.Bundle) (ctrl.Result, error) {
	var psList kardinalv1alpha1.PromotionStepList
	if err := r.List(ctx, &psList,
		client.InNamespace(b.Namespace),
		client.MatchingLabels{"kardinal.io/bundle": b.Name},
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("list promotion steps for bundle %s: %w", b.Name, err)
	}

	if len(psList.Items) == 0 {
		// K-05: even when no PromotionSteps exist, compute metrics if all environments
		// in status are already Verified (this happens on re-reconcile after Graph deletion).
		if b.Status.Metrics == nil && len(b.Status.Environments) > 0 {
			if metrics := computeBundleMetrics(b, b.Status.Environments); metrics != nil {
				patch := client.MergeFrom(b.DeepCopy())
				b.Status.Metrics = metrics
				if patchErr := r.Status().Patch(ctx, b, patch); patchErr != nil {
					return ctrl.Result{}, fmt.Errorf("patch bundle metrics: %w", patchErr)
				}
				log.Info().Int64("commitToProductionMinutes", metrics.CommitToProductionMinutes).
					Msg("K-05: bundle metrics computed")
			}
		}
		log.Debug().Msg("no promotion steps found for bundle, nothing to sync")
		return ctrl.Result{}, nil
	}

	// Build a map of current environment statuses so we can update idempotently.
	envMap := make(map[string]kardinalv1alpha1.EnvironmentStatus, len(b.Status.Environments))
	for _, env := range b.Status.Environments {
		envMap[env.Name] = env
	}

	changed := false
	for _, ps := range psList.Items {
		envName := ps.Spec.Environment
		prev := envMap[envName]

		updated := kardinalv1alpha1.EnvironmentStatus{
			Name:            envName,
			Phase:           ps.Status.State,
			PRURL:           ps.Status.PRURL,
			HealthCheckedAt: prev.HealthCheckedAt, // preserve if already set
			SoakMinutes:     prev.SoakMinutes,     // will be updated below if HealthCheckedAt is set
		}

		// Use the prURL from outputs if available (more reliable than status.PRURL).
		if prURL, ok := ps.Status.Outputs["prURL"]; ok && prURL != "" {
			updated.PRURL = prURL
		}

		// Set HealthCheckedAt when step reaches Verified state.
		// time.Now() is used here inside a CRD status write — Graph-first compliant.
		if ps.Status.State == "Verified" && prev.HealthCheckedAt == nil {
			now := metav1.NewTime(time.Now().UTC())
			updated.HealthCheckedAt = &now
		}

		// Update SoakMinutes if HealthCheckedAt is set.
		// This is the PG-3 fix: soakMinutes is now a CRD field written by the
		// BundleReconciler (owns Bundle status), so the PolicyGate reconciler can
		// read it without calling time.Since() in its hot path.
		// time.Now() here is inside a CRD status write — Graph-first compliant.
		if updated.HealthCheckedAt != nil {
			elapsed := time.Now().UTC().Sub(updated.HealthCheckedAt.UTC())
			if elapsed > 0 {
				updated.SoakMinutes = int64(elapsed.Minutes())
			}
		}

		// Only mark changed if something actually differs.
		if prev.Phase != updated.Phase || prev.PRURL != updated.PRURL ||
			(updated.HealthCheckedAt != nil && prev.HealthCheckedAt == nil) ||
			updated.SoakMinutes != prev.SoakMinutes {
			envMap[envName] = updated
			changed = true
		}
	}

	if !changed {
		log.Debug().Msg("bundle evidence already up to date")
		return ctrl.Result{}, nil
	}

	// Rebuild the environments slice from the updated map.
	envs := make([]kardinalv1alpha1.EnvironmentStatus, 0, len(envMap))
	for _, env := range envMap {
		envs = append(envs, env)
	}

	// K-05: Compute deployment metrics when all environments are Verified.
	// Only runs once (when metrics is nil and all envs just reached Verified).
	if b.Status.Metrics == nil {
		metrics := computeBundleMetrics(b, envs)
		if metrics != nil {
			b.Status.Metrics = metrics
		}
	}

	patch := client.MergeFrom(b.DeepCopy())
	b.Status.Environments = envs
	if err := r.Status().Patch(ctx, b, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch bundle evidence: %w", err)
	}

	log.Info().Int("environments", len(envs)).Msg("bundle evidence synced from PromotionStep status")
	return ctrl.Result{}, nil
}

// computeBundleMetrics derives deployment efficiency metrics from a Bundle's
// environments slice. Returns nil if not all environments are Verified yet.
//
// K-05: commitToProductionMinutes is the elapsed time from Bundle creation to the
// last environment reaching HealthCheckedAt (the final verification timestamp).
// Graph-first: reads from CRD status only; time.Now() is used only to write the
// Metrics field as a CRD status update.
func computeBundleMetrics(b *kardinalv1alpha1.Bundle, envs []kardinalv1alpha1.EnvironmentStatus) *kardinalv1alpha1.BundleMetrics {
	// Only compute when all environments have reached Verified (have HealthCheckedAt set).
	var latestHealthCheck *time.Time
	for i := range envs {
		if envs[i].HealthCheckedAt == nil {
			return nil // not all verified yet
		}
		t := envs[i].HealthCheckedAt.Time
		if latestHealthCheck == nil || t.After(*latestHealthCheck) {
			latestHealthCheck = &t
		}
	}
	if latestHealthCheck == nil {
		return nil
	}

	created := b.CreationTimestamp.Time
	if created.IsZero() {
		return nil
	}

	elapsed := latestHealthCheck.Sub(created)
	if elapsed < 0 {
		elapsed = -elapsed
	}

	return &kardinalv1alpha1.BundleMetrics{
		CommitToProductionMinutes: int64(elapsed.Minutes()),
	}
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
//
// It adds Watches for:
//   - PromotionStep changes: when a PromotionStep state changes, re-queue the Bundle
//     to sync evidence. Replaces the old cross-CRD copyEvidenceToBundle.
//   - Graph deletion: when the kro Graph backing a Bundle is deleted externally,
//     re-queue the Bundle so it can recreate the Graph (#490).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.Add(r); err != nil {
		return fmt.Errorf("add reconciler as runnable: %w", err)
	}

	// promotionStepMapper maps a PromotionStep change event to a Bundle reconcile request.
	// It reads the kardinal.io/bundle label set by the Graph builder on PromotionStep nodes.
	promotionStepMapper := func(ctx context.Context, obj client.Object) []reconcile.Request {
		bundleName := obj.GetLabels()["kardinal.io/bundle"]
		if bundleName == "" {
			return nil
		}
		return []reconcile.Request{
			{NamespacedName: client.ObjectKey{
				Name:      bundleName,
				Namespace: obj.GetNamespace(),
			}},
		}
	}

	// graphMapper maps a kro Graph change event to a Bundle reconcile request.
	// When a Graph is deleted externally (kubectl delete graph <name>), the owning
	// Bundle is re-queued so ensureGraphExists can recreate it (#490).
	// Reads the kardinal.io/bundle label set by the Graph builder.
	graphMapper := func(ctx context.Context, obj client.Object) []reconcile.Request {
		bundleName := obj.GetLabels()["kardinal.io/bundle"]
		if bundleName == "" {
			return nil
		}
		return []reconcile.Request{
			{NamespacedName: client.ObjectKey{
				Name:      bundleName,
				Namespace: obj.GetNamespace(),
			}},
		}
	}

	// graphObject is an unstructured placeholder for the kro Graph CRD.
	// We use unstructured to avoid a compile-time dependency on the kro module.
	graphObject := &unstructured.Unstructured{}
	graphObject.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "experimental.kro.run",
		Version: "v1alpha1",
		Kind:    "Graph",
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&kardinalv1alpha1.Bundle{}).
		// Watch PromotionSteps: evidence sync when PS state changes.
		Watches(&kardinalv1alpha1.PromotionStep{}, handler.EnqueueRequestsFromMapFunc(promotionStepMapper)).
		// Watch Graphs: recreate when Graph is deleted externally (#490).
		Watches(graphObject, handler.EnqueueRequestsFromMapFunc(graphMapper)).
		Complete(r)
}
