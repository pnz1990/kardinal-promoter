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
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/workqueue"
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
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/fairqueue"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/shard"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
)

// secretRecheckInterval is how often a Pipeline refused for a missing
// git.secretRef Secret, or reported GitCredentialMissing or
// SecretNotReferenceable, is checked again once it has been so for a while
// (secretRecheck): the reconciler watches no Secrets. The controller may
// only get Secrets by name, never list or watch them (#1266), so a Secret
// created or labelled to follow the fix-it is seen by these re-checks.
const secretRecheckInterval = time.Minute

// secretRecheck is when to check a Pipeline's git Secret again while Ready
// says the Secret is missing or not usable (reason, since the condition's
// lastTransitionTime when it already says so). Right after the problem is
// reported someone is likely following the fix-it, so the Secret is read
// every 2s for 2 minutes, then every 15s for 15 minutes, then every
// secretRecheckInterval. Each check is one uncached Get of one Secret.
func secretRecheck(current *metav1.Condition, reason string, now time.Time) time.Duration {
	age := time.Duration(0)
	if current != nil && current.Reason == reason && !current.LastTransitionTime.IsZero() {
		age = now.Sub(current.LastTransitionTime.Time)
	}
	switch {
	case age < 2*time.Minute:
		return 2 * time.Second
	case age < 15*time.Minute:
		return 15 * time.Second
	}
	return secretRecheckInterval
}

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

// A git Secret (spec.git.secretRef) without the kardinal.io/referenceable:
// "true" label is not used (docs/guides/security.md#secrets-referenced-by-custom-resources,
// #1506): the Pipeline is Ready=False with reason SecretNotReferenceable and
// the command that labels it, and every git step waits until it is labeled.
// conditionSecretReferenceable is the warning condition of older
// controllers, removed from every Pipeline.
const (
	conditionSecretReferenceable = "SecretReferenceable"
	reasonSecretNotReferenceable = kardinalv1alpha1.ReasonSecretNotReferenceable
	labelReferenceable           = kardinalv1alpha1.LabelSecretReferenceable
)

// secretNotReferenceable returns the Ready=False/SecretNotReferenceable
// condition of p, or nil, from its git Secret as readGitSecret read it.
func secretNotReferenceable(p *kardinalv1alpha1.Pipeline, gs gitSecret) *metav1.Condition {
	if gs.secret == nil || gs.secret.Labels[labelReferenceable] == "true" {
		return nil
	}
	return &metav1.Condition{
		Type: "Ready", Status: metav1.ConditionFalse, Reason: reasonSecretNotReferenceable,
		ObservedGeneration: p.Generation,
		Message: fmt.Sprintf("git Secret %s is not labeled %s=true, so git does not use it; label it: "+
			"kubectl label secret %s -n %s %s=true", gs.name, labelReferenceable, gs.name, p.Namespace, labelReferenceable),
	}
}

// Reconciler watches Pipeline objects, validates them, and sets status.conditions
// and status.phase.
type Reconciler struct {
	// Workers is how many objects are reconciled at once (--pipeline-workers);
	// 0 is the manager's default. One object is never reconciled twice at
	// once: the work queue serializes it.
	Workers int

	client.Client

	// ControllerSecret is the Secret holding the controller's own SCM token:
	// a Pipeline whose git.secretRef names it is Ready=False/ValidationFailed.
	// Nil refuses nothing.
	ControllerSecret *scm.ControllerTokenSecret

	// AllowedRepositories is --scm-allowed-repositories: a Pipeline that
	// would need the shared SCM token for a spec.git.url it does not allow is
	// Ready=False/RepositoryNotAllowed (#1332). Nil allows every repository.
	AllowedRepositories *scm.RepositoryAllowlist

	// CompactAbove is --graph-compact-above: a Pipeline whose new Bundles
	// would get a compact Graph and that uses a feature the compact shape
	// does not carry yet is Ready=False. Nil is graph.DefaultCompactAbove.
	CompactAbove *int

	// Reader reads the Argo CD Applications a fleet selector matches. It is
	// not cached: the controller does not watch Applications, whose CRD may
	// be missing. Nil uses Client.
	Reader client.Reader
	// FleetApplicationNamespaces are the namespaces a fleet selector of kind
	// Application may read (--fleet-application-namespaces); empty is
	// argocd. A selector naming another namespace is refused, so a Pipeline
	// cannot list the Applications of a namespace its author cannot read.
	FleetApplicationNamespaces []string
	// FleetClusterProfileNamespaces are the namespaces besides the
	// Pipeline's own a ClusterProfile selector may read
	// (--fleet-clusterprofile-namespaces).
	FleetClusterProfileNamespaces []string

	// Now is the clock of hold expiry (spec.holds[].expiresAt). Nil is
	// time.Now.
	Now func() time.Time

	// HoldBundleGrace is how long a hold may name a Bundle that does not
	// exist before the controller reports it (--hold-bundle-grace). 0 is
	// DefaultHoldBundleGrace.
	HoldBundleGrace time.Duration

	// Recorder emits the HoldBundleMissing Warning Event. Nil emits none
	// (tests).
	Recorder events.EventRecorder
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
	// A git Secret that exists but is not labelled, and that would make the
	// URL allowed once labelled, is reported as SecretNotReferenceable below,
	// with the label command, not as RepositoryNotAllowed.
	ownSecret := false
	if !r.AllowedRepositories.Allows(p.Spec.Git.URL) {
		state, err := scm.PipelineSecretState(ctx, r.Client, &p)
		if err != nil {
			return ctrl.Result{}, err
		}
		ownSecret = state != scm.PipelineSecretNone
	}
	// The fleets are resolved first: validate checks them as the Graph
	// builder will see them.
	desiredFleets := r.resolveFleets(ctx, &p)
	validated := p.DeepCopy()
	validated.Status.Fleets = desiredFleets
	desired := r.validate(validated, ownSecret)
	gs, err := r.readGitSecret(ctx, &p)
	if err != nil {
		return ctrl.Result{}, err
	}
	// Nothing watches Secrets: a git.secretRef Secret created later, or a
	// label added to one, is seen by the periodic re-checks below. A
	// selector fleet is re-resolved as often.
	var result ctrl.Result
	if holdRecheck > 0 && (result.RequeueAfter == 0 || holdRecheck < result.RequeueAfter) {
		result.RequeueAfter = holdRecheck
	}
	readyNow := meta.FindStatusCondition(p.Status.Conditions, "Ready")
	if desired.Reason == scm.ReasonRepositoryNotAllowed && p.Spec.Git.SecretRef != nil && !ownSecret {
		result.RequeueAfter = secretRecheck(readyNow, desired.Reason, r.now())
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
	// A valid spec whose git has no credential: every git step of a Bundle
	// would wait for it, so Ready says so before the first Bundle, with the
	// command that fixes it. Re-checked soon (secretRecheck): Secrets are
	// not watched.
	if desired.Status == metav1.ConditionTrue {
		c := gitCredentialMissing(&p, gs)
		if c == nil {
			c = secretNotReferenceable(&p, gs)
		}
		if c != nil {
			desired = *c
			if d := secretRecheck(readyNow, c.Reason, r.now()); result.RequeueAfter == 0 || d < result.RequeueAfter {
				result.RequeueAfter = d
			}
		}
	}
	if hasSelectorFleet(&p) && (result.RequeueAfter == 0 || fleetResync < result.RequeueAfter) {
		result.RequeueAfter = fleetResync
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
	// Only this Pipeline's Bundles, through the spec.pipeline index: the
	// namespace may hold every other Pipeline's history too (#1654).
	var bundleList kardinalv1alpha1.BundleList
	if err := r.List(ctx, &bundleList, client.InNamespace(p.Namespace),
		client.MatchingFields{lifecycle.IndexBundlePipeline: p.Name}); err != nil {
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
	// The environment projection (display only, environments.go). Gate
	// instances matter only while a Bundle is in flight.
	var gates []kardinalv1alpha1.PolicyGate
	if anyInFlight(p.Name, bundleList.Items) {
		var gateList kardinalv1alpha1.PolicyGateList
		if err := r.List(ctx, &gateList, client.InNamespace(p.Namespace),
			client.MatchingLabels{lifecycle.LabelPipeline: p.Name}); err != nil {
			return ctrl.Result{}, fmt.Errorf("list gates of pipeline %s: %w", p.Name, err)
		}
		gates = gateList.Items
	}
	// validated carries the fleets just resolved, as the Graph builder will
	// see them.
	desiredEnvs := ProjectEnvironments(validated, bundleList.Items, steps, gates, p.Status.Environments, r.now())
	desiredFailed := FailedVersion(p.Name, bundleList.Items)
	desiredSummary := WithFailure(Summarize(&p, desired, desiredEnvs), desiredFailed, desiredEnvs)
	desiredEnvLine := EnvironmentLine(desiredEnvs)
	desiredMetrics := ComputeDeploymentMetrics(&p, bundleList.Items, steps, r.now().UTC())

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
	fleetsMatch := fleetsEqual(p.Status.Fleets, desiredFleets)
	envsMatch := environmentsEqual(p.Status.Environments, desiredEnvs) &&
		p.Status.Summary == desiredSummary && p.Status.EnvironmentSummary == desiredEnvLine &&
		failedVersionEqual(p.Status.FailedVersion, desiredFailed)
	if condMatch && phaseMatch && metricsMatch && pausedMatch && conflictMatch && secretMatch && fleetsMatch && envsMatch {
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
	meta.RemoveStatusCondition(&p.Status.Conditions, conditionSecretReferenceable)
	p.Status.DeploymentMetrics = desiredMetrics
	p.Status.Fleets = desiredFleets
	p.Status.Environments = desiredEnvs
	p.Status.Summary = desiredSummary
	p.Status.EnvironmentSummary = desiredEnvLine
	p.Status.FailedVersion = desiredFailed

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

// pipelineForGateInstance maps a gate instance to the Pipeline its
// kardinal.io/pipeline label names; a template has no kardinal.io/bundle
// label and maps to none.
func pipelineForGateInstance(_ context.Context, obj client.Object) []ctrl.Request {
	l := obj.GetLabels()
	if l[lifecycle.LabelPipeline] == "" || l["kardinal.io/bundle"] == "" {
		return nil
	}
	return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: obj.GetNamespace(), Name: l[lifecycle.LabelPipeline]}}}
}

// gateReadyChanged passes gate creates and deletes, and updates that change
// status.ready: a gate re-evaluates often, and only its readiness changes
// what the Pipeline shows.
var gateReadyChanged = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldG, okOld := e.ObjectOld.(*kardinalv1alpha1.PolicyGate)
		newG, okNew := e.ObjectNew.(*kardinalv1alpha1.PolicyGate)
		return okOld && okNew && oldG.Status.Ready != newG.Status.Ready
	},
}

// anyInFlight reports whether a Bundle of pipeline is new, Available or
// Promoting, so gates may hold it.
func anyInFlight(pipeline string, bundles []kardinalv1alpha1.Bundle) bool {
	for i := range bundles {
		if bundles[i].Spec.Pipeline == pipeline && !lifecycle.Halted(&bundles[i]) && inFlightBundlePhases[bundles[i].Status.Phase] {
			return true
		}
	}
	return false
}

// pipelineForStep maps a PromotionStep to the Pipeline its
// spec.pipelineName names.
func pipelineForStep(_ context.Context, obj client.Object) []ctrl.Request {
	s, ok := obj.(*kardinalv1alpha1.PromotionStep)
	if !ok || s.Spec.PipelineName == "" {
		return nil
	}
	return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: s.Namespace, Name: s.Spec.PipelineName}}}
}

// statusCoalesce is how long a Pipeline waits, after one of its
// PromotionSteps moved to an intermediate state (Promoting, WaitingForMerge,
// HealthChecking, ...), before it derives its status again. The work queue
// holds a key once and keeps the earliest time, so every change in the window
// is written in one status update. Each update rewrites the whole Pipeline
// (its status.environments has an entry per environment): a 496-environment
// wave changed step states several times a second, and one write per change
// multiplied the Pipeline's etcd writes and watch traffic (#1719). A step that
// settles (stepSettled), a Bundle phase change, a gate instance's readiness
// and any Pipeline event are not delayed: the CLI and the UI show the end of
// a promotion as soon as it happens (kardinal ship --wait, get pipelines).
const statusCoalesce = 2 * time.Second

// stepSettled reports whether an update moved a PromotionStep to a state
// that ends its promotion, Verified or one DerivePhase reports as Degraded.
func stepSettled(e event.UpdateEvent) bool {
	s, ok := e.ObjectNew.(*kardinalv1alpha1.PromotionStep)
	return ok && (s.Status.State == "Verified" || degradedStates[s.Status.State])
}

// coalesced is handler.EnqueueRequestsFromMapFunc that adds each request
// statusCoalesce later, or at once for an update urgent reports.
func coalesced(fn handler.MapFunc, urgent func(event.UpdateEvent) bool) handler.EventHandler {
	type queue = workqueue.TypedRateLimitingInterface[reconcile.Request]
	add := func(ctx context.Context, q queue, now bool, objs ...client.Object) {
		for _, o := range objs {
			for _, req := range fn(ctx, o) {
				if now {
					q.Add(req)
				} else {
					q.AddAfter(req, statusCoalesce)
				}
			}
		}
	}
	return handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q queue) { add(ctx, q, false, e.Object) },
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q queue) {
			add(ctx, q, urgent(e), e.ObjectOld, e.ObjectNew)
		},
		DeleteFunc:  func(ctx context.Context, e event.DeleteEvent, q queue) { add(ctx, q, false, e.Object) },
		GenericFunc: func(ctx context.Context, e event.GenericEvent, q queue) { add(ctx, q, false, e.Object) },
	}
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

	// A fleet whose targets cannot be resolved fails every Bundle's build.
	if err := graph.ValidateFleets(p); err != nil {
		return invalid(err.Error())
	}
	// A paused fleet that is not a fleet pauses nothing: say so rather than
	// leave the entry to pause an environment that becomes a fleet later.
	// (A CRD rule would cost more than the schema's CEL budget: 100 entries
	// times 500 environments.)
	if err := graph.ValidatePausedFleets(p); err != nil {
		return invalid(err.Error())
	}

	// A git.secretRef in another namespace is refused on purpose (confused
	// deputy), so it is a validation error, not an unimplemented field.
	if err := graph.ValidateSecretRef(p); err != nil {
		return invalid(err.Error())
	}
	if err := r.ControllerSecret.Refuses(p); err != nil {
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
	// A pr template that does not parse or render would fail every PR of
	// the environment (docs/pr-evidence.md#customising-the-pr).
	if err := scm.ValidatePipelinePR(p); err != nil {
		return invalid(err.Error())
	}

	if msgs := graph.UnimplementedFields(p); len(msgs) > 0 {
		return metav1.Condition{
			Type: "Ready", Status: metav1.ConditionFalse, Reason: reasonNotImplemented,
			Message: "not implemented or not supported, so a Bundle fails when it reaches an environment " +
				"that uses one: " + strings.Join(msgs, "; "),
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
	if err := lifecycle.IndexBundlesByPipeline(context.Background(), mgr.GetFieldIndexer()); err != nil {
		return err
	}

	b := ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{MaxConcurrentReconciles: r.Workers,
			// Workers are shared fairly between namespaces (#1577).
			NewQueue: fairqueue.NewFor(mgr)}).
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
		// A gate instance of one of its Bundles that turns ready or not
		// changes status.environments[].blockedBy.
		Watches(&kardinalv1alpha1.PolicyGate{}, handler.EnqueueRequestsFromMapFunc(pipelineForGateInstance),
			builder.WithPredicates(gateReadyChanged)).
		// A Bundle phase change re-derives status.phase: a Bundle held by a
		// gate has no PromotionStep to trigger it (E2E-R05).
		Watches(&kardinalv1alpha1.Bundle{}, handler.EnqueueRequestsFromMapFunc(pipelineForBundle),
			builder.WithPredicates(bundlePhaseChanged)).
		// Enqueue the pipeline named by spec.pipelineName when a PromotionStep
		// is created or deleted, or its state changes (stepStateChanged): the
		// status writes a step makes while it runs (messages, retries, health
		// checks) do not change what the Pipeline derives from it (#1509).
		Watches(&kardinalv1alpha1.PromotionStep{}, coalesced(pipelineForStep, stepSettled),
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
