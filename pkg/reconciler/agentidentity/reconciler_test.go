// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agentidentity_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentidentity"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
)

func scheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	require.NoError(t, admissionregistrationv1.AddToScheme(s))
	return s
}

func id(name string, age int, sponsor string, subjects ...v1alpha1.AgentSubject) *v1alpha1.AgentIdentity {
	return &v1alpha1.AgentIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1,
			CreationTimestamp: metav1.NewTime(time.Date(2026, 10, 1, 0, age, 0, 0, time.UTC))},
		Spec: v1alpha1.AgentIdentitySpec{Subjects: subjects, Sponsor: v1alpha1.AgentSponsor{User: sponsor},
			Scope: v1alpha1.AgentScope{Namespaces: []string{"*"}}},
	}
}

func sa(ns, name string) v1alpha1.AgentSubject {
	return v1alpha1.AgentSubject{Kind: "ServiceAccount", Namespace: ns, Name: name}
}

// countingClient counts status patches.
func countingClient(t *testing.T, patches *atomic.Int32, objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.AgentIdentity{}, &v1alpha1.AgentIndex{}).
		WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string,
			obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			patches.Add(1)
			return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
		}}).Build()
}

func TestReconcile_AgentIdentity(t *testing.T) {
	ctx := context.Background()
	var patches atomic.Int32
	c := countingClient(t, &patches,
		id("fixer", 0, "alice", sa("ci", "fixer")),
		id("late", 1, "bob", sa("ci", "fixer")),
	)
	r := &agentidentity.IndexReconciler{Client: c, Name: "kardinal-promoter"}
	_, err := r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	var fixer, late v1alpha1.AgentIdentity
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "fixer"}, &fixer))
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "late"}, &late))
	assert.True(t, fixer.Status.Ready)
	assert.Empty(t, fixer.Status.Reason)
	assert.False(t, late.Status.Ready)
	assert.Contains(t, late.Status.Reason, "already agent fixer")
	assert.Equal(t, "SubjectConflict", late.Status.Conditions[0].Reason)
	assert.EqualValues(t, 1, late.Status.ObservedGeneration)

	// Idempotent: a re-run writes nothing.
	n := patches.Load()
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	assert.Equal(t, n, patches.Load(), "a second build patches nothing")

	// Deleting the older one hands the subject to the newer one.
	require.NoError(t, c.Delete(ctx, &fixer))
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "late"}, &late))
	assert.True(t, late.Status.Ready)

	// The start-up build of a replica writes the index only.
	parked := id("parked", 2, "carol", sa("ci", "parked"))
	require.NoError(t, c.Create(ctx, parked))
	_, err = (&agentidentity.IndexReconciler{Client: c, Name: "kardinal-promoter", StatusesDisabled: true}).Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "parked"}, parked))
	assert.Empty(t, parked.Status.Conditions)
}

func TestReconcile_AgentIndex(t *testing.T) {
	ctx := context.Background()
	var patches atomic.Int32
	parked := id("parked", 2, "bob", sa("ci", "parked"))
	parked.Spec.Suspended = true
	c := countingClient(t, &patches, id("fixer", 0, "alice", sa("ci", "fixer")), parked)
	r := &agentidentity.IndexReconciler{Client: c, Name: "kardinal-promoter"}

	// The index is created when missing.
	_, err := r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	var idx v1alpha1.AgentIndex
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "kardinal-promoter"}, &idx))
	assert.Equal(t, 2, idx.Status.ObservedIdentities)
	assert.Equal(t, "fixer", idx.Status.Users["system:serviceaccount:ci:fixer"].Agent)
	assert.Equal(t, "alice", idx.Status.Users["system:serviceaccount:ci:fixer"].Sponsor)
	assert.Equal(t, "suspended", idx.Status.Users["system:serviceaccount:ci:parked"].Blocked)
	require.Len(t, idx.Status.Conditions, 3, "Ready, NearFull, and StatusesWritten after the statuses")
	assert.False(t, meta.IsStatusConditionTrue(idx.Status.Conditions, agentidentity.ConditionNearFull))
	assert.True(t, meta.IsStatusConditionTrue(idx.Status.Conditions, agentidentity.ConditionReady))
	assert.True(t, meta.IsStatusConditionTrue(idx.Status.Conditions, agentidentity.ConditionStatusesWritten))

	// Idempotent.
	n := patches.Load()
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	assert.Equal(t, n, patches.Load())

	// Unsuspend: the entry is no longer blocked.
	var p v1alpha1.AgentIdentity
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "parked"}, &p))
	p.Spec.Suspended = false
	require.NoError(t, c.Update(ctx, &p))
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "kardinal-promoter"}, &idx))
	assert.Empty(t, idx.Status.Users["system:serviceaccount:ci:parked"].Blocked)

	// A deleted index is recreated.
	require.NoError(t, c.Delete(ctx, &idx))
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "kardinal-promoter"}, &idx))
	assert.Len(t, idx.Status.Users, 2)

	// When the chart creates the index, the controller waits for it: it
	// never creates it, so a Helm install never finds it there (#1929).
	require.NoError(t, c.Delete(ctx, &idx))
	r.ChartCreated = true
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.ErrorIs(t, err, agentidentity.ErrIndexNotCreated)
	assert.True(t, apierrors.IsNotFound(c.Get(ctx, client.ObjectKey{Name: "kardinal-promoter"}, &idx)))
	require.NoError(t, c.Create(ctx, &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}}))
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err, "built once the chart created it")
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "kardinal-promoter"}, &idx))
	assert.Len(t, idx.Status.Users, 2)

	// Deleted after the install (the chart's guard policy exists): the
	// controller recreates it with the guard's Helm ownership (#1959, QA).
	guard := &admissionregistrationv1.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter-agent-index",
		Labels:      map[string]string{"app.kubernetes.io/managed-by": "Helm", "helm.sh/chart": "kardinal-promoter-0.12.0"},
		Annotations: map[string]string{"meta.helm.sh/release-name": "kp", "meta.helm.sh/release-namespace": "kardinal-system"}}}
	gc := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(guard).Build()
	require.NoError(t, c.Delete(ctx, &idx))
	r.Guard, r.Reader = "kardinal-promoter-agent-index", gc
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "kardinal-promoter"}, &idx))
	assert.Equal(t, "Helm", idx.Labels["app.kubernetes.io/managed-by"])
	assert.Equal(t, "kp", idx.Annotations["meta.helm.sh/release-name"])
	assert.Equal(t, "kardinal-system", idx.Annotations["meta.helm.sh/release-namespace"])
	assert.Equal(t, "keep", idx.Annotations["helm.sh/resource-policy"])
	assert.Len(t, idx.Status.Users, 2)
}

func TestReconcile_AgentIndexFull(t *testing.T) {
	ctx := context.Background()
	var patches atomic.Int32
	c := countingClient(t, &patches, id("a", 0, "alice", sa("ci", "a")), id("b", 1, "alice", sa("ci", "b")))
	r := &agentidentity.IndexReconciler{Client: c, Name: "k", MaxIndexBytes: 150}
	_, err := r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	var idx v1alpha1.AgentIndex
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "k"}, &idx))
	assert.Equal(t, "IndexFull", idx.Status.Conditions[0].Reason)
	assert.Equal(t, metav1.ConditionFalse, idx.Status.Conditions[0].Status, "a full index is not Ready: admission fails closed")
	assert.Empty(t, idx.Status.Users, "and is written empty, small enough to write")

	var b v1alpha1.AgentIdentity
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "b"}, &b))
	assert.False(t, b.Status.Ready)
	assert.Equal(t, "IndexFull", b.Status.Conditions[0].Reason)
}

// TestEnsureIndex: every replica builds the index at start: created when
// missing, written Ready, and idempotent.
func TestEnsureIndex(t *testing.T) {
	ctx := context.Background()
	var patches atomic.Int32
	c := countingClient(t, &patches)
	e := agentidentity.NewEnsureIndexForTest(c, "k")
	require.NoError(t, e.Start(ctx))
	var idx v1alpha1.AgentIndex
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "k"}, &idx))
	assert.True(t, meta.IsStatusConditionTrue(idx.Status.Conditions, "Ready"))
	n := patches.Load()
	require.NoError(t, e.Start(ctx), "idempotent")
	assert.Equal(t, n, patches.Load())
	assert.False(t, e.NeedLeaderElection())
}

// TestIndex_TwoBuilders (#1917): two builders race on one index. Builder A
// (a replica's build) reads the index and the AgentIdentities before a
// suspension; the suspension is applied and builder B (the leader) writes
// it; A's write, from its older read, is refused with a Conflict, and the
// index keeps the suspension. And a replica's start-up build leaves a Ready
// index alone.
func TestIndex_TwoBuilders(t *testing.T) {
	ctx := context.Background()
	s := scheme(t)
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(id("fixer", 0, "alice", sa("ci", "fixer"))).
		WithStatusSubresource(&v1alpha1.AgentIdentity{}, &v1alpha1.AgentIndex{}).Build()
	leader := &agentidentity.IndexReconciler{Client: base, Name: "k"}
	_, err := leader.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)

	suspend := func() {
		var cur v1alpha1.AgentIdentity
		require.NoError(t, base.Get(ctx, client.ObjectKey{Name: "fixer"}, &cur))
		cur.Spec.Suspended = true
		require.NoError(t, base.Update(ctx, &cur))
	}
	// A's view: its list of AgentIdentities is the one before the
	// suspension (a stale cache); between its reads and its write the
	// suspension lands and the leader writes it.
	var before v1alpha1.AgentIdentityList
	require.NoError(t, base.List(ctx, &before))
	raced := false
	stale := interceptor.NewClient(base, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if l, ok := list.(*v1alpha1.AgentIdentityList); ok {
				before.DeepCopyInto(l)
				return nil
			}
			return c.List(ctx, list, opts...)
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch,
			opts ...client.SubResourcePatchOption) error {
			if !raced {
				raced = true
				suspend()
				_, err := leader.Reconcile(ctx, reconcile.Request{})
				require.NoError(t, err)
			}
			return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
		},
	})
	// A must have something to write: make its stale view differ from the
	// stored index (a second subject only it knows about).
	before.Items[0].Spec.Subjects = append(before.Items[0].Spec.Subjects, sa("ci", "other"))
	_, err = (&agentidentity.IndexReconciler{Client: stale, Name: "k"}).Reconcile(ctx, reconcile.Request{})
	require.Error(t, err, "the stale write is refused")
	assert.True(t, apierrors.IsConflict(errors.Unwrap(err)) || strings.Contains(err.Error(), "the object has been modified"), err.Error())
	var idx v1alpha1.AgentIndex
	require.NoError(t, base.Get(ctx, client.ObjectKey{Name: "k"}, &idx))
	assert.Equal(t, "suspended", idx.Status.Users["system:serviceaccount:ci:fixer"].Blocked, "the suspension holds")

	// A replica's start-up build does not touch a Ready index.
	e := agentidentity.NewEnsureIndexForTest(stale, "k")
	require.NoError(t, e.Start(ctx))
	require.NoError(t, base.Get(ctx, client.ObjectKey{Name: "k"}, &idx))
	assert.Equal(t, "suspended", idx.Status.Users["system:serviceaccount:ci:fixer"].Blocked)
	_, has := idx.Status.Users["system:serviceaccount:ci:other"]
	assert.False(t, has, "the stale view was not written")
}

// TestReconcile_AgentIndexNearFull (#2076): the index records its entries'
// size and its bound, and is NearFull from 80% of it, with the numbers in
// the message; the leader's build exports them as kardinal_agent_index_bytes
// and kardinal_agent_index_max_bytes, a replica's start-up build does not.
// Covers ACTOR-REG-03.
func TestReconcile_AgentIndexNearFull(t *testing.T) {
	ids := []v1alpha1.AgentIdentity{*id("a", 0, "alice", sa("ci", "a")), *id("b", 1, "alice", sa("ci", "b"))}
	size := actor.Evaluate(ids, 1<<20).Bytes
	require.Positive(t, size)
	for _, tc := range []struct {
		name     string
		max      int
		nearFull metav1.ConditionStatus
		ready    metav1.ConditionStatus
		message  []string
	}{
		{"70%", size * 10 / 7, metav1.ConditionFalse, metav1.ConditionTrue, []string{"(70%)", "more AgentIdentities of the average size"}},
		{"exactly 80%", size * 100 / 80, metav1.ConditionTrue, metav1.ConditionTrue, []string{"(80%)", "past"}},
		{"85%", size * 100 / 85, metav1.ConditionTrue, metav1.ConditionTrue, []string{"(85%)", "past", "remove AgentIdentities or subjects"}},
		{"exactly at the bound", size, metav1.ConditionTrue, metav1.ConditionTrue, []string{"(100%)"}},
		{"one byte over the bound", size - 1, metav1.ConditionTrue, metav1.ConditionFalse, []string{"(100%)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var patches atomic.Int32
			c := countingClient(t, &patches, &ids[0], &ids[1])
			observability.AgentIndexBytes.Set(-1)
			r := &agentidentity.IndexReconciler{Client: c, Name: "k", MaxIndexBytes: tc.max}
			_, err := r.Reconcile(ctx, reconcile.Request{})
			require.NoError(t, err)
			var idx v1alpha1.AgentIndex
			require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "k"}, &idx))
			assert.Equal(t, int64(size), idx.Status.Bytes, "the entries' size, also when over the bound")
			assert.Equal(t, int64(tc.max), idx.Status.MaxBytes)
			nf := meta.FindStatusCondition(idx.Status.Conditions, agentidentity.ConditionNearFull)
			require.NotNil(t, nf)
			assert.Equal(t, tc.nearFull, nf.Status, nf.Message)
			for _, m := range tc.message {
				assert.Contains(t, nf.Message, m)
			}
			assert.Equal(t, tc.ready, meta.FindStatusCondition(idx.Status.Conditions, agentidentity.ConditionReady).Status)
			assert.Equal(t, float64(size), testutil.ToFloat64(observability.AgentIndexBytes))
			assert.Equal(t, float64(tc.max), testutil.ToFloat64(observability.AgentIndexMaxBytes))

			// A new leader (after a restart or a failover) whose build
			// changes nothing still sets the metrics, or both alerts would
			// stay silent until the next AgentIdentity change.
			observability.AgentIndexBytes.Set(-1)
			observability.AgentIndexMaxBytes.Set(-1)
			n := patches.Load()
			leader := &agentidentity.IndexReconciler{Client: c, Name: "k", MaxIndexBytes: tc.max}
			_, err = leader.Reconcile(ctx, reconcile.Request{})
			require.NoError(t, err)
			assert.Equal(t, n, patches.Load(), "the index is current: nothing is written")
			assert.Equal(t, float64(size), testutil.ToFloat64(observability.AgentIndexBytes), "the new leader sets the metrics")
			assert.Equal(t, float64(tc.max), testutil.ToFloat64(observability.AgentIndexMaxBytes))

			// A replica's start-up build writes the same status, not the
			// metrics: only the leader keeps them current.
			observability.AgentIndexBytes.Set(-1)
			r2 := &agentidentity.IndexReconciler{Client: countingClient(t, &patches, ids[0].DeepCopy(), ids[1].DeepCopy()),
				Name: "k", MaxIndexBytes: tc.max, StatusesDisabled: true}
			_, err = r2.Reconcile(ctx, reconcile.Request{})
			require.NoError(t, err)
			assert.Equal(t, float64(-1), testutil.ToFloat64(observability.AgentIndexBytes))
		})
	}
}

// TestIdentityChanged covers the index reconciler's AgentIdentity watch
// (docs/design/24-agent-budgets.md W0): creates, deletes, spec changes and
// changes of the status fields writeStatuses owns rebuild the index; a label,
// an annotation or another writer's status field (v0.14's budget usage) does
// not.
// Covers ACTOR-INDEX-06.
func TestIdentityChanged(t *testing.T) {
	base := id("fixer", 0, "alice", sa("ci", "fixer"))
	base.Status = v1alpha1.AgentIdentityStatus{Ready: true, ObservedGeneration: 1,
		Conditions: []metav1.Condition{{Type: agentidentity.ConditionReady, Status: metav1.ConditionTrue, Reason: "Valid",
			ObservedGeneration: 1, LastTransitionTime: metav1.Now()}}}
	edit := func(f func(*v1alpha1.AgentIdentity)) *v1alpha1.AgentIdentity {
		c := base.DeepCopy()
		f(c)
		return c
	}
	for _, tc := range []struct {
		name string
		new  *v1alpha1.AgentIdentity
		want bool
	}{
		{"nothing changed", edit(func(*v1alpha1.AgentIdentity) {}), false},
		{"a spec change (generation)", edit(func(a *v1alpha1.AgentIdentity) { a.Generation = 2; a.Spec.Suspended = true }), true},
		{"a label", edit(func(a *v1alpha1.AgentIdentity) { a.Labels = map[string]string{"team": "a"} }), false},
		{"an annotation", edit(func(a *v1alpha1.AgentIdentity) { a.Annotations = map[string]string{"note": "x"} }), false},
		{"another writer's condition", edit(func(a *v1alpha1.AgentIdentity) {
			a.Status.Conditions = append(a.Status.Conditions, metav1.Condition{Type: "BudgetCurrent", Status: metav1.ConditionTrue, Reason: "Evaluated"})
		}), false},
		{"status.ready", edit(func(a *v1alpha1.AgentIdentity) { a.Status.Ready = false }), true},
		{"status.reason", edit(func(a *v1alpha1.AgentIdentity) { a.Status.Reason = "tampered" }), true},
		{"status.observedGeneration", edit(func(a *v1alpha1.AgentIdentity) { a.Status.ObservedGeneration = 0 }), true},
		{"the Ready condition", edit(func(a *v1alpha1.AgentIdentity) { a.Status.Conditions[0].Status = metav1.ConditionFalse }), true},
		{"the Ready condition's reason alone", edit(func(a *v1alpha1.AgentIdentity) { a.Status.Conditions[0].Reason = "Tampered" }), true},
		{"the Ready condition's message alone", edit(func(a *v1alpha1.AgentIdentity) { a.Status.Conditions[0].Message = "tampered" }), true},
		{"the Ready condition's observedGeneration alone", edit(func(a *v1alpha1.AgentIdentity) { a.Status.Conditions[0].ObservedGeneration = 0 }), true},
		{"the Ready condition removed", edit(func(a *v1alpha1.AgentIdentity) { a.Status.Conditions = nil }), true},
		{"the Ready condition's transition time alone", edit(func(a *v1alpha1.AgentIdentity) {
			a.Status.Conditions[0].LastTransitionTime = metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		}), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, agentidentity.IdentityChanged.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: tc.new}))
		})
	}
	assert.True(t, agentidentity.IdentityChanged.Create(event.CreateEvent{Object: base}), "a create")
	assert.True(t, agentidentity.IdentityChanged.Delete(event.DeleteEvent{Object: base}), "a delete")
	assert.True(t, agentidentity.IdentityChanged.Generic(event.GenericEvent{Object: base}), "a generic event")
}
