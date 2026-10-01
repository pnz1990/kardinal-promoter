// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graphcleanup_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/graphcleanup"
)

var managed = map[string]string{"app.kubernetes.io/managed-by": "kardinal-promoter"}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

// kardinalGraph is Graph g in ns, created by Bundle b, holding finalizers, and
// deleted when deleting.
func kardinalGraph(ns string, deleting bool, finalizers ...string) *unstructured.Unstructured {
	g := &unstructured.Unstructured{}
	g.SetGroupVersionKind(graph.GraphGVK)
	g.SetNamespace(ns)
	g.SetName("g")
	g.SetLabels(map[string]string{"kardinal.io/bundle": "b"})
	ctl := true
	g.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Bundle",
		Name: "b", UID: "bundle-uid", Controller: &ctl}})
	g.SetFinalizers(finalizers)
	if deleting {
		now := metav1.Now()
		g.SetDeletionTimestamp(&now)
	}
	return g
}

func namespace(name string, terminating bool) *corev1.Namespace {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if terminating {
		ns.Status.Phase = corev1.NamespaceTerminating
	}
	return ns
}

// applierBinding is the applier RoleBinding in ns, recording reader bindings in
// readers, and being deleted when deleting.
func applierBinding(ns string, deleting bool, readers string) *rbacv1.RoleBinding {
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: graph.DefaultApplierClusterRole, Namespace: ns, Labels: managed},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: graph.DefaultApplierClusterRole},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: graph.DefaultGraphServiceAccount, Namespace: ns}},
	}
	if readers != "" {
		rb.Annotations = map[string]string{graph.AnnotationReaderNamespaces: readers}
	}
	if deleting {
		now := metav1.Now()
		rb.DeletionTimestamp = &now
		rb.Finalizers = []string{"test/hold"}
	}
	return rb
}

// readerBinding is the reader RoleBinding in ns for the Graphs of graphNS.
func readerBinding(ns, graphNS string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: graph.DefaultReaderClusterRole + "-" + graphNS, Namespace: ns, Labels: managed},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: graph.DefaultReaderClusterRole},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: graph.DefaultGraphServiceAccount, Namespace: graphNS}},
	}
}

// readsGraph is a Graph in ns with a ref into each of refNamespaces.
func readsGraph(ns string, refNamespaces ...string) *graph.Graph {
	g := &graph.Graph{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: ns}}
	for _, rns := range refNamespaces {
		g.Spec.Nodes = append(g.Spec.Nodes, graph.GraphNode{ID: "ref" + rns, Ref: map[string]interface{}{
			"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
			"metadata": map[string]interface{}{"name": "app", "namespace": rns}}})
	}
	return g
}

// graphLister serves Graphs by namespace.
type graphLister struct {
	graphs map[string][]*graph.Graph
	err    error
	calls  int
}

func (l *graphLister) List(_ context.Context, ns string) ([]*graph.Graph, error) {
	l.calls++
	return l.graphs[ns], l.err
}

func reconcile(t *testing.T, r *graphcleanup.Reconciler, ns string) (ctrl.Result, error) {
	t.Helper()
	return r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "g"}})
}

// getGraph returns Graph g in ns, or nil when it is gone.
func getGraph(t *testing.T, c client.Client, ns string) *unstructured.Unstructured {
	t.Helper()
	g := &unstructured.Unstructured{}
	g.SetGroupVersionKind(graph.GraphGVK)
	err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "g"}, g)
	if apierrors.IsNotFound(err) {
		return nil
	}
	require.NoError(t, err)
	return g
}

// TestReconciler_NamespaceDeletion covers B45: in a namespace being deleted,
// kro cannot delete a Graph's resources once the applier RoleBinding is gone,
// so the Reconciler removes kro's finalizer from kardinal's Graphs, and only
// then.
func TestReconciler_NamespaceDeletion(t *testing.T) {
	other := kardinalGraph("team-a", true, graphcleanup.KroFinalizer)
	other.SetLabels(nil)
	notBundle := kardinalGraph("team-a", true, graphcleanup.KroFinalizer)
	notBundle.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "d", UID: "u"}})
	tests := []struct {
		name          string
		graph         *unstructured.Unstructured
		objs          []client.Object // besides the Graph
		wantFinalizer bool
		wantRequeue   bool
		wantGone      bool
	}{
		{
			name:     "the namespace is terminating and the applier binding is gone",
			graph:    kardinalGraph("team-a", true, graphcleanup.KroFinalizer),
			objs:     []client.Object{namespace("team-a", true)},
			wantGone: true,
		},
		{
			name:     "the applier binding is being deleted",
			graph:    kardinalGraph("team-a", true, graphcleanup.KroFinalizer),
			objs:     []client.Object{namespace("team-a", true), applierBinding("team-a", true, "")},
			wantGone: true,
		},
		{
			name:     "the namespace is gone",
			graph:    kardinalGraph("team-a", true, graphcleanup.KroFinalizer),
			wantGone: true,
		},
		{
			name:          "other finalizers stay",
			graph:         kardinalGraph("team-a", true, graphcleanup.KroFinalizer, "example.com/keep"),
			objs:          []client.Object{namespace("team-a", true)},
			wantFinalizer: false,
		},
		{
			name:          "the applier binding exists: kro tears the Graph down itself",
			graph:         kardinalGraph("team-a", true, graphcleanup.KroFinalizer),
			objs:          []client.Object{namespace("team-a", true), applierBinding("team-a", false, "")},
			wantFinalizer: true,
			wantRequeue:   true,
		},
		{
			name:          "the namespace is not being deleted",
			graph:         kardinalGraph("team-a", true, graphcleanup.KroFinalizer),
			objs:          []client.Object{namespace("team-a", false)},
			wantFinalizer: true,
		},
		{
			name:          "the Graph is not being deleted",
			graph:         kardinalGraph("team-a", false, graphcleanup.KroFinalizer),
			objs:          []client.Object{namespace("team-a", true)},
			wantFinalizer: true,
		},
		{
			name:          "a Graph without the kardinal.io/bundle label",
			graph:         other,
			objs:          []client.Object{namespace("team-a", true)},
			wantFinalizer: true,
		},
		{
			name:          "a Graph not controlled by a Bundle",
			graph:         notBundle,
			objs:          []client.Object{namespace("team-a", true)},
			wantFinalizer: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(append(tt.objs, tt.graph)...).Build()
			r := &graphcleanup.Reconciler{Client: c, APIReader: c, Graphs: &graphLister{},
				Identity: &graph.IdentityProvisioner{Writer: c, Reader: c}}

			res, err := reconcile(t, r, "team-a")
			require.NoError(t, err)
			if tt.wantRequeue {
				assert.Positive(t, res.RequeueAfter)
			} else {
				assert.Zero(t, res.RequeueAfter)
			}
			got := getGraph(t, c, "team-a")
			if tt.wantGone {
				assert.Nil(t, got, "the Graph is gone")
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.wantFinalizer, slices.Contains(got.GetFinalizers(), graphcleanup.KroFinalizer),
				"finalizers %v", got.GetFinalizers())
			for _, f := range tt.graph.GetFinalizers() {
				if f != graphcleanup.KroFinalizer {
					assert.Contains(t, got.GetFinalizers(), f)
				}
			}
		})
	}
}

// TestReconciler_DeletesResourcesOutsideNamespace covers B45: a resource kro
// recorded outside the Graph's namespace is deleted before kro's finalizer is
// removed, and while that fails the finalizer stays.
func TestReconciler_DeletesResourcesOutsideNamespace(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "deleted", true: "delete fails"}[fail], func(t *testing.T) {
			g := kardinalGraph("team-a", true, graphcleanup.KroFinalizer)
			require.NoError(t, unstructured.SetNestedSlice(g.Object, []interface{}{
				map[string]interface{}{"apiVersion": "v1", "kind": "ConfigMap", "namespace": "team-a", "name": "in", "uid": "u1"},
				map[string]interface{}{"apiVersion": "v1", "kind": "ConfigMap", "namespace": "shared", "name": "out", "uid": "u2"},
			}, "status", "managedResources"))
			var deleted []string
			c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(g, namespace("team-a", true)).
				WithInterceptorFuncs(interceptor.Funcs{
					Delete: func(_ context.Context, _ client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
						var o client.DeleteOptions
						o.ApplyOptions(opts)
						require.NotNil(t, o.Preconditions, "the delete has a UID precondition")
						deleted = append(deleted, obj.GetNamespace()+"/"+obj.GetName()+"@"+string(*o.Preconditions.UID))
						if fail {
							return apierrors.NewForbidden(corev1.Resource("configmaps"), obj.GetName(), errors.New("denied"))
						}
						return nil
					},
				}).Build()
			r := &graphcleanup.Reconciler{Client: c, APIReader: c, Graphs: &graphLister{},
				Identity: &graph.IdentityProvisioner{Writer: c, Reader: c}}

			_, err := reconcile(t, r, "team-a")
			assert.Equal(t, []string{"shared/out@u2"}, deleted, "only the resource outside team-a")
			if fail {
				require.Error(t, err)
				assert.Contains(t, getGraph(t, c, "team-a").GetFinalizers(), graphcleanup.KroFinalizer)
				return
			}
			require.NoError(t, err)
			assert.Nil(t, getGraph(t, c, "team-a"))
		})
	}
}

// TestReconciler_PrunesOnGraphDelete covers B46: when a Graph is gone, the
// reader RoleBindings of its namespace that no remaining Graph reads through
// are deleted, even when the applier RoleBinding (and its record) went with
// the namespace.
func TestReconciler_PrunesOnGraphDelete(t *testing.T) {
	tests := []struct {
		name     string
		objs     []client.Object
		graphs   []*graph.Graph
		wantLeft []string
	}{
		{
			name:     "the namespace's last Graph",
			objs:     []client.Object{applierBinding("team-a", false, "argocd"), readerBinding("argocd", "team-a")},
			wantLeft: nil,
		},
		{
			name:     "the namespace is gone with its applier binding",
			objs:     []client.Object{readerBinding("argocd", "team-a"), readerBinding("flux-system", "team-a")},
			wantLeft: nil,
		},
		{
			name:     "another Graph still reads argocd",
			objs:     []client.Object{applierBinding("team-a", false, "argocd"), readerBinding("argocd", "team-a")},
			graphs:   []*graph.Graph{readsGraph("team-a", "argocd")},
			wantLeft: []string{"argocd/" + graph.DefaultReaderClusterRole + "-team-a"},
		},
		{
			name:     "another namespace's binding stays",
			objs:     []client.Object{readerBinding("argocd", "team-b")},
			wantLeft: []string{"argocd/" + graph.DefaultReaderClusterRole + "-team-b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(tt.objs...).Build()
			lister := &graphLister{graphs: map[string][]*graph.Graph{"team-a": tt.graphs}}
			r := &graphcleanup.Reconciler{Client: c, APIReader: c, Graphs: lister,
				Identity: &graph.IdentityProvisioner{Writer: c, Reader: c, ReaderNamespaces: graph.DefaultReaderNamespaces}}

			_, err := reconcile(t, r, "team-a")
			require.NoError(t, err)
			assert.Equal(t, 1, lister.calls, "the remaining Graphs are listed")
			assert.ElementsMatch(t, tt.wantLeft, readerBindingKeys(t, c))
		})
	}
}

func TestReconciler_PruneListError(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(readerBinding("argocd", "team-a")).Build()
	r := &graphcleanup.Reconciler{Client: c, APIReader: c, Graphs: &graphLister{err: errors.New("boom")},
		Identity: &graph.IdentityProvisioner{Writer: c, Reader: c, ReaderNamespaces: graph.DefaultReaderNamespaces}}
	_, err := reconcile(t, r, "team-a")
	require.ErrorContains(t, err, "boom")
	assert.Len(t, readerBindingKeys(t, c), 1, "nothing is pruned without the Graph list")
}

func TestReconciler_NilIdentity(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).
		WithObjects(kardinalGraph("team-a", true, graphcleanup.KroFinalizer)).Build()
	r := &graphcleanup.Reconciler{Client: c, APIReader: c, Graphs: &graphLister{}}
	res, err := reconcile(t, r, "team-a")
	require.NoError(t, err)
	assert.Zero(t, res)
	assert.NotNil(t, getGraph(t, c, "team-a"))
}

func TestIsKardinalGraph(t *testing.T) {
	assert.True(t, graphcleanup.IsKardinalGraph(kardinalGraph("ns", false)))
	g := kardinalGraph("ns", false)
	g.SetLabels(nil)
	assert.False(t, graphcleanup.IsKardinalGraph(g), "no kardinal.io/bundle label")
	g = kardinalGraph("ns", false)
	refs := g.GetOwnerReferences()
	refs[0].Controller = nil
	g.SetOwnerReferences(refs)
	assert.False(t, graphcleanup.IsKardinalGraph(g), "the Bundle is not the controller")
	g = kardinalGraph("ns", false)
	refs = g.GetOwnerReferences()
	refs[0].APIVersion = "example.com/v1"
	g.SetOwnerReferences(refs)
	assert.False(t, graphcleanup.IsKardinalGraph(g), "a Bundle of another group")
}

// readerBindingKeys lists the reader RoleBindings as namespace/name.
func readerBindingKeys(t *testing.T, c client.Client) []string {
	t.Helper()
	var list rbacv1.RoleBindingList
	require.NoError(t, c.List(context.Background(), &list))
	var out []string
	for _, rb := range list.Items {
		if rb.RoleRef.Name == graph.DefaultReaderClusterRole {
			out = append(out, rb.Namespace+"/"+rb.Name)
		}
	}
	return out
}
