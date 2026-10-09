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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/eventfilter"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/kubeevent"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/shard"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
)

const (
	// defaultFailureThreshold is used when spec.failureThreshold <= 0.
	defaultFailureThreshold = 3

	// requeueInterval is how long to wait before retrying after the rollback
	// Bundle could not be listed or created. No watched object changes on
	// that error, so a timer retries it. A policy below its threshold is not
	// polled: the watches in SetupWithManager re-evaluate it.
	requeueInterval = 30 * time.Second

	labelPipeline    = "kardinal.io/pipeline"
	labelEnvironment = "kardinal.io/environment"
	labelBundle      = "kardinal.io/bundle"

	// ConditionRollbackRefused is True when the failure threshold was reached
	// but the rollback planner refused to create a rollback Bundle (#1314).
	ConditionRollbackRefused = "RollbackRefused"
	// ReasonNoSafeTarget: nothing safe to roll back to (lifecycle.ErrConflict).
	ReasonNoSafeTarget = "NoSafeTarget"
	// ReasonInvalidPolicy: the Pipeline or environment the policy names does
	// not exist (lifecycle.ErrNotFound, lifecycle.ErrInvalid).
	ReasonInvalidPolicy = "InvalidPolicy"
	// ReasonRollbackCreated: a rollback Bundle was created or found.
	ReasonRollbackCreated = "RollbackCreated"
	// ReasonBelowThreshold: the failures dropped below the threshold after a
	// refusal, so the refusal no longer applies.
	ReasonBelowThreshold = "BelowThreshold"
)

// Reconciler monitors a RollbackPolicy and triggers auto-rollback when the
// associated PromotionStep's consecutive health failures exceed the threshold.
// It is idempotent and safe to re-run after a crash.
type Reconciler struct {
	client.Client

	// NowFn returns the current time. Overridable for testing.
	NowFn func() time.Time

	// Recorder emits the Warning Event when a rollback is refused. Nil
	// disables Events.
	Recorder events.EventRecorder
}

// Reconcile processes one RollbackPolicy event.
// It is idempotent: safe to re-run after a crash at any point.
//
// A RollbackPolicy deleted while it is reconciled ends the reconcile (objectgone).
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return objectgone.Reconcile(ctx, req, rollbackPoliciesResource, r.reconcile)
}

// rollbackPoliciesResource is the resource objectgone matches a NotFound against.
var rollbackPoliciesResource = v1alpha1.GroupVersion.WithResource("rollbackpolicies").GroupResource()

func (r *Reconciler) reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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
		// No poll: the PromotionStep watch (policiesForStep) enqueues this
		// policy when a step of spec.bundleRef in spec.environment appears.
		log.Debug().
			Str("pipeline", rp.Spec.PipelineName).
			Str("environment", rp.Spec.Environment).
			Str("bundle", rp.Spec.BundleRef).
			Msg("no PromotionStep found yet for the bundle; waiting for one")
		return ctrl.Result{}, nil
	}

	// The CRD defaults spec.failureThreshold to 3; an explicit value <= 0
	// is treated as 3 too.
	threshold := rp.Spec.FailureThreshold
	if threshold <= 0 {
		threshold = defaultFailureThreshold
	}

	triggered := failures >= threshold
	// Below the threshold, ShouldRollback is false again: it can only be
	// true here when no rollback Bundle was recorded (the terminal case
	// returned above), e.g. after RollbackRefused. Clearing it creates
	// nothing, and a later crossing reuses any rollback Bundle that exists
	// (ensureRollbackBundle), so it cannot start a second rollback.
	// Unless the rollback Bundle exists: an evaluation that created it and
	// stopped before recording it (a crash between the Create and the status
	// patch) left ShouldRollback true. The policy stays triggered and records
	// the Bundle below, whatever the failures are now; the rollback itself may
	// have made them drop. The lookup runs only in that case, so a policy that
	// was never triggered reads no Bundles.
	if !triggered && rp.Status.ShouldRollback {
		existing, err := r.existingRollback(ctx, &rp)
		if err != nil {
			return ctrl.Result{}, err
		}
		triggered = existing != ""
	}

	// Write status: always update consecutiveFailures and lastEvaluatedAt.
	now := metav1.NewTime(r.now())
	patch := client.MergeFrom(rp.DeepCopy())
	rp.Status.ConsecutiveFailures = failures
	rp.Status.LastEvaluatedAt = &now
	rp.Status.ShouldRollback = triggered
	if !triggered {
		clearRefusal(&rp, failures, threshold, now)
	}

	if err := r.Status().Patch(ctx, &rp, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch rollbackpolicy status %s: %w", req.Name, err)
	}

	// If threshold exceeded, create rollback Bundle (if not already done).
	if rp.Status.ShouldRollback {
		rbName, refusal, err := r.ensureRollbackBundle(ctx, log, &rp)
		if err != nil {
			log.Error().Err(err).Msg("failed to ensure rollback bundle")
			return ctrl.Result{RequeueAfter: requeueInterval}, nil
		}
		if err := r.recordOutcome(ctx, &rp, rbName, refusal, now); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Not yet triggered. No poll: the inputs above are the policy's spec and
	// the status of the Bundle's PromotionSteps, and SetupWithManager watches
	// both, so a change re-evaluates the policy.
	return ctrl.Result{}, nil
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

// recordOutcome writes the result of a rollback attempt to the policy status:
// status.rollbackBundleName and RollbackRefused=False when a rollback Bundle
// exists, or RollbackRefused=True with the planner's reason when it refused
// (#1314). The status is patched only when it changes, and the Warning Event
// is emitted only when RollbackRefused turns True, so a policy that stays
// refused does not emit an Event per reconcile.
func (r *Reconciler) recordOutcome(ctx context.Context, rp *v1alpha1.RollbackPolicy,
	rbName string, refusal *refusal, now metav1.Time) error {
	patch := client.MergeFrom(rp.DeepCopy())
	wasRefused := meta.IsStatusConditionTrue(rp.Status.Conditions, ConditionRollbackRefused)
	cond := metav1.Condition{
		Type:               ConditionRollbackRefused,
		Status:             metav1.ConditionFalse,
		Reason:             ReasonRollbackCreated,
		Message:            fmt.Sprintf("rollback Bundle %s was created", rbName),
		ObservedGeneration: rp.Generation,
		LastTransitionTime: now,
	}
	changed := false
	if refusal != nil {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionTrue, refusal.reason, refusal.message
	} else if rp.Status.RollbackBundleName == nil || *rp.Status.RollbackBundleName != rbName {
		rp.Status.RollbackBundleName = &rbName
		changed = true
	}
	if meta.SetStatusCondition(&rp.Status.Conditions, cond) {
		changed = true
	}
	if !changed {
		return nil
	}
	if err := r.Status().Patch(ctx, rp, patch); err != nil {
		return fmt.Errorf("patch rollbackpolicy outcome %s: %w", rp.Name, err)
	}
	if refusal != nil && !wasRefused {
		kubeevent.Emit(r.Recorder, rp, corev1.EventTypeWarning, ConditionRollbackRefused, "Rollback",
			fmt.Sprintf("env %s: no rollback Bundle created for %s after %s: %s",
				rp.Spec.Environment, rp.Spec.BundleRef, healthFailures(rp.Status.ConsecutiveFailures), refusal.message))
	}
	return nil
}

// clearRefusal sets RollbackRefused to False when it is True and the failures
// are below the threshold: the refusal was for a rollback that is no longer
// due. The caller writes the status.
func clearRefusal(rp *v1alpha1.RollbackPolicy, failures, threshold int, now metav1.Time) {
	if !meta.IsStatusConditionTrue(rp.Status.Conditions, ConditionRollbackRefused) {
		return
	}
	meta.SetStatusCondition(&rp.Status.Conditions, metav1.Condition{
		Type:               ConditionRollbackRefused,
		Status:             metav1.ConditionFalse,
		Reason:             ReasonBelowThreshold,
		Message:            fmt.Sprintf("%s, below the threshold of %d", healthFailures(failures), threshold),
		ObservedGeneration: rp.Generation,
		LastTransitionTime: now,
	})
}

// healthFailures returns "1 consecutive health failure", or "<n> consecutive
// health failures" for any other count n.
func healthFailures(n int) string {
	if n == 1 {
		return "1 consecutive health failure"
	}
	return fmt.Sprintf("%d consecutive health failures", n)
}

// refusal is why the rollback planner did not plan a rollback.
type refusal struct {
	reason  string
	message string
}

// ensureRollbackBundle creates a rollback Bundle if one doesn't already exist.
// It returns the name of the rollback Bundle (new or existing), or a refusal
// when the planner found nothing safe to roll back to.
//
// The rollback is planned by lifecycle.PlanRollback, the planner shared with
// `kardinal rollback`, the UI and onHealthFailure=rollback: it restores the
// artifacts of the most recent Bundle, other than spec.bundleRef, that was
// Verified in the environment, and never re-promotes the failing image. When
// there is nothing safe to roll back to, or spec.bundleRef is itself a
// rollback (a failing rollback does not start another), no Bundle is created
// (C04-gates-06).
func (r *Reconciler) ensureRollbackBundle(ctx context.Context, log zerolog.Logger,
	rp *v1alpha1.RollbackPolicy) (string, *refusal, error) {
	existing, err := r.existingRollback(ctx, rp)
	if err != nil {
		return "", nil, err
	}
	if existing != "" {
		log.Debug().Str("existing_rollback", existing).Msg("rollback bundle already exists, reusing")
		return existing, nil, nil
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
		reason := ReasonNoSafeTarget
		switch {
		case errors.Is(err, lifecycle.ErrConflict):
		case errors.Is(err, lifecycle.ErrInvalid), errors.Is(err, lifecycle.ErrNotFound):
			reason = ReasonInvalidPolicy
		default:
			return "", nil, fmt.Errorf("plan rollback of bundle %s: %w", rp.Spec.BundleRef, err)
		}
		log.Warn().Err(err).
			Str("bundleRef", rp.Spec.BundleRef).
			Str("environment", rp.Spec.Environment).
			Msg("auto-rollback: nothing safe to roll back to; no rollback bundle created, human intervention required")
		return "", &refusal{reason: reason, message: kubeevent.Truncate(err.Error())}, nil
	}

	lifecycle.StampCreatedBy(plan.Bundle, lifecycle.ControllerCreator)
	if err := r.Create(ctx, plan.Bundle); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", nil, fmt.Errorf("create rollback bundle: %w", err)
	}

	log.Info().
		Str("rollback_bundle", rollbackName).
		Str("original_bundle", rp.Spec.BundleRef).
		Str("rollback_to", plan.Target.Name).
		Int("failures", rp.Status.ConsecutiveFailures).
		Str("pipeline", rp.Spec.PipelineName).
		Str("environment", rp.Spec.Environment).
		Msg("auto-rollback: created rollback bundle via RollbackPolicy")

	return rollbackName, nil, nil
}

// existingRollback returns the name of the rollback Bundle that rolls back
// spec.bundleRef in spec.environment, or "": one created by either automatic
// path (this one or onHealthFailure=rollback), or a rollback of the Bundle
// created before the shared planner existed (no kardinal.io/rollback-from
// annotation, the failing Bundle in provenance.rollbackOf).
func (r *Reconciler) existingRollback(ctx context.Context, rp *v1alpha1.RollbackPolicy) (string, error) {
	var bundles v1alpha1.BundleList
	if err := r.List(ctx, &bundles, client.InNamespace(rp.Namespace)); err != nil {
		return "", fmt.Errorf("list bundles: %w", err)
	}
	for _, b := range bundles.Items {
		if b.Labels[lifecycle.LabelRollback] == "true" &&
			b.Annotations[lifecycle.AnnotationRollbackFrom] == "" &&
			b.Spec.Provenance != nil &&
			b.Spec.Provenance.RollbackOf == rp.Spec.BundleRef {
			return b.Name, nil
		}
	}
	existing, err := lifecycle.FindRollback(ctx, r.Client, rp.Namespace,
		rp.Spec.PipelineName, rp.Spec.Environment, rp.Spec.BundleRef)
	if err != nil {
		return "", fmt.Errorf("find rollback of bundle %s: %w", rp.Spec.BundleRef, err)
	}
	return existing, nil
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
// The watches cover every input of the threshold check, so a policy is not
// polled:
//   - RollbackPolicy spec or annotation changes (failureThreshold, bundleRef,
//     environment, pipelineName). Its own status writes do not trigger a
//     reconcile, and only this reconciler writes them.
//   - PromotionStep create, update and delete, mapped by policiesForStep on
//     the step's Bundle, pipeline and environment. On update both the old and
//     the new step are mapped, so a change of
//     status.consecutiveHealthFailures, or a step moving away, enqueues.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.RollbackPolicy{}, builder.WithPredicates(eventfilter.SpecOrAnnotationChanged)).
		Watches(&v1alpha1.PromotionStep{}, handler.EnqueueRequestsFromMapFunc(r.policiesForStep))
	return shard.Active().Complete(b, tracing.WrapReconciler("rollbackpolicy", r), &v1alpha1.RollbackPolicyList{})
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
		// Policies are not polled: this event is lost, and the log is all
		// that shows it.
		zerolog.Ctx(ctx).Error().Err(err).
			Str("namespace", obj.GetNamespace()).Str("promotionstep", obj.GetName()).
			Msg("failed to list RollbackPolicies for PromotionStep event; the next event of the step re-evaluates them")
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
