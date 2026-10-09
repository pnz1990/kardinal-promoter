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
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/controller"

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
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/shard"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/translator"
)

// Bundle phases.
const (
	phaseAvailable  = "Available"
	phasePromoting  = "Promoting"
	phaseVerified   = "Verified"
	phaseFailed     = "Failed"
	phaseSuperseded = "Superseded"
	// phaseRejected is final: spec.rejected is set (kardinal reject).
	phaseRejected = "Rejected"
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
	// condRejected is True once spec.rejected is set; its message names who
	// rejected the Bundle and why.
	condRejected = "Rejected"
)

// errGraphDeletedAfterFailure is returned by syncGraph when the Graph of a
// Bundle that failed promoting is missing. It is not recreated: recreating it
// would promote the failed artifacts again from the first environment.
var errGraphDeletedAfterFailure = errors.New("the Graph of this failed Bundle was deleted; it is not recreated, " +
	"so the failed promotion does not run again. Create a new Bundle, or change the Pipeline to retry this one")

// errNamespaceTerminating is returned by translate when the Bundle's namespace
// is being deleted, so nothing was translated.
var errNamespaceTerminating = errors.New("the namespace is being deleted")

// errBundleRetired is returned by translate for a Bundle that the API server
// says is retired (#1492) while the cache does not yet: a retired Bundle's
// Graph is never built again.
var errBundleRetired = errors.New("the bundle's Graph was retired")

// skipTranslate reports whether a translate error means "nothing to do".
func skipTranslate(err error) bool {
	return errors.Is(err, errNamespaceTerminating) || errors.Is(err, errBundleRetired)
}

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
	// Workers is how many Bundles are reconciled at once (--bundle-workers);
	// 0 is the manager's default. One Bundle is never reconciled twice at
	// once (the work queue); the maxConcurrentPromotions count of one
	// Pipeline runs under lockPipeline.
	Workers int
	// slotLocks are the per-Pipeline locks of lockPipeline.
	slotLocks sync.Map

	client.Client
	// APIReader reads straight from the API server (mgr.GetAPIReader()). The
	// maxConcurrentPromotions count reads through it, so a Bundle this
	// reconciler moved to Promoting a moment ago counts even before the
	// informer cache has it (#1310). When nil, Client is used (tests).
	APIReader client.Reader
	// Translator creates the kro Graph for a Bundle+Pipeline pair.
	// May be nil in test environments where translation is not needed.
	Translator BundleTranslator
	// GraphChecker detects whether the Graph CR still exists.
	// When nil, graph recreation is skipped (backward-compatible).
	GraphChecker GraphChecker
	// Recorder emits events.k8s.io/v1 Events for Bundle phase transitions.
	// When nil, event emission is skipped (backward-compatible).
	Recorder events.EventRecorder
	// PolicyNamespaces is the controller's --policy-namespaces list, so the
	// PolicyGate templates hashed for a GraphBuildFailed retry (#1312) are
	// the ones the Translator reads. Nil means the controller default.
	PolicyNamespaces []string
	// Retire says when the Graph of a finished Bundle is retired (#1492).
	// The zero value keeps every Graph.
	Retire RetirePolicy
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
//   - any phase with spec.rejected set: Rejected (markRejected), which is
//     final; only the evidence is synced after that.
//
// A Bundle deleted while it is reconciled ends the reconcile (objectgone).
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	res, err := objectgone.Reconcile(ctx, req, bundlesResource, r.reconcile)
	if apierrors.IsConflict(err) {
		// A status patch carried an older resourceVersion: another writer (the
		// retirement controller, or this one from a stale cache) changed the
		// Bundle. Read it again rather than overwrite it (#1492).
		zerolog.Ctx(ctx).Debug().Err(err).Str("bundle", req.Name).Msg("bundle changed under the reconcile; requeueing")
		return ctrl.Result{RequeueAfter: requeueConflict}, nil
	}
	return res, err
}

// requeueConflict is how soon a reconcile whose status patch lost a race is
// run again.
const requeueConflict = time.Second

// statusPatch is the merge patch of a Bundle's status from before, with an
// optimistic lock: the patch fails with a Conflict when the Bundle changed
// since before was read. Without it, a patch computed from a stale copy can
// put back what another writer changed: status.conditions is a list, which a
// merge patch replaces whole, and a stale phase could undo a retirement.
func statusPatch(before *kardinalv1alpha1.Bundle) client.Patch {
	return client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})
}

// bundlesResource is the resource objectgone matches a NotFound against.
var bundlesResource = kardinalv1alpha1.GroupVersion.WithResource("bundles").GroupResource()

func (r *Reconciler) reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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

	// A rejection wins over every phase, Verified and Superseded included,
	// and is recomputed from spec on every reconcile, so a crash between the
	// spec write and this status write only delays it.
	// It runs on a retired Bundle too (#1492): rejecting a Verified Bundle
	// whose Graph is gone is the usual case, and the early return for
	// retired Bundles comes later. A rejection recorded before the
	// rejected-artifact set existed gets the set now.
	if b.Spec.Rejected != nil && (b.Status.Phase != phaseRejected || b.Status.RejectedArtifacts == nil) {
		return r.markRejected(ctx, log, &b)
	}
	// A rejection is about the artifacts: a Bundle that has not finished and
	// carries an image or config commit of a rejected Bundle is rejected
	// too, so a CI retry of a rejected build does not promote it again.
	if inFlightOrFailed(b.Status.Phase) {
		if from, err := r.rejectedArtifactOf(ctx, &b); err != nil {
			log.Warn().Err(err).Msg("failed to check for rejected artifacts (non-fatal)")
		} else if from != "" {
			return r.markRejectedArtifact(ctx, log, &b, from)
		}
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

// handleBound reconciles a Bundle past Available: Promoting, Failed, Verified,
// Superseded or Rejected.
func (r *Reconciler) handleBound(ctx context.Context, log zerolog.Logger,
	b *kardinalv1alpha1.Bundle) (ctrl.Result, error) {
	active := b.Status.Phase != phaseVerified && b.Status.Phase != phaseSuperseded &&
		b.Status.Phase != phaseRejected

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

	// A retired Bundle is final: its Graph is gone and is never rebuilt, not
	// even by a Pipeline change, and its status is not synced from steps any
	// more (status.retiredSteps holds them). The retirement controller
	// (setupRetire) deletes the Graph.
	if lifecycle.Retired(b) {
		return ctrl.Result{}, nil
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
		if skipTranslate(err) {
			return nil
		}
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
		graphName, tErr := r.translate(ctx, log, pipeline, b)
		if skipTranslate(tErr) {
			return nil
		}
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
		if err := r.checkGatesCreated(ctx, b, g); err != nil {
			log.Warn().Err(err).Str("graph", name).Msg("check gate instances (non-fatal)")
		}
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
// with the Bundle Failed, so only the next Pipeline change, or for
// GraphBuildFailed a change to its PolicyGates (gatesChanged), retries.
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
	if currentHash == b.Status.PipelineSpecHash && !r.gatesChanged(ctx, log, b, pipeline) {
		return nil
	}
	log.Info().
		Str("graph", b.Status.GraphRef).
		Str("oldHash", b.Status.PipelineSpecHash).
		Str("newHash", currentHash).
		Msg("pipeline spec changed — updating Graph in place")
	if _, err := r.translate(ctx, log, pipeline, b); err != nil {
		return fmt.Errorf("update graph for changed pipeline spec: %w", err)
	}
	b.Status.PipelineSpecHash = currentHash
	b.Status.PolicyGatesHash = ""
	meta.RemoveStatusCondition(&b.Status.Conditions, condInvalidSpec)
	return nil
}

// gatesHashFor returns the hash of the PolicyGate templates that apply to
// pipeline's environments as they are now, read as the Translator reads them
// (translator.CollectGates, translator.GatesHash). "" when they cannot be
// read; no retry is keyed on it then.
func (r *Reconciler) gatesHashFor(ctx context.Context, log zerolog.Logger, pipeline *kardinalv1alpha1.Pipeline) string {
	if pipeline == nil {
		return ""
	}
	gates, err := translator.CollectGates(ctx, r.Client, r.PolicyNamespaces, pipeline)
	if err != nil {
		log.Warn().Err(err).Msg("failed to read policy gates for the retry hash (non-fatal)")
		return ""
	}
	return translator.GatesHash(pipeline, gates)
}

// usedGatesHash returns the hash of the PolicyGate templates the failed
// Graph build was given (translator.BuildError), or "" when cause carries
// none: then the gates did not take part, and no gate change retries.
func usedGatesHash(pipeline *kardinalv1alpha1.Pipeline, cause error) string {
	var be *translator.BuildError
	if !errors.As(cause, &be) {
		return ""
	}
	return translator.GatesHash(pipeline, be.Gates)
}

// gatesChanged reports whether b failed with GraphBuildFailed and the
// PolicyGate templates of pipeline differ from the ones it failed with.
func (r *Reconciler) gatesChanged(ctx context.Context, log zerolog.Logger, b *kardinalv1alpha1.Bundle,
	pipeline *kardinalv1alpha1.Pipeline) bool {
	if b.Status.PolicyGatesHash == "" || !meta.IsStatusConditionTrue(b.Status.Conditions, condInvalidSpec) {
		return false
	}
	h := r.gatesHashFor(ctx, log, pipeline)
	return h != "" && h != b.Status.PolicyGatesHash
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

	patch := statusPatch(b.DeepCopy())
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

// enforceHistoryLimit deletes the oldest terminal Bundles (Verified/Failed/Superseded,
// and Rejected for carrying a rejected artifact; never one with spec.rejected)
// for the given pipeline in the given namespace, keeping at most historyLimit bundles.
//
// This implements Pipeline.spec.historyLimit enforcement (spec #910). Non-terminal
// Bundles (Available, Promoting) and Rejected Bundles are never deleted by
// this function.
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

	keep := heldHistory(pipeline, allBundles.Items)
	terminal := make([]*kardinalv1alpha1.Bundle, 0, len(allBundles.Items))
	for i := range allBundles.Items {
		b := &allBundles.Items[i]
		if keep[b.Name] {
			continue
		}
		if b.Spec.Rejected != nil {
			// A rejected Bundle (spec.rejected) is kept whatever its phase,
			// also before the reconciler marked it Rejected: it is the record
			// that its artifacts must not be promoted again (rollback,
			// promote and Subscriptions skip any Bundle carrying a rejected
			// artifact, lifecycle.RejectedArtifacts). Rejections are rare and
			// made by hand, so they stay few.
			continue
		}
		switch b.Status.Phase {
		case phaseVerified, phaseFailed, phaseSuperseded, phaseRejected:
			// phaseRejected without spec.rejected: a Bundle the controller
			// rejected because it carries a rejected artifact
			// (markRejectedArtifact). It adds nothing to the record, which
			// the original rejected Bundle holds, so it is history like the
			// others.
			terminal = append(terminal, b)
		}
	}
	if len(terminal) <= limit {
		return nil
	}
	terminal, err := r.withoutLiveCarriers(ctx, namespace, pipeline.Name, allBundles.Items, terminal)
	if err != nil {
		return err
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

// heldHistory is the Bundles history GC keeps while a hold lasts (spec.holds,
// #1528): the Bundle each hold names, its rollbackOf, and every Bundle that
// deploys one of its artifacts (an image, config commit or chart). The gate
// exemption checks the held Bundle against them (lifecycle.VerifyHeldRollback),
// and none of them counts toward historyLimit.
func heldHistory(p *kardinalv1alpha1.Pipeline, bundles []kardinalv1alpha1.Bundle) map[string]bool {
	keep := map[string]bool{}
	for i := range bundles {
		held := &bundles[i]
		if lifecycle.HoldNaming(p, held.Name) == nil {
			continue
		}
		keep[held.Name] = true
		if held.Spec.Provenance != nil && held.Spec.Provenance.RollbackOf != "" {
			keep[held.Spec.Provenance.RollbackOf] = true
		}
		images := map[kardinalv1alpha1.ImageRef]bool{}
		for _, img := range held.Spec.Images {
			images[img] = true
		}
		for j := range bundles {
			o := &bundles[j]
			for _, img := range o.Spec.Images {
				if images[img] {
					keep[o.Name] = true
				}
			}
			if c, oc := held.Spec.ConfigRef, o.Spec.ConfigRef; c != nil && oc != nil && c.CommitSHA != "" &&
				c.GitRepo == oc.GitRepo && c.CommitSHA == oc.CommitSHA {
				keep[o.Name] = true
			}
			if ch, och := held.Spec.Chart, o.Spec.Chart; ch != nil && och != nil && *ch == *och {
				keep[o.Name] = true
			}
		}
	}
	return keep
}

// withoutLiveCarriers drops from terminal the Bundles rejected for carrying
// a rejected artifact whose change is still live in an environment
// (lifecycle.RejectedLiveEnvs, retired steps included): they are what that
// environment runs, and the views show them with the roll-back hint, so
// history GC keeps them. The steps are listed only when there is a carrier.
func (r *Reconciler) withoutLiveCarriers(ctx context.Context, namespace, pipeline string,
	bundles []kardinalv1alpha1.Bundle, terminal []*kardinalv1alpha1.Bundle) ([]*kardinalv1alpha1.Bundle, error) {
	var steps []kardinalv1alpha1.PromotionStep
	listed := false
	out := terminal[:0:0]
	for _, b := range terminal {
		if b.Status.Phase == phaseRejected {
			if !listed {
				var list kardinalv1alpha1.PromotionStepList
				if err := r.List(ctx, &list, client.InNamespace(namespace),
					client.MatchingLabels{lifecycle.LabelPipeline: pipeline}); err != nil {
					return nil, fmt.Errorf("enforceHistoryLimit: list promotion steps: %w", err)
				}
				steps = lifecycle.AddRetiredSteps(list.Items, bundles, map[string]string{lifecycle.LabelPipeline: pipeline})
				listed = true
			}
			if len(lifecycle.RejectedLiveEnvs(b, steps)) > 0 {
				continue
			}
		}
		out = append(out, b)
	}
	return out, nil
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
	newer, _, err := r.newerSiblings(ctx, b, countVerified)
	return newer, err
}

// newerSiblings scans the same-type Bundles created after b once. newer is
// hasNewerSibling's answer. replaced reports a newer one that is Promoting or
// Verified: it has the Pipeline's slot or finished, so b, a Failed Bundle, is
// not held for a maxConcurrentPromotions slot (#1349). A newer one that is new
// or Available may itself wait for the slot, so it does not count.
func (r *Reconciler) newerSiblings(ctx context.Context, b *kardinalv1alpha1.Bundle,
	countVerified bool) (newer, replaced bool, err error) {
	// The Bundle an environment is held on (spec.holds, kardinal rollback
	// --hold) is never superseded: the hold pins the environment to it until
	// it is released.
	var p kardinalv1alpha1.Pipeline
	if getErr := r.Get(ctx, client.ObjectKey{Namespace: b.Namespace, Name: b.Spec.Pipeline}, &p); getErr == nil &&
		lifecycle.HoldNaming(&p, b.Name) != nil {
		return false, false, nil
	}
	siblings, err := r.pipelineBundleList(ctx, b.Namespace, b.Spec.Pipeline)
	if err != nil {
		return false, false, fmt.Errorf("list bundles for supersession check: %w", err)
	}
	rejected := lifecycle.RejectedArtifactsOf(siblings, b.Spec.Pipeline)
	for i := range siblings {
		s := &siblings[i]
		if s.Name == b.Name || s.Spec.Type != b.Spec.Type {
			continue // image bundles are only superseded by image bundles, etc.
		}
		if _, bad := rejected.Carries(s); bad {
			continue // a rejected Bundle, or one carrying a rejected artifact, never promotes: it supersedes nothing
		}
		switch s.Status.Phase {
		case phaseSuperseded, phaseFailed, phaseRejected:
			continue
		case phaseVerified:
			if !countVerified {
				continue
			}
		}
		if lifecycle.CompareCreation(s, b) > 0 {
			newer = true
			if s.Status.Phase == phasePromoting || s.Status.Phase == phaseVerified {
				replaced = true
			}
		}
	}
	return newer, replaced, nil
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
	patch := statusPatch(b.DeepCopy())
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
	meta.RemoveStatusCondition(&b.Status.Conditions, graph.CondBundleWaitingForSlot)
	setBundleCondition(b, condReady, metav1.ConditionFalse, "Superseded",
		"superseded by a newer bundle for the same pipeline and type")
}

func (r *Reconciler) superseded(b *kardinalv1alpha1.Bundle) {
	r.event(b, corev1.EventTypeNormal, "Superseded",
		fmt.Sprintf("superseded by newer bundle for pipeline %s", b.Spec.Pipeline))
	observability.BundlesTotal.WithLabelValues(phaseSuperseded).Inc()
}

// markRejected sets the phase of a Bundle whose spec.rejected is set to
// Rejected, whatever the phase was. Nothing re-translates or recreates the
// Graph of a Rejected Bundle (handleBound treats it as settled). The Graph is
// kept: every PromotionStep template holds on the Bundle phase (graph
// buildPromotionStepNode), so no new step is created and the existing steps
// stay as history. The PromotionStep reconciler cancels the steps that have
// not reached the environment yet.
//
// Graph-first: the Bundle reconciler writes only its own status, from its
// own spec.
func (r *Reconciler) markRejected(ctx context.Context, log zerolog.Logger,
	b *kardinalv1alpha1.Bundle) (ctrl.Result, error) {
	from := b.Status.Phase
	// What the rejection rejects: the artifacts not already Verified before
	// this Bundle where it went (lifecycle.RejectedSetOf), so an unchanged
	// sidecar does not block a rollback to the Bundle before.
	siblings, err := r.pipelineBundleList(ctx, b.Namespace, b.Spec.Pipeline)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("list bundles of pipeline %s: %w", b.Spec.Pipeline, err)
	}
	// The steps say where the change got past the merge, retired ones too.
	var stepList kardinalv1alpha1.PromotionStepList
	if err := r.List(ctx, &stepList, client.InNamespace(b.Namespace),
		client.MatchingLabels{lifecycle.LabelBundle: b.Name}); err != nil {
		return ctrl.Result{}, fmt.Errorf("list promotion steps of bundle %s: %w", b.Name, err)
	}
	steps := lifecycle.AddRetiredSteps(stepList.Items, []kardinalv1alpha1.Bundle{*b},
		map[string]string{lifecycle.LabelBundle: b.Name})
	set := lifecycle.RejectedSetOf(b, siblings, steps)
	patch := statusPatch(b.DeepCopy())
	msg := rejectionMessage(b.Spec.Rejected)
	b.Status.RejectedArtifacts = &set
	b.Status.Phase = phaseRejected
	setBundleCondition(b, condReady, metav1.ConditionFalse, "Rejected", msg)
	setBundleCondition(b, condRejected, metav1.ConditionTrue, "Rejected", msg)
	if err := r.Status().Patch(ctx, b, patch); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("patch bundle status Rejected: %w", err)
	}
	if from == phaseRejected {
		return ctrl.Result{}, nil // only the rejected-artifact set was added
	}
	log.Info().Str("from", from).Str("by", b.Spec.Rejected.By).Str("reason", b.Spec.Rejected.Reason).
		Int("rejectedImages", len(set.Images)).Msg("bundle rejected")
	r.event(b, corev1.EventTypeWarning, "Rejected", msg)
	observability.BundlesTotal.WithLabelValues(phaseRejected).Inc()
	return ctrl.Result{}, nil
}

// inFlightOrFailed reports whether a Bundle in phase can still promote.
func inFlightOrFailed(phase string) bool {
	switch phase {
	case "", phaseAvailable, phasePromoting, phaseFailed:
		return true
	}
	return false
}

// rejectedArtifactOf names the rejected Bundle of b's pipeline whose image or
// config commit b carries, or "" (lifecycle.RejectedArtifacts). It reads the
// sibling list from the cache, as supersession does.
func (r *Reconciler) rejectedArtifactOf(ctx context.Context, b *kardinalv1alpha1.Bundle) (string, error) {
	siblings, err := r.pipelineBundleList(ctx, b.Namespace, b.Spec.Pipeline)
	if err != nil {
		return "", err
	}
	name, _ := lifecycle.RecordedRejectedArtifactsOf(siblings, b.Spec.Pipeline).Carries(b)
	return name, nil
}

// markRejectedArtifact rejects b, which carries an artifact of the rejected
// Bundle from: phase Rejected (final), Ready False and Rejected True with
// reason RejectedArtifact. spec.rejected stays unset, since nobody rejected
// b itself; everything that skips Rejected Bundles skips it.
func (r *Reconciler) markRejectedArtifact(ctx context.Context, log zerolog.Logger,
	b *kardinalv1alpha1.Bundle, from string) (ctrl.Result, error) {
	patch := statusPatch(b.DeepCopy())
	msg := fmt.Sprintf("carries an image or config commit of the rejected bundle %s; it is never promoted", from)
	b.Status.Phase = phaseRejected
	setBundleCondition(b, condReady, metav1.ConditionFalse, "RejectedArtifact", msg)
	setBundleCondition(b, condRejected, metav1.ConditionTrue, "RejectedArtifact", msg)
	if err := r.Status().Patch(ctx, b, patch); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("patch bundle status Rejected (artifact): %w", err)
	}
	log.Info().Str("rejectedBundle", from).Msg("bundle carries a rejected artifact — rejected")
	r.event(b, corev1.EventTypeWarning, "Rejected", msg)
	observability.BundlesTotal.WithLabelValues(phaseRejected).Inc()
	return ctrl.Result{}, nil
}

// rejectionMessage is the Rejected condition message.
func rejectionMessage(rej *kardinalv1alpha1.BundleRejection) string {
	return fmt.Sprintf("rejected by %s: %s; it is never promoted again", rej.By, rej.Reason)
}

// markPipelineNotFound records that the Bundle's Pipeline does not exist.
// The Bundle is kept: the Pipeline may be applied after the Bundle, or
// spec.pipeline may be a typo the caller has to see. Creating the Pipeline
// re-queues the Bundle through the Pipeline watch.
//
// In a namespace being deleted the Pipeline is gone with the namespace, which
// deletes the Bundle next: nothing is recorded, since the status write and the
// Warning Event would only fail or be refused.
func (r *Reconciler) markPipelineNotFound(ctx context.Context, log zerolog.Logger,
	b *kardinalv1alpha1.Bundle) (ctrl.Result, error) {
	if r.namespaceDeleting(ctx, log, b.Namespace) {
		log.Debug().Str("pipeline", b.Spec.Pipeline).Msg("namespace is being deleted — pipeline gone with it")
		return ctrl.Result{}, nil
	}
	patch := statusPatch(b.DeepCopy())
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
		// With several workers two Available Bundles of the Pipeline could
		// both count a free slot: the count and the Promoting write that
		// takes the slot run under the Pipeline's lock.
		defer r.lockPipeline(b.Namespace, b.Spec.Pipeline)()
		active, err := r.countPromoting(ctx, b)
		if err != nil {
			// A failed read is not a free slot: retry instead of promoting past the cap.
			return ctrl.Result{}, fmt.Errorf("maxConcurrentPromotions: count promoting bundles: %w", err)
		}
		if active >= limit {
			log.Info().Int("active", active).Int("limit", limit).Str("pipeline", b.Spec.Pipeline).
				Msg("maxConcurrentPromotions reached — Available bundle waiting")
			patch := statusPatch(b.DeepCopy())
			if setBundleCondition(b, condReady, metav1.ConditionFalse, "WaitingForSlot", slotMessage(limit)) {
				pErr := r.Status().Patch(ctx, b, patch)
				if apierrors.IsNotFound(pErr) {
					log.Debug().Msg("bundle deleted before WaitingForSlot patch — ignoring")
					return ctrl.Result{}, nil
				}
				if pErr != nil {
					log.Warn().Err(pErr).Msg("failed to record WaitingForSlot (non-fatal)")
				}
			}
			return ctrl.Result{RequeueAfter: requeueSlow}, nil
		}
	}

	// Pausing is enforced by the PromotionStep reconciler, which holds steps
	// while the Pipeline's freeze gate exists (see pkg/lifecycle/pause.go), so
	// a paused Pipeline still gets its Graph and resumes where it stopped.
	graphName, err := r.translate(ctx, log, &pipeline, b)
	if skipTranslate(err) {
		return ctrl.Result{}, nil
	}
	if errors.Is(err, graph.ErrInvalid) {
		// The Graph cannot be built from this Pipeline, Bundle and gates (a
		// denied skip, custom steps, an invalid name or node ID). A retry
		// fails the same way, so fail the Bundle with the reason.
		return r.markInvalid(ctx, log, b, &pipeline, err)
	}
	if err != nil {
		// An API or RBAC error (timeout, conflict, missing permission). Stay
		// Available and retry with backoff; the condition shows the error. A
		// Bundle deleted meanwhile needs no retry.
		patch := statusPatch(b.DeepCopy())
		if setBundleCondition(b, condReady, metav1.ConditionFalse, "TranslationError",
			fmt.Sprintf("graph creation failed, retrying: %v", err)) {
			pErr := r.Status().Patch(ctx, b, patch)
			if apierrors.IsNotFound(pErr) {
				log.Debug().Err(err).Msg("bundle deleted while its graph was created — ignoring")
				return ctrl.Result{}, nil
			}
			if pErr != nil {
				log.Warn().Err(pErr).Msg("failed to record TranslationError (non-fatal)")
			}
			r.event(b, corev1.EventTypeWarning, "TranslationError",
				fmt.Sprintf("graph creation failed for pipeline %s, retrying: %v", b.Spec.Pipeline, err))
		}
		log.Error().Err(err).Msg("failed to translate bundle to graph")
		return ctrl.Result{}, fmt.Errorf("translate bundle %s: %w", b.Name, err)
	}

	patch := statusPatch(b.DeepCopy())
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
//
// It lists through the uncached APIReader (#1310). The Bundle controller runs
// one reconcile at a time and only the leader reconciles, and the Promoting
// status patch is accepted by the API server before the next reconcile starts,
// so an uncached count always sees the previous admission. The spec.pipeline
// field index exists only in the informer cache, so the namespace is listed
// and filtered on spec.pipeline in memory. That is one API read per cap check,
// and only for a Pipeline that sets maxConcurrentPromotions.
func (r *Reconciler) countPromoting(ctx context.Context, b *kardinalv1alpha1.Bundle) (int, error) {
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	var list kardinalv1alpha1.BundleList
	if err := reader.List(ctx, &list, client.InNamespace(b.Namespace)); err != nil {
		return 0, fmt.Errorf("list bundles of pipeline %s: %w", b.Spec.Pipeline, err)
	}
	n := 0
	for i := range list.Items {
		s := &list.Items[i]
		if s.Spec.Pipeline == b.Spec.Pipeline && s.Name != b.Name && s.Status.Phase == phasePromoting {
			n++
		}
	}
	return n, nil
}

// slotMessage is the Ready message of a Bundle waiting for a slot.
func slotMessage(limit int) string {
	return fmt.Sprintf("maxConcurrentPromotions (%d) reached; waiting for a promoting bundle to finish", limit)
}

// slotTaken reports whether the maxConcurrentPromotions cap of b's Pipeline is
// full without b: at least limit other Bundles are Promoting. It is false when
// the Pipeline is unknown or sets no cap, or b has no Graph (nothing to hold).
// The count reads through the APIReader (countPromoting).
func (r *Reconciler) slotTaken(ctx context.Context, b *kardinalv1alpha1.Bundle,
	pipeline *kardinalv1alpha1.Pipeline) (limit int, taken bool, err error) {
	if pipeline == nil || pipeline.Spec.MaxConcurrentPromotions <= 0 || b.Status.GraphRef == "" {
		return 0, false, nil
	}
	limit = pipeline.Spec.MaxConcurrentPromotions
	active, err := r.countPromoting(ctx, b)
	if err != nil {
		return limit, false, fmt.Errorf("maxConcurrentPromotions: count promoting bundles: %w", err)
	}
	return limit, active >= limit, nil
}

// setSlotHold sets the WaitingForSlot condition of a Failed Bundle while held,
// and removes it otherwise, in memory. The condition is what the Graph and the
// PromotionStep reconciler hold the Bundle's steps on (#1349).
func setSlotHold(b *kardinalv1alpha1.Bundle, held bool, limit int) {
	if !held {
		meta.RemoveStatusCondition(&b.Status.Conditions, graph.CondBundleWaitingForSlot)
		return
	}
	setBundleCondition(b, graph.CondBundleWaitingForSlot, metav1.ConditionTrue, "SlotTaken",
		fmt.Sprintf("maxConcurrentPromotions (%d) reached; this failed Bundle creates no new step and its pending steps wait until a promoting bundle finishes", limit))
}

// translate translates b into its Graph unless b's namespace is being
// deleted. A namespace being deleted refuses new objects, so translating would
// only fail creating the Graph ServiceAccount, its RoleBindings or the Graph,
// and the namespace deletion deletes the Bundle next. translate then returns
// errNamespaceTerminating without trying, or when the API server refused an
// object because a namespace is being deleted and b's namespace is the one
// that started terminating meanwhile.
//
// The namespace is read before the translation, and again only when the
// translation was refused for a terminating namespace (namespaceDeleting).
func (r *Reconciler) translate(ctx context.Context, log zerolog.Logger,
	pipeline *kardinalv1alpha1.Pipeline, b *kardinalv1alpha1.Bundle) (string, error) {
	if r.namespaceDeleting(ctx, log, b.Namespace) {
		log.Debug().Msg("namespace is being deleted — not translating the bundle")
		return "", errNamespaceTerminating
	}
	// The retirement controller writes status.retiredAt; this reconcile may
	// hold a copy from before. Rebuilding a Bundle that had a Graph is rare
	// (a Pipeline change, a deleted Graph), so read it fresh first.
	if b.Status.GraphRef != "" && r.retiredSince(ctx, b) {
		log.Debug().Msg("bundle retired — its Graph is not built again")
		return "", errBundleRetired
	}
	name, err := r.Translator.Translate(ctx, pipeline, b)
	if apierrors.HasStatusCause(err, corev1.NamespaceTerminatingCause) && r.namespaceDeleting(ctx, log, b.Namespace) {
		log.Debug().Err(err).Msg("namespace is being deleted — bundle not translated")
		return "", errNamespaceTerminating
	}
	if err == nil && b.Status.GraphRef != "" && r.retiredSince(ctx, b) {
		// Retired between the check above and Translate: delete the Graph
		// just written (the retirement controller would too, on the Graph's
		// create event).
		if d, ok := r.GraphChecker.(graphDeleter); ok {
			if dErr := d.Delete(ctx, b.Namespace, name); dErr != nil && !apierrors.IsNotFound(dErr) {
				return "", fmt.Errorf("delete the Graph of retired bundle %s: %w", b.Name, dErr)
			}
		}
		return "", errBundleRetired
	}
	return name, err
}

// retiredSince reports whether the API server says b is retired (its
// status.retiredAt is set), whatever the cached copy says. Without an
// APIReader it reports false.
func (r *Reconciler) retiredSince(ctx context.Context, b *kardinalv1alpha1.Bundle) bool {
	if r.APIReader == nil {
		return false
	}
	var fresh kardinalv1alpha1.Bundle
	return r.APIReader.Get(ctx, client.ObjectKeyFromObject(b), &fresh) == nil && lifecycle.Retired(&fresh)
}

// namespaceDeleting reports whether namespace is being deleted. It reads the
// namespace from the API server (the controller has no Namespace cache), so it
// is called only around a translation and on a missing Pipeline. A namespace
// that cannot be read counts as not being deleted.
func (r *Reconciler) namespaceDeleting(ctx context.Context, log zerolog.Logger, namespace string) bool {
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	var ns corev1.Namespace
	if err := reader.Get(ctx, client.ObjectKey{Name: namespace}, &ns); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Debug().Err(err).Msg("failed to read the bundle's namespace (non-fatal)")
		}
		return false
	}
	return ns.DeletionTimestamp != nil || ns.Status.Phase == corev1.NamespaceTerminating
}

// markInvalid fails a Bundle that has no Graph yet because its Pipeline,
// intent or gates cannot be built into one. The Pipeline spec hash is stored,
// so a changed Pipeline retries the Bundle (retryIfPipelineChanged).
func (r *Reconciler) markInvalid(ctx context.Context, log zerolog.Logger, b *kardinalv1alpha1.Bundle,
	pipeline *kardinalv1alpha1.Pipeline, cause error) (ctrl.Result, error) {
	patch := statusPatch(b.DeepCopy())
	reason, msg, _ := setInvalid(b, pipeline, cause, usedGatesHash(pipeline, cause))
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
// retries. For GraphBuildFailed it also stores gatesHash, the hash of the
// PolicyGate templates the build used, so a change to them retries too
// (#1312). The reason is CircularDependency or InvalidPipeline for a bad
// environment order, GraphBuildFailed for a Translate error wrapping
// graph.ErrInvalid, and InvalidIntent otherwise. changed reports whether the
// InvalidSpec condition changed.
//
// Graph-first: a pure mutation of the in-memory Bundle before a status patch.
func setInvalid(b *kardinalv1alpha1.Bundle, pipeline *kardinalv1alpha1.Pipeline,
	cause error, gatesHash string) (reason, msg string, changed bool) {
	reason = "InvalidIntent"
	hint := "apply a corrected Pipeline to retry"
	b.Status.PolicyGatesHash = ""
	if errors.Is(cause, graph.ErrInvalid) {
		reason = "GraphBuildFailed"
		hint = "fix the Pipeline, Bundle or PolicyGate it names; a change to the Pipeline or its PolicyGates retries this Bundle"
		b.Status.PolicyGatesHash = gatesHash
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
// Available when the Pipeline spec differs from the one that failed, or, for
// GraphBuildFailed, when the PolicyGate templates do (gatesChanged, #1312). A Bundle
// failed before the hash was recorded (older releases) stays Failed, so an
// upgrade never re-promotes an old image. A newer sibling that is in flight or
// Verified supersedes the Bundle instead.
func (r *Reconciler) retryIfPipelineChanged(ctx context.Context, log zerolog.Logger,
	b *kardinalv1alpha1.Bundle, pipeline *kardinalv1alpha1.Pipeline) (ctrl.Result, error) {
	if pipeline == nil || b.Status.PipelineSpecHash == "" {
		return ctrl.Result{}, nil
	}
	hash := pipelineSpecHashFor(pipeline)
	if hash == "" {
		return ctrl.Result{}, nil
	}
	pipelineChanged := hash != b.Status.PipelineSpecHash
	if !pipelineChanged && !r.gatesChanged(ctx, log, b, pipeline) {
		return ctrl.Result{}, nil
	}
	what := "pipeline " + b.Spec.Pipeline
	if !pipelineChanged {
		what = "the PolicyGates of pipeline " + b.Spec.Pipeline
	}
	newer, err := r.hasNewerSibling(ctx, b, true)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("retry failed bundle: %w", err)
	}
	if newer {
		return r.markSuperseded(ctx, log, b)
	}

	patch := statusPatch(b.DeepCopy())
	b.Status.Phase = phaseAvailable
	b.Status.PipelineSpecHash = ""
	b.Status.PolicyGatesHash = ""
	meta.RemoveStatusCondition(&b.Status.Conditions, condInvalidSpec)
	meta.RemoveStatusCondition(&b.Status.Conditions, condFailed)
	meta.RemoveStatusCondition(&b.Status.Conditions, graph.CondBundleWaitingForSlot)
	setBundleCondition(b, condReady, metav1.ConditionFalse, "Available", what+" changed; retrying promotion")
	if err := r.Status().Patch(ctx, b, patch); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("patch bundle status Available (retry): %w", err)
	}
	log.Info().Bool("pipelineChanged", pipelineChanged).Msg("pipeline or its gates changed — retrying failed bundle")
	r.event(b, corev1.EventTypeNormal, "Retrying", fmt.Sprintf("%s changed; retrying promotion", what))
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
		reason, msg, changed := setInvalid(b, pipeline, syncErr, usedGatesHash(pipeline, syncErr))
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
					// The metrics are written once, so a failed read is
					// retried rather than recorded as zero interventions.
					var gates kardinalv1alpha1.PolicyGateList
					if err := r.List(ctx, &gates,
						client.InNamespace(b.Namespace),
						client.MatchingLabels{"kardinal.io/bundle": b.Name},
					); err != nil {
						return ctrl.Result{}, fmt.Errorf("list gate instances for bundle %s: %w", b.Name, err)
					}
					b.Status.Metrics = computeBundleMetrics(b, expected, steps, gates.Items)
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
		// One read decides the three cases below: a newer same-type Bundle
		// (in flight or Verified) supersedes this one instead of letting it
		// recover; one that is Promoting or Verified (replaced) also means it
		// is not held for a slot.
		newer, replaced, newerErr := r.newerSiblings(ctx, b, true)
		if newerErr != nil {
			log.Warn().Err(newerErr).Msg("failed to check for newer bundle (non-fatal)")
		}
		if newerErr == nil && newer && failedEnv != nil && rejected == nil && onlyRollingBack(steps) {
			// Every failing environment is RollingBack: the rollback Bundle
			// carries it from here. The Bundle reconciler can see the step's
			// RollingBack before its cache has the rollback Bundle, which
			// turns this Bundle Failed instead of Superseded (#1428). Once the
			// newer Bundle is seen (its create event re-queues this one, see
			// waitingSiblings) the outcome is Superseded, as when it was seen
			// first.
			supersede(b)
			after = append(after, func() { r.superseded(b) })
			break
		}
		if newerErr != nil {
			break // keep the hold and the phase as they are; the next event retries
		}
		// maxConcurrentPromotions (#1349): a Failed Bundle does not hold a
		// slot, so while the cap is full it is held (WaitingForSlot): its
		// Graph creates no step and its Pending steps wait, and it does not
		// recover into Promoting. The hold is lifted when a slot frees: the
		// sibling's phase change re-queues it (waitingSiblings). A Bundle
		// that a newer Promoting or Verified one replaced is not held: it can
		// only be superseded. A newer one that is new or Available may be
		// waiting for the slot itself, so it does not lift the hold.
		limit, held := 0, false
		if !replaced {
			if pipeline != nil && pipeline.Spec.MaxConcurrentPromotions > 0 {
				// A recovery back into Promoting takes a slot too: count it and
				// write the phase under the Pipeline's lock, as handleAvailable
				// does, so a recovering and an Available Bundle never take the
				// same free slot (#1509).
				defer r.lockPipeline(b.Namespace, b.Spec.Pipeline)()
			}
			var err error
			if limit, held, err = r.slotTaken(ctx, b, pipeline); err != nil {
				// A failed read is not a free slot: keep the hold as it is and retry.
				return ctrl.Result{}, err
			}
		}
		setSlotHold(b, held, limit)
		if failedEnv == nil && rejected == nil && !graphDeleted && stepsObserved &&
			!meta.IsStatusConditionTrue(b.Status.Conditions, condInvalidSpec) {
			switch {
			case newer:
				supersede(b)
				after = append(after, func() { r.superseded(b) })
			case held:
				setBundleCondition(b, condReady, metav1.ConditionFalse, "WaitingForSlot", slotMessage(limit))
				log.Info().Int("limit", limit).Msg("maxConcurrentPromotions reached — failed bundle waits to recover")
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
	if err := r.Status().Patch(ctx, b, statusPatch(before)); err != nil {
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
	"Rejected":         "Reject",
	"PipelineNotFound": "ResolvePipeline",
	"TranslationError": "CreateGraph",
	"Promoting":        "Promote",
	"Failed":           "Promote",
	"Retrying":         "Retry",
	"GraphDeleted":     "SyncGraph",
	"GraphSyncFailed":  "SyncGraph",
	"Verified":         "Verify",
	"Recovered":        "Promote",
	"GraphRetired":     "RetireGraph",
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
			if len(e.Regions) >= 2 { //nolint:staticcheck // SA1019: aggregates steps from a Graph built before regions were removed
				regions[e.Name] = len(e.Regions) //nolint:staticcheck // SA1019: aggregates steps from a Graph built before regions were removed
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

// onlyRollingBack reports whether every failing PromotionStep in steps is
// RollingBack.
func onlyRollingBack(steps []kardinalv1alpha1.PromotionStep) bool {
	n := 0
	for i := range steps {
		switch st := steps[i].Status.State; {
		case st == "RollingBack":
			n++
		case failedState(st):
			return false
		}
	}
	return n > 0
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
// resets of the Bundle's PromotionSteps. operatorInterventions counts the
// entries in spec.overrides of the Bundle's gate instances (the PolicyGates
// labelled kardinal.io/bundle=<name>): kardinal override appends one entry per
// override, and the Graph never copies a template's overrides to an instance
// (#1308).
// Graph-first: reads CRD fields only; the result is written to Bundle status.
func computeBundleMetrics(b *kardinalv1alpha1.Bundle, expected []string,
	steps []kardinalv1alpha1.PromotionStep, gates []kardinalv1alpha1.PolicyGate) *kardinalv1alpha1.BundleMetrics {
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
	for i := range gates {
		if gates[i].Labels["kardinal.io/bundle"] == b.Name {
			m.OperatorInterventions += len(gates[i].Spec.Overrides)
		}
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
//     and a failed one is retried;
//   - PolicyGate templates (create, delete, spec or label change): the Bundles
//     whose Graph could not be built (InvalidSpec), so a fixed gate retries
//     them (#1312).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(),
		&kardinalv1alpha1.Bundle{}, indexPipeline, bundlePipelineIndex); err != nil {
		return fmt.Errorf("index Bundle by spec.pipeline: %w", err)
	}

	// Unstructured, so kardinal does not import the kro module.
	graphObject := &unstructured.Unstructured{}
	graphObject.SetGroupVersionKind(graph.GraphGVK)

	// Registered whatever the policy: with every delay 0 a Pipeline's
	// kardinal.io/graph-retire-after annotation still retires its Bundles.
	if err := r.setupRetire(mgr); err != nil {
		return fmt.Errorf("set up the bundle retirement controller: %w", err)
	}
	b := ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{MaxConcurrentReconciles: r.Workers}).
		For(&kardinalv1alpha1.Bundle{}).
		Watches(&kardinalv1alpha1.Bundle{}, handler.EnqueueRequestsFromMapFunc(r.waitingSiblings),
			builder.WithPredicates(bundlePhaseChanged)).
		Watches(&kardinalv1alpha1.PromotionStep{}, handler.EnqueueRequestsFromMapFunc(bundleLabelMapper)).
		Watches(graphObject, handler.EnqueueRequestsFromMapFunc(bundleLabelMapper)).
		Watches(&kardinalv1alpha1.Pipeline{}, handler.EnqueueRequestsFromMapFunc(r.pipelineBundles),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&kardinalv1alpha1.PolicyGate{}, handler.EnqueueRequestsFromMapFunc(r.gateBundles),
			builder.WithPredicates(gateTemplateChanged))
	return shard.Active().Complete(b, tracing.WrapReconciler("bundle", r), &kardinalv1alpha1.BundleList{})
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
// whose state the event can change: the new, Available or Promoting ones
// created before it (a newer Bundle supersedes them), every new, Available
// or Promoting one when the Pipeline sets maxConcurrentPromotions (a phase
// change can take or free their slot, #1349), and the Failed ones as below.
// Newer in-flight siblings of an uncapped Pipeline are not re-queued: nothing
// an older Bundle does changes them, and re-queueing every sibling on every
// event made a burst of Bundles quadratic (#1492 scale run: 1,161 queued).
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
	capped, cappedRead := false, false
	isCapped := func() bool {
		if !cappedRead {
			capped, cappedRead = r.capped(ctx, b), true
		}
		return capped
	}
	for i := range siblings {
		s := &siblings[i]
		if s.Name == b.Name {
			continue
		}
		switch s.Status.Phase {
		case "", phaseAvailable, phasePromoting:
			if lifecycle.CompareCreation(b, s) > 0 || isCapped() {
				reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(s)})
			}
		case phaseFailed:
			// A Failed Bundle yields to a newer one while it rolls back
			// (#1428), so a newer Bundle re-queues it. Under a
			// maxConcurrentPromotions cap every phase change can take or
			// free the slot a Failed Bundle waits for (#1349). Only a Bundle
			// with a Graph can roll back or be held.
			if s.Status.GraphRef != "" && (lifecycle.CompareCreation(b, s) > 0 || isCapped()) {
				reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(s)})
			}
		}
	}
	return reqs
}

// isGateTemplate reports whether obj is a PolicyGate template: not an
// instance a Graph stamped (kardinal.io/gate-template) and not generated.
func isGateTemplate(obj client.Object) bool {
	g, ok := obj.(*kardinalv1alpha1.PolicyGate)
	if !ok || g.Spec.Generated {
		return false
	}
	_, instance := g.Labels["kardinal.io/gate-template"]
	return !instance
}

// gateTemplateChanged passes PolicyGate template creates and deletes, and
// updates that change the spec or labels. Status writes are not passed.
var gateTemplateChanged = predicate.Funcs{
	CreateFunc:  func(e event.CreateEvent) bool { return isGateTemplate(e.Object) },
	DeleteFunc:  func(e event.DeleteEvent) bool { return isGateTemplate(e.Object) },
	GenericFunc: func(event.GenericEvent) bool { return false },
	UpdateFunc: func(e event.UpdateEvent) bool {
		if !isGateTemplate(e.ObjectNew) && !isGateTemplate(e.ObjectOld) {
			return false
		}
		return e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration() ||
			!maps.Equal(e.ObjectOld.GetLabels(), e.ObjectNew.GetLabels())
	},
}

// gateBundles maps a PolicyGate template event to every Bundle whose Graph
// could not be built (InvalidSpec True). A template in an org policy
// namespace applies to Pipelines in any namespace, so all namespaces are
// listed; such Bundles are few, and each compares its stored gates hash
// before it retries.
func (r *Reconciler) gateBundles(ctx context.Context, _ client.Object) []reconcile.Request {
	var list kardinalv1alpha1.BundleList
	if err := r.List(ctx, &list); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("gateBundles: list bundles failed — a gate change may not retry a failed bundle")
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		b := &list.Items[i]
		if b.Status.PolicyGatesHash != "" && meta.IsStatusConditionTrue(b.Status.Conditions, condInvalidSpec) {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(b)})
		}
	}
	return reqs
}

// capped reports whether b's Pipeline, read from the cache, sets
// maxConcurrentPromotions. A Pipeline that cannot be read counts as capped,
// so a held Bundle is still re-queued.
func (r *Reconciler) capped(ctx context.Context, b *kardinalv1alpha1.Bundle) bool {
	var p kardinalv1alpha1.Pipeline
	if err := r.Get(ctx, client.ObjectKey{Name: b.Spec.Pipeline, Namespace: b.Namespace}, &p); err != nil {
		return !apierrors.IsNotFound(err)
	}
	return p.Spec.MaxConcurrentPromotions > 0
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

// lockPipeline locks the maxConcurrentPromotions slot count of one
// Pipeline and returns the unlock.
func (r *Reconciler) lockPipeline(namespace, pipeline string) (unlock func()) {
	v, _ := r.slotLocks.LoadOrStore(namespace+"/"+pipeline, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
