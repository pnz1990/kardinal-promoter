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

func TestRefNamespaces(t *testing.T) {
	g := refGraph("default", "prod", "", "argocd", "prod")
	assert.Equal(t, []string{"argocd", "default", "prod"}, graph.RefNamespaces(g))
	assert.Empty(t, graph.RefNamespaces(refGraph("default")))
}

func TestIdentityProvisioner_Ensure(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(identityScheme(t)).WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "prod"}},
	).Build()
	p := &graph.IdentityProvisioner{Writer: c, Reader: c}
	g := refGraph("default", "prod")

	require.NoError(t, p.Ensure(ctx, g))
	// Idempotent.
	require.NoError(t, p.Ensure(ctx, g))

	var sa corev1.ServiceAccount
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "default", Name: graph.DefaultGraphServiceAccount}, &sa))

	want := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: graph.DefaultGraphServiceAccount, Namespace: "default"}

	var applier rbacv1.RoleBinding
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "default", Name: graph.DefaultApplierClusterRole}, &applier))
	assert.Equal(t, graph.DefaultApplierClusterRole, applier.RoleRef.Name)
	assert.Equal(t, "ClusterRole", applier.RoleRef.Kind)
	assert.Equal(t, []rbacv1.Subject{want}, applier.Subjects)

	var reader rbacv1.RoleBinding
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "prod", Name: graph.DefaultReaderClusterRole + "-default"}, &reader))
	assert.Equal(t, graph.DefaultReaderClusterRole, reader.RoleRef.Name)
	assert.Equal(t, []rbacv1.Subject{want}, reader.Subjects)
}

func TestIdentityProvisioner_RepairsSubjects(t *testing.T) {
	ctx := context.Background()
	stale := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: graph.DefaultApplierClusterRole, Namespace: "default"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: graph.DefaultApplierClusterRole},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "old", Namespace: "default"}},
	}
	c := fake.NewClientBuilder().WithScheme(identityScheme(t)).WithObjects(stale).Build()
	p := &graph.IdentityProvisioner{Writer: c, Reader: c, ServiceAccountName: "custom"}

	require.NoError(t, p.Ensure(ctx, refGraph("default")))

	var rb rbacv1.RoleBinding
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(stale), &rb))
	require.Len(t, rb.Subjects, 1)
	assert.Equal(t, "custom", rb.Subjects[0].Name)
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
