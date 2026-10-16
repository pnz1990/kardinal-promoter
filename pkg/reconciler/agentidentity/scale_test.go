// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agentidentity_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentidentity"
)

// TestIndex_StatusesFromOneEvaluate (#1937): one index build writes every
// AgentIdentity's status, from one List and one Evaluate: a change costs one
// pass over the registry, not one per AgentIdentity (2,000 passes of 2,000
// at 2,000 agents). A second build writes nothing, and a suspension writes
// the index and the one status it concerns.
//
// Covers ACTOR-REG-02.
func TestIndex_StatusesFromOneEvaluate(t *testing.T) {
	const n = 2000
	ctx := context.Background()
	objs := make([]client.Object, 0, n)
	for i := 0; i < n; i++ {
		objs = append(objs, id(fmt.Sprintf("agent-%04d", i), i, "alice", sa("ci", fmt.Sprintf("agent-%04d", i))))
	}
	// Two AgentIdentities claim one subject: the newer is not ready.
	objs = append(objs, id("late", n+1, "bob", sa("ci", "agent-0000")))
	var lists, patches atomic.Int32
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.AgentIdentity{}, &v1alpha1.AgentIndex{}).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, l client.ObjectList, opts ...client.ListOption) error {
				if _, ok := l.(*v1alpha1.AgentIdentityList); ok {
					lists.Add(1)
				}
				return c.List(ctx, l, opts...)
			},
			SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch,
				opts ...client.SubResourcePatchOption) error {
				patches.Add(1)
				return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
			}}).Build()
	r := &agentidentity.IndexReconciler{Client: c, Name: "kardinal-promoter"}

	start := time.Now()
	_, err := r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	t.Logf("one build of %d identities: %s, %d lists, %d status patches", n+1, time.Since(start), lists.Load(), patches.Load())
	assert.EqualValues(t, 1, lists.Load(), "one List per build")
	assert.EqualValues(t, n+3, patches.Load(), "the index, every AgentIdentity once, and StatusesWritten")

	var l v1alpha1.AgentIdentityList
	require.NoError(t, c.List(ctx, &l))
	for _, it := range l.Items {
		if it.Name == "late" {
			assert.False(t, it.Status.Ready)
			assert.Contains(t, it.Status.Reason, "already agent agent-0000")
			continue
		}
		require.True(t, it.Status.Ready, it.Name)
		require.EqualValues(t, 1, it.Status.ObservedGeneration, it.Name)
	}

	// Idempotent: a second build writes nothing.
	patches.Store(0)
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	assert.EqualValues(t, 0, patches.Load())

	// A suspension: the index and that one status.
	var a v1alpha1.AgentIdentity
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "agent-0042"}, &a))
	a.Spec.Suspended = true
	a.Generation = 2
	require.NoError(t, c.Update(ctx, &a))
	patches.Store(0)
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	assert.EqualValues(t, 3, patches.Load(), "the index, agent-0042, and StatusesWritten")
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "agent-0042"}, &a))
	assert.EqualValues(t, 2, a.Status.ObservedGeneration, "its status observes the change")
	var idx v1alpha1.AgentIndex
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "kardinal-promoter"}, &idx))
	assert.Equal(t, "suspended", idx.Status.Users["system:serviceaccount:ci:agent-0042"].Blocked)
}

// TestIndex_IndexBeforeStatuses: a build writes the index first, then the
// changed statuses, then one StatusesWritten condition on the index. A
// suspension is in the index (where admission reads it) before any status
// patch of the build completes: 2,000 status patches at the client's rate
// limit must not delay it. The condition write is the AgentIndex event that
// wakes the agent.* gates once the statuses they read are written (#1869).
//
// Covers ACTOR-REG-02.
func TestIndex_IndexBeforeStatuses(t *testing.T) {
	ctx := context.Background()
	var order []string
	suspendedFirst := true
	c := fake.NewClientBuilder().WithScheme(scheme(t)).
		WithObjects(id("fixer", 0, "alice", sa("ci", "fixer")), id("other", 1, "alice", sa("ci", "other"))).
		WithStatusSubresource(&v1alpha1.AgentIdentity{}, &v1alpha1.AgentIndex{}).
		WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: func(ctx context.Context, cl client.Client, sub string, obj client.Object,
			patch client.Patch, opts ...client.SubResourcePatchOption) error {
			order = append(order, fmt.Sprintf("%T", obj))
			if a, ok := obj.(*v1alpha1.AgentIdentity); ok && a.Name == "fixer" && a.Spec.Suspended {
				var idx v1alpha1.AgentIndex
				require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: "kardinal-promoter"}, &idx))
				suspendedFirst = suspendedFirst && idx.Status.Users["system:serviceaccount:ci:fixer"].Blocked == "suspended"
			}
			return cl.SubResource(sub).Patch(ctx, obj, patch, opts...)
		}}).Build()
	r := &agentidentity.IndexReconciler{Client: c, Name: "kardinal-promoter"}
	_, err := r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	assert.Equal(t, []string{"*v1alpha1.AgentIndex", "*v1alpha1.AgentIdentity", "*v1alpha1.AgentIdentity", "*v1alpha1.AgentIndex"}, order)
	var idx v1alpha1.AgentIndex
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "kardinal-promoter"}, &idx))
	assert.True(t, meta.IsStatusConditionTrue(idx.Status.Conditions, agentidentity.ConditionStatusesWritten))

	// Suspend: the index has the block before fixer's status patch runs.
	var fixer v1alpha1.AgentIdentity
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "fixer"}, &fixer))
	fixer.Spec.Suspended = true
	fixer.Generation++
	require.NoError(t, c.Update(ctx, &fixer))
	order = nil
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	assert.Equal(t, []string{"*v1alpha1.AgentIndex", "*v1alpha1.AgentIdentity", "*v1alpha1.AgentIndex"}, order)
	assert.True(t, suspendedFirst, "the suspension is in the index before any status patch")

	// Nothing changed: no write at all.
	order = nil
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	assert.Empty(t, order)
}

// TestIndex_StatusWritesSurvive: an AgentIdentity deleted during a build is
// skipped, the others' statuses are written; a status patch from a stale
// read gets a Conflict (optimistic lock) instead of writing an older verdict.
func TestIndex_StatusWritesSurvive(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(scheme(t)).
		WithObjects(id("gone", 0, "alice", sa("ci", "gone")), id("kept", 1, "alice", sa("ci", "kept"))).
		WithStatusSubresource(&v1alpha1.AgentIdentity{}, &v1alpha1.AgentIndex{}).
		WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: func(ctx context.Context, cl client.Client, sub string, obj client.Object,
			patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if obj.GetName() == "gone" {
				// Deleted between the List and its status patch.
				require.NoError(t, cl.Delete(ctx, obj))
			}
			return cl.SubResource(sub).Patch(ctx, obj, patch, opts...)
		}}).Build()
	_, err := (&agentidentity.IndexReconciler{Client: c, Name: "kardinal-promoter"}).Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err, "a deleted identity is skipped, not an aborted build")
	var kept v1alpha1.AgentIdentity
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "kept"}, &kept))
	assert.True(t, kept.Status.Ready)

	// A stale read: the stored identity is newer than the one the patch is
	// based on.
	stale := kept.DeepCopy()
	kept.Spec.Suspended = true
	require.NoError(t, c.Update(ctx, &kept))
	stale.Status.Ready = false // so the build would write it
	err = agentidentity.WriteStatusesForTest(ctx, c, []v1alpha1.AgentIdentity{*stale})
	require.Error(t, err)
	assert.True(t, apierrors.IsConflict(err), "%v", err)
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "kept"}, &kept))
	assert.True(t, kept.Spec.Suspended, "the newer object stands")
}
