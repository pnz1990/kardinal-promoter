// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package agentidentity reconciles the agent registry (docs/actors.md):
// IndexReconciler writes the AgentIndex status that the chart's
// <release>-actor ValidatingAdmissionPolicy reads as params, and each
// AgentIdentity's status.ready, from one actor.Evaluate over every
// AgentIdentity per change. An AgentIdentity is ready exactly when the index
// lists it unblocked, and the same AgentIdentities always produce the same
// status: a re-run writes nothing.
package agentidentity

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/rs/zerolog"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
)

// ConditionReady is the condition both kinds carry.
const ConditionReady = "Ready"

func evaluate(ctx context.Context, c client.Client, maxBytes int) ([]v1alpha1.AgentIdentity, actor.Registry, error) {
	var list v1alpha1.AgentIdentityList
	if err := c.List(ctx, &list); err != nil {
		return nil, actor.Registry{}, fmt.Errorf("list agent identities: %w", err)
	}
	if maxBytes == 0 {
		maxBytes = actor.MaxIndexBytes
	}
	return list.Items, actor.Evaluate(list.Items, maxBytes), nil
}

// writeStatuses writes each AgentIdentity's verdict from reg, patching only
// the statuses that change (#1937): one Evaluate decides them all, so a
// change costs one pass over the registry, not one per AgentIdentity. Each
// patch carries the resourceVersion read, so a stale read gets a Conflict
// and the rebuild is retried instead of writing an older verdict.
func writeStatuses(ctx context.Context, c client.Client, ids []v1alpha1.AgentIdentity, reg actor.Registry) (int, error) {
	written := 0
	for i := range ids {
		id := &ids[i]
		v, ok := reg.Verdicts[id.Name]
		if !ok {
			continue
		}
		base := id.DeepCopy()
		cond := metav1.Condition{Type: ConditionReady, Status: metav1.ConditionTrue, Reason: v.Reason,
			Message: v.Message, ObservedGeneration: id.Generation}
		reason := ""
		if !v.Ready {
			cond.Status, reason = metav1.ConditionFalse, v.Message
		}
		changed := meta.SetStatusCondition(&id.Status.Conditions, cond)
		if id.Status.Ready != v.Ready || id.Status.Reason != reason || id.Status.ObservedGeneration != id.Generation {
			id.Status.Ready, id.Status.Reason, id.Status.ObservedGeneration = v.Ready, reason, id.Generation
			changed = true
		}
		if !changed {
			continue
		}
		err := c.Status().Patch(ctx, id, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		switch {
		case apierrors.IsNotFound(err):
			continue // deleted since the List
		case err != nil:
			return written, fmt.Errorf("patch agent identity %s status: %w", id.Name, err)
		}
		written++
		zerolog.Ctx(ctx).Info().Str("agentidentity", id.Name).Bool("ready", v.Ready).Str("reason", v.Reason).
			Msg("agent identity checked")
	}
	if written > 0 {
		zerolog.Ctx(ctx).Debug().Int("written", written).Int("identities", len(ids)).Msg("agent identity statuses written")
	}
	return written, nil
}

// IndexReconciler writes the status of the AgentIndex named Name, creating
// it when it is missing, and the status of every AgentIdentity, all from one
// actor.Evaluate per change (#1937).
type IndexReconciler struct {
	client.Client
	Name string
	// MaxIndexBytes bounds the index (0: actor.MaxIndexBytes).
	MaxIndexBytes int
	// StatusesDisabled leaves the AgentIdentity statuses alone (the
	// start-up build of every replica).
	StatusesDisabled bool
	// ChartCreated is true when the chart creates the index (Helm,
	// --agent-index-from-chart): the controller then never creates it, it
	// waits for it. Helm installs the Deployment before the AgentIndex, so
	// a controller that created the index first would fail the install with
	// "already exists" (Helm checks ownership before it starts, not at each
	// create).
	ChartCreated bool
	// Guard names the chart's <release>-agent-index ValidatingAdmissionPolicy,
	// read through Reader (uncached). Helm creates the AgentIndex before the
	// policies, so when the guard exists and the index does not, the index
	// was deleted after the install (a collection delete, or with the guard
	// unbound): the controller recreates it with the guard's Helm ownership
	// labels and annotations, so Helm adopts it on the next upgrade (#1959).
	Guard  string
	Reader client.Reader
}

// ErrIndexNotCreated: the chart has not created the index yet.
var ErrIndexNotCreated = errors.New("the agent index does not exist yet: the chart creates it " +
	"(if it was deleted after the install, helm upgrade the release to restore it)")

// chartIndex is the index as the chart would create it, from the guard
// policy's Helm ownership metadata; false when the guard does not exist (the
// install has not reached the policies yet).
func (r *IndexReconciler) chartIndex(ctx context.Context) (v1alpha1.AgentIndex, bool, error) {
	if r.Guard == "" || r.Reader == nil {
		return v1alpha1.AgentIndex{}, false, nil
	}
	var guard admissionregistrationv1.ValidatingAdmissionPolicy
	if err := r.Reader.Get(ctx, client.ObjectKey{Name: r.Guard}, &guard); err != nil {
		if apierrors.IsNotFound(err) {
			return v1alpha1.AgentIndex{}, false, nil
		}
		return v1alpha1.AgentIndex{}, false, fmt.Errorf("get the agent index guard %s: %w", r.Guard, err)
	}
	labels := map[string]string{}
	for k, v := range guard.Labels {
		labels[k] = v
	}
	ann := map[string]string{"helm.sh/resource-policy": "keep"}
	for _, k := range []string{"meta.helm.sh/release-name", "meta.helm.sh/release-namespace"} {
		if v, ok := guard.Annotations[k]; ok {
			ann[k] = v
		}
	}
	return v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: r.Name, Labels: labels, Annotations: ann}}, true, nil
}

// Reconcile rebuilds the index.
func (r *IndexReconciler) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	var idx v1alpha1.AgentIndex
	if err := r.Get(ctx, client.ObjectKey{Name: r.Name}, &idx); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, fmt.Errorf("get agent index %s: %w", r.Name, err)
		}
		if r.ChartCreated {
			chart, deleted, err := r.chartIndex(ctx)
			if err != nil {
				return reconcile.Result{}, err
			}
			if !deleted {
				// Retried with backoff, and woken by the index's own watch.
				return reconcile.Result{}, fmt.Errorf("agent index %s: %w", r.Name, ErrIndexNotCreated)
			}
			zerolog.Ctx(ctx).Warn().Str("agentindex", r.Name).
				Msg("the chart's agent index was deleted after the install: recreating it with the release's Helm ownership")
			idx = chart
		} else {
			idx = v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: r.Name,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "kardinal-promoter"}}}
		}
		if err := r.Create(ctx, &idx); err != nil && !apierrors.IsAlreadyExists(err) {
			return reconcile.Result{}, fmt.Errorf("create agent index %s: %w", r.Name, err)
		}
		if err := r.Get(ctx, client.ObjectKey{Name: r.Name}, &idx); err != nil {
			return reconcile.Result{}, fmt.Errorf("get agent index %s: %w", r.Name, err)
		}
	}
	ids, reg, err := evaluate(ctx, r.Client, r.MaxIndexBytes)
	if err != nil {
		return reconcile.Result{}, err
	}
	base := idx.DeepCopy()
	want := reg.Index
	want.Conditions = idx.Status.Conditions
	cond := metav1.Condition{Type: ConditionReady, Status: metav1.ConditionTrue, Reason: "Built",
		Message: fmt.Sprintf("%d agent identities", reg.Index.ObservedIdentities), ObservedGeneration: idx.Generation}
	if reg.Full {
		// Written empty and not Ready: the actor policy then refuses the
		// writes it judges (fail closed).
		cond.Status, cond.Reason = metav1.ConditionFalse, actor.ReasonIndexFull
		cond.Message = "the agent index is over its size bound: remove AgentIdentities or subjects; until then kardinal " +
			"refuses the writes the actor policy checks"
	}
	meta.SetStatusCondition(&want.Conditions, cond)
	want.Bytes, want.MaxBytes = int64(reg.Bytes), int64(reg.MaxBytes)
	meta.SetStatusCondition(&want.Conditions, nearFull(reg, idx.Generation))
	if equalIndex(idx.Status, want) {
		r.setIndexMetrics(reg)
		return reconcile.Result{}, r.statuses(ctx, &idx, ids, reg)
	}
	// The index first, then the statuses: admission decides with the index,
	// so a suspension or a scope change takes effect once it is written, not
	// after every changed status at the client's rate limit (2,000 patches at
	// 20 QPS are 100 s). The statuses follow; statuses wakes the gates once
	// they are written.
	idx.Status = want
	// Optimistic concurrency (#1917): a builder whose read of the index is
	// older than another builder's write (a replica's start-up build from a
	// stale cache, racing the leader's) gets a Conflict instead of undoing
	// the newer index, for example a suspension just applied.
	if err := r.Status().Patch(ctx, &idx, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return reconcile.Result{}, fmt.Errorf("patch agent index %s status: %w", r.Name, err)
	}
	r.setIndexMetrics(reg)
	zerolog.Ctx(ctx).Info().Str("agentindex", r.Name).Int("identities", reg.Index.ObservedIdentities).
		Int("users", len(want.Users)).Int("groups", len(want.Groups)).Msg("agent index written")
	r.dropManagedFields(ctx, &idx)
	return reconcile.Result{}, r.statuses(ctx, &idx, ids, reg)
}

// setIndexMetrics records the index's size once its status says it. Only
// the reconciler that keeps the index current (the leader's) does: a
// replica's start-up build (StatusesDisabled) would leave a value that no
// longer changes.
func (r *IndexReconciler) setIndexMetrics(reg actor.Registry) {
	if r.StatusesDisabled {
		return
	}
	observability.AgentIndexBytes.Set(float64(reg.Bytes))
	observability.AgentIndexMaxBytes.Set(float64(reg.MaxBytes))
}

// NearFullPercent is the share of the bound from which the AgentIndex is
// NearFull (#2076): kardinal supports up to 4,000 AgentIdentities, about 80%
// of the bound (docs/operate.md#scale-and-limits).
const NearFullPercent = 80

// ConditionNearFull is the AgentIndex condition that warns, before the
// bound refuses writes, that the index is close to it.
const ConditionNearFull = "NearFull"

// nearFull is the NearFull condition of reg: True from NearFullPercent of
// the bound, with the size, the share and how many more AgentIdentities of
// the average size fit.
func nearFull(reg actor.Registry, generation int64) metav1.Condition {
	c := metav1.Condition{Type: ConditionNearFull, Status: metav1.ConditionFalse, Reason: "HasRoom", ObservedGeneration: generation}
	if reg.MaxBytes <= 0 {
		return c
	}
	pct := 100 * reg.Bytes / reg.MaxBytes
	room := ""
	if n := reg.Index.ObservedIdentities; n > 0 && reg.Bytes > 0 && reg.Bytes < reg.MaxBytes {
		room = fmt.Sprintf("; about %d more AgentIdentities of the average size (%d bytes) fit",
			(reg.MaxBytes-reg.Bytes)*n/reg.Bytes, reg.Bytes/n)
	}
	c.Message = fmt.Sprintf("the entries take %d bytes of %d (%d%%)%s", reg.Bytes, reg.MaxBytes, pct, room)
	if pct >= NearFullPercent {
		c.Status, c.Reason = metav1.ConditionTrue, "NearFull"
		c.Message += fmt.Sprintf("; past %d bytes the index is not Ready and kardinal refuses the writes the actor "+
			"policy checks: remove AgentIdentities or subjects", reg.MaxBytes)
	}
	return c
}

// dropManagedFields clears the index's metadata.managedFields after a
// status write (#1936). The API server converts the whole param object,
// managedFields included, for every request the actor policies match, and a
// status write records a field entry for every user and group key: about as
// large as the entries themselves. Only the controller writes the index, so
// nothing reads them. A failure costs only speed: it is logged, not retried.
func (r *IndexReconciler) dropManagedFields(ctx context.Context, idx *v1alpha1.AgentIndex) {
	tracked := false
	for _, f := range idx.ManagedFields {
		tracked = tracked || f.Subresource == "status"
	}
	if !tracked {
		return
	}
	reset := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"managedFields":[{}]}}`))
	if err := r.Patch(ctx, idx, reset); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("agentindex", r.Name).Msg("clear the agent index managedFields")
	}
}

// statuses writes the AgentIdentity statuses, after the index (admission
// reads the index: a suspension does not wait for them), then wakes the
// agent.* gates, which read both, with one StatusesWritten condition on the
// index. The start-up build of a replica that does not lead
// (StatusesDisabled) leaves them to the leader.
func (r *IndexReconciler) statuses(ctx context.Context, idx *v1alpha1.AgentIndex, ids []v1alpha1.AgentIdentity,
	reg actor.Registry) error {
	if r.StatusesDisabled {
		return nil
	}
	written, err := writeStatuses(ctx, r.Client, ids, reg)
	if err != nil || written == 0 {
		return err
	}
	// Wake the agent.* gates: they re-evaluate on AgentIndex events
	// (agentIndexGateRequests) and read status.ready, which was written
	// after the index. One condition write per build, whatever the number of
	// statuses.
	base := idx.DeepCopy()
	meta.SetStatusCondition(&idx.Status.Conditions, metav1.Condition{Type: ConditionStatusesWritten,
		Status: metav1.ConditionTrue, Reason: "Written", ObservedGeneration: idx.Generation,
		Message: fmt.Sprintf("%d agent identity statuses written after index version %s", written, idx.ResourceVersion)})
	if err := r.Status().Patch(ctx, idx, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil &&
		!apierrors.IsNotFound(err) {
		return fmt.Errorf("record the agent identity statuses on index %s: %w", r.Name, err)
	}
	r.dropManagedFields(ctx, idx)
	return nil
}

// ConditionStatusesWritten is the AgentIndex condition written after a
// build changed AgentIdentity statuses, so the gates that read them
// re-evaluate.
const ConditionStatusesWritten = "StatusesWritten"

// ensureIndex builds the index once at start on every replica, leader or
// not (NeedLeaderElection false), when it has never been built: a replica is ready only once the index is
// Ready, and during a rolling upgrade the leader can be a release that does
// not know the index. Every replica writes the same status (Evaluate is
// deterministic); afterwards the leader's IndexReconciler keeps it current.
type ensureIndex struct {
	r    *IndexReconciler
	sync func(context.Context) bool
}

// NeedLeaderElection: every replica builds the index it waits for.
func (e *ensureIndex) NeedLeaderElection() bool { return false }

// Start builds the index, retrying until it succeeds or ctx ends.
func (e *ensureIndex) Start(ctx context.Context) error {
	if e.sync != nil && !e.sync(ctx) {
		return nil
	}
	for {
		// Only an index that was never built: once one is Ready, the
		// leader's IndexReconciler keeps it current, and a start-up build
		// from this replica's cache could be older than it (#1917).
		var idx v1alpha1.AgentIndex
		if err := e.r.Get(ctx, client.ObjectKey{Name: e.r.Name}, &idx); err == nil &&
			meta.IsStatusConditionTrue(idx.Status.Conditions, ConditionReady) {
			return nil
		}
		builder := *e.r
		builder.StatusesDisabled = true
		_, err := builder.Reconcile(ctx, reconcile.Request{})
		if err == nil {
			return nil
		}
		zerolog.Ctx(ctx).Warn().Err(err).Str("agentindex", e.r.Name).Msg("build the agent index; retrying")
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(5 * time.Second):
		}
	}
}

// equalIndex compares two statuses, treating nil and empty maps as equal
// (the API server drops empty maps).
func equalIndex(a, b v1alpha1.AgentIndexStatus) bool {
	norm := func(s v1alpha1.AgentIndexStatus) v1alpha1.AgentIndexStatus {
		if len(s.Users) == 0 {
			s.Users = nil
		}
		if len(s.Groups) == 0 {
			s.Groups = nil
		}
		return s
	}
	return reflect.DeepEqual(norm(a), norm(b))
}

// SetupWithManager rebuilds the index on any AgentIdentity change and on any
// change to the index itself (a deleted index is recreated), and once at
// start on every replica.
func (r *IndexReconciler) SetupWithManager(mgr manager.Manager) error {
	one := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: r.Name}}}
	})
	// Every replica, leader or not, builds the index once at start.
	if err := mgr.Add(&ensureIndex{r: r, sync: mgr.GetCache().WaitForCacheSync}); err != nil {
		return fmt.Errorf("add agent index start-up build: %w", err)
	}
	return ctrl.ControllerManagedBy(mgr).Named("agentindex").
		Watches(&v1alpha1.AgentIdentity{}, one).
		Watches(&v1alpha1.AgentIndex{}, one).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}
