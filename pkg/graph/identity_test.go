// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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
	c := fake.NewClientBuilder().WithScheme(identityScheme(t)).Build()
	p := &graph.IdentityProvisioner{Writer: c, Reader: c, ReaderNamespaces: []string{"prod"}}
	g := refGraph("default", "prod")

	unbound, err := p.Ensure(ctx, g)
	require.NoError(t, err)
	assert.Empty(t, unbound)
	// Idempotent.
	unbound, err = p.Ensure(ctx, g)
	require.NoError(t, err)
	assert.Empty(t, unbound)

	var sa corev1.ServiceAccount
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "default", Name: graph.DefaultGraphServiceAccount}, &sa))

	want := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: graph.DefaultGraphServiceAccount, Namespace: "default"}

	var applier rbacv1.RoleBinding
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "default", Name: graph.DefaultApplierClusterRole}, &applier))
	assert.Equal(t, graph.DefaultApplierClusterRole, applier.RoleRef.Name)
	assert.Equal(t, "ClusterRole", applier.RoleRef.Kind)
	assert.Equal(t, []rbacv1.Subject{want}, applier.Subjects)
	assert.Equal(t, "prod", applier.Annotations[graph.AnnotationReaderNamespaces])

	var reader rbacv1.RoleBinding
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "prod", Name: graph.DefaultReaderClusterRole + "-default"}, &reader))
	assert.Equal(t, graph.DefaultReaderClusterRole, reader.RoleRef.Name)
	assert.Equal(t, []rbacv1.Subject{want}, reader.Subjects)
}

// readerBindings returns the namespaces holding a reader RoleBinding for graphNS.
func readerBindings(t *testing.T, c client.Client, graphNS string) []string {
	t.Helper()
	var list rbacv1.RoleBindingList
	require.NoError(t, c.List(context.Background(), &list))
	var out []string
	for _, rb := range list.Items {
		if rb.Name == graph.DefaultReaderClusterRole+"-"+graphNS {
			out = append(out, rb.Namespace)
		}
	}
	return out
}

// TestIdentityProvisioner_ReaderNamespaces verifies that reader RoleBindings
// are created only in the Graph's own namespace and the namespaces the
// operator allows, never in the Kubernetes system namespaces, so a Pipeline
// author cannot grant its Graph read access to another tenant's namespace
// (C01-graph-04).
func TestIdentityProvisioner_ReaderNamespaces(t *testing.T) {
	refs := []string{"kube-system", "argocd", "team-b", "", "kube-public"}
	tests := []struct {
		name        string
		allowed     []string
		wantBound   []string
		wantUnbound []string
	}{
		{name: "none allowed", wantBound: []string{"team-a"},
			wantUnbound: []string{"argocd", "kube-public", "kube-system", "team-b"}},
		{name: "defaults", allowed: graph.DefaultReaderNamespaces, wantBound: []string{"argocd", "team-a"},
			wantUnbound: []string{"kube-public", "kube-system", "team-b"}},
		{name: "all namespaces still excludes system namespaces", allowed: []string{graph.AllNamespaces},
			wantBound: []string{"argocd", "team-a", "team-b"}, wantUnbound: []string{"kube-public", "kube-system"}},
		{name: "system namespaces are never allowed", allowed: []string{"kube-system"},
			wantBound: []string{"team-a"}, wantUnbound: []string{"argocd", "kube-public", "kube-system", "team-b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(identityScheme(t)).Build()
			p := &graph.IdentityProvisioner{Writer: c, Reader: c, ReaderNamespaces: tt.allowed}
			unbound, err := p.Ensure(context.Background(), refGraph("team-a", refs...))
			require.NoError(t, err)
			assert.Equal(t, tt.wantUnbound, unbound)
			assert.ElementsMatch(t, tt.wantBound, readerBindings(t, c, "team-a"))
		})
	}
}

// TestIdentityProvisioner_UnbindableNamespaces verifies that a reader
// namespace that does not exist yet, or where the controller may not create
// RoleBindings, is returned as unbound instead of failing the Graph or being
// skipped silently, and is bound on the next call once it exists
// (C01-graph-19 a).
func TestIdentityProvisioner_UnbindableNamespaces(t *testing.T) {
	missing := map[string]bool{"prod": true}
	c := fake.NewClientBuilder().WithScheme(identityScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			switch {
			case missing[obj.GetNamespace()]:
				return apierrors.NewNotFound(corev1.Resource("namespaces"), obj.GetNamespace())
			case obj.GetNamespace() == "locked":
				return apierrors.NewForbidden(rbacv1.Resource("rolebindings"), obj.GetName(), errors.New("denied"))
			}
			return c.Create(ctx, obj, opts...)
		},
	}).Build()
	p := &graph.IdentityProvisioner{Writer: c, Reader: c, ReaderNamespaces: []string{graph.AllNamespaces}}
	g := refGraph("team-a", "prod", "locked", "argocd")

	unbound, err := p.Ensure(context.Background(), g)
	require.NoError(t, err)
	assert.Equal(t, []string{"locked", "prod"}, unbound)
	assert.ElementsMatch(t, []string{"argocd"}, readerBindings(t, c, "team-a"))

	missing["prod"] = false
	unbound, err = p.Ensure(context.Background(), g)
	require.NoError(t, err)
	assert.Equal(t, []string{"locked"}, unbound)
	assert.ElementsMatch(t, []string{"argocd", "prod"}, readerBindings(t, c, "team-a"))
}

// TestIdentityProvisioner_TerminatingNamespace covers B54: when the Graph's own
// namespace is being deleted, the Graph cannot be created either, so a reader
// binding refused there is an error and not an unbound namespace (with a
// warning). A refusal in another terminating namespace leaves it unbound.
func TestIdentityProvisioner_TerminatingNamespace(t *testing.T) {
	for _, tt := range []struct {
		name        string
		terminating string // the namespace that refuses new objects
		wantUnbound []string
		wantErr     bool
	}{
		{name: "the Graph's own namespace", terminating: "team-a", wantErr: true},
		{name: "another namespace", terminating: "prod", wantUnbound: []string{"prod"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(identityScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					rb, ok := obj.(*rbacv1.RoleBinding)
					if !ok || rb.Namespace != tt.terminating || rb.RoleRef.Name != graph.DefaultReaderClusterRole {
						return c.Create(ctx, obj, opts...)
					}
					err := apierrors.NewForbidden(rbacv1.Resource("rolebindings"), obj.GetName(),
						errors.New("unable to create new content in namespace "+tt.terminating+" because it is being terminated"))
					err.ErrStatus.Details.Causes = append(err.ErrStatus.Details.Causes, metav1.StatusCause{
						Type: corev1.NamespaceTerminatingCause, Field: "metadata.namespace",
						Message: "namespace " + tt.terminating + " is being terminated"})
					return err
				},
			}).Build()
			p := &graph.IdentityProvisioner{Writer: c, Reader: c, ReaderNamespaces: []string{graph.AllNamespaces}}

			unbound, err := p.Ensure(context.Background(), refGraph("team-a", "team-a", "prod"))
			if tt.wantErr {
				require.Error(t, err)
				assert.True(t, apierrors.HasStatusCause(err, corev1.NamespaceTerminatingCause), "the cause is kept: %v", err)
				assert.Nil(t, unbound)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantUnbound, unbound)
			assert.ElementsMatch(t, []string{"team-a"}, readerBindings(t, c, "team-a"))
		})
	}
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

	_, err := p.Ensure(ctx, refGraph("default"))
	require.NoError(t, err)

	var rb rbacv1.RoleBinding
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(stale), &rb))
	require.Len(t, rb.Subjects, 1)
	assert.Equal(t, "custom", rb.Subjects[0].Name)
}

// TestIdentityProvisioner_LeavesUnmanagedBindings verifies that a RoleBinding
// with the controller's name that the controller did not create is never
// rewritten: accepted as is when it already grants the role, and otherwise
// reported (C01-graph-19 c).
func TestIdentityProvisioner_LeavesUnmanagedBindings(t *testing.T) {
	ours := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: graph.DefaultGraphServiceAccount, Namespace: "team-a"}
	admin := rbacv1.Subject{Kind: rbacv1.UserKind, Name: "alice", APIGroup: rbacv1.GroupName}
	unmanaged := func(ns, name, role string, subjects ...rbacv1.Subject) *rbacv1.RoleBinding {
		return &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role},
			Subjects:   subjects,
		}
	}
	readerName := graph.DefaultReaderClusterRole + "-team-a"

	t.Run("reader binding without our subject", func(t *testing.T) {
		rb := unmanaged("argocd", readerName, graph.DefaultReaderClusterRole, admin)
		c := fake.NewClientBuilder().WithScheme(identityScheme(t)).WithObjects(rb).Build()
		p := &graph.IdentityProvisioner{Writer: c, Reader: c, ReaderNamespaces: graph.DefaultReaderNamespaces}
		unbound, err := p.Ensure(context.Background(), refGraph("team-a", "argocd"))
		require.NoError(t, err)
		assert.Equal(t, []string{"argocd"}, unbound)
		var got rbacv1.RoleBinding
		require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(rb), &got))
		assert.Equal(t, []rbacv1.Subject{admin}, got.Subjects, "subjects must not be rewritten")
	})

	t.Run("reader binding that already grants the role", func(t *testing.T) {
		rb := unmanaged("argocd", readerName, graph.DefaultReaderClusterRole, admin, ours)
		c := fake.NewClientBuilder().WithScheme(identityScheme(t)).WithObjects(rb).Build()
		p := &graph.IdentityProvisioner{Writer: c, Reader: c, ReaderNamespaces: graph.DefaultReaderNamespaces}
		unbound, err := p.Ensure(context.Background(), refGraph("team-a", "argocd"))
		require.NoError(t, err)
		assert.Empty(t, unbound)
		var got rbacv1.RoleBinding
		require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(rb), &got))
		assert.Equal(t, []rbacv1.Subject{admin, ours}, got.Subjects)

		// Not recorded, so Prune never deletes it.
		require.NoError(t, p.Prune(context.Background(), "team-a", nil))
		require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(rb), &got))
	})

	t.Run("applier binding without our subject", func(t *testing.T) {
		rb := unmanaged("team-a", graph.DefaultApplierClusterRole, graph.DefaultApplierClusterRole, admin)
		c := fake.NewClientBuilder().WithScheme(identityScheme(t)).WithObjects(rb).Build()
		p := &graph.IdentityProvisioner{Writer: c, Reader: c}
		_, err := p.Ensure(context.Background(), refGraph("team-a"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not managed by kardinal-promoter")
	})
}

// TestIdentityProvisioner_Prune verifies that reader RoleBindings no Graph in
// the namespace reads through any more are deleted, and the rest are kept
// (C01-graph-04, C01-graph-19 b).
func TestIdentityProvisioner_Prune(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(identityScheme(t)).Build()
	p := &graph.IdentityProvisioner{Writer: c, Reader: c, ReaderNamespaces: []string{graph.AllNamespaces}}

	older := refGraph("team-a", "", "argocd", "flux-system")
	newer := refGraph("team-a", "", "argocd", "prod")
	for _, g := range []*graph.Graph{older, newer} {
		_, err := p.Ensure(ctx, g)
		require.NoError(t, err)
	}
	assert.ElementsMatch(t, []string{"argocd", "flux-system", "prod", "team-a"}, readerBindings(t, c, "team-a"))

	// Both Graphs still exist: nothing is pruned.
	require.NoError(t, p.Prune(ctx, "team-a", []*graph.Graph{older, newer}))
	assert.ElementsMatch(t, []string{"argocd", "flux-system", "prod", "team-a"}, readerBindings(t, c, "team-a"))

	// The older Graph is gone: flux-system is no longer read.
	require.NoError(t, p.Prune(ctx, "team-a", []*graph.Graph{newer}))
	assert.ElementsMatch(t, []string{"argocd", "prod", "team-a"}, readerBindings(t, c, "team-a"))
	var applier rbacv1.RoleBinding
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: graph.DefaultApplierClusterRole}, &applier))
	assert.Equal(t, "argocd,prod,team-a", applier.Annotations[graph.AnnotationReaderNamespaces])

	// No Graphs left.
	require.NoError(t, p.Prune(ctx, "team-a", nil))
	assert.Empty(t, readerBindings(t, c, "team-a"))
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: graph.DefaultApplierClusterRole}, &applier))
	assert.NotContains(t, applier.Annotations, graph.AnnotationReaderNamespaces)

	// Idempotent.
	require.NoError(t, p.Prune(ctx, "team-a", nil))
}

// TestIdentityProvisioner_PruneWithoutApplierBinding covers B46: when the
// applier RoleBinding, which records where the reader bindings are, is gone
// (its namespace is being deleted), Prune still finds and deletes them in the
// Graph namespace and in every namespace ReaderNamespaces names. With
// AllNamespaces the bindings elsewhere are found only by PruneIn, which the
// sweep calls with the namespaces it listed them in.
func TestIdentityProvisioner_PruneWithoutApplierBinding(t *testing.T) {
	tests := []struct {
		name       string
		allowed    []string
		refs       []string
		pruneIn    []string // nil: Prune
		wantLeft   []string
		keepRecord bool // the applier RoleBinding is not deleted
	}{
		{name: "named namespaces", allowed: graph.DefaultReaderNamespaces, refs: []string{"", "argocd"}},
		{name: "a binding the record does not list is found too", allowed: graph.DefaultReaderNamespaces,
			refs: []string{"argocd"}, keepRecord: true, pruneIn: []string{"argocd"}},
		{name: "all namespaces: Prune finds only the Graph namespace", allowed: []string{graph.AllNamespaces},
			refs: []string{"", "prod"}, wantLeft: []string{"prod"}},
		{name: "all namespaces: PruneIn finds the rest", allowed: []string{graph.AllNamespaces},
			refs: []string{"", "prod"}, pruneIn: []string{"prod"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			c := fake.NewClientBuilder().WithScheme(identityScheme(t)).Build()
			p := &graph.IdentityProvisioner{Writer: c, Reader: c, ReaderNamespaces: tt.allowed}
			_, err := p.Ensure(ctx, refGraph("team-a", tt.refs...))
			require.NoError(t, err)
			require.NotEmpty(t, readerBindings(t, c, "team-a"))
			applier := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: graph.DefaultApplierClusterRole}}
			if tt.keepRecord {
				// Drop the record only: the binding exists but is not listed.
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(applier), applier))
				delete(applier.Annotations, graph.AnnotationReaderNamespaces)
				require.NoError(t, c.Update(ctx, applier))
			} else {
				require.NoError(t, c.Delete(ctx, applier))
			}

			if tt.pruneIn == nil {
				require.NoError(t, p.Prune(ctx, "team-a", nil))
			} else {
				require.NoError(t, p.PruneIn(ctx, "team-a", nil, tt.pruneIn))
			}
			assert.ElementsMatch(t, tt.wantLeft, readerBindings(t, c, "team-a"))
		})
	}
}

// TestIdentityProvisioner_PruneLeavesForeignBindings verifies that Prune, even
// without the applier RoleBinding's record, deletes only reader bindings the
// controller created: a binding with the reader binding's name that lacks the
// managed-by label, or binds another role, is left alone.
func TestIdentityProvisioner_PruneLeavesForeignBindings(t *testing.T) {
	managed := map[string]string{"app.kubernetes.io/managed-by": "kardinal-promoter"}
	name := graph.DefaultReaderClusterRole + "-team-a"
	unlabeled := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "argocd"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: graph.DefaultReaderClusterRole},
	}
	otherRole := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "flux-system", Labels: managed},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "admin"},
	}
	c := fake.NewClientBuilder().WithScheme(identityScheme(t)).WithObjects(unlabeled, otherRole).Build()
	p := &graph.IdentityProvisioner{Writer: c, Reader: c, ReaderNamespaces: graph.DefaultReaderNamespaces}

	require.NoError(t, p.Prune(context.Background(), "team-a", nil))
	assert.ElementsMatch(t, []string{"argocd", "flux-system"}, readerBindings(t, c, "team-a"))
}

// TestIdentityProvisioner_ReaderBinding verifies which RoleBindings the sweep
// treats as reader bindings, and for which Graph namespace.
func TestIdentityProvisioner_ReaderBinding(t *testing.T) {
	managed := map[string]string{"app.kubernetes.io/managed-by": "kardinal-promoter"}
	sa := func(ns string) rbacv1.Subject {
		return rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: graph.DefaultGraphServiceAccount, Namespace: ns}
	}
	reader := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: graph.DefaultReaderClusterRole}
	binding := func(name string, labels map[string]string, ref rbacv1.RoleRef, subjects ...rbacv1.Subject) *rbacv1.RoleBinding {
		return &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "argocd", Labels: labels},
			RoleRef: ref, Subjects: subjects}
	}
	name := graph.DefaultReaderClusterRole + "-team-a"
	tests := []struct {
		name   string
		rb     *rbacv1.RoleBinding
		wantNS string
	}{
		{name: "reader binding", rb: binding(name, managed, reader, sa("team-a")), wantNS: "team-a"},
		{name: "no managed-by label", rb: binding(name, nil, reader, sa("team-a"))},
		{name: "another role", rb: binding(name, managed,
			rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: graph.DefaultApplierClusterRole}, sa("team-a"))},
		{name: "a Role, not the ClusterRole", rb: binding(name, managed,
			rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: graph.DefaultReaderClusterRole}, sa("team-a"))},
		{name: "two subjects", rb: binding(name, managed, reader, sa("team-a"), sa("team-b"))},
		{name: "another ServiceAccount", rb: binding(name, managed, reader,
			rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "default", Namespace: "team-a"})},
		{name: "named for another namespace", rb: binding(graph.DefaultReaderClusterRole+"-team-b", managed, reader, sa("team-a"))},
	}
	p := &graph.IdentityProvisioner{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ns, ok := p.ReaderBinding(tt.rb)
			assert.Equal(t, tt.wantNS != "", ok)
			assert.Equal(t, tt.wantNS, ns)
		})
	}
}

func TestIdentityProvisioner_RefusesForeignRoleRef(t *testing.T) {
	ctx := context.Background()
	foreign := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: graph.DefaultApplierClusterRole, Namespace: "default"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
	}
	c := fake.NewClientBuilder().WithScheme(identityScheme(t)).WithObjects(foreign).Build()
	p := &graph.IdentityProvisioner{Writer: c, Reader: c}

	_, err := p.Ensure(ctx, refGraph("default"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cluster-admin")
}

func TestIdentityProvisioner_NilSafe(t *testing.T) {
	var p *graph.IdentityProvisioner
	p.Lock()
	defer p.Unlock()
	unbound, err := p.Ensure(context.Background(), refGraph("default"))
	assert.NoError(t, err)
	assert.Empty(t, unbound)
	assert.NoError(t, p.Prune(context.Background(), "default", nil))
	assert.NoError(t, p.PruneIn(context.Background(), "default", nil, []string{"argocd"}))
	assert.True(t, p.MayRead("default", "kube-system"), "no provisioner: identity is managed outside the controller")
}
