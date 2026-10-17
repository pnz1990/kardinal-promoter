//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// closePRFinalizer is the PromotionStep finalizer that closes the step's PR
// when the step is deleted.
const closePRFinalizer = "kardinal.io/close-pr"

// kroGraphFinalizer is the finalizer kro keeps on a Graph until it has
// deleted the Graph's children.
const kroGraphFinalizer = "kro.run/graph-finalizer"

// TestCLI_DeleteBundleClosesPR deletes, with kardinal delete bundle, a Bundle
// whose prod step waits for its PR to merge. The command's help says deleting
// a Bundle cancels its in-progress promotion, so the PR must be closed, with a
// comment, as when a newer Bundle supersedes one: a PR left open can still be
// merged, and merging it would change prod with no PromotionStep tracking it.
// Only the step with the open PR holds the close-pr finalizer; the Verified
// test step holds none and goes at once. Covers CLI-DELETE-BUNDLE-01,
// STEP-DELETE-PR-01.
func TestCLI_DeleteBundleClosesPR(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))

	b := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, b, "test", "Verified", promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, b, "prod", "WaitingForMerge", promoteTimeout)
	pr := a.openPR(t, b, "prod")
	assert.Contains(t, stepFinalizers(t, e, a.ns, b, "prod"), closePRFinalizer, "the step with the open PR")
	assert.NotContains(t, stepFinalizers(t, e, a.ns, b, "test"), closePRFinalizer, "the Verified step")

	assert.Contains(t, e.MustKardinal(t, a.ns, "delete", "bundle", b), "Bundle "+b+" deleted\n")
	framework.Eventually(t, 2*time.Minute, "the steps of "+b+" are gone", func(ctx context.Context) (bool, string) {
		var left []string
		for _, env := range a.envs {
			ps, ok, err := e.Step(ctx, a.ns, pipelineName, b, env)
			if err != nil {
				return false, err.Error()
			}
			if ok {
				left = append(left, fmt.Sprintf("%s (%s, finalizers %v)", env, ps.Status.State, ps.Finalizers))
			}
		}
		return len(left) == 0, fmt.Sprintf("left: %v", left)
	})
	e.WaitPRState(t, a.repo, pr.Number, "closed", time.Minute)
	assert.Len(t, e.PRComments(t, a.repo, pr.Number, "kardinal closed this PR: bundle "+b+" was deleted"), 1,
		"one comment says why kardinal closed the PR")
	assertEnvAt(t, a, "prod", fixtures.V1)
}

// TestGraph_NamespaceDeletionFinishes deletes a namespace whose Bundle waits
// on a prod PR, after first deleting the Graph's applier RoleBinding
// (<release>-graph-applier). The namespace controller deletes RoleBindings,
// Bundles and Graphs in no set order; this fixes the order that used to hang.
// kro tears a Graph down as the kardinal-graph ServiceAccount, and without
// the RoleBinding every delete is 403 ("cannot delete resource
// promotionsteps"), so kro kept its finalizer and the namespace stayed
// Terminating for good. The controller now drops kro's finalizer from its own
// Graphs in a terminating namespace once the applier RoleBinding is gone, and
// the namespace controller deletes the children. The prod step's close-pr
// finalizer must not stall the namespace either: the controller closes the PR,
// with one comment naming the namespace, and lets the step go.
//
// Covers GRAPH-NSDELETE-01, STEP-DELETE-PR-02.
func TestGraph_NamespaceDeletionFinishes(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	b := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, b, "prod", "WaitingForMerge", promoteTimeout)
	pr := a.openPR(t, b, "prod")
	assert.Contains(t, stepFinalizers(t, e, a.ns, b, "prod"), closePRFinalizer, "the step with the open PR")
	graph := a.bundle(t, b).Status.GraphRef
	require.NotEmpty(t, graph, "the Bundle has a Graph")

	// Runs before the namespace cleanup e.Namespace registered: on failure it
	// lets a stuck namespace go.
	t.Cleanup(func() { releaseNamespace(t, e, a.ns) })
	rb := applierBinding(t, e, a.ns)
	require.NoError(t, e.Kube.RbacV1().RoleBindings(a.ns).Delete(ctx, rb.Name, metav1.DeleteOptions{}))
	require.NoError(t, e.Kube.CoreV1().Namespaces().Delete(ctx, a.ns, metav1.DeleteOptions{}))

	framework.Eventually(t, 3*time.Minute, "namespace "+a.ns+" is gone", func(ctx context.Context) (bool, string) {
		ns, err := e.Kube.CoreV1().Namespaces().Get(ctx, a.ns, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, ""
		}
		if err != nil {
			return false, err.Error()
		}
		return false, fmt.Sprintf("phase=%s; %s", ns.Status.Phase, describeGraph(ctx, e, a.ns, graph))
	})
	e.WaitPRState(t, a.repo, pr.Number, "closed", time.Minute)
	assert.Len(t, e.PRComments(t, a.repo, pr.Number, "kardinal closed this PR: namespace "+a.ns+" was deleted"), 1,
		"one comment says why kardinal closed the PR")
}

// nodesShape pins p to the nodes Graph shape, whatever the controller's
// --graph-compact-above.
func nodesShape(p *v1alpha1.Pipeline) *v1alpha1.Pipeline {
	if p.Annotations == nil {
		p.Annotations = map[string]string{}
	}
	p.Annotations["kardinal.io/graph-shape"] = "nodes"
	return p
}

// healthRefCleanupMessage is what the controller logs when it has removed
// the health ref nodes an earlier release left in Graphs.
const healthRefCleanupMessage = "removed the health ref nodes of an earlier release from Graphs (#1283)"

// TestGraph_HealthRefCleanup stops the controller and leaves what a release
// before #1283 left: a health ref node and the reader labels in a Graph, a
// reader RoleBinding in argocd recorded on the applier RoleBinding, and an
// unrecorded one of the same shape for another namespace. When the
// controller starts, it removes the health ref node and the labels, keeps
// every other node, deletes the recorded binding and the record, and keeps
// the unrecorded binding (the upgrade notes list those). The promotion goes
// on. Not parallel: it stops the controller.
//
// Covers GRAPH-HEALTHREF-CLEANUP-01.
func TestGraph_HealthRefCleanup(t *testing.T) {
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, nodesShape(a.pipeline(map[string]string{"prod": "pr-review"})))
	b := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, b, "prod", "WaitingForMerge", promoteTimeout)
	graph := a.bundle(t, b).Status.GraphRef
	require.NotEmpty(t, graph)
	applier := applierBinding(t, e, a.ns)
	readerRole := strings.TrimSuffix(applier.RoleRef.Name, "-graph-applier") + "-graph-reader"
	sa := applier.Subjects[0]

	start := e.StopController(t)
	g, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(a.ns).Get(ctx, graph, metav1.GetOptions{})
	require.NoError(t, err)
	nodes, _, _ := unstructured.NestedSlice(g.Object, "spec", "nodes")
	want := len(nodes)
	nodes = append(nodes, map[string]interface{}{"id": "healthProd", "ref": map[string]interface{}{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]interface{}{"name": fixtures.Workload("prod"), "namespace": a.ns}}})
	require.NoError(t, unstructured.SetNestedSlice(g.Object, nodes, "spec", "nodes"))
	labels := g.GetLabels()
	labels["kardinal.io/reader-labels"], labels["reads.kardinal.io/"+a.ns] = "true", "true"
	g.SetLabels(labels)
	_, err = e.Dynamic.Resource(framework.GraphGVR).Namespace(a.ns).Update(ctx, g, metav1.UpdateOptions{})
	require.NoError(t, err, "put back a health ref node, as an earlier release built the Graph")

	other := e.Namespace(t)
	old := func(graphNS string) *rbacv1.RoleBinding {
		return &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: readerRole + "-" + graphNS, Namespace: framework.ArgoCDNamespace,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "kardinal-promoter"}},
			RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: readerRole},
			Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: sa.Name, Namespace: graphNS}},
		}
	}
	recorded, unrecorded := old(a.ns), old(other)
	for _, rb := range []*rbacv1.RoleBinding{recorded, unrecorded} {
		created, err := e.Kube.RbacV1().RoleBindings(rb.Namespace).Create(ctx, rb, metav1.CreateOptions{})
		require.NoError(t, err, "create RoleBinding %s", rb.Name)
		*rb = *created
		t.Cleanup(func() {
			err := e.Kube.RbacV1().RoleBindings(rb.Namespace).Delete(context.Background(), rb.Name, metav1.DeleteOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("delete RoleBinding %s: %v", rb.Name, err)
			}
		})
	}
	applier = applierBinding(t, e, a.ns)
	if applier.Annotations == nil {
		applier.Annotations = map[string]string{}
	}
	applier.Annotations["kardinal.io/reader-namespaces"] = framework.ArgoCDNamespace + "," + a.ns
	_, err = e.Kube.RbacV1().RoleBindings(a.ns).Update(ctx, applier, metav1.UpdateOptions{})
	require.NoError(t, err, "record the reader binding, as an earlier release did")

	started := time.Now()
	start()
	e.WaitControllerLog(t, started, 2*time.Minute, "the cleanup removes the health ref node",
		framework.LogMessage(healthRefCleanupMessage, "runnable", "health-ref-cleanup"))
	g, err = e.Dynamic.Resource(framework.GraphGVR).Namespace(a.ns).Get(ctx, graph, metav1.GetOptions{})
	require.NoError(t, err)
	nodes, _, _ = unstructured.NestedSlice(g.Object, "spec", "nodes")
	assert.Len(t, nodes, want, "only the health ref node goes")
	assert.NotContains(t, g.GetLabels(), "kardinal.io/reader-labels")
	assert.NotContains(t, g.GetLabels(), "reads.kardinal.io/"+a.ns)
	framework.Eventually(t, time.Minute, "the recorded reader binding and the record deleted", func(ctx context.Context) (bool, string) {
		_, err := e.Kube.RbacV1().RoleBindings(framework.ArgoCDNamespace).Get(ctx, recorded.Name, metav1.GetOptions{})
		if !apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("recorded binding: %v", err)
		}
		_, ok := applierBinding(t, e, a.ns).Annotations["kardinal.io/reader-namespaces"]
		return !ok, "the record is still there"
	})
	got, err := e.Kube.RbacV1().RoleBindings(framework.ArgoCDNamespace).Get(ctx, unrecorded.Name, metav1.GetOptions{})
	if assert.NoError(t, err, "the unrecorded binding is kept") {
		assert.Equal(t, unrecorded.UID, got.UID)
	}

	pr := a.openPR(t, b, "prod")
	require.NoError(t, e.Git.MergePR(ctx, a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, b, "prod", "Verified", promoteTimeout)
}

// stepFinalizers returns the finalizers of the step of bundle for env.
func stepFinalizers(t *testing.T, e *framework.Env, ns, bundle, env string) []string {
	t.Helper()
	ps, ok, err := e.Step(context.Background(), ns, pipelineName, bundle, env)
	require.NoError(t, err)
	require.True(t, ok, "the %s step of %s exists", env, bundle)
	return ps.Finalizers
}

// applierBinding returns the RoleBinding of the Graph applier role in ns,
// which the controller creates for the kardinal-graph service account.
func applierBinding(t *testing.T, e *framework.Env, ns string) *rbacv1.RoleBinding {
	t.Helper()
	list, err := e.Kube.RbacV1().RoleBindings(ns).List(context.Background(), metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/managed-by=kardinal-promoter"})
	require.NoError(t, err)
	for i := range list.Items {
		if strings.HasSuffix(list.Items[i].RoleRef.Name, "-graph-applier") {
			return &list.Items[i]
		}
	}
	t.Fatalf("no Graph applier RoleBinding in %s", ns)
	return nil
}

// describeGraph reports the finalizers and conditions of Graph name in ns.
func describeGraph(ctx context.Context, e *framework.Env, ns, name string) string {
	g, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "Graph gone"
	}
	if err != nil {
		return err.Error()
	}
	conds, _, _ := unstructured.NestedSlice(g.Object, "status", "conditions")
	return fmt.Sprintf("Graph finalizers=%v conditions=%v", g.GetFinalizers(), conds)
}

// releaseNamespace lets a namespace a failed test left Terminating go: it
// writes diagnostics, then removes the kro finalizer from the Graphs and the
// close-pr finalizer from the PromotionSteps still in it.
func releaseNamespace(t *testing.T, e *framework.Env, ns string) {
	ctx := context.Background()
	if _, err := e.Kube.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); apierrors.IsNotFound(err) {
		return
	}
	if t.Failed() {
		e.Diagnose(t, ns)
	}
	stepGVR := schema.GroupVersionResource{Group: v1alpha1.GroupVersion.Group, Version: v1alpha1.GroupVersion.Version, Resource: "promotionsteps"}
	for gvr, finalizer := range map[schema.GroupVersionResource]string{
		framework.GraphGVR: kroGraphFinalizer,
		stepGVR:            closePRFinalizer,
	} {
		list, err := e.Dynamic.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Logf("list %s in %s: %v", gvr.Resource, ns, err)
			continue
		}
		for _, o := range list.Items {
			if !slices.Contains(o.GetFinalizers(), finalizer) {
				continue
			}
			patch := fmt.Sprintf(`{"metadata":{"finalizers":null,"resourceVersion":%q}}`, o.GetResourceVersion())
			if _, err := e.Dynamic.Resource(gvr).Namespace(ns).Patch(ctx, o.GetName(), types.MergePatchType,
				[]byte(patch), metav1.PatchOptions{}); err != nil && !apierrors.IsNotFound(err) {
				t.Logf("release %s %s/%s: %v", gvr.Resource, ns, o.GetName(), err)
				continue
			}
			t.Logf("removed %s from %s %s/%s", finalizer, gvr.Resource, ns, o.GetName())
		}
	}
}
