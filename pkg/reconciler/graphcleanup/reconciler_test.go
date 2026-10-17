// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graphcleanup_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
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
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
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
		rb.Annotations = map[string]string{graphcleanup.AnnotationReaderNamespaces: readers}
	}
	if deleting {
		now := metav1.Now()
		rb.DeletionTimestamp = &now
		rb.Finalizers = []string{"test/hold"}
	}
	return rb
}

// oldReaderRole is the reader ClusterRole of releases before #1283, as the
// controller named it by default.
const oldReaderRole = "kardinal-graph-reader"

// readerBinding is the reader RoleBinding in ns for the Graphs of graphNS.
func readerBinding(ns, graphNS string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: oldReaderRole + "-" + graphNS, Namespace: ns, Labels: managed},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: oldReaderRole},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: graph.DefaultGraphServiceAccount, Namespace: graphNS}},
	}
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
			name:          "the namespace is not being deleted: checked again later",
			graph:         kardinalGraph("team-a", true, graphcleanup.KroFinalizer),
			objs:          []client.Object{namespace("team-a", false)},
			wantFinalizer: true,
			wantRequeue:   true,
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
			r := &graphcleanup.Reconciler{Client: c, APIReader: c,
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

// TestReconciler_StuckBeforeNamespaceDeletion covers the #1387 review: a
// Graph whose teardown is stuck before its namespace is deleted (the applier
// RoleBinding went first, so kro's deletes are forbidden) gets no event when
// the namespace starts terminating. It is requeued while it waits, and the
// reconcile after the namespace starts terminating removes kro's finalizer.
func TestReconciler_StuckBeforeNamespaceDeletion(t *testing.T) {
	ns := namespace("team-a", false)
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).
		WithObjects(ns, kardinalGraph("team-a", true, graphcleanup.KroFinalizer)).Build()
	r := &graphcleanup.Reconciler{Client: c, APIReader: c,
		Identity: &graph.IdentityProvisioner{Writer: c, Reader: c}}

	res, err := reconcile(t, r, "team-a")
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, res.RequeueAfter, "the stuck Graph is checked again")
	require.NotNil(t, getGraph(t, c, "team-a"))

	ns.Status.Phase = corev1.NamespaceTerminating
	require.NoError(t, c.Status().Update(context.Background(), ns))
	res, err = reconcile(t, r, "team-a")
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)
	assert.Nil(t, getGraph(t, c, "team-a"), "kro's finalizer is removed and the Graph goes")
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
			r := &graphcleanup.Reconciler{Client: c, APIReader: c,
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

// TestReconciler_FinalizerConflict covers B55: when the Graph changed since
// it was read, removing kro's finalizer hits a conflict. The Reconciler reads
// the Graph again and retries, and when the conflict persists it requeues
// without a reconcile error.
func TestReconciler_FinalizerConflict(t *testing.T) {
	annotate := func(g *unstructured.Unstructured) {
		g.SetAnnotations(map[string]string{"example.com/written": g.GetResourceVersion()})
	}
	dropKro := func(g *unstructured.Unstructured) {
		g.SetFinalizers(slices.DeleteFunc(g.GetFinalizers(), func(f string) bool { return f == graphcleanup.KroFinalizer }))
	}
	tests := []struct {
		name          string
		finalizers    []string
		writes        int // the Patch calls preceded by another writer's update
		write         func(*unstructured.Unstructured)
		wantPatches   int
		wantRequeue   bool
		wantGone      bool
		wantFinalizer []string
	}{
		{
			name:        "the Graph changed once",
			finalizers:  []string{graphcleanup.KroFinalizer},
			writes:      1,
			write:       annotate,
			wantPatches: 2,
			wantGone:    true,
		},
		{
			name:          "the Graph changes under every retry",
			finalizers:    []string{graphcleanup.KroFinalizer},
			writes:        100,
			write:         annotate,
			wantPatches:   5,
			wantRequeue:   true,
			wantFinalizer: []string{graphcleanup.KroFinalizer},
		},
		{
			name:          "kro removed its finalizer first",
			finalizers:    []string{graphcleanup.KroFinalizer, "example.com/keep"},
			writes:        1,
			write:         dropKro,
			wantPatches:   1,
			wantFinalizer: []string{"example.com/keep"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patches := 0
			c := fake.NewClientBuilder().WithScheme(newScheme(t)).
				WithObjects(kardinalGraph("team-a", true, tt.finalizers...), namespace("team-a", true)).
				WithInterceptorFuncs(interceptor.Funcs{
					Patch: func(ctx context.Context, w client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
						patches++
						if patches <= tt.writes {
							cur := getGraph(t, w, "team-a")
							tt.write(cur)
							require.NoError(t, w.Update(ctx, cur))
						}
						return w.Patch(ctx, obj, p, opts...)
					},
				}).Build()
			r := &graphcleanup.Reconciler{Client: c, APIReader: c,
				Identity: &graph.IdentityProvisioner{Writer: c, Reader: c}}

			res, err := reconcile(t, r, "team-a")
			require.NoError(t, err, "a conflict is not a reconcile error")
			assert.Equal(t, tt.wantPatches, patches)
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
			assert.Equal(t, tt.wantFinalizer, got.GetFinalizers())
		})
	}
}

func TestReconciler_NilIdentity(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).
		WithObjects(kardinalGraph("team-a", true, graphcleanup.KroFinalizer)).Build()
	r := &graphcleanup.Reconciler{Client: c, APIReader: c}
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
		if rb.RoleRef.Name == oldReaderRole {
			out = append(out, rb.Namespace+"/"+rb.Name)
		}
	}
	return out
}

// fakeGraphs records the namespaces of its RemoveAllHealthRefs calls. The
// first len(failing) calls report failing[i] Graphs it could not clean
// (and an error); later calls report none, unless always is set.
type fakeGraphs struct {
	namespaces []string
	failing    []int
	always     bool
}

func (f *fakeGraphs) RemoveAllHealthRefs(_ context.Context, ns string) (int, int, error) {
	f.namespaces = append(f.namespaces, ns)
	n := len(f.namespaces) - 1
	if f.always || n < len(f.failing) {
		failed := 1
		if n < len(f.failing) {
			failed = f.failing[n]
		}
		return 1, failed, errors.New("graph team-a/g2: forbidden")
	}
	return 1, 0, nil
}

// TestHealthRefCleanup (#1283): at startup the leader removes the health
// ref nodes from the Graphs and deletes the reader RoleBindings the applier
// bindings record, then the record. A binding that is not the controller's
// (unmanaged, another role, another subject) stays, and so does one in a
// namespace that is not recorded. A failure does not stop the manager.
func TestHealthRefCleanup(t *testing.T) {
	ctx := context.Background()
	foreignSubject := readerBinding("flux-system", "team-a")
	foreignSubject.Subjects[0].Name = "someone-else"
	unmanaged := readerBinding("monitoring", "team-a")
	unmanaged.Labels = nil
	pipeline := func(ns string) *v1alpha1.Pipeline {
		return &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: ns}}
	}
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		pipeline("team-a"), pipeline("team-b"),
		applierBinding("team-a", false, "argocd,team-a,flux-system,monitoring"),
		applierBinding("team-b", false, ""),
		readerBinding("argocd", "team-a"), readerBinding("team-a", "team-a"),
		foreignSubject, unmanaged,
		readerBinding("argocd", "team-b"), // not recorded: from a release before #1244
	).Build()
	graphs := &fakeGraphs{}
	cl := &graphcleanup.HealthRefCleanup{Graphs: graphs, Client: c, APIReader: c, Identity: &graph.IdentityProvisioner{}}
	assert.True(t, cl.NeedLeaderElection())
	require.NoError(t, cl.Start(ctx))

	assert.Equal(t, []string{""}, graphs.namespaces, "every namespace's Graphs")
	assert.ElementsMatch(t, []string{"flux-system/" + oldReaderRole + "-team-a", "monitoring/" + oldReaderRole + "-team-a",
		"argocd/" + oldReaderRole + "-team-b"}, readerBindingKeys(t, c))
	var applier rbacv1.RoleBinding
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: graph.DefaultApplierClusterRole}, &applier))
	assert.NotContains(t, applier.Annotations, graphcleanup.AnnotationReaderNamespaces, "the record goes")

	assert.Zero(t, testutil.ToFloat64(observability.HealthRefCleanupPending), "nothing left")

	// Idempotent; namespace mode looks at the watched namespace only.
	require.NoError(t, cl.Start(ctx))
	cl.Namespace = "team-b"
	cl.Graphs = &fakeGraphs{}
	require.NoError(t, cl.Start(ctx))
	assert.Equal(t, []string{"team-b"}, cl.Graphs.(*fakeGraphs).namespaces)
}

// TestHealthRefCleanup_RetriesUntilClean (QA on #2124): a pass that cannot
// clean a Graph does not end the cleanup. It retries, with a backoff, until
// a pass leaves nothing, and the metric says what is left meanwhile. Against
// a refusal that never ends it keeps trying until the manager stops, with
// the metric at what is left; a failure never stops the manager.
//
// Covers GRAPH-HEALTHREF-CLEANUP-02.
func TestHealthRefCleanup_RetriesUntilClean(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	graphs := &fakeGraphs{failing: []int{2, 1}}
	cl := &graphcleanup.HealthRefCleanup{Graphs: graphs, Client: c, APIReader: c,
		Identity: &graph.IdentityProvisioner{}, Backoff: time.Millisecond}
	require.NoError(t, cl.Start(context.Background()))
	assert.Len(t, graphs.namespaces, 3, "two passes left a Graph, the third cleaned it")
	assert.Zero(t, testutil.ToFloat64(observability.HealthRefCleanupPending))

	refused := &fakeGraphs{always: true}
	cl.Graphs = refused
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	require.NoError(t, cl.Start(ctx), "returns when the manager stops")
	assert.Greater(t, len(refused.namespaces), 2, "it kept retrying")
	assert.Equal(t, 1.0, testutil.ToFloat64(observability.HealthRefCleanupPending), "the refused Graph is pending")
}

// TestHealthRefCleanup_CustomRoleNames: the reader role is named after the
// applier role the chart named <fullname>-graph-applier; an applier role of
// another name leaves the bindings, as the reader role cannot be told.
func TestHealthRefCleanup_CustomRoleNames(t *testing.T) {
	ctx := context.Background()
	applier := applierBinding("team-a", false, "argocd")
	applier.Name, applier.RoleRef.Name = "rel-kardinal-graph-applier", "rel-kardinal-graph-applier"
	reader := readerBinding("argocd", "team-a")
	reader.Name, reader.RoleRef.Name = "rel-kardinal-graph-reader-team-a", "rel-kardinal-graph-reader"
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a"}}, applier, reader).Build()
	cl := &graphcleanup.HealthRefCleanup{Client: c, APIReader: c,
		Identity: &graph.IdentityProvisioner{ApplierClusterRole: "rel-kardinal-graph-applier"}}
	require.NoError(t, cl.Start(ctx))
	var got rbacv1.RoleBinding
	assert.True(t, apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(reader), &got)))

	odd := applierBinding("team-a", false, "argocd")
	odd.Name, odd.RoleRef.Name = "graph-writer", "graph-writer"
	c = fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a"}}, odd, readerBinding("argocd", "team-a")).Build()
	cl = &graphcleanup.HealthRefCleanup{Client: c, APIReader: c, Identity: &graph.IdentityProvisioner{ApplierClusterRole: "graph-writer"}}
	require.NoError(t, cl.Start(ctx))
	assert.Len(t, readerBindingKeys(t, c), 1, "left alone")
}
