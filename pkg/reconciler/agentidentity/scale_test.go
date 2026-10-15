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
	assert.EqualValues(t, n+2, patches.Load(), "the index and every AgentIdentity, once")

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
	assert.EqualValues(t, 2, patches.Load(), "the index and agent-0042")
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "agent-0042"}, &a))
	assert.EqualValues(t, 2, a.Status.ObservedGeneration, "its status observes the change")
	var idx v1alpha1.AgentIndex
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "kardinal-promoter"}, &idx))
	assert.Equal(t, "suspended", idx.Status.Users["system:serviceaccount:ci:agent-0042"].Blocked)
}

// TestIndex_StatusesBeforeIndex: a build writes the AgentIdentity statuses
// before the index. The PolicyGate reconciler re-evaluates agent.* gates on
// an AgentIndex event and reads the AgentIdentity's status.ready, so an index
// written first woke the gates while the agent still looked unregistered,
// and they stayed blocked until their recheck (found on e2e-live,
// TestCLI_MCP, with #1869).
//
// Covers ACTOR-REG-02.
func TestIndex_StatusesBeforeIndex(t *testing.T) {
	var order []string
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(id("fixer", 0, "alice", sa("ci", "fixer"))).
		WithStatusSubresource(&v1alpha1.AgentIdentity{}, &v1alpha1.AgentIndex{}).
		WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object,
			patch client.Patch, opts ...client.SubResourcePatchOption) error {
			order = append(order, fmt.Sprintf("%T", obj))
			return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
		}}).Build()
	_, err := (&agentidentity.IndexReconciler{Client: c, Name: "kardinal-promoter"}).Reconcile(context.Background(), reconcile.Request{})
	require.NoError(t, err)
	assert.Equal(t, []string{"*v1alpha1.AgentIdentity", "*v1alpha1.AgentIndex"}, order)
}
