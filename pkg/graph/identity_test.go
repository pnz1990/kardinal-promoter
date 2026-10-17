// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

func identityScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	return s
}

func refGraph(ns string, refNamespaces ...string) *graph.Graph {
	g := &graph.Graph{ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: ns}}
	g.Spec.Nodes = append(g.Spec.Nodes, graph.GraphNode{ID: "step", Template: map[string]interface{}{}})
	for _, rns := range refNamespaces {
		md := map[string]interface{}{"name": "x"}
		if rns != "" {
			md["namespace"] = rns
		}
		g.Spec.Nodes = append(g.Spec.Nodes, graph.GraphNode{
			ID:  "ref" + rns,
			Ref: map[string]interface{}{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": md},
		})
	}
	return g
}

// TestIdentityProvisioner_Ensure: the Graph ServiceAccount and the applier
// RoleBinding in the Graph's namespace, idempotently. A ref into another
// namespace binds nothing there: Graphs have no health ref nodes, so there is
// no reader role to bind (#1283).
func TestIdentityProvisioner_Ensure(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(identityScheme(t)).Build()
	p := &graph.IdentityProvisioner{Writer: c, Reader: c}
	g := refGraph("default", "argocd", "")

	require.NoError(t, p.Ensure(ctx, g))
	require.NoError(t, p.Ensure(ctx, g), "idempotent")

	var sa corev1.ServiceAccount
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "default", Name: graph.DefaultGraphServiceAccount}, &sa))
	assert.Equal(t, graph.DefaultGraphServiceAccount, p.ServiceAccount())

	want := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: graph.DefaultGraphServiceAccount, Namespace: "default"}
	var applier rbacv1.RoleBinding
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "default", Name: graph.DefaultApplierClusterRole}, &applier))
	assert.Equal(t, graph.DefaultApplierClusterRole, applier.RoleRef.Name)
	assert.Equal(t, "ClusterRole", applier.RoleRef.Kind)
	assert.Equal(t, []rbacv1.Subject{want}, applier.Subjects)
	assert.Empty(t, applier.Annotations)

	var all rbacv1.RoleBindingList
	require.NoError(t, c.List(ctx, &all))
	require.Len(t, all.Items, 1, "the applier binding only")
}

// TestIdentityProvisioner_RepairsSubjects verifies that a binding the
// controller created is repaired.
func TestIdentityProvisioner_RepairsSubjects(t *testing.T) {
	ctx := context.Background()
	stale := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: graph.DefaultApplierClusterRole, Namespace: "default",
			Labels: map[string]string{"app.kubernetes.io/managed-by": "kardinal-promoter"}},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: graph.DefaultApplierClusterRole},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "old", Namespace: "default"}},
	}
	c := fake.NewClientBuilder().WithScheme(identityScheme(t)).WithObjects(stale).Build()
	p := &graph.IdentityProvisioner{Writer: c, Reader: c, ServiceAccountName: "custom"}

	require.NoError(t, p.Ensure(ctx, refGraph("default")))

	var rb rbacv1.RoleBinding
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(stale), &rb))
	require.Len(t, rb.Subjects, 1)
	assert.Equal(t, "custom", rb.Subjects[0].Name)
}

// TestIdentityProvisioner_LeavesUnmanagedBindings verifies that an applier
// RoleBinding the controller did not create is never rewritten: accepted as
// is when it already grants the role, and otherwise reported (C01-graph-19 c).
func TestIdentityProvisioner_LeavesUnmanagedBindings(t *testing.T) {
	ours := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: graph.DefaultGraphServiceAccount, Namespace: "team-a"}
	admin := rbacv1.Subject{Kind: rbacv1.UserKind, Name: "alice", APIGroup: rbacv1.GroupName}
	unmanaged := func(subjects ...rbacv1.Subject) *rbacv1.RoleBinding {
		return &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: graph.DefaultApplierClusterRole, Namespace: "team-a"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: graph.DefaultApplierClusterRole},
			Subjects:   subjects,
		}
	}
	t.Run("without our subject", func(t *testing.T) {
		rb := unmanaged(admin)
		c := fake.NewClientBuilder().WithScheme(identityScheme(t)).WithObjects(rb).Build()
		p := &graph.IdentityProvisioner{Writer: c, Reader: c}
		err := p.Ensure(context.Background(), refGraph("team-a"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not managed by kardinal-promoter")
		var got rbacv1.RoleBinding
		require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(rb), &got))
		assert.Equal(t, []rbacv1.Subject{admin}, got.Subjects, "subjects must not be rewritten")
	})
	t.Run("already granting the role", func(t *testing.T) {
		rb := unmanaged(admin, ours)
		c := fake.NewClientBuilder().WithScheme(identityScheme(t)).WithObjects(rb).Build()
		p := &graph.IdentityProvisioner{Writer: c, Reader: c}
		require.NoError(t, p.Ensure(context.Background(), refGraph("team-a")))
		var got rbacv1.RoleBinding
		require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(rb), &got))
		assert.Equal(t, []rbacv1.Subject{admin, ours}, got.Subjects)
	})
}

func TestIdentityProvisioner_RefusesForeignRoleRef(t *testing.T) {
	ctx := context.Background()
	foreign := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: graph.DefaultApplierClusterRole, Namespace: "default"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
	}
	c := fake.NewClientBuilder().WithScheme(identityScheme(t)).WithObjects(foreign).Build()
	p := &graph.IdentityProvisioner{Writer: c, Reader: c}

	err := p.Ensure(ctx, refGraph("default"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cluster-admin")
}

func TestIdentityProvisioner_NilSafe(t *testing.T) {
	var p *graph.IdentityProvisioner
	assert.NoError(t, p.Ensure(context.Background(), refGraph("default")))
}
