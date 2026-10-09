// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package pipeline implements the PipelineReconciler which watches Pipeline
// objects, validates their environment configuration, and sets status conditions
// and status.phase based on active PromotionStep states.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/controller"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/shard"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
)

// secretRecheckInterval is how often a Pipeline refused for a missing
// git.secretRef Secret is checked again: the reconciler watches no Secrets.
const secretRecheckInterval = time.Minute

// Ready condition reasons.
const (
	reasonValid            = "Valid"
	reasonValidationFailed = "ValidationFailed"
	// reasonNotImplemented: the spec sets a reserved field the controller
	// does not implement (graph.UnimplementedFields), so a Bundle fails when
	// it reaches an environment that uses one, or when its Graph is built.
	reasonNotImplemented = "NotImplemented"
)

// The Paused condition is set while spec.paused is true and removed when it is
// not. True/FreezeGateActive: the freeze gate holds new promotions.
// False/FreezeGateNameConflict: a user PolicyGate has the freeze gate's name,
// so nothing holds the pipeline until that gate is renamed or deleted.
const (
	conditionPaused              = "Paused"
	reasonFreezeGateActive       = "FreezeGateActive"
	reasonFreezeGateNameConflict = "FreezeGateNameConflict"
)

// The SecretReferenceable condition warns that the git Secret
// (spec.git.secretRef) lacks the kardinal.io/referenceable: "true" label
// (docs/guides/security.md#secrets-referenced-by-custom-resources). In
// v0.10.0 the Secret is still used; v0.11 refuses it (#1506). The condition is
// absent when no Secret is named, when the Secret does not exist (the steps
// report that), and when it is labeled.
const (
	conditionSecretReferenceable = "SecretReferenceable"
	reasonSecretNotReferenceable = "SecretNotReferenceable"
	labelReferenceable           = "kardinal.io/referenceable"
	// secretRecheck re-reads an unlabeled git Secret: Secrets are not
	// watched, so labeling one is seen at the next recheck or Pipeline event.
	secretRecheck = 5 * time.Minute
)

// gitSecretCondition returns the SecretReferenceable warning for p, or nil.
// The Secret is read from the API server (Secrets are not cached).
func (r *Reconciler) gitSecretCondition(ctx context.Context, p *kardinalv1alpha1.Pipeline) (*metav1.Condition, error) {
	ref := p.Spec.Git.SecretRef
	if ref == nil || ref.Name == "" {
		return nil, nil
	}
	var secret corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: ref.Name}, &secret); err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get git secret %s: %w", ref.Name, err)
	}
	if secret.Labels[labelReferenceable] == "true" {
		return nil, nil
	}
	return &metav1.Condition{
		Type: conditionSecretReferenceable, Status: metav1.ConditionFalse, Reason: reasonSecretNotReferenceable,
		ObservedGeneration: p.Generation,
		Message: fmt.Sprintf("git Secret %s is not labeled %s=true; it is still used in v0.10.0 but will be refused "+
			"in v0.11: label it (kubectl label secret %s %s=true)", ref.Name, labelReferenceable, ref.Name, labelReferenceable),
	}, nil
}

// Reconciler watches Pipeline objects, validates them, and sets status.conditions
// and status.phase.
type Reconciler struct {
	// Workers is how many objects are reconciled at once (--pipeline-workers);
	// 0 is the manager's default. One object is never reconciled twice at
	// once: the work queue serializes it.
	Workers int

	client.Client

	// AllowedRepositories is --scm-allowed-repositories: a Pipeline that
	// would need the shared SCM token for a spec.git.url it does not allow is
	// Ready=False/RepositoryNotAllowed (#1332). Nil allows every repository.
	AllowedRepositories *scm.RepositoryAllowlist

	// CompactAbove is --graph-compact-above: a Pipeline whose new Bundles
	// would get a compact Graph and that uses a feature the compact shape
	// does not carry yet is Ready=False. Nil is graph.DefaultCompactAbove.
	CompactAbove *int

	// Now is the clock of hold expiry (spec.holds[].expiresAt). Nil is
	// time.Now.
	Now func() time.Time
}

// Reconcile is called whenever a Pipeline, one of its PromotionSteps, the
// phase of one of its Bundles or its freeze gate changes. It validates the
// spec, converges the freeze gate to spec.paused, derives status.phase from
// the Bundles and PromotionSteps and sets the Ready condition. A Pipeline
// deleted while it is reconciled ends the reconcile (objectgone).
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return objectgone.Reconcile(ctx, req, pipelinesResource, r.reconcile)
}

// pipelinesResource is the resource objectgone matches a NotFound against.
var pipelinesResource = kardinalv1alpha1.GroupVersion.WithResource("pipelines").GroupResource()

func (r *Reconciler) reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().
		Str("pipeline", req.Name).
		Str("namespace", req.Namespace).
		Logger()

	var p kardinalv1alpha1.Pipeline
	if err := r.Get(ctx, req.NamespacedName, &p); err != nil {
		if apierrors.IsNotFound(err) {
			log.Debug().Msg("pipeline not found, likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get pipeline: %w", err)
	}

	// Holds (#1528): expiry, the HoldCreated/HoldReleased AuditEvents and
	// status.observedHolds.
	holdRecheck, updated, err := r.reconcileHolds(ctx, log, &p)
	if err != nil || updated {
		return ctrl.Result{}, err
	}

	// spec.paused is the request; the freeze gate is what the PromotionStep
	// reconciler reads. Converging here makes a plain spec edit pause too, and
	// recreates a freeze gate deleted by hand while the pipeline is paused.
	desiredPaused, err := r.convergeFreezeGate(ctx, &p)
	if apierrors.HasStatusCause(err, corev1.NamespaceTerminatingCause) {
		// The namespace is being deleted: it refuses the freeze gate, and its
		// deletion deletes the Pipeline next.
		log.Debug().Err(err).Msg("namespace is being deleted — freeze gate not created")
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if desiredPaused != nil && desiredPaused.Status == metav1.ConditionFalse {
		log.Warn().Str("reason", desiredPaused.Reason).Msg(desiredPaused.Message)
	}

	// ownSecret is read only when the allowlist would refuse the URL (#1332).
	ownSecret := false
	if !r.AllowedRepositories.Allows(p.Spec.Git.URL) {
		if ownSecret, err = scm.PipelineSecretExists(ctx, r.Client, &p); err != nil {
			return ctrl.Result{}, err
		}
	}
	desired := r.validate(&p, ownSecret)
	desiredSecret, err := r.gitSecretCondition(ctx, &p)
	if err != nil {
		return ctrl.Result{}, err
	}
	// Nothing watches Secrets: a git.secretRef Secret created later, or a
	// label added to one, is seen by these periodic re-checks.
	var result ctrl.Result
	if desiredSecret != nil {
		result.RequeueAfter = secretRecheck
	}
	if holdRecheck > 0 && (result.RequeueAfter == 0 || holdRecheck < result.RequeueAfter) {
		result.RequeueAfter = holdRecheck
	}
	if desired.Reason == scm.ReasonRepositoryNotAllowed && p.Spec.Git.SecretRef != nil && !ownSecret {
		result.RequeueAfter = secretRecheckInterval
	}
	if desired.Status == metav1.ConditionTrue {
		conflict, err := r.renderedBranchConflict(ctx, &p)
		if err != nil {
			return ctrl.Result{}, err
		}
		if conflict != "" {
			desired = metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: reasonRenderedBranchConflict,
				Message: conflict, ObservedGeneration: p.Generation}
		}
	}

	// Derive status.phase from Bundle phases and PromotionStep states.
	// This is a Watch-node pattern: we read Bundle and PromotionStep CRD status
	// (written by their reconcilers) and write only our own CRD status (Pipeline.status.phase).
	// A failed read returns the error without patching, so a transient apiserver
	// or cache error never overwrites a good phase and metrics.
	var stepList kardinalv1alpha1.PromotionStepList
	if err := r.List(ctx, &stepList,
		client.InNamespace(p.Namespace),
		client.MatchingFields{"spec.pipelineName": p.Name},
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("list promotion steps of pipeline %s: %w", p.Name, err)
	}

	// Compute aggregate deployment metrics from Bundles + PromotionSteps.
	// Graph-first: reads only CRD status fields written by their own reconcilers.
	// Writes only to Pipeline.status.deploymentMetrics (our own CRD).
	var bundleList kardinalv1alpha1.BundleList
	if err := r.List(ctx, &bundleList, client.InNamespace(p.Namespace)); err != nil {
		return ctrl.Result{}, fmt.Errorf("list bundles of pipeline %s: %w", p.Name, err)
	}
	// Pipelines that share a repository and branch must write separate paths
	// (PathConflict). Reads Pipelines, writes only this Pipeline's status.
	var pipelines kardinalv1alpha1.PipelineList
	if err := r.List(ctx, &pipelines); err != nil {
		return ctrl.Result{}, fmt.Errorf("list pipelines: %w", err)
	}
	desiredConflict := pathConflict(&p, pipelines.Items)

	// Retired Bundles (#1492) keep their steps in status.retiredSteps.
	steps := lifecycle.AddRetiredSteps(stepList.Items, bundleList.Items,
		map[string]string{lifecycle.LabelPipeline: p.Name})
	desiredPhase := DerivePhase(p.Name, bundleList.Items, steps)
	desiredMetrics := ComputeDeploymentMetrics(&p, bundleList.Items, steps, time.Now().UTC())

	// Idempotency: only patch if something changed.
	condMatch := conditionMatches(p.Status.Conditions, desired)
	phaseMatch := p.Status.Phase == desiredPhase
	metricsMatch := deploymentMetricsEqual(p.Status.DeploymentMetrics, desiredMetrics)
	pausedMatch := meta.FindStatusCondition(p.Status.Conditions, conditionPaused) == nil
	if desiredPaused != nil {
		pausedMatch = conditionMatches(p.Status.Conditions, *desiredPaused)
	}
	conflictMatch := meta.FindStatusCondition(p.Status.Conditions, conditionPathConflict) == nil
	if desiredConflict != nil {
		conflictMatch = conditionMatches(p.Status.Conditions, *desiredConflict)
	}
	secretMatch := meta.FindStatusCondition(p.Status.Conditions, conditionSecretReferenceable) == nil
	if desiredSecret != nil {
		secretMatch = conditionMatches(p.Status.Conditions, *desiredSecret)
	}
	if condMatch && phaseMatch && metricsMatch && pausedMatch && conflictMatch && secretMatch {
		log.Debug().
			Str("reason", desired.Reason).
			Str("phase", desiredPhase).
			Msg("pipeline status already correct, skipping")
		return result, nil
	}

	patch := client.MergeFrom(p.DeepCopy())
	p.Status.Phase = desiredPhase
	// SetStatusCondition keeps lastTransitionTime unless the status changes.
	meta.SetStatusCondition(&p.Status.Conditions, desired)
	if desiredPaused != nil {
		meta.SetStatusCondition(&p.Status.Conditions, *desiredPaused)
	} else {
		meta.RemoveStatusCondition(&p.Status.Conditions, conditionPaused)
	}
	if desiredConflict != nil {
		meta.SetStatusCondition(&p.Status.Conditions, *desiredConflict)
		log.Warn().Msg(desiredConflict.Message)
	} else {
		meta.RemoveStatusCondition(&p.Status.Conditions, conditionPathConflict)
	}
	if desiredSecret != nil {
		if meta.FindStatusCondition(p.Status.Conditions, conditionSecretReferenceable) == nil {
			log.Warn().Str("secret", p.Spec.Git.SecretRef.Name).Msg(desiredSecret.Message)
		}
		meta.SetStatusCondition(&p.Status.Conditions, *desiredSecret)
	} else {
		meta.RemoveStatusCondition(&p.Status.Conditions, conditionSecretReferenceable)
	}
	p.Status.DeploymentMetrics = desiredMetrics

	if err := r.Status().Patch(ctx, &p, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch pipeline status: %w", err)
	}

	log.Info().
		Str("reason", desired.Reason).
		Str("phase", desiredPhase).
		Int("environments", len(p.Spec.Environments)).
		Msg("pipeline status updated")

	return result, nil
}

// convergeFreezeGate creates the freeze gate of a paused pipeline and deletes
// it when the pipeline is not paused. It returns the desired Paused condition,
// nil when the pipeline is not paused. A user PolicyGate with the freeze gate's
// name is not an error to retry: it is reported as Paused=False until the user
// renames or deletes it (the PolicyGate watch then re-enqueues the Pipeline).
func (r *Reconciler) convergeFreezeGate(ctx context.Context, p *kardinalv1alpha1.Pipeline) (*metav1.Condition, error) {
	if !p.Spec.Paused {
		if err := lifecycle.RemoveFreezeGate(ctx, r.Client, p.Namespace, p.Name); err != nil {
			return nil, fmt.Errorf("resume: %w", err)
		}
		return nil, nil
	}
	err := lifecycle.EnsureFreezeGate(ctx, r.Client, p)
	switch {
	case errors.Is(err, lifecycle.ErrConflict):
		return &metav1.Condition{
			Type: conditionPaused, Status: metav1.ConditionFalse, Reason: reasonFreezeGateNameConflict,
			Message: err.Error(), ObservedGeneration: p.Generation,
		}, nil
	case err != nil:
		return nil, fmt.Errorf("pause: %w", err)
	}
	return &metav1.Condition{
		Type: conditionPaused, Status: metav1.ConditionTrue, Reason: reasonFreezeGateActive,
		Message:            fmt.Sprintf("new promotions are held by PolicyGate %s", lifecycle.FreezeGateName(p.Name)),
		ObservedGeneration: p.Generation,
	}, nil
}

// pipelineForFreezeGate maps a PolicyGate named freeze-<pipeline> to that
// Pipeline. Mapping by name, not owner, also re-enqueues the Pipeline when a
// user gate blocking its freeze gate is renamed or deleted.
func pipelineForFreezeGate(_ context.Context, obj client.Object) []ctrl.Request {
	name, ok := strings.CutPrefix(obj.GetName(), lifecycle.FreezeGateName(""))
	if !ok || name == "" {
		return nil
	}
	return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: obj.GetNamespace(), Name: name}}}
}

// pipelineForBundle maps a Bundle to the Pipeline named by spec.pipeline.
func pipelineForBundle(_ context.Context, obj client.Object) []ctrl.Request {
	b, ok := obj.(*kardinalv1alpha1.Bundle)
	if !ok || b.Spec.Pipeline == "" {
		return nil
	}
	return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: b.Namespace, Name: b.Spec.Pipeline}}}
}

// bundlePhaseChanged passes Bundle creates and deletes, and updates that
// change status.phase; evidence and condition updates do not change the
// Pipeline phase.
var bundlePhaseChanged = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldB, okOld := e.ObjectOld.(*kardinalv1alpha1.Bundle)
		newB, okNew := e.ObjectNew.(*kardinalv1alpha1.Bundle)
		return okOld && okNew && oldB.Status.Phase != newB.Status.Phase
	},
}

// stepStateChanged passes the PromotionStep events the Pipeline status
// depends on: creation, deletion, and an update that changes status.state,
// or outputs.noChanges. The deployment metrics read the health-check step's
// start, which a step writes as it enters HealthChecking. The rest are a running
// step's own status writes, about one a second while it promotes and every
// health check while it bakes; mapping each to its Pipeline reconciled every
// Pipeline about 20 times per Bundle (#1509).
var stepStateChanged = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldS, okOld := e.ObjectOld.(*kardinalv1alpha1.PromotionStep)
		newS, okNew := e.ObjectNew.(*kardinalv1alpha1.PromotionStep)
		if !okOld || !okNew {
			return true
		}
		return oldS.Status.State != newS.Status.State ||
			oldS.Status.Outputs["noChanges"] != newS.Status.Outputs["noChanges"]
	},
}

// Step states that DerivePhase reports as Degraded.
var degradedStates = map[string]bool{
	"Failed":         true,
	"AbortedByAlarm": true,
	"RollingBack":    true,
}

// Bundle phases that DerivePhase reports as Promoting: new, waiting for a
// Graph or a maxConcurrentPromotions slot, or promoting (possibly held by a
// gate before its first PromotionStep exists).
var inFlightBundlePhases = map[string]bool{
	"":          true,
	"Available": true,
	"Promoting": true,
}

// DerivePhase computes the Pipeline.status.phase from the Bundles and the
// PromotionSteps of pipeline pipelineName:
//   - "Degraded"  — the newest Bundle in some environment is Failed,
//     AbortedByAlarm or RollingBack there, or is a Rejected Bundle whose
//     change is live there, or the newest Bundle of the Pipeline is Failed
//     (it may have failed before any step was created)
//   - "Promoting" — a Bundle is new, Available or Promoting (held by a gate
//     or a maxConcurrentPromotions slot counts), or the newest Bundle in some
//     environment is not Verified there yet (E2E-R05)
//   - "Ready"     — nothing is in flight and the newest Bundle is Verified in
//     every environment it reached
//   - "Unknown"   — no Bundle is in flight and no PromotionStep counts (the
//     UI shows "Idle")
//
// Newest follows the rule the UI (ui_api.go) and the CLI (current_bundle.go)
// use to pick the current Bundle: Superseded and Rejected Bundles
// (lifecycle.Halted) and their steps are skipped (handleSuperseded fails a superseded Bundle's cancelled steps),
// except that a Rejected Bundle stays the newest in an environment where its
// change is live (lifecycle.RejectedLiveStep), which makes the Pipeline
// Degraded until a rollback or a newer Bundle replaces it, and
// Bundles are ordered by lifecycle.CompareCreation, the order supersession
// uses. A step whose Bundle is not listed (another pipeline's, or being
// deleted) is skipped too. Each environment is judged by every step its newest
// Bundle has there, so a multi-region environment is Degraded when any region
// is, whatever the List order.
func DerivePhase(pipelineName string, bundles []kardinalv1alpha1.Bundle, steps []kardinalv1alpha1.PromotionStep) string {
	byName := make(map[string]*kardinalv1alpha1.Bundle, len(bundles))
	rejected := make(map[string]*kardinalv1alpha1.Bundle)
	inFlight := false
	var newestBundle *kardinalv1alpha1.Bundle
	for i := range bundles {
		b := &bundles[i]
		if b.Spec.Pipeline != pipelineName {
			continue
		}
		if lifecycle.Halted(b) {
			if lifecycle.Rejected(b) {
				rejected[b.Name] = b
			}
			continue
		}
		byName[b.Name] = b
		if inFlightBundlePhases[b.Status.Phase] {
			inFlight = true
		}
		if newestBundle == nil || lifecycle.CompareCreation(b, newestBundle) > 0 {
			newestBundle = b
		}
	}

	// Pass 1: the newest Bundle per environment. A Rejected Bundle counts in
	// an environment where its change is live (lifecycle.RejectedLiveStep).
	newest := make(map[string]*kardinalv1alpha1.Bundle)
	for i := range steps {
		s := &steps[i]
		b, ok := byName[s.Spec.BundleName]
		if !ok {
			rb, isRejected := rejected[s.Spec.BundleName]
			if !isRejected || !lifecycle.RejectedLiveStep(rb, s) {
				continue
			}
			b = rb
		}
		if cur, ok := newest[s.Spec.Environment]; !ok || lifecycle.CompareCreation(b, cur) > 0 {
			newest[s.Spec.Environment] = b
		}
	}

	// Pass 2: every step of that Bundle in the environment counts.
	counted, hasDegraded, allVerified := 0, false, true
	for i := range steps {
		s := &steps[i]
		b, ok := newest[s.Spec.Environment]
		if !ok || b.Name != s.Spec.BundleName {
			continue
		}
		counted++
		if lifecycle.RejectedLiveStep(b, s) {
			// A rejected change is deployed here: roll back.
			hasDegraded = true
		}
		if degradedStates[s.Status.State] {
			hasDegraded = true
		}
		if s.Status.State != "Verified" {
			allVerified = false
		}
	}

	switch {
	case hasDegraded || (newestBundle != nil && newestBundle.Status.Phase == "Failed"):
		return "Degraded"
	case inFlight || !allVerified:
		return "Promoting"
	case counted > 0:
		return "Ready"
	default:
		return "Unknown"
	}
}

// validate checks pipeline invariants and returns the desired Ready condition:
// True/Valid, False/ValidationFailed with the first problem found, or
// False/NotImplemented listing the reserved fields that are set but not
// implemented (the same list "kardinal validate" reports).
func (r *Reconciler) validate(p *kardinalv1alpha1.Pipeline, ownSecret bool) metav1.Condition {
	invalid := func(msg string) metav1.Condition {
		return metav1.Condition{
			Type: "Ready", Status: metav1.ConditionFalse, Reason: reasonValidationFailed,
			Message: msg, ObservedGeneration: p.Generation,
		}
	}

	// The Graph shape annotation must name a shape; a Bundle of this Pipeline
	// would fail with GraphBuildFailed otherwise.
	if v, ok := p.Annotations[graph.AnnotationGraphShape]; ok && v != graph.GraphShapeCompact && v != graph.GraphShapeNodes {
		return invalid(fmt.Sprintf("annotation %s=%q: use %q or %q, or remove it",
			graph.AnnotationGraphShape, v, graph.GraphShapeCompact, graph.GraphShapeNodes))
	}

	// A Pipeline whose new Bundles would get a compact Graph must not use a
	// feature the compact shape does not carry yet: each Bundle would fail
	// with GraphBuildFailed.
	b := graph.NewBuilder()
	if r.CompactAbove != nil {
		b.CompactAbove = *r.CompactAbove
	}
	if b.WouldBeCompact(p, len(p.Spec.Environments)) {
		if f := graph.CompactUnsupported(graph.BuildInput{Pipeline: p}); len(f) > 0 {
			return invalid(fmt.Sprintf("its Bundles get a compact Graph (more than %d environments, or the %s annotation), "+
				"and the compact shape does not support %s yet; use the annotation %s: %s or remove the feature",
				b.CompactAbove, graph.AnnotationGraphShape, strings.Join(f, ", "), graph.AnnotationGraphShape, graph.GraphShapeNodes))
		}
	}

	// Check for duplicate environment names
	seen := make(map[string]struct{}, len(p.Spec.Environments))
	for _, env := range p.Spec.Environments {
		if _, exists := seen[env.Name]; exists {
			return invalid(fmt.Sprintf("duplicate environment name %q in pipeline spec", env.Name))
		}
		seen[env.Name] = struct{}{}
	}

	// Check dependsOn references valid environments
	for _, env := range p.Spec.Environments {
		for _, dep := range env.DependsOn {
			if _, exists := seen[dep]; !exists {
				return invalid(fmt.Sprintf(
					"environment %q has dependsOn %q which does not exist in this pipeline",
					env.Name, dep,
				))
			}
		}
	}

	// Check the promotion order resolves (no dependsOn or wave cycle). Every
	// Bundle of a cyclic pipeline would fail with CircularDependency.
	if err := graph.DetectCycle(p); err != nil {
		return invalid(err.Error())
	}

	// A git.secretRef in another namespace is refused on purpose (confused
	// deputy), so it is a validation error, not an unimplemented field.
	if err := graph.ValidateSecretRef(p); err != nil {
		return invalid(err.Error())
	}
	// The Pipeline would have the controller's shared SCM token act on a
	// spec.git.url --scm-allowed-repositories does not allow (#1332): it has
	// no git.secretRef Secret that exists (ownSecret), or an environment
	// whose PR that token opens. The PromotionStep reconciler refuses the
	// step with the same error, and the SCM provider refuses the calls.
	if err := r.AllowedRepositories.CheckPipeline(p, ownSecret); err != nil {
		return metav1.Condition{
			Type: "Ready", Status: metav1.ConditionFalse, Reason: scm.ReasonRepositoryNotAllowed,
			Message: scm.NotAllowedMessage(err), ObservedGeneration: p.Generation,
		}
	}
	// argocd + pr-review is refused by the CRD at apply time; a Pipeline
	// stored before that rule is caught here (#1281).
	if err := graph.ValidateUpdateStrategy(p); err != nil {
		return invalid(err.Error())
	}
	if err := graph.ValidateRenderedBranches(p); err != nil {
		return invalid(err.Error())
	}

	if msgs := graph.UnimplementedFields(p); len(msgs) > 0 {
		return metav1.Condition{
			Type: "Ready", Status: metav1.ConditionFalse, Reason: reasonNotImplemented,
			Message: "not implemented or not supported, so a Bundle fails when it reaches an environment " +
				"that uses one (steps, promotionTemplate and two or more regions fail it when its Graph is " +
				"built): " + strings.Join(msgs, "; "),
			ObservedGeneration: p.Generation,
		}
	}

	return metav1.Condition{
		Type: "Ready", Status: metav1.ConditionTrue, Reason: reasonValid,
		Message: "Pipeline spec is valid", ObservedGeneration: p.Generation,
	}
}

// conditionMatches returns true if conditions already holds the desired Ready
// condition (status, reason, message and observed generation) — used for
// idempotency checks.
func conditionMatches(conditions []metav1.Condition, desired metav1.Condition) bool {
	c := meta.FindStatusCondition(conditions, desired.Type)
	return c != nil && c.Status == desired.Status && c.Reason == desired.Reason &&
		c.Message == desired.Message && c.ObservedGeneration == desired.ObservedGeneration
}

// SetupWithManager registers the PipelineReconciler with the controller-runtime Manager.
// It watches Pipeline objects AND PromotionSteps (to update pipeline phase when step
// states change). The PromotionStep watch maps each step to its pipeline via
// spec.pipelineName, triggering a reconcile of the pipeline.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Index PromotionSteps by spec.pipelineName for efficient lookup in Reconcile.
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&kardinalv1alpha1.PromotionStep{},
		"spec.pipelineName",
		func(obj client.Object) []string {
			s, ok := obj.(*kardinalv1alpha1.PromotionStep)
			if !ok || s.Spec.PipelineName == "" {
				return nil
			}
			return []string{s.Spec.PipelineName}
		},
	); err != nil {
		return fmt.Errorf("index PromotionStep by spec.pipelineName: %w", err)
	}

	b := ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{MaxConcurrentReconciles: r.Workers}).
		For(&kardinalv1alpha1.Pipeline{}).
		// A Pipeline that renders to a branch of the same repository may
		// clear or cause a rendered branch conflict.
		Watches(&kardinalv1alpha1.Pipeline{}, handler.EnqueueRequestsFromMapFunc(r.renderingPipelinesSharingRepo)).
		// A Pipeline on the same repository and branch changed: re-check
		// PathConflict on the others.
		Watches(&kardinalv1alpha1.Pipeline{}, r.pipelinePeers()).
		// Deleting the freeze gate by hand while the pipeline is paused, or
		// removing a user gate that has its name, re-enqueues the Pipeline.
		Watches(&kardinalv1alpha1.PolicyGate{}, handler.EnqueueRequestsFromMapFunc(pipelineForFreezeGate)).
		// A Bundle phase change re-derives status.phase: a Bundle held by a
		// gate has no PromotionStep to trigger it (E2E-R05).
		Watches(&kardinalv1alpha1.Bundle{}, handler.EnqueueRequestsFromMapFunc(pipelineForBundle),
			builder.WithPredicates(bundlePhaseChanged)).
		// Enqueue the pipeline named by spec.pipelineName when a PromotionStep
		// is created or deleted, or its state changes (stepStateChanged): the
		// status writes a step makes while it runs (messages, retries, health
		// checks) do not change what the Pipeline derives from it (#1509).
		Watches(&kardinalv1alpha1.PromotionStep{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []ctrl.Request {
				s, ok := obj.(*kardinalv1alpha1.PromotionStep)
				if !ok || s.Spec.PipelineName == "" {
					return nil
				}
				return []ctrl.Request{{
					NamespacedName: client.ObjectKey{
						Name:      s.Spec.PipelineName,
						Namespace: s.Namespace,
					},
				}}
			}),
			builder.WithPredicates(stepStateChanged),
		)
	return shard.Active().Complete(b, tracing.WrapReconciler("pipeline", r), &kardinalv1alpha1.PipelineList{})
}

// deploymentMetricsEqual returns true when a and b represent the same metrics.
// We compare the scalar fields; ComputedAt is intentionally excluded from the
// equality check to avoid patching on every reconcile when no data changed.
// SampleSize IS included: if more Bundles become available, we want to recompute.
func deploymentMetricsEqual(a, b *kardinalv1alpha1.PipelineDeploymentMetrics) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.RolloutsLast30Days == b.RolloutsLast30Days &&
		a.P50CommitToProdMinutes == b.P50CommitToProdMinutes &&
		a.P90CommitToProdMinutes == b.P90CommitToProdMinutes &&
		a.AutoRollbackRateMillis == b.AutoRollbackRateMillis &&
		a.OperatorInterventionRateMillis == b.OperatorInterventionRateMillis &&
		a.StaleProdDays == b.StaleProdDays &&
		a.SampleSize == b.SampleSize &&
		a.Deployments == b.Deployments &&
		a.FailedDeployments == b.FailedDeployments &&
		a.ChangeFailureRateMillis == b.ChangeFailureRateMillis &&
		a.MeanTimeToRestoreMinutes == b.MeanTimeToRestoreMinutes &&
		a.RestoredFailures == b.RestoredFailures
}
