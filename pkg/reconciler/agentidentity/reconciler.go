// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package agentidentity reconciles the agent registry (docs/actors.md):
//
//   - Reconciler validates each AgentIdentity against all the others and
//     writes its status.ready;
//   - IndexReconciler writes the AgentIndex status that the chart's
//     <release>-actor ValidatingAdmissionPolicy reads as params.
//
// Both decide with actor.Evaluate over every AgentIdentity, so an
// AgentIdentity is ready exactly when the index lists it unblocked, and the
// same AgentIdentities always produce the same status: a re-run writes
// nothing.
package agentidentity

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
)

// ConditionReady is the condition both kinds carry.
const ConditionReady = "Ready"

// Reconciler writes AgentIdentity status.
type Reconciler struct {
	client.Client
	// MaxIndexBytes bounds the index (0: actor.MaxIndexBytes).
	MaxIndexBytes int
}

func evaluate(ctx context.Context, c client.Client, maxBytes int) (actor.Registry, error) {
	var list v1alpha1.AgentIdentityList
	if err := c.List(ctx, &list); err != nil {
		return actor.Registry{}, fmt.Errorf("list agent identities: %w", err)
	}
	if maxBytes == 0 {
		maxBytes = actor.MaxIndexBytes
	}
	return actor.Evaluate(list.Items, maxBytes), nil
}

// Reconcile writes the verdict for one AgentIdentity.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var id v1alpha1.AgentIdentity
	if err := r.Get(ctx, client.ObjectKey{Name: req.Name}, &id); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	reg, err := evaluate(ctx, r.Client, r.MaxIndexBytes)
	if err != nil {
		return reconcile.Result{}, err
	}
	v, ok := reg.Verdicts[id.Name]
	if !ok {
		return reconcile.Result{}, nil // deleted between the Get and the List
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
		return reconcile.Result{}, nil
	}
	if err := r.Status().Patch(ctx, &id, client.MergeFrom(base)); err != nil {
		return reconcile.Result{}, fmt.Errorf("patch agent identity %s status: %w", id.Name, err)
	}
	zerolog.Ctx(ctx).Info().Str("agentidentity", id.Name).Bool("ready", v.Ready).Str("reason", v.Reason).
		Msg("agent identity checked")
	return reconcile.Result{}, nil
}

// SetupWithManager watches every AgentIdentity: a change to one can change
// another's verdict (a shared subject, a sponsor that became an agent, the
// index size), so each event re-checks them all. One worker: decisions are
// made one at a time.
func (r *Reconciler) SetupWithManager(mgr manager.Manager) error {
	all := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		var list v1alpha1.AgentIdentityList
		if err := mgr.GetClient().List(ctx, &list); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("list agent identities")
			return nil
		}
		out := make([]reconcile.Request, 0, len(list.Items))
		for _, id := range list.Items {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKey{Name: id.Name}})
		}
		return out
	})
	return ctrl.ControllerManagedBy(mgr).Named("agentidentity").
		Watches(&v1alpha1.AgentIdentity{}, all).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}

// IndexReconciler writes the status of the AgentIndex named Name, and
// creates it when it is missing.
type IndexReconciler struct {
	client.Client
	Name string
	// MaxIndexBytes bounds the index (0: actor.MaxIndexBytes).
	MaxIndexBytes int
}

// Reconcile rebuilds the index.
func (r *IndexReconciler) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	var idx v1alpha1.AgentIndex
	if err := r.Get(ctx, client.ObjectKey{Name: r.Name}, &idx); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, fmt.Errorf("get agent index %s: %w", r.Name, err)
		}
		idx = v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: r.Name,
			Labels: map[string]string{"app.kubernetes.io/managed-by": "kardinal-promoter"}}}
		if err := r.Create(ctx, &idx); err != nil && !apierrors.IsAlreadyExists(err) {
			return reconcile.Result{}, fmt.Errorf("create agent index %s: %w", r.Name, err)
		}
		if err := r.Get(ctx, client.ObjectKey{Name: r.Name}, &idx); err != nil {
			return reconcile.Result{}, fmt.Errorf("get agent index %s: %w", r.Name, err)
		}
	}
	reg, err := evaluate(ctx, r.Client, r.MaxIndexBytes)
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
	if equalIndex(idx.Status, want) {
		return reconcile.Result{}, nil
	}
	idx.Status = want
	if err := r.Status().Patch(ctx, &idx, client.MergeFrom(base)); err != nil {
		return reconcile.Result{}, fmt.Errorf("patch agent index %s status: %w", r.Name, err)
	}
	zerolog.Ctx(ctx).Info().Str("agentindex", r.Name).Int("identities", reg.Index.ObservedIdentities).
		Int("users", len(want.Users)).Int("groups", len(want.Groups)).Msg("agent index written")
	return reconcile.Result{}, nil
}

// ensureIndex builds the index once at start on every replica, leader or
// not (NeedLeaderElection false): a replica is ready only once the index is
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
		_, err := e.r.Reconcile(ctx, reconcile.Request{})
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
