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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentidentity"
)

func scheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
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
	r := &agentidentity.Reconciler{Client: c}
	for _, n := range []string{"fixer", "late"} {
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKey{Name: n}})
		require.NoError(t, err)
	}
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
	for _, name := range []string{"fixer", "late"} {
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKey{Name: name}})
		require.NoError(t, err)
	}
	assert.Equal(t, n, patches.Load(), "a second reconcile patches nothing")

	// Deleting the older one hands the subject to the newer one.
	require.NoError(t, c.Delete(ctx, &fixer))
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKey{Name: "late"}})
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "late"}, &late))
	assert.True(t, late.Status.Ready)

	// A deleted AgentIdentity is not an error.
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKey{Name: "fixer"}})
	require.NoError(t, err)
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
	require.Len(t, idx.Status.Conditions, 1)
	assert.Equal(t, metav1.ConditionTrue, idx.Status.Conditions[0].Status)

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

	ir := &agentidentity.Reconciler{Client: c, MaxIndexBytes: 150}
	_, err = ir.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKey{Name: "b"}})
	require.NoError(t, err)
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
