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
		cond.Reason, cond.Message = actor.ReasonIndexFull, "the index is full: some AgentIdentities are left out (their status says which)"
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

// ensureIndex creates the AgentIndex name when it is missing, on every
// replica (NeedLeaderElection false). It writes no status: the leader's
// IndexReconciler does.
type ensureIndex struct {
	c    client.Reader
	w    client.Writer
	name string
}

// NeedLeaderElection: every replica creates the index it waits for.
func (e *ensureIndex) NeedLeaderElection() bool { return false }

// Start creates the index, retrying until it exists or ctx ends.
func (e *ensureIndex) Start(ctx context.Context) error {
	for {
		err := e.ensure(ctx)
		if err == nil {
			return nil
		}
		zerolog.Ctx(ctx).Warn().Err(err).Str("agentindex", e.name).Msg("create the agent index; retrying")
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(5 * time.Second):
		}
	}
}

func (e *ensureIndex) ensure(ctx context.Context) error {
	var idx v1alpha1.AgentIndex
	err := e.c.Get(ctx, client.ObjectKey{Name: e.name}, &idx)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get agent index %s: %w", e.name, err)
	}
	idx = v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: e.name,
		Labels: map[string]string{"app.kubernetes.io/managed-by": "kardinal-promoter"}}}
	if err := e.w.Create(ctx, &idx); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create agent index %s: %w", e.name, err)
	}
	return nil
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
// start.
func (r *IndexReconciler) SetupWithManager(mgr manager.Manager) error {
	one := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: r.Name}}}
	})
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		if !mgr.GetCache().WaitForCacheSync(ctx) {
			return nil
		}
		_, err := r.Reconcile(ctx, reconcile.Request{})
		if err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("build agent index at start")
		}
		return nil
	})); err != nil {
		return fmt.Errorf("add agent index start-up build: %w", err)
	}
	// Every replica, leader or not, creates the index when it is missing:
	// a replica is ready only once it exists, and during a rolling upgrade
	// the leader can be a release that does not know the index.
	if err := mgr.Add(&ensureIndex{c: mgr.GetAPIReader(), w: mgr.GetClient(), name: r.Name}); err != nil {
		return fmt.Errorf("add agent index creation: %w", err)
	}
	return ctrl.ControllerManagedBy(mgr).Named("agentindex").
		Watches(&v1alpha1.AgentIdentity{}, one).
		Watches(&v1alpha1.AgentIndex{}, one).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}
