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

// Package bundle implements the BundleReconciler. It owns Bundle.status: it
// sets status.phase = Available on new Bundles, validates the Pipeline and
// triggers the Pipeline-to-Graph translation, supersedes a Bundle when a newer
// sibling of the same type exists (each Bundle supersedes itself, so there are
// no cross-CRD writes), syncs per-environment promotion evidence from
// PromotionStep status, and moves the phase to Verified or Failed from that
// evidence.
package bundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/kubeevent"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
)

// Bundle phases.
const (
	phaseAvailable  = "Available"
	phasePromoting  = "Promoting"
	phaseVerified   = "Verified"
	phaseFailed     = "Failed"
	phaseSuperseded = "Superseded"
)

// Bundle condition types.
const (
	condReady = "Ready"
	// condInvalidSpec is True when the Pipeline, the Bundle intent or the
	// PolicyGates cannot be built into a Graph. A changed Pipeline retries the
	// Bundle; for a Bundle that has a Graph, so does a successful recreate.
	condInvalidSpec = "InvalidSpec"
	// condFailed is True while a PromotionStep has failed or kro rejected the Graph.
	condFailed = "Failed"
	// condGraphAccepted and condGraphReady mirror the Graph's Accepted and
	// Ready conditions, so `kubectl describe bundle` shows why kro rejected or
	// is still applying the Graph.
	condGraphAccepted = "GraphAccepted"
	condGraphReady    = "GraphReady"
	// condGraphSynced is False while re-translating or recreating the Graph
	// fails, or (reason GraphDeleted) while the Graph of a Bundle that failed
	// promoting is missing. It is only written once a sync has failed.
	condGraphSynced = "GraphSynced"
)

// errGraphDeletedAfterFailure is returned by syncGraph when the Graph of a
// Bundle that failed promoting is missing. It is not recreated: recreating it
// would promote the failed artifacts again from the first environment.
var errGraphDeletedAfterFailure = errors.New("the Graph of this failed Bundle was deleted; it is not recreated, " +
	"so the failed promotion does not run again. Create a new Bundle, or change the Pipeline to retry this one")

// indexPipeline is the Bundle field index on spec.pipeline.
const indexPipeline = "spec.pipeline"

// requeueSlow is the retry interval for conditions that need a change
// elsewhere (a missing Pipeline, a full maxConcurrentPromotions slot, a failing
// Graph sync). The watches in SetupWithManager usually act sooner.
const requeueSlow = 30 * time.Second

// BundleTranslator is the interface the BundleReconciler uses to translate a
// Bundle+Pipeline into a kro Graph. Abstracted as an interface for testability.
type BundleTranslator interface {
	Translate(ctx context.Context, pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle) (string, error)
}

// GraphChecker checks whether a kro Graph exists by name in a given namespace.
// When the value also implements Get(ctx, namespace, name) (*graph.Graph, error),
// as *graph.GraphClient does, the reconciler mirrors the Graph's conditions.
type GraphChecker interface {
	GraphExists(ctx context.Context, namespace, name string) (bool, error)
}

// graphReader reads a Graph with its status.
type graphReader interface {
	Get(ctx context.Context, namespace, name string) (*graph.Graph, error)
}

// Reconciler watches Bundle objects, sets Available phase, triggers translation,
// manages Bundle supersession, syncs evidence from PromotionStep status into
// Bundle.status.environments, and derives the Verified and Failed phases.
type Reconciler struct {
	client.Client
	// Translator creates the kro Graph for a Bundle+Pipeline pair.
	// May be nil in test environments where translation is not needed.
	Translator BundleTranslator
	// GraphChecker detects whether the Graph CR still exists.
	// When nil, graph recreation is skipped (backward-compatible).
	GraphChecker GraphChecker
	// Recorder emits events.k8s.io/v1 Events for Bundle phase transitions.
	// When nil, event emission is skipped (backward-compatible).
	Recorder events.EventRecorder
}

// Reconcile is called whenever a Bundle is created or updated, and whenever a
// PromotionStep, Graph, Pipeline or sibling Bundle event maps to it (see
// SetupWithManager).
//
// State machine:
//   - "" (new): set Available.
//   - Available: supersede itself if a newer same-type Bundle is in flight;
//     otherwise validate the Pipeline, wait for a maxConcurrentPromotions slot,
//     translate, and move to Promoting. An invalid Pipeline or intent moves to
//     Failed; a translate error stays Available and is retried.
//   - Promoting: supersede itself if a newer same-type Bundle is in flight;
//     keep the Graph current; sync evidence; move to Failed when a step fails
//     or kro rejects the Graph, and to Verified when every environment the
//     Graph promotes is Verified.
//   - Failed: move back to Promoting when nothing is failing any more (a
//     retried step, an accepted Graph), or back to Available when a Pipeline
//     that failed validation is changed. A newer sibling supersedes it instead.
//   - Verified, Superseded: settled; only the evidence is synced.
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
	case phaseAvailable:
		if superseded, err := r.hasNewerSibling(ctx, &b, false); err != nil {
			log.Warn().Err(err).Msg("failed to check for newer bundle (non-fatal)")
		} else if superseded {
			return r.markSuperseded(ctx, log, &b)
		}
		return r.handleAvailable(ctx, log, &b)
	default:
		return r.handleBound(ctx, log, &b)
	}
}

// handleBound reconciles a Bundle past Available: Promoting, Failed, Verified
// or Superseded.
func (r *Reconciler) handleBound(ctx context.Context, log zerolog.Logger,
	b *kardinalv1alpha1.Bundle) (ctrl.Result, error) {
	active := b.Status.Phase != phaseVerified && b.Status.Phase != phaseSuperseded

	// A newer same-type Bundle that started while this one was in flight
	// supersedes it (#281).
	if b.Status.Phase == phasePromoting {
		if superseded, err := r.hasNewerSibling(ctx, b, false); err != nil {
			log.Warn().Err(err).Msg("failed to check for newer bundle during Promoting (non-fatal)")
		} else if superseded {
			return r.markSuperseded(ctx, log, b)
		}
	}

	var pipeline *kardinalv1alpha1.Pipeline
	var pl kardinalv1alpha1.Pipeline
	if err := r.Get(ctx, client.ObjectKey{Name: b.Spec.Pipeline, Namespace: b.Namespace}, &pl); err != nil {
		switch {
		case !apierrors.IsNotFound(err):
			// Transient error: still sync evidence on a best-effort basis.
			log.Warn().Err(err).Msg("failed to read parent pipeline (non-fatal), syncing evidence only")
		case b.Status.GraphRef != "" || b.Status.PipelineSpecHash != "":
			// The Pipeline this Bundle was promoted with has been deleted.
			// Delete this Bundle so it is not orphaned (#270). Graph-first:
			// only our own object is deleted.
			log.Info().Str("pipeline", b.Spec.Pipeline).Str("phase", b.Status.Phase).
				Msg("parent pipeline deleted — self-deleting orphaned Bundle")
			if delErr := r.Delete(ctx, b); delErr != nil && !apierrors.IsNotFound(delErr) {
				return ctrl.Result{}, fmt.Errorf("delete orphaned bundle: %w", delErr)
			}
			return ctrl.Result{}, nil
		case active:
			return r.markPipelineNotFound(ctx, log, b)
		}
	} else {
		pipeline = &pl
	}

	// A Bundle that failed validation before any Graph was created is retried
	// only when the Pipeline changes.
	if b.Status.Phase == phaseFailed && b.Status.GraphRef == "" {
		return r.retryIfPipelineChanged(ctx, log, b, pipeline)
	}

	before := b.DeepCopy()
	var syncErr error
	switch {
	case active && pipeline != nil:
		syncErr = r.syncGraph(ctx, log, b, pipeline)
	case b.Status.Phase == phaseVerified:
		r.refreshGraphConditions(ctx, log, b)
	}
	return r.handleSyncEvidence(ctx, log, before, b, pipeline, syncErr)
}

// refreshGraphConditions mirrors the Graph conditions of a Verified Bundle
// until GraphReady is True (E2E-R03). The Bundle turns Verified on the last
// PromotionStep event, but kro re-evaluates readyWhen on a backoff requeue and
// sets Graph Ready later; the Graph watch then lands here. The Graph is only
// read: a Verified Bundle's Graph is never re-translated or recreated. Once
// GraphReady is True it is not read again. A Superseded Bundle's Graph never
// converges (its steps are Failed), so it is not refreshed.
func (r *Reconciler) refreshGraphConditions(ctx context.Context, log zerolog.Logger, b *kardinalv1alpha1.Bundle) {
	if r.GraphChecker == nil || meta.IsStatusConditionTrue(b.Status.Conditions, condGraphReady) {
		return
	}
	name := b.Status.GraphRef
	if name == "" {
		name = graph.GraphNameFrom(b.Spec.Pipeline, b.Name)
	}
	g, _, err := r.readGraph(ctx, b.Namespace, name)
	if err != nil {
		log.Warn().Err(err).Str("graph", name).Msg("failed to read graph of verified bundle (non-fatal)")
		return
	}
	if g != nil {
		mirrorGraphConditions(b, g)
	}
}

// syncGraph keeps the Graph of an active Bundle current. It re-translates the
// Graph in place when the Pipeline spec changed (#626), recreates a Graph that
// was deleted externally (#490), and mirrors the Graph's Accepted and Ready
// conditions into the Bundle. It only changes b in memory; the caller patches.
// The returned error is a failed re-translate or recreate; it wraps
// graph.ErrInvalid when the Graph cannot be built (see handleSyncEvidence).
// A successful re-translate or recreate clears InvalidSpec.
//
// The Graph of a Bundle that failed promoting (failedPromoting) is not
// recreated: errGraphDeletedAfterFailure is returned instead. Only a Pipeline
// change (the re-translate above) rebuilds it.
func (r *Reconciler) syncGraph(ctx context.Context, log zerolog.Logger,
	b *kardinalv1alpha1.Bundle, pipeline *kardinalv1alpha1.Pipeline) error {
	if r.Translator == nil || r.GraphChecker == nil {
		return nil // no-op in test environments without a real translator
	}
	if err := r.ensurePipelineSpecCurrent(ctx, log, b, pipeline); err != nil {
		return err
	}

	name := b.Status.GraphRef
	if name == "" {
		name = graph.GraphNameFrom(b.Spec.Pipeline, b.Name)
	}
	g, exists, err := r.readGraph(ctx, b.Namespace, name)
	if err != nil {
		// Non-fatal: a failed read must not block evidence sync.
		log.Warn().Err(err).Str("graph", name).Msg("failed to read graph (non-fatal)")
		return nil
	}
	if !exists {
		if failedPromoting(b) {
			log.Info().Str("graph", name).Msg("graph of a failed bundle deleted — not recreating")
			return errGraphDeletedAfterFailure
		}
		log.Info().Str("graph", name).Msg("graph deleted externally — recreating")
		graphName, tErr := r.Translator.Translate(ctx, pipeline, b)
		if tErr != nil {
			return fmt.Errorf("recreate graph %s: %w", name, tErr)
		}
		b.Status.GraphRef = graphName
		meta.RemoveStatusCondition(&b.Status.Conditions, condInvalidSpec)
		log.Info().Str("graph", graphName).Msg("graph recreated after external deletion")
		return nil
	}
	if g != nil {
		mirrorGraphConditions(b, g)
	}
	return nil
}

// failedPromoting reports whether b is Failed because a step failed or kro
// rejected its Graph (or failed before the Failed condition existed), rather
// than only because its Graph could not be built (InvalidSpec). Re-running such
// a Bundle from scratch would promote the failed artifacts again.
func failedPromoting(b *kardinalv1alpha1.Bundle) bool {
	return b.Status.Phase == phaseFailed &&
		(meta.IsStatusConditionTrue(b.Status.Conditions, condFailed) ||
			!meta.IsStatusConditionTrue(b.Status.Conditions, condInvalidSpec))
}

// readGraph returns the Graph when the GraphChecker can read it, or only
// whether it exists otherwise.
func (r *Reconciler) readGraph(ctx context.Context, ns, name string) (*graph.Graph, bool, error) {
	if gr, ok := r.GraphChecker.(graphReader); ok {
		g, err := gr.Get(ctx, ns, name)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil, false, nil
			}
			return nil, false, err
		}
		return g, true, nil
	}
	exists, err := r.GraphChecker.GraphExists(ctx, ns, name)
	return nil, exists, err
}

// mirrorGraphConditions copies the Graph's Accepted and Ready conditions into
// the Bundle as GraphAccepted and GraphReady. A condition kro wrote for an
// older Graph generation is skipped, so a re-translated Graph is judged only
// once kro has evaluated the new spec.
func mirrorGraphConditions(b *kardinalv1alpha1.Bundle, g *graph.Graph) {
	for _, m := range []struct{ from, to string }{
		{from: "Accepted", to: condGraphAccepted},
		{from: "Ready", to: condGraphReady},
	} {
		c := meta.FindStatusCondition(g.Status.Conditions, m.from)
		if c == nil || (c.ObservedGeneration != 0 && c.ObservedGeneration < g.Generation) {
			continue
		}
		reason := c.Reason
		if reason == "" {
			reason = "Unknown"
		}
		setBundleCondition(b, m.to, c.Status, reason, c.Message)
	}
}

// graphRejected returns the mirrored GraphAccepted condition when kro rejected
// the Graph, or nil.
func graphRejected(b *kardinalv1alpha1.Bundle) *metav1.Condition {
	c := meta.FindStatusCondition(b.Status.Conditions, condGraphAccepted)
	if c != nil && c.Status == metav1.ConditionFalse {
		return c
	}
	return nil
}

// ensurePipelineSpecCurrent detects when a Pipeline spec has changed since the
// Graph was last built, and re-translates the Graph in place.
//
// This fixes issue #626: Pipeline spec changes (new environments, changed
// policyNamespaces, updated git config) were invisible to in-flight Bundles
// because the Graph spec is set when it is created.
//
// Mechanism:
//  1. Hash the current Pipeline spec (spec.paused excluded, see pipelineSpecHashFor).
//  2. Compare to Bundle.status.pipelineSpecHash (set when the Graph was created).
//  3. If different: re-run the translator, which updates the existing Graph's
//     spec (create-or-update). kro applies the new nodes and prunes only the
//     removed ones, so environments that are already Verified are not promoted
//     again. Deleting the Graph instead would delete every PromotionStep with
//     it (ledger G6).
//  4. Store the new hash in b (the caller patches it).
//
// On a transient translate error the stored hash is kept, so the update is
// retried. On a graph.ErrInvalid error handleSyncEvidence stores the new hash
// with the Bundle Failed, so only the next Pipeline change retries.
func (r *Reconciler) ensurePipelineSpecCurrent(ctx context.Context, log zerolog.Logger, b *kardinalv1alpha1.Bundle,
	pipeline *kardinalv1alpha1.Pipeline) error {
	currentHash := pipelineSpecHashFor(pipeline)
	if currentHash == "" {
		return nil // hash computation failed — skip to avoid a spurious update
	}
	if b.Status.PipelineSpecHash == "" {
		// Not initialised (Bundle promoted before #634): an empty stored hash
		// means "unknown", not "changed". Record the current one.
		b.Status.PipelineSpecHash = currentHash
		return nil
	}
	if currentHash == b.Status.PipelineSpecHash {
		return nil
	}
	log.Info().
		Str("graph", b.Status.GraphRef).
		Str("oldHash", b.Status.PipelineSpecHash).
		Str("newHash", currentHash).
		Msg("pipeline spec changed — updating Graph in place")
	if _, err := r.Translator.Translate(ctx, pipeline, b); err != nil {
		return fmt.Errorf("update graph for changed pipeline spec: %w", err)
	}
	b.Status.PipelineSpecHash = currentHash
	meta.RemoveStatusCondition(&b.Status.Conditions, condInvalidSpec)
	return nil
}

// defaultHistoryLimit is the number of completed Bundle promotions to retain
// when Pipeline.spec.historyLimit is unset or zero.
const defaultHistoryLimit = 50

// handleNew sets the phase to Available on a newly-created Bundle.
// Supersession of older bundles is not done here (BU-1 fix). Each bundle
// is responsible for superseding itself when it detects a newer bundle exists.
//
// History GC is enforced here at the natural write boundary: when a new Bundle
// is created, enforce historyLimit on terminal siblings (Verified/Failed/Superseded)
// for the same pipeline. Oldest-first deletion. See spec #910.
func (r *Reconciler) handleNew(ctx context.Context, log zerolog.Logger,
	b *kardinalv1alpha1.Bundle) (ctrl.Result, error) {
	if b.Spec.Pipeline != "" {
		var pipeline kardinalv1alpha1.Pipeline
		if err := r.Get(ctx, client.ObjectKey{Name: b.Spec.Pipeline, Namespace: b.Namespace}, &pipeline); err != nil {
			if !apierrors.IsNotFound(err) {
				// Non-fatal: GC failure should not block promotion.
				log.Warn().Err(err).Msg("history GC: failed to get pipeline (non-fatal)")
			}
		} else if gcErr := r.enforceHistoryLimit(ctx, log, &pipeline, b.Namespace); gcErr != nil {
			log.Warn().Err(gcErr).Msg("history GC: enforce failed (non-fatal)")
		}
	}

	patch := client.MergeFrom(b.DeepCopy())
	b.Status.Phase = phaseAvailable
	// Ready=False/Available lets operators observe the phase via kubectl wait
	// and lets GitOps controllers gate on standard conditions.
	setBundleCondition(b, condReady, metav1.ConditionFalse, "Available", "bundle received; awaiting promotion")
	if err := r.Status().Patch(ctx, b, patch); err != nil {
		if apierrors.IsNotFound(err) {
			log.Debug().Msg("bundle deleted before Available patch — ignoring")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("patch bundle status Available: %w", err)
	}

	log.Info().
		Str("phase", phaseAvailable).
		Str("type", b.Spec.Type).
		Str("pipeline", b.Spec.Pipeline).
		Msg("bundle phase set to Available")
	r.event(b, corev1.EventTypeNormal, "Available",
		fmt.Sprintf("bundle received for pipeline %s; awaiting promotion", b.Spec.Pipeline))

	// 500ms is the minimum safe floor: avoids a hot loop that bypasses
	// controller-runtime rate limiting under concurrent Bundle load
	// (design doc 15-production-readiness.md).
	return ctrl.Result{RequeueAfter: 500 * time.Millisecond}, nil
}

// enforceHistoryLimit deletes the oldest terminal Bundles (Verified/Failed/Superseded)
// for the given pipeline in the given namespace, keeping at most historyLimit bundles.
//
// This implements Pipeline.spec.historyLimit enforcement (spec #910). Non-terminal
// Bundles (Available, Promoting) are never deleted by this function.
//
// Ordering: oldest-first by creation (lifecycle.CompareCreation).
// Default limit: defaultHistoryLimit (50) when spec.historyLimit is unset or zero.
//
// Graph-first: we only delete Bundles (our own CRD). No cross-CRD writes.
// Idempotent: deleting N-limit bundles twice yields the same result as once.
func (r *Reconciler) enforceHistoryLimit(ctx context.Context, log zerolog.Logger,
	pipeline *kardinalv1alpha1.Pipeline, namespace string) error {
	limit := pipeline.Spec.HistoryLimit
	if limit <= 0 {
		limit = defaultHistoryLimit
	}

	var allBundles kardinalv1alpha1.BundleList
	if err := r.List(ctx, &allBundles,
		client.InNamespace(namespace),
		client.MatchingFields{indexPipeline: pipeline.Name},
	); err != nil {
		return fmt.Errorf("enforceHistoryLimit: list bundles: %w", err)
	}

	terminal := make([]*kardinalv1alpha1.Bundle, 0, len(allBundles.Items))
	for i := range allBundles.Items {
		switch allBundles.Items[i].Status.Phase {
		case phaseVerified, phaseFailed, phaseSuperseded:
			terminal = append(terminal, &allBundles.Items[i])
		}
	}
	if len(terminal) <= limit {
		return nil
	}

	slices.SortFunc(terminal, lifecycle.CompareCreation)
	excess := len(terminal) - limit
	for _, b := range terminal[:excess] {
		log.Info().
			Str("bundle", b.Name).
			Str("phase", b.Status.Phase).
			Str("pipeline", pipeline.Name).
			Int("historyLimit", limit).
			Msg("history GC: deleting terminal bundle beyond historyLimit")
		if err := r.Delete(ctx, b); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("enforceHistoryLimit: delete bundle %s: %w", b.Name, err)
		}
	}
	log.Info().
		Str("pipeline", pipeline.Name).
		Int("deleted", excess).
		Int("remaining", limit).
		Msg("history GC: complete")
	return nil
}

// hasNewerSibling reports whether the pipeline has a Bundle of the same type
// created after b (lifecycle.CompareCreation) that is still in flight: new,
// Available or Promoting. With countVerified, a Verified sibling counts too;
// a Failed Bundle uses that before it recovers, so it never overtakes a newer
// Bundle that already finished.
//
// This is self-supersession: each Bundle checks whether it should yield to a
// newer sibling and writes only its own status (BU-1 / BU-4, no cross-CRD
// mutations).
func (r *Reconciler) hasNewerSibling(ctx context.Context, b *kardinalv1alpha1.Bundle, countVerified bool) (bool, error) {
	siblings, err := r.pipelineBundleList(ctx, b.Namespace, b.Spec.Pipeline)
	if err != nil {
		return false, fmt.Errorf("list bundles for supersession check: %w", err)
	}
	for i := range siblings {
		s := &siblings[i]
		if s.Name == b.Name || s.Spec.Type != b.Spec.Type {
			continue // image bundles are only superseded by image bundles, etc.
		}
		switch s.Status.Phase {
		case phaseSuperseded, phaseFailed:
			continue
		case phaseVerified:
			if !countVerified {
				continue
			}
		}
		if lifecycle.CompareCreation(s, b) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// pipelineBundleList lists the Bundles of a pipeline through the spec.pipeline index.
func (r *Reconciler) pipelineBundleList(ctx context.Context, ns, pipeline string) ([]kardinalv1alpha1.Bundle, error) {
	if pipeline == "" {
		return nil, nil
	}
	var list kardinalv1alpha1.BundleList
	if err := r.List(ctx, &list, client.InNamespace(ns), client.MatchingFields{indexPipeline: pipeline}); err != nil {
		return nil, fmt.Errorf("list bundles of pipeline %s: %w", pipeline, err)
	}
	return list.Items, nil
}

// markSuperseded sets this bundle's status.phase to "Superseded" (self-supersession).
func (r *Reconciler) markSuperseded(ctx context.Context, log zerolog.Logger,
	b *kardinalv1alpha1.Bundle) (ctrl.Result, error) {
	patch := client.MergeFrom(b.DeepCopy())
	supersede(b)
	if err := r.Status().Patch(ctx, b, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch bundle status Superseded: %w", err)
	}
	log.Info().
		Str("pipeline", b.Spec.Pipeline).
		Str("type", b.Spec.Type).
		Msg("bundle superseded by newer bundle (self-supersession)")
	r.superseded(b)
	return ctrl.Result{}, nil
}

func supersede(b *kardinalv1alpha1.Bundle) {
	b.Status.Phase = phaseSuperseded
	setBundleCondition(b, condReady, metav1.ConditionFalse, "Superseded",
		"superseded by a newer bundle for the same pipeline and type")
}

func (r *Reconciler) superseded(b *kardinalv1alpha1.Bundle) {
	r.event(b, corev1.EventTypeNormal, "Superseded",
		fmt.Sprintf("superseded by newer bundle for pipeline %s", b.Spec.Pipeline))
	observability.BundlesTotal.WithLabelValues(phaseSuperseded).Inc()
}

// markPipelineNotFound records that the Bundle's Pipeline does not exist.
// The Bundle is kept: the Pipeline may be applied after the Bundle, or
// spec.pipeline may be a typo the caller has to see. Creating the Pipeline
// re-queues the Bundle through the Pipeline watch.
func (r *Reconciler) markPipelineNotFound(ctx context.Context, log zerolog.Logger,
	b *kardinalv1alpha1.Bundle) (ctrl.Result, error) {
	patch := client.MergeFrom(b.DeepCopy())
	msg := fmt.Sprintf("pipeline %q not found in namespace %s; create it or fix spec.pipeline", b.Spec.Pipeline, b.Namespace)
	if setBundleCondition(b, condReady, metav1.ConditionFalse, "PipelineNotFound", msg) {
		if err := r.Status().Patch(ctx, b, patch); err != nil {
			if apierrors.IsNotFound(err) {
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, fmt.Errorf("patch bundle status PipelineNotFound: %w", err)
		}
		log.Warn().Str("pipeline", b.Spec.Pipeline).Msg("pipeline not found — bundle waiting")
		r.event(b, corev1.EventTypeWarning, "PipelineNotFound", msg)
	}
	return ctrl.Result{RequeueAfter: requeueSlow}, nil
}

// handleAvailable validates the Pipeline, waits for a maxConcurrentPromotions
// slot, triggers Graph creation and advances the phase to Promoting.
func (r *Reconciler) handleAvailable(ctx context.Context, log zerolog.Logger,
	b *kardinalv1alpha1.Bundle) (ctrl.Result, error) {
	var pipeline kardinalv1alpha1.Pipeline
	if err := r.Get(ctx, client.ObjectKey{Name: b.Spec.Pipeline, Namespace: b.Namespace}, &pipeline); err != nil {
		if apierrors.IsNotFound(err) {
			return r.markPipelineNotFound(ctx, log, b)
		}
		return ctrl.Result{}, fmt.Errorf("get pipeline %s: %w", b.Spec.Pipeline, err)
	}

	if r.Translator == nil {
		log.Debug().Msg("translator not configured, skipping graph creation")
		return ctrl.Result{}, nil
	}

	// Validate first: a Pipeline or intent the Graph cannot be built from is a
	// permanent error until someone changes it, so fail with the reason.
	if _, err := graph.PromotedEnvironments(&pipeline, b); err != nil {
		return r.markInvalid(ctx, log, b, &pipeline, err)
	}

	// maxConcurrentPromotions: a Bundle leaves Promoting when it is Verified,
	// Failed or Superseded, and that phase change re-queues the waiting
	// siblings (see waitingSiblings). Reads only Bundle status.
	if limit := pipeline.Spec.MaxConcurrentPromotions; limit > 0 {
		active, err := r.countPromoting(ctx, b)
		if err != nil {
			// A failed read is not a free slot: retry instead of promoting past the cap.
			return ctrl.Result{}, fmt.Errorf("maxConcurrentPromotions: count promoting bundles: %w", err)
		}
		if active >= limit {
			log.Info().Int("active", active).Int("limit", limit).Str("pipeline", b.Spec.Pipeline).
				Msg("maxConcurrentPromotions reached — Available bundle waiting")
			patch := client.MergeFrom(b.DeepCopy())
			if setBundleCondition(b, condReady, metav1.ConditionFalse, "WaitingForSlot",
				fmt.Sprintf("maxConcurrentPromotions (%d) reached; waiting for a promoting bundle to finish", limit)) {
				if pErr := r.Status().Patch(ctx, b, patch); pErr != nil && !apierrors.IsNotFound(pErr) {
					log.Warn().Err(pErr).Msg("failed to record WaitingForSlot (non-fatal)")
				}
			}
			return ctrl.Result{RequeueAfter: requeueSlow}, nil
		}
	}

	// Pausing is enforced by the PromotionStep reconciler, which holds steps
	// while the Pipeline's freeze gate exists (see pkg/lifecycle/pause.go), so
	// a paused Pipeline still gets its Graph and resumes where it stopped.
	graphName, err := r.Translator.Translate(ctx, &pipeline, b)
	if errors.Is(err, graph.ErrInvalid) {
		// The Graph cannot be built from this Pipeline, Bundle and gates (a
		// denied skip, custom steps, an invalid name or node ID). A retry
		// fails the same way, so fail the Bundle with the reason.
		return r.markInvalid(ctx, log, b, &pipeline, err)
	}
	if err != nil {
		// An API or RBAC error (timeout, conflict, missing permission). Stay
		// Available and retry with backoff; the condition shows the error.
		log.Error().Err(err).Msg("failed to translate bundle to graph")
		patch := client.MergeFrom(b.DeepCopy())
		if setBundleCondition(b, condReady, metav1.ConditionFalse, "TranslationError",
			fmt.Sprintf("graph creation failed, retrying: %v", err)) {
			if pErr := r.Status().Patch(ctx, b, patch); pErr != nil && !apierrors.IsNotFound(pErr) {
				log.Warn().Err(pErr).Msg("failed to record TranslationError (non-fatal)")
			}
			r.event(b, corev1.EventTypeWarning, "TranslationError",
				fmt.Sprintf("graph creation failed for pipeline %s, retrying: %v", b.Spec.Pipeline, err))
		}
		return ctrl.Result{}, fmt.Errorf("translate bundle %s: %w", b.Name, err)
	}

	patch := client.MergeFrom(b.DeepCopy())
	b.Status.Phase = phasePromoting
	b.Status.GraphRef = graphName                              // for recreation detection (#490)
	b.Status.PipelineSpecHash = pipelineSpecHashFor(&pipeline) // for change detection (#626)
	setBundleCondition(b, condReady, metav1.ConditionFalse, "Promoting", "promotion in progress")
	if err := r.Status().Patch(ctx, b, patch); err != nil {
		if apierrors.IsNotFound(err) {
			log.Debug().Msg("bundle deleted before Promoting patch — ignoring")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("patch bundle status Promoting: %w", err)
	}

	log.Info().Str("phase", phasePromoting).Str("graph", graphName).Msg("bundle advancing to Promoting")
	r.event(b, corev1.EventTypeNormal, "Promoting",
		fmt.Sprintf("graph %s created; promotion started for pipeline %s", graphName, b.Spec.Pipeline))
	observability.BundlesTotal.WithLabelValues(phasePromoting).Inc()
	return ctrl.Result{}, nil
}

// countPromoting counts the other Promoting Bundles of b's pipeline.
func (r *Reconciler) countPromoting(ctx context.Context, b *kardinalv1alpha1.Bundle) (int, error) {
	siblings, err := r.pipelineBundleList(ctx, b.Namespace, b.Spec.Pipeline)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range siblings {
		if siblings[i].Name != b.Name && siblings[i].Status.Phase == phasePromoting {
			n++
		}
	}
	return n, nil
}

// markInvalid fails a Bundle that has no Graph yet because its Pipeline,
// intent or gates cannot be built into one. The Pipeline spec hash is stored,
// so a changed Pipeline retries the Bundle (retryIfPipelineChanged).
func (r *Reconciler) markInvalid(ctx context.Context, log zerolog.Logger, b *kardinalv1alpha1.Bundle,
	pipeline *kardinalv1alpha1.Pipeline, cause error) (ctrl.Result, error) {
	patch := client.MergeFrom(b.DeepCopy())
	reason, msg, _ := setInvalid(b, pipeline, cause)
	if err := r.Status().Patch(ctx, b, patch); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("patch bundle status Failed: %w", err)
	}
	log.Warn().Str("reason", reason).Err(cause).Msg("bundle failed: pipeline cannot be built into a graph")
	r.event(b, corev1.EventTypeWarning, "Failed",
		fmt.Sprintf("promotion failed for pipeline %s: %s", b.Spec.Pipeline, msg))
	observability.BundlesTotal.WithLabelValues(phaseFailed).Inc()
	return ctrl.Result{}, nil
}

// setInvalid marks b Failed with InvalidSpec for cause, in memory, and stores
// the hash of the Pipeline spec that failed, so the next Pipeline change
// retries. The reason is CircularDependency or InvalidPipeline for a bad
// environment order, GraphBuildFailed for a Translate error wrapping
// graph.ErrInvalid, and InvalidIntent otherwise. changed reports whether the
// InvalidSpec condition changed.
//
// Graph-first: a pure mutation of the in-memory Bundle before a status patch.
func setInvalid(b *kardinalv1alpha1.Bundle, pipeline *kardinalv1alpha1.Pipeline,
	cause error) (reason, msg string, changed bool) {
	reason = "InvalidIntent"
	hint := "apply a corrected Pipeline to retry"
	if errors.Is(cause, graph.ErrInvalid) {
		reason = "GraphBuildFailed"
		hint = "fix the Pipeline, Bundle or PolicyGate it names; a Pipeline change retries this Bundle"
	}
	if cycleErr := graph.DetectCycle(pipeline); cycleErr != nil {
		reason = "InvalidPipeline"
		if strings.Contains(cycleErr.Error(), "circular dependency") {
			reason = "CircularDependency"
		}
	}
	msg = fmt.Sprintf("%v — %s", cause, hint)

	b.Status.Phase = phaseFailed
	b.Status.PipelineSpecHash = pipelineSpecHashFor(pipeline)
	setBundleCondition(b, condReady, metav1.ConditionFalse, "Failed", "promotion failed: "+msg)
	changed = setBundleCondition(b, condInvalidSpec, metav1.ConditionTrue, reason, msg)
	return reason, msg, changed
}

// retryIfPipelineChanged moves a Bundle that failed validation back to
// Available when the Pipeline spec differs from the one that failed. A Bundle
// failed before the hash was recorded (older releases) stays Failed, so an
// upgrade never re-promotes an old image. A newer sibling that is in flight or
// Verified supersedes the Bundle instead.
func (r *Reconciler) retryIfPipelineChanged(ctx context.Context, log zerolog.Logger,
	b *kardinalv1alpha1.Bundle, pipeline *kardinalv1alpha1.Pipeline) (ctrl.Result, error) {
	if pipeline == nil || b.Status.PipelineSpecHash == "" {
		return ctrl.Result{}, nil
	}
	if hash := pipelineSpecHashFor(pipeline); hash == "" || hash == b.Status.PipelineSpecHash {
		return ctrl.Result{}, nil
	}
	newer, err := r.hasNewerSibling(ctx, b, true)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("retry failed bundle: %w", err)
	}
	if newer {
		return r.markSuperseded(ctx, log, b)
	}

	patch := client.MergeFrom(b.DeepCopy())
	b.Status.Phase = phaseAvailable
	b.Status.PipelineSpecHash = ""
	meta.RemoveStatusCondition(&b.Status.Conditions, condInvalidSpec)
	meta.RemoveStatusCondition(&b.Status.Conditions, condFailed)
	setBundleCondition(b, condReady, metav1.ConditionFalse, "Available", "pipeline changed; retrying promotion")
	if err := r.Status().Patch(ctx, b, patch); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("patch bundle status Available (retry): %w", err)
	}
	log.Info().Msg("pipeline changed — retrying failed bundle")
	r.event(b, corev1.EventTypeNormal, "Retrying",
		fmt.Sprintf("pipeline %s changed; retrying promotion", b.Spec.Pipeline))
	return ctrl.Result{RequeueAfter: 500 * time.Millisecond}, nil
}

// handleSyncEvidence merges the state of the Bundle's PromotionSteps into
// Bundle.status.environments and, for an active Bundle, derives the phase:
//   - Promoting → Failed when an environment failed or kro rejected the Graph;
//   - Promoting → Verified when every environment the Graph promotes is Verified;
//   - Promoting or Failed → Failed with InvalidSpec when syncErr wraps
//     graph.ErrInvalid: the changed Pipeline or the gates cannot be built into
//     a Graph, so a retry fails the same way;
//   - Failed → Promoting when nothing is failing any more and the Graph builds
//     (Superseded instead when a newer sibling is in flight or Verified). A
//     Bundle that failed promoting recovers only when it still has steps and
//     its Graph: deleting the Graph deletes the steps, and their absence is
//     not a recovery.
//
// before is b as read at the start of the reconcile; the status is patched
// only when it changed, so an event with nothing new writes nothing.
//
// Graph-purity: this is the Bundle reconciler writing its own status from
// PromotionStep status (the replacement for PS-9 copyEvidenceToBundle).
func (r *Reconciler) handleSyncEvidence(ctx context.Context, log zerolog.Logger, before, b *kardinalv1alpha1.Bundle,
	pipeline *kardinalv1alpha1.Pipeline, syncErr error) (ctrl.Result, error) {
	var psList kardinalv1alpha1.PromotionStepList
	if err := r.List(ctx, &psList,
		client.InNamespace(b.Namespace),
		client.MatchingLabels{"kardinal.io/bundle": b.Name},
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("list promotion steps for bundle %s: %w", b.Name, err)
	}
	steps := psList.Items
	// time.Now() here is inside a CRD status write — Graph-first compliant.
	now := time.Now().UTC()
	b.Status.Environments = environmentStatuses(b, pipeline, steps, now)

	var after []func()
	invalidBuild := errors.Is(syncErr, graph.ErrInvalid)
	graphDeleted := errors.Is(syncErr, errGraphDeletedAfterFailure)
	switch {
	case graphDeleted:
		if setBundleCondition(b, condGraphSynced, metav1.ConditionFalse, "GraphDeleted", syncErr.Error()) {
			msg := syncErr.Error()
			after = append(after, func() {
				r.event(b, corev1.EventTypeWarning, "GraphDeleted", msg)
			})
		}
	case invalidBuild:
		wasFailed := before.Status.Phase == phaseFailed
		reason, msg, changed := setInvalid(b, pipeline, syncErr)
		setBundleCondition(b, condGraphSynced, metav1.ConditionFalse, "InvalidSpec", syncErr.Error())
		if changed {
			log.Warn().Str("reason", reason).Err(syncErr).Msg("bundle failed: graph cannot be built")
			after = append(after, func() {
				r.event(b, corev1.EventTypeWarning, "Failed",
					fmt.Sprintf("promotion failed for pipeline %s: %s", b.Spec.Pipeline, msg))
				if !wasFailed {
					observability.BundlesTotal.WithLabelValues(phaseFailed).Inc()
				}
			})
		}
	case syncErr != nil:
		if setBundleCondition(b, condGraphSynced, metav1.ConditionFalse, "UpdateFailed", syncErr.Error()) {
			msg := syncErr.Error()
			after = append(after, func() {
				r.event(b, corev1.EventTypeWarning, "GraphSyncFailed", msg)
			})
		}
	case meta.IsStatusConditionFalse(b.Status.Conditions, condGraphSynced) &&
		!meta.IsStatusConditionTrue(b.Status.Conditions, condInvalidSpec):
		// While InvalidSpec is True the Graph still has the spec before the
		// failed change, so it is not current.
		setBundleCondition(b, condGraphSynced, metav1.ConditionTrue, "Synced", "graph is current")
	}

	failedEnv := firstFailedEnvironment(b.Status.Environments, steps)
	rejected := graphRejected(b)
	switch b.Status.Phase {
	case phasePromoting:
		switch {
		case failedEnv != nil || rejected != nil:
			reason, msg := failureCause(failedEnv, rejected, steps)
			b.Status.Phase = phaseFailed
			setBundleCondition(b, condReady, metav1.ConditionFalse, "Failed", "promotion failed: "+msg)
			setBundleCondition(b, condFailed, metav1.ConditionTrue, reason, msg)
			after = append(after, func() {
				r.event(b, corev1.EventTypeWarning, "Failed",
					fmt.Sprintf("promotion failed for pipeline %s: %s", b.Spec.Pipeline, msg))
				observability.BundlesTotal.WithLabelValues(phaseFailed).Inc()
			})
		case pipeline != nil:
			expected, err := graph.PromotedEnvironments(pipeline, b)
			if err == nil && allVerified(b.Status.Environments, expected) {
				b.Status.Phase = phaseVerified
				if b.Status.Metrics == nil {
					b.Status.Metrics = computeBundleMetrics(b, expected, steps)
				}
				setBundleCondition(b, condReady, metav1.ConditionTrue, "Verified", "all environments verified")
				n := len(expected)
				after = append(after, func() {
					r.event(b, corev1.EventTypeNormal, "Verified",
						fmt.Sprintf("all %d environment(s) verified for pipeline %s", n, b.Spec.Pipeline))
					observability.BundlesTotal.WithLabelValues(phaseVerified).Inc()
				})
			}
		}
	case phaseFailed:
		stepsObserved := len(steps) > 0 || !failedPromoting(before)
		if failedEnv == nil && rejected == nil && !graphDeleted && stepsObserved &&
			!meta.IsStatusConditionTrue(b.Status.Conditions, condInvalidSpec) {
			newer, err := r.hasNewerSibling(ctx, b, true)
			switch {
			case err != nil:
				log.Warn().Err(err).Msg("failed to check for newer bundle before recovering (non-fatal)")
			case newer:
				supersede(b)
				after = append(after, func() { r.superseded(b) })
			default:
				b.Status.Phase = phasePromoting
				setBundleCondition(b, condReady, metav1.ConditionFalse, "Promoting", "promotion in progress")
				setBundleCondition(b, condFailed, metav1.ConditionFalse, "Recovered", "no environment is failing")
				after = append(after, func() {
					r.event(b, corev1.EventTypeNormal, "Recovered",
						fmt.Sprintf("no environment is failing any more; promotion resumed for pipeline %s", b.Spec.Pipeline))
				})
			}
		}
	}

	result := soakRequeue(b)
	if syncErr != nil && !invalidBuild && !graphDeleted {
		log.Error().Err(syncErr).Msg("graph sync failed — requeuing")
		result = ctrl.Result{RequeueAfter: requeueSlow}
	}
	if equality.Semantic.DeepEqual(before.Status, b.Status) {
		log.Debug().Msg("bundle status already up to date")
		return result, nil
	}
	if err := r.Status().Patch(ctx, b, client.MergeFrom(before)); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("patch bundle status: %w", err)
	}
	for _, f := range after {
		f()
	}
	log.Debug().Int("environments", len(b.Status.Environments)).Str("phase", b.Status.Phase).
		Msg("bundle status synced from PromotionSteps")
	return result, nil
}

// failureCause returns the Failed condition reason and message for a failed
// environment or a rejected Graph.
func failureCause(env *kardinalv1alpha1.EnvironmentStatus, rejected *metav1.Condition,
	steps []kardinalv1alpha1.PromotionStep) (string, string) {
	if env == nil {
		return "GraphRejected", fmt.Sprintf("kro rejected the Graph: %s", rejected.Message)
	}
	detail := ""
	for i := range steps {
		s := &steps[i]
		if s.Spec.Environment == env.Name && failedState(s.Status.State) && s.Status.Message != "" {
			detail = s.Status.Message
			break
		}
	}
	if detail == "" {
		detail = "step " + env.Phase
	}
	return "StepFailed", fmt.Sprintf("environment %s: %s", env.Name, detail)
}

// soakRequeue keeps status.environments[*].soakMinutes ticking while a
// Promoting Bundle has a Verified environment. Once the PromotionSteps settle
// nothing else re-triggers this reconcile, so a soak-based gate such as
// upstream.uat.soakMinutes >= 30 would never see the time pass.
func soakRequeue(b *kardinalv1alpha1.Bundle) ctrl.Result {
	if b.Status.Phase != phasePromoting {
		return ctrl.Result{}
	}
	for _, env := range b.Status.Environments {
		if env.HealthCheckedAt != nil {
			return ctrl.Result{RequeueAfter: time.Minute}
		}
	}
	return ctrl.Result{}
}

// pipelineSpecHashFor returns a stable SHA-256 hex hash of the given Pipeline
// spec, used to detect Pipeline spec changes that require a Graph update (#626).
// It covers only spec fields, so label, annotation and status writes do not
// count. spec.paused is excluded: pausing changes nothing in the Graph (the
// PromotionStep reconciler holds steps), so pause and resume must not
// re-translate every in-flight Graph.
func pipelineSpecHashFor(pipeline *kardinalv1alpha1.Pipeline) string {
	spec := pipeline.Spec
	spec.Paused = false
	raw, err := json.Marshal(spec)
	if err != nil {
		return "" // should never happen for a valid Pipeline object
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// setBundleCondition sets a condition on the Bundle with meta.SetStatusCondition,
// so LastTransitionTime changes only when the status changes (#C02-13). It
// reports whether anything changed.
//
// Graph-first: a pure mutation of the in-memory Bundle before a status patch.
func setBundleCondition(b *kardinalv1alpha1.Bundle, condType string, status metav1.ConditionStatus, reason, message string) bool {
	return meta.SetStatusCondition(&b.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: b.Generation,
	})
}

// eventActions maps each Bundle Event reason to the events.k8s.io/v1 action,
// which says what the controller did. The API requires an action.
var eventActions = map[string]string{
	"Available":        "Accept",
	"Superseded":       "Supersede",
	"PipelineNotFound": "ResolvePipeline",
	"TranslationError": "CreateGraph",
	"Promoting":        "Promote",
	"Failed":           "Promote",
	"Retrying":         "Retry",
	"GraphDeleted":     "SyncGraph",
	"GraphSyncFailed":  "SyncGraph",
	"Verified":         "Verify",
	"Recovered":        "Promote",
}

// event emits an Event when a Recorder is configured.
func (r *Reconciler) event(b *kardinalv1alpha1.Bundle, eventType, reason, message string) {
	action, ok := eventActions[reason]
	if !ok {
		action = "Reconcile"
	}
	kubeevent.Emit(r.Recorder, b, eventType, reason, action, message)
}

// failedState reports whether a PromotionStep state is a failure.
func failedState(state string) bool {
	return state == "Failed" || state == "AbortedByAlarm" || state == "RollingBack"
}

// environmentStatuses aggregates the Bundle's PromotionSteps into one entry
// per environment. An environment with several regions has one step per
// region: it is Verified only when every region is Verified, and a failed
// region fails it. Entries for environments without a current step are kept
// as evidence. The order is the Pipeline's promotion order, then by name, so
// the status does not change between reconciles.
func environmentStatuses(b *kardinalv1alpha1.Bundle, pipeline *kardinalv1alpha1.Pipeline,
	steps []kardinalv1alpha1.PromotionStep, now time.Time) []kardinalv1alpha1.EnvironmentStatus {
	regions := map[string]int{}
	var order []string
	if pipeline != nil {
		for _, e := range pipeline.Spec.Environments {
			if len(e.Regions) >= 2 {
				regions[e.Name] = len(e.Regions)
			}
		}
		order, _ = graph.PromotedEnvironments(pipeline, b)
	}

	byEnv := map[string][]*kardinalv1alpha1.PromotionStep{}
	for i := range steps {
		s := &steps[i]
		byEnv[s.Spec.Environment] = append(byEnv[s.Spec.Environment], s)
	}
	out := make(map[string]kardinalv1alpha1.EnvironmentStatus, len(b.Status.Environments)+len(byEnv))
	for _, e := range b.Status.Environments {
		out[e.Name] = e
	}
	for env, group := range byEnv {
		slices.SortFunc(group, func(x, y *kardinalv1alpha1.PromotionStep) int { return strings.Compare(x.Name, y.Name) })
		st := out[env]
		st.Name = env
		st.Phase = environmentPhase(group, max(regions[env], 1))
		for _, s := range group {
			if u := s.Status.Outputs["prURL"]; u != "" {
				st.PRURL = u
				break
			}
			if s.Status.PRURL != "" {
				st.PRURL = s.Status.PRURL
				break
			}
		}
		if st.Phase != phaseVerified {
			st.HealthCheckedAt = nil
			st.SoakMinutes = 0
			out[env] = st
			continue
		}
		if st.HealthCheckedAt == nil {
			var at time.Time
			for _, s := range group {
				t, ok := lifecycle.VerifiedTime(s)
				if !ok {
					t = now
				}
				if t.After(at) {
					at = t
				}
			}
			checked := metav1.NewTime(at.UTC().Truncate(time.Second))
			st.HealthCheckedAt = &checked
		}
		// PG-3: soakMinutes is a CRD field written here, so the PolicyGate
		// reconciler reads it instead of calling time.Since in its hot path.
		st.SoakMinutes = 0
		if elapsed := now.Sub(st.HealthCheckedAt.Time); elapsed > 0 {
			st.SoakMinutes = int64(elapsed.Minutes())
		}
		out[env] = st
	}

	envs := make([]kardinalv1alpha1.EnvironmentStatus, 0, len(out))
	for _, name := range order {
		if e, ok := out[name]; ok {
			envs = append(envs, e)
			delete(out, name)
		}
	}
	rest := make([]string, 0, len(out))
	for name := range out {
		rest = append(rest, name)
	}
	slices.Sort(rest)
	for _, name := range rest {
		envs = append(envs, out[name])
	}
	return envs
}

// environmentPhase is the phase of an environment promoted by the given steps,
// one per region: the first failure, else the first step still in progress,
// else Promoting while regions have no step yet, else Verified.
func environmentPhase(group []*kardinalv1alpha1.PromotionStep, regions int) string {
	for _, s := range group {
		if failedState(s.Status.State) {
			return s.Status.State
		}
	}
	for _, s := range group {
		switch s.Status.State {
		case phaseVerified:
		case "":
			return "Pending"
		default:
			return s.Status.State
		}
	}
	if len(group) < regions {
		return phasePromoting
	}
	return phaseVerified
}

// firstFailedEnvironment returns the first environment, in status order, that
// has a failing PromotionStep now. Evidence kept for an environment whose step
// is gone does not count.
func firstFailedEnvironment(envs []kardinalv1alpha1.EnvironmentStatus,
	steps []kardinalv1alpha1.PromotionStep) *kardinalv1alpha1.EnvironmentStatus {
	failing := map[string]bool{}
	for i := range steps {
		if failedState(steps[i].Status.State) {
			failing[steps[i].Spec.Environment] = true
		}
	}
	for i := range envs {
		if failing[envs[i].Name] {
			return &envs[i]
		}
	}
	return nil
}

// allVerified reports whether every expected environment is Verified.
func allVerified(envs []kardinalv1alpha1.EnvironmentStatus, expected []string) bool {
	if len(expected) == 0 {
		return false
	}
	phase := make(map[string]string, len(envs))
	for _, e := range envs {
		phase[e.Name] = e.Phase
	}
	for _, name := range expected {
		if phase[name] != phaseVerified {
			return false
		}
	}
	return true
}

// computeBundleMetrics derives the deployment metrics of a Bundle whose
// expected environments are all Verified.
//
// K-05: commitToProductionMinutes is the time from Bundle creation to the last
// expected environment reaching HealthCheckedAt. bakeResets sums the bake
// resets of the Bundle's PromotionSteps.
// Graph-first: reads CRD status only; the result is written to Bundle status.
func computeBundleMetrics(b *kardinalv1alpha1.Bundle, expected []string,
	steps []kardinalv1alpha1.PromotionStep) *kardinalv1alpha1.BundleMetrics {
	checked := make(map[string]time.Time, len(b.Status.Environments))
	for _, e := range b.Status.Environments {
		if e.HealthCheckedAt != nil {
			checked[e.Name] = e.HealthCheckedAt.Time
		}
	}
	var latest time.Time
	for _, name := range expected {
		if t := checked[name]; t.After(latest) {
			latest = t
		}
	}
	m := &kardinalv1alpha1.BundleMetrics{}
	if created := b.CreationTimestamp.Time; !created.IsZero() && latest.After(created) {
		m.CommitToProductionMinutes = int64(latest.Sub(created).Minutes())
	}
	for i := range steps {
		m.BakeResets += steps[i].Status.BakeResets
	}
	return m
}

// SetupWithManager registers the BundleReconciler with the controller-runtime
// Manager. Besides the Bundle itself it watches:
//   - sibling Bundles: when a Bundle is created or deleted, or its phase
//     changes, the same pipeline's Bundles that are new, Available or
//     Promoting are re-queued, so an older one supersedes itself at once and
//     one waiting for a maxConcurrentPromotions slot starts when a slot frees;
//   - PromotionSteps: evidence sync and the Verified/Failed phase;
//   - Graphs: recreation after an external delete (#490) and the mirrored
//     GraphAccepted/GraphReady conditions;
//   - Pipelines (spec changes, create, delete): all the pipeline's Bundles, so
//     their Graphs are updated (#626), a Bundle waiting for its Pipeline starts,
//     and a failed one is retried.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(),
		&kardinalv1alpha1.Bundle{}, indexPipeline, bundlePipelineIndex); err != nil {
		return fmt.Errorf("index Bundle by spec.pipeline: %w", err)
	}

	// Unstructured, so kardinal does not import the kro module.
	graphObject := &unstructured.Unstructured{}
	graphObject.SetGroupVersionKind(graph.GraphGVK)

	return ctrl.NewControllerManagedBy(mgr).
		For(&kardinalv1alpha1.Bundle{}).
		Watches(&kardinalv1alpha1.Bundle{}, handler.EnqueueRequestsFromMapFunc(r.waitingSiblings),
			builder.WithPredicates(bundlePhaseChanged)).
		Watches(&kardinalv1alpha1.PromotionStep{}, handler.EnqueueRequestsFromMapFunc(bundleLabelMapper)).
		Watches(graphObject, handler.EnqueueRequestsFromMapFunc(bundleLabelMapper)).
		Watches(&kardinalv1alpha1.Pipeline{}, handler.EnqueueRequestsFromMapFunc(r.pipelineBundles),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}

// bundlePipelineIndex is the spec.pipeline index function.
func bundlePipelineIndex(obj client.Object) []string {
	b, ok := obj.(*kardinalv1alpha1.Bundle)
	if !ok || b.Spec.Pipeline == "" {
		return nil
	}
	return []string{b.Spec.Pipeline}
}

// bundleLabelMapper maps a PromotionStep or Graph to the Bundle named by its
// kardinal.io/bundle label, set by the Graph builder.
func bundleLabelMapper(_ context.Context, obj client.Object) []reconcile.Request {
	name := obj.GetLabels()["kardinal.io/bundle"]
	if name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: name, Namespace: obj.GetNamespace()}}}
}

// bundlePhaseChanged passes Bundle creates and deletes, and updates that
// change status.phase.
var bundlePhaseChanged = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldB, okOld := e.ObjectOld.(*kardinalv1alpha1.Bundle)
		newB, okNew := e.ObjectNew.(*kardinalv1alpha1.Bundle)
		return okOld && okNew && oldB.Status.Phase != newB.Status.Phase
	},
}

// waitingSiblings maps a Bundle event to the other Bundles of its pipeline
// that are new, Available or Promoting.
func (r *Reconciler) waitingSiblings(ctx context.Context, obj client.Object) []reconcile.Request {
	b, ok := obj.(*kardinalv1alpha1.Bundle)
	if !ok {
		return nil
	}
	siblings, err := r.pipelineBundleList(ctx, b.Namespace, b.Spec.Pipeline)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("pipeline", b.Spec.Pipeline).
			Msg("waitingSiblings: list bundles failed")
		return nil
	}
	var reqs []reconcile.Request
	for i := range siblings {
		s := &siblings[i]
		switch s.Status.Phase {
		case "", phaseAvailable, phasePromoting:
			if s.Name != b.Name {
				reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(s)})
			}
		}
	}
	return reqs
}

// pipelineBundles maps a Pipeline event to all Bundles of the pipeline.
func (r *Reconciler) pipelineBundles(ctx context.Context, obj client.Object) []reconcile.Request {
	bundles, err := r.pipelineBundleList(ctx, obj.GetNamespace(), obj.GetName())
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("pipeline", obj.GetName()).
			Msg("pipelineBundles: list bundles failed — pipeline changes may not propagate")
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(bundles))
	for i := range bundles {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&bundles[i])})
	}
	return reqs
}
