// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/shard"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
)

// AnnotationGraphRetireAfter on a Pipeline replaces every delay of the
// controller's RetirePolicy for the Pipeline's Bundles. A Go duration; "0"
// keeps their Graphs.
const AnnotationGraphRetireAfter = "kardinal.io/graph-retire-after"

// Reasons of the GraphRetired condition.
const (
	retireReasonScheduled = "Scheduled"
	retireReasonWaiting   = "WaitingForSteps"
	retireReasonRetiring  = "Retiring"
	retireReasonRetired   = "Retired"
	retireReasonTooMany   = "TooManySteps"
)

// requeueRetireWait is how often a Bundle due to retire is checked again
// while one of its steps is still settling.
const requeueRetireWait = time.Minute

// RetirePolicy says how long the Graph of a finished Bundle is kept before
// it is retired: deleted, with the Bundle's PromotionSteps kept as
// status.retiredSteps (#1492). kro holds every Graph it reconciles in memory,
// so keeping the Graph of every finished Bundle until historyLimit prunes the
// Bundle runs kro out of memory at a few hundred Graphs. A zero delay keeps
// the Graphs of that kind of Bundle.
type RetirePolicy struct {
	// Superseded applies to a Superseded Bundle, and to a Verified one that a
	// newer Bundle replaced in every environment it promoted.
	Superseded time.Duration
	// Failed applies to a Failed Bundle that no newer Bundle replaced in
	// every environment it touched (one that was replaced gets Superseded).
	// A retired Failed Bundle no longer recovers, and a Pipeline change does
	// not rebuild it.
	Failed time.Duration
	// Verified applies to a Verified Bundle that is still deployed in an
	// environment.
	Verified time.Duration
}

// DefaultRetirePolicy is the controller default.
var DefaultRetirePolicy = RetirePolicy{
	// Nothing reads a Superseded Bundle's Graph once its steps have settled;
	// the delay is a grace period. At a few Bundles a second, each minute of
	// it is a hundred Graphs more in kro.
	Superseded: time.Minute,
	Failed:     24 * time.Hour,
	Verified:   time.Hour,
}

// graphDeleter deletes a Graph (graph.GraphClient).
type graphDeleter interface {
	Delete(ctx context.Context, namespace, name string) error
}

// retireDelay returns how long b's Graph is kept after b finished, whether b
// is finished at all, and, when the Pipeline's AnnotationGraphRetireAfter is
// not a non-negative duration, a note saying it is ignored. pipeline may be
// nil.
func (r *Reconciler) retireDelay(ctx context.Context, log zerolog.Logger, b *kardinalv1alpha1.Bundle,
	pipeline *kardinalv1alpha1.Pipeline) (d time.Duration, finished bool, badAnnotation string, err error) {
	switch b.Status.Phase {
	case phaseSuperseded:
		d = r.Retire.Superseded
	case phaseFailed:
		if b.Status.GraphRef == "" {
			return 0, false, "", nil // no Graph to retire
		}
		// A newer Bundle Verified in every environment this one touched has
		// taken its place: nobody rolls it back or resumes it any more.
		replaced, err := r.replacedEverywhere(ctx, b, false)
		if err != nil {
			return 0, false, "", err
		}
		d = r.Retire.Failed
		if replaced {
			d = r.Retire.Superseded
		}
	case phaseVerified:
		replaced, err := r.replacedEverywhere(ctx, b, true)
		if err != nil {
			return 0, false, "", err
		}
		d = r.Retire.Verified
		if replaced {
			d = r.Retire.Superseded
		}
	default:
		return 0, false, "", nil
	}
	if pipeline != nil {
		if v, ok := pipeline.Annotations[AnnotationGraphRetireAfter]; ok {
			pd, err := time.ParseDuration(v)
			if err != nil || pd < 0 {
				log.Warn().Str("annotation", AnnotationGraphRetireAfter).Str("value", v).
					Msg("ignoring an invalid Graph retirement delay on the Pipeline")
				badAnnotation = fmt.Sprintf("the Pipeline's %s=%q is ignored: it is not a non-negative Go duration",
					AnnotationGraphRetireAfter, v)
			} else {
				d = pd
			}
		}
	}
	return d, true, badAnnotation, nil
}

// replacedEverywhere reports whether, in every environment b promoted
// (verifiedOnly: those where it is Verified; otherwise every environment in
// its status.environments, the ones a Failed Bundle touched), a newer Bundle
// of the same type is Verified, so b is deployed nowhere. A Bundle that
// touched no environment is not replaced.
func (r *Reconciler) replacedEverywhere(ctx context.Context, b *kardinalv1alpha1.Bundle, verifiedOnly bool) (bool, error) {
	siblings, err := r.pipelineBundleList(ctx, b.Namespace, b.Spec.Pipeline)
	if err != nil {
		return false, err
	}
	touched := 0
	for _, env := range b.Status.Environments {
		if verifiedOnly && env.Phase != "Verified" {
			continue
		}
		touched++
		replaced := false
		for i := range siblings {
			s := &siblings[i]
			if s.Name == b.Name || s.Spec.Type != b.Spec.Type || lifecycle.CompareCreation(s, b) <= 0 {
				continue
			}
			for _, se := range s.Status.Environments {
				if se.Name == env.Name && se.Phase == "Verified" {
					replaced = true
					break
				}
			}
			if replaced {
				break
			}
		}
		if !replaced {
			return false, nil
		}
	}
	return touched > 0 || verifiedOnly, nil
}

// retire retires the Graph of a finished Bundle once its delay has passed
// (RetirePolicy). It returns the requeue for the next check.
//
// The GraphRetired condition carries the state. It is Unknown/WaitingForSteps
// while the Bundle is finished but a PromotionStep has not settled (is not
// Verified or Failed, or still holds its PR finalizer), and False/Scheduled
// once every step has; that transition's lastTransitionTime starts the delay.
// When the delay has passed, one status write records the steps in
// status.retiredSteps and sets True/Retiring; the Graph is deleted, and the
// condition becomes True/Retired. kro then deletes the Graph's PromotionSteps,
// PolicyGate instances and PRStatuses (its finalizer, from
// status.managedResources). A Bundle that is no longer finished (a Failed one
// that recovered) loses the Scheduled condition, so its delay starts again.
//
// Graph-first: the Bundle reconciler writes only its own status and deletes
// only the Graph it owns. time.Now() is read for this status write.
func (r *Reconciler) retire(ctx context.Context, log zerolog.Logger, b *kardinalv1alpha1.Bundle,
	pipeline *kardinalv1alpha1.Pipeline) (ctrl.Result, error) {
	if lifecycle.Retired(b) {
		return ctrl.Result{}, r.finishRetire(ctx, log, b)
	}
	cond := meta.FindStatusCondition(b.Status.Conditions, lifecycle.ConditionGraphRetired)
	delay, finished, badAnnotation, err := r.retireDelay(ctx, log, b, pipeline)
	if err != nil {
		return ctrl.Result{}, err
	}
	note := ""
	if badAnnotation != "" {
		note = "; " + badAnnotation
	}
	if !finished || delay == 0 {
		if cond != nil {
			before := b.DeepCopy()
			meta.RemoveStatusCondition(&b.Status.Conditions, lifecycle.ConditionGraphRetired)
			return ctrl.Result{}, r.patchStatus(ctx, b, before)
		}
		return ctrl.Result{}, nil
	}
	var list kardinalv1alpha1.PromotionStepList
	if err := r.List(ctx, &list, client.InNamespace(b.Namespace),
		client.MatchingLabels{lifecycle.LabelBundle: b.Name}); err != nil {
		return ctrl.Result{}, fmt.Errorf("list promotion steps for bundle %s: %w", b.Name, err)
	}
	busy, aborted := unsettledStep(list.Items)
	// A step stopped by a health alarm waits for a person (resume or
	// rollback) unless the Bundle was superseded: keep it as long as a
	// Failed Bundle.
	if aborted && b.Status.Phase != phaseSuperseded && r.Retire.Failed > delay {
		delay = r.Retire.Failed
	}
	if busy != "" {
		// Unknown until every step has settled; the delay starts when they
		// have (the transition to False), so a step read just after it
		// settles is still there.
		before := b.DeepCopy()
		if setBundleCondition(b, lifecycle.ConditionGraphRetired, metav1.ConditionUnknown, retireReasonWaiting,
			fmt.Sprintf("the Graph is retired %s after every PromotionStep has settled; %s has not%s", delay, busy, note)) {
			r.badDelayEvent(b, badAnnotation)
			if err := r.patchStatus(ctx, b, before); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: requeueRetireWait}, nil
	}
	records := make([]kardinalv1alpha1.RetiredStep, 0, len(list.Items)+len(b.Status.RetiredSteps))
	live := make(map[string]bool, len(list.Items))
	for i := range list.Items {
		records = append(records, lifecycle.RetiredStepOf(&list.Items[i]))
		live[list.Items[i].Name] = true
	}
	// Records written before the retirement (a fleet target removed while
	// the Bundle promoted, keepRemovedFleetTargets) stay.
	for _, rs := range b.Status.RetiredSteps {
		if !live[rs.Name] {
			records = append(records, rs)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	if why := tooManySteps(records); why != "" {
		// Kept for good: the records would not fit in the Bundle. Written
		// once: every later reconcile finds the same condition and writes
		// nothing.
		before := b.DeepCopy()
		if !setBundleCondition(b, lifecycle.ConditionGraphRetired, metav1.ConditionFalse, retireReasonTooMany,
			why+"; the Graph is kept") {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, r.patchStatus(ctx, b, before)
	}

	msg := fmt.Sprintf("every PromotionStep has settled; the Graph is deleted in %s and the steps are kept in "+
		"status.retiredSteps%s", delay, note)
	scheduled := cond != nil && cond.Status == metav1.ConditionFalse
	if !scheduled || cond.Message != msg {
		// A changed message (the delay or the annotation changed) keeps the
		// delay's start: the status stays False.
		before := b.DeepCopy()
		if setBundleCondition(b, lifecycle.ConditionGraphRetired, metav1.ConditionFalse, retireReasonScheduled, msg) {
			r.badDelayEvent(b, badAnnotation)
		}
		if err := r.patchStatus(ctx, b, before); err != nil {
			return ctrl.Result{}, err
		}
		if !scheduled {
			return ctrl.Result{RequeueAfter: delay}, nil
		}
		cond = meta.FindStatusCondition(b.Status.Conditions, lifecycle.ConditionGraphRetired)
	}

	// time.Now() here decides a status write of this Bundle.
	if left := delay - time.Since(cond.LastTransitionTime.Time); left > 0 {
		return ctrl.Result{RequeueAfter: left}, nil
	}

	before := b.DeepCopy()
	b.Status.RetiredSteps = records
	now := metav1.Now()
	b.Status.RetiredAt = &now
	setBundleCondition(b, lifecycle.ConditionGraphRetired, metav1.ConditionTrue, retireReasonRetiring,
		fmt.Sprintf("deleting the Graph; %d PromotionStep(s) kept in status.retiredSteps", len(records)))
	if err := r.patchStatus(ctx, b, before); err != nil {
		return ctrl.Result{}, err
	}
	log.Info().Int("steps", len(records)).Str("phase", b.Status.Phase).Dur("after", delay).
		Msg("retiring the Graph of a finished bundle")
	return ctrl.Result{}, r.finishRetire(ctx, log, b)
}

// tooManySteps says why records do not fit in status.retiredSteps: more than
// maxRetiredSteps, or more than maxRetiredBytes marshalled. "" when they fit.
func tooManySteps(records []kardinalv1alpha1.RetiredStep) string {
	if len(records) > maxRetiredSteps {
		return fmt.Sprintf("the Bundle has %d PromotionSteps, more than status.retiredSteps holds (%d)",
			len(records), maxRetiredSteps)
	}
	raw, err := json.Marshal(records)
	if err != nil || len(raw) > maxRetiredBytes {
		return fmt.Sprintf("the records of the Bundle's %d PromotionSteps take %d bytes, more than %d",
			len(records), len(raw), maxRetiredBytes)
	}
	return ""
}

// maxRetiredSteps is the most records status.retiredSteps holds (its CRD
// maxItems), and maxRetiredBytes the most bytes they may take: a Bundle is
// one etcd object (1.5 MiB).
const (
	maxRetiredSteps = 1000
	maxRetiredBytes = 512 * 1024
)

// finishRetire deletes the Graph of a retired Bundle and marks the condition
// Retired. It is a no-op once the condition says Retired.
func (r *Reconciler) finishRetire(ctx context.Context, log zerolog.Logger, b *kardinalv1alpha1.Bundle) error {
	cond := meta.FindStatusCondition(b.Status.Conditions, lifecycle.ConditionGraphRetired)
	name := b.Status.GraphRef
	if name == "" {
		name = graph.GraphNameFrom(b.Spec.Pipeline, b.Name)
	}
	if cond != nil && cond.Status == metav1.ConditionTrue && cond.Reason == retireReasonRetired {
		// Retired already. A Graph that exists again was created by a
		// translation that read the Bundle before it was retired (the Graph
		// watch brought us here): delete it too.
		if r.GraphChecker == nil {
			return nil
		}
		exists, err := r.GraphChecker.GraphExists(ctx, b.Namespace, name)
		if err != nil || !exists {
			return err
		}
		log.Info().Str("graph", name).Msg("deleting a Graph created for a bundle that is already retired")
	}
	d, ok := r.GraphChecker.(graphDeleter)
	if !ok {
		return fmt.Errorf("delete graph %s/%s of retired bundle: the Graph client cannot delete Graphs", b.Namespace, name)
	}
	if err := d.Delete(ctx, b.Namespace, name); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete graph %s/%s of retired bundle: %w", b.Namespace, name, err)
	}
	before := b.DeepCopy()
	setBundleCondition(b, lifecycle.ConditionGraphRetired, metav1.ConditionTrue, retireReasonRetired,
		fmt.Sprintf("the Graph was deleted; %d PromotionStep(s) kept in status.retiredSteps", len(b.Status.RetiredSteps)))
	if err := r.patchStatus(ctx, b, before); err != nil {
		return err
	}
	r.event(b, corev1.EventTypeNormal, "GraphRetired",
		fmt.Sprintf("Graph %s deleted; %d PromotionStep(s) kept in status.retiredSteps", name, len(b.Status.RetiredSteps)))
	log.Info().Str("graph", name).Msg("graph of a finished bundle retired")
	return nil
}

// unsettledStep returns the name and state of a step that may still act: one
// that is not in a final state, has a finalizer (it is closing its PR) or is
// being deleted. "" when every step has settled. The final states are
// Verified, Failed, RollingBack (the rollback is a Bundle of its own) and
// AbortedByAlarm (a person resumes or rolls back; aborted reports one, so the
// caller keeps the Graph as long as a Failed Bundle's).
func unsettledStep(steps []kardinalv1alpha1.PromotionStep) (busy string, aborted bool) {
	for i := range steps {
		s := &steps[i]
		switch {
		case s.DeletionTimestamp != nil:
			return fmt.Sprintf("PromotionStep %s (being deleted)", s.Name), aborted
		case len(s.Finalizers) > 0:
			return fmt.Sprintf("PromotionStep %s (finalizer %s)", s.Name, s.Finalizers[0]), aborted
		}
		switch s.Status.State {
		case "Verified", "Failed", "RollingBack":
		case "AbortedByAlarm":
			aborted = true
		default:
			state := s.Status.State
			if state == "" {
				state = "not started"
			}
			return fmt.Sprintf("PromotionStep %s (%s)", s.Name, state), aborted
		}
	}
	return "", aborted
}

// badDelayEvent emits a Warning Event naming an ignored retirement delay
// annotation. note is retireDelay's badAnnotation; "" emits nothing.
func (r *Reconciler) badDelayEvent(b *kardinalv1alpha1.Bundle, note string) {
	if note != "" {
		r.event(b, corev1.EventTypeWarning, "InvalidRetireDelay", note)
	}
}

// patchStatus merge-patches b's status from before with an optimistic lock:
// the Bundle reconciler writes the same status, and a merge patch of
// status.conditions from a stale copy would drop its changes. A conflict is
// returned (apierrors.IsConflict); the caller retries with a fresh read.
func (r *Reconciler) patchStatus(ctx context.Context, b, before *kardinalv1alpha1.Bundle) error {
	err := r.Status().Patch(ctx, b, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("patch bundle status: %w", err)
	}
	return nil
}

// requeueRetireConflict is how soon retirement retries after its status
// patch lost a race with the Bundle reconciler.
const requeueRetireConflict = time.Second

// finishedPhase passes Bundle events of a finished Bundle not yet fully
// retired, the ones the retirement controller acts on.
func finishedPhase(obj client.Object) bool {
	b, ok := obj.(*kardinalv1alpha1.Bundle)
	if !ok {
		return false
	}
	switch b.Status.Phase {
	case phaseSuperseded, phaseFailed, phaseVerified:
	default:
		// A Bundle that left a finished phase (a Failed one that recovered)
		// may hold a GraphRetired condition to clear.
		return meta.FindStatusCondition(b.Status.Conditions, lifecycle.ConditionGraphRetired) != nil &&
			!lifecycle.Retired(b)
	}
	cond := meta.FindStatusCondition(b.Status.Conditions, lifecycle.ConditionGraphRetired)
	return cond == nil || cond.Reason != retireReasonRetired
}

// reconcileRetire is the retirement controller's reconcile. It runs on its
// own work queue, apart from the Bundle reconciler's, so finished Bundles
// are retired on time while the Bundle queue is long (a burst of Bundles, a
// controller that just took the leader lease and re-reads every Bundle).
func (r *Reconciler) reconcileRetire(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().Str("bundle", req.Name).Str("namespace", req.Namespace).
		Str("controller", "bundle-retire").Logger()
	var b kardinalv1alpha1.Bundle
	if err := r.Get(ctx, req.NamespacedName, &b); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	var pipeline *kardinalv1alpha1.Pipeline
	var p kardinalv1alpha1.Pipeline
	switch err := r.Get(ctx, client.ObjectKey{Namespace: b.Namespace, Name: b.Spec.Pipeline}, &p); {
	case err == nil:
		pipeline = &p
	case !apierrors.IsNotFound(err):
		return ctrl.Result{}, fmt.Errorf("get pipeline %s: %w", b.Spec.Pipeline, err)
	}
	res, err := r.retire(ctx, log, &b, pipeline)
	if apierrors.IsConflict(err) {
		return ctrl.Result{RequeueAfter: requeueRetireConflict}, nil
	}
	return res, err
}

// setupRetire registers the retirement controller: finished Bundles, the
// PromotionSteps whose settling starts a Bundle's delay, a Bundle turning
// Verified (it can replace older Verified ones everywhere, shortening their
// delay), and a change to a Pipeline's AnnotationGraphRetireAfter.
func (r *Reconciler) setupRetire(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		Named("bundle-retire").
		For(&kardinalv1alpha1.Bundle{}, builder.WithPredicates(predicate.NewPredicateFuncs(finishedPhase))).
		Watches(&kardinalv1alpha1.PromotionStep{}, handler.EnqueueRequestsFromMapFunc(bundleLabelMapper)).
		Watches(&kardinalv1alpha1.Bundle{}, handler.EnqueueRequestsFromMapFunc(r.olderVerified),
			builder.WithPredicates(becameVerified)).
		Watches(&kardinalv1alpha1.Pipeline{}, handler.EnqueueRequestsFromMapFunc(r.finishedBundles),
			builder.WithPredicates(retireAnnotationChanged)).
		Watches(graphObject(), handler.EnqueueRequestsFromMapFunc(bundleLabelMapper),
			builder.WithPredicates(predicate.Funcs{
				CreateFunc:  func(event.CreateEvent) bool { return true },
				UpdateFunc:  func(event.UpdateEvent) bool { return false },
				DeleteFunc:  func(event.DeleteEvent) bool { return false },
				GenericFunc: func(event.GenericEvent) bool { return false },
			})).
		WithOptions(controller.Options{MaxConcurrentReconciles: retireWorkers})
	// Sharded like the Bundle reconciler (#1505): a shard retires only the
	// Bundles of the namespaces it holds.
	return shard.Active().Complete(b, tracing.WrapReconciler("bundle-retire", reconcile.Func(r.reconcileRetire)),
		&kardinalv1alpha1.BundleList{})
}

// graphObject is an empty kro Graph, unstructured so kardinal does not import
// the kro module.
func graphObject() *unstructured.Unstructured {
	g := &unstructured.Unstructured{}
	g.SetGroupVersionKind(graph.GraphGVK)
	return g
}

// becameVerified passes a Bundle update that sets status.phase to Verified.
var becameVerified = predicate.Funcs{
	CreateFunc:  func(event.CreateEvent) bool { return false },
	DeleteFunc:  func(event.DeleteEvent) bool { return false },
	GenericFunc: func(event.GenericEvent) bool { return false },
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldB, okOld := e.ObjectOld.(*kardinalv1alpha1.Bundle)
		newB, okNew := e.ObjectNew.(*kardinalv1alpha1.Bundle)
		return okOld && okNew && oldB.Status.Phase != phaseVerified && newB.Status.Phase == phaseVerified
	},
}

// olderVerified maps a Bundle that turned Verified to the older Verified and
// Failed Bundles of its Pipeline and type that are not retired: it may have
// replaced them in every environment, which retires them sooner
// (RetirePolicy.Superseded instead of Verified or Failed).
func (r *Reconciler) olderVerified(ctx context.Context, obj client.Object) []reconcile.Request {
	b, ok := obj.(*kardinalv1alpha1.Bundle)
	if !ok {
		return nil
	}
	siblings, err := r.pipelineBundleList(ctx, b.Namespace, b.Spec.Pipeline)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("pipeline", b.Spec.Pipeline).Msg("olderVerified: list bundles failed")
		return nil
	}
	var reqs []reconcile.Request
	for i := range siblings {
		s := &siblings[i]
		if s.Name != b.Name && s.Spec.Type == b.Spec.Type &&
			(s.Status.Phase == phaseVerified || s.Status.Phase == phaseFailed) &&
			!lifecycle.Retired(s) && lifecycle.CompareCreation(b, s) > 0 {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(s)})
		}
	}
	return reqs
}

// retireAnnotationChanged passes a Pipeline update that changes its
// AnnotationGraphRetireAfter.
var retireAnnotationChanged = predicate.Funcs{
	CreateFunc:  func(event.CreateEvent) bool { return false },
	DeleteFunc:  func(event.DeleteEvent) bool { return false },
	GenericFunc: func(event.GenericEvent) bool { return false },
	UpdateFunc: func(e event.UpdateEvent) bool {
		return e.ObjectOld.GetAnnotations()[AnnotationGraphRetireAfter] !=
			e.ObjectNew.GetAnnotations()[AnnotationGraphRetireAfter]
	},
}

// finishedBundles maps a Pipeline to its Bundles the retirement controller
// acts on (finishedPhase).
func (r *Reconciler) finishedBundles(ctx context.Context, obj client.Object) []reconcile.Request {
	bundles, err := r.pipelineBundleList(ctx, obj.GetNamespace(), obj.GetName())
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("pipeline", obj.GetName()).Msg("finishedBundles: list bundles failed")
		return nil
	}
	var reqs []reconcile.Request
	for i := range bundles {
		if finishedPhase(&bundles[i]) {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&bundles[i])})
		}
	}
	return reqs
}

// Validate refuses a negative delay.
func (p RetirePolicy) Validate() error {
	for name, d := range map[string]time.Duration{"superseded": p.Superseded, "failed": p.Failed, "verified": p.Verified} {
		if d < 0 {
			return fmt.Errorf("graph retirement delay for %s Bundles is %s: use 0 (keep the Graphs) or a positive duration", name, d)
		}
	}
	return nil
}

// retireWorkers is the retirement controller's worker count. Each reconcile
// reads the cache and writes at most two status patches and one delete.
const retireWorkers = 4
