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
// with one comment naming the namespace, and lets the step go. The reader RoleBinding in argocd goes with the
// namespace's last Graph. Covers GRAPH-NSDELETE-01, STEP-DELETE-PR-02,
// GRAPH-READER-PRUNE-02.
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
	// A compact Graph (the controller run with graph.compactAbove=0) has no
	// health ref, so no reader binding: the namespace deletion is checked
	// either way.
	compact := bundleGraph(t, e, a.ns, b).GetLabels()["kardinal.io/graph-shape"] == "compact"
	if !compact {
		require.NotEmpty(t, readerBindings(t, e, a.ns), "the Graph reads argocd through a reader binding")
	}

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
	framework.Eventually(t, time.Minute, "no reader binding for "+a.ns+" in argocd", func(context.Context) (bool, string) {
		left := readerBindings(t, e, a.ns)
		return len(left) == 0, fmt.Sprintf("left: %v", left)
	})
}

// nodesShape pins p to the nodes Graph shape. Reader RoleBindings exist only
// for health ref nodes, which a compact Graph does not have, so the reader
// binding tests pin the shape whatever the controller's --graph-compact-above.
func nodesShape(p *v1alpha1.Pipeline) *v1alpha1.Pipeline {
	if p.Annotations == nil {
		p.Annotations = map[string]string{}
	}
	p.Annotations["kardinal.io/graph-shape"] = "nodes"
	return p
}

// TestGraph_ReaderBindingPruned deletes the only Bundle of a namespace whose
// Graph read an Argo CD Application. For that read the controller created a
// RoleBinding in argocd granting the namespace's kardinal-graph service
// account the reader role. Prune used to run only while a Bundle was
// translated, so after the last Bundle was deleted the binding stayed in
// argocd for good. Now the controller prunes when a Graph is deleted.
// Covers GRAPH-READER-PRUNE-01.
func TestGraph_ReaderBindingPruned(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, nodesShape(a.pipeline(nil)))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	graph := a.bundle(t, bundle).Status.GraphRef
	require.NotEmpty(t, graph, "the Bundle has a Graph")
	require.NotEmpty(t, readerBindings(t, e, a.ns), "the Graph reads argocd through a reader binding")

	e.MustKardinal(t, a.ns, "delete", "bundle", bundle)
	framework.Eventually(t, 2*time.Minute, "Graph "+graph+" is gone", func(ctx context.Context) (bool, string) {
		_, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(a.ns).Get(ctx, graph, metav1.GetOptions{})
		return apierrors.IsNotFound(err), fmt.Sprintf("err=%v", err)
	})
	framework.Eventually(t, time.Minute, "no reader binding for "+a.ns+" in argocd", func(context.Context) (bool, string) {
		left := readerBindings(t, e, a.ns)
		return len(left) == 0, fmt.Sprintf("left: %v", left)
	})
}

// sweptMessage is what the controller logs for each reader RoleBinding it
// deletes.
const sweptMessage = "graph identity: deleted a reader rolebinding no Graph reads through"

// readerRecord is the annotation on the applier RoleBinding that records the
// namespaces the controller bound the reader role in.
const readerRecord = "kardinal.io/reader-namespaces"

// TestGraph_ReaderBindingSweep stops the controller and leaves behind, in
// argocd, two reader RoleBindings that no Graph reads through. One is of a
// namespace whose only Bundle, and so its Graph, is deleted while the
// controller is down; its record on the applier RoleBinding is removed too, as
// the versions that did not record their bindings left it. The other, made as
// the controller makes them, is of a namespace that is gone. The Graph-delete
// prune never sees either Graph go. When the controller starts, its startup
// sweep deletes exactly those two (the next sweep is 10 minutes away), and
// keeps the binding a live Graph reads through and an unlabeled binding of the
// same shape. An upgrade restarts the controller the same way, but the
// upgrade suite cannot leave such bindings: v0.8.1 made no RoleBindings.
// Not parallel: it stops the controller.
//
// Covers GRAPH-READER-SWEEP-01.
func TestGraph_ReaderBindingSweep(t *testing.T) {
	e := framework.New(t)
	ctx := context.Background()
	live, emptied := newArgoApp(t, e, "test"), newArgoApp(t, e, "test")
	var bundles []string
	for _, a := range []*app{live, emptied} {
		a.apply(t, nodesShape(a.pipeline(nil)))
		bundles = append(bundles, e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2))
	}
	for i, a := range []*app{live, emptied} {
		e.WaitStepState(t, a.ns, pipelineName, bundles[i], "test", "Verified", promoteTimeout)
	}
	liveRB, emptiedRB := readerBinding(t, e, live.ns), readerBinding(t, e, emptied.ns)
	assert.Equal(t, liveRB.RoleRef.Name+"-"+live.ns, liveRB.Name, "a reader binding is named after its Graph namespace")
	graph := emptied.bundle(t, bundles[1]).Status.GraphRef
	require.NotEmpty(t, graph, "the Bundle has a Graph")
	gone, goneUnlabeled := e.Namespace(t), e.Namespace(t)
	for _, ns := range []string{gone, goneUnlabeled} {
		require.NoError(t, e.Kube.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{}))
	}
	framework.Eventually(t, 2*time.Minute, "namespaces "+gone+" and "+goneUnlabeled+" are gone", func(ctx context.Context) (bool, string) {
		for _, ns := range []string{gone, goneUnlabeled} {
			if _, err := e.Kube.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				return false, fmt.Sprintf("%s: err=%v", ns, err)
			}
		}
		return true, ""
	})

	start := e.StopController(t)
	require.NoError(t, e.Client.Delete(ctx, emptied.bundle(t, bundles[1])))
	framework.Eventually(t, 2*time.Minute, "Graph "+graph+" is gone", func(ctx context.Context) (bool, string) {
		_, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(emptied.ns).Get(ctx, graph, metav1.GetOptions{})
		return apierrors.IsNotFound(err), describeGraph(ctx, e, emptied.ns, graph)
	})
	applier := applierBinding(t, e, emptied.ns)
	require.Contains(t, strings.Split(applier.Annotations[readerRecord], ","), framework.ArgoCDNamespace, "the record lists argocd")
	delete(applier.Annotations, readerRecord)
	_, err := e.Kube.RbacV1().RoleBindings(emptied.ns).Update(ctx, applier, metav1.UpdateOptions{})
	require.NoError(t, err, "remove the record")
	stale := sameShape(liveRB, gone, liveRB.Labels)
	unlabeled := sameShape(liveRB, goneUnlabeled, nil)
	for _, rb := range []*rbacv1.RoleBinding{stale, unlabeled} {
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
	assert.Equal(t, []string{emptiedRB.Name}, readerBindings(t, e, emptied.ns), "the Graph went while the controller was down")

	started := time.Now()
	start()
	for _, s := range []struct{ name, graphNS string }{{emptiedRB.Name, emptied.ns}, {stale.Name, gone}} {
		e.WaitControllerLog(t, started, time.Minute, "the startup sweep deletes "+s.name, framework.LogMessage(sweptMessage,
			"runnable", "graph-reader-sweep", "rolebinding", framework.ArgoCDNamespace+"/"+s.name, "graphNamespace", s.graphNS))
		_, err := e.Kube.RbacV1().RoleBindings(framework.ArgoCDNamespace).Get(ctx, s.name, metav1.GetOptions{})
		assert.True(t, apierrors.IsNotFound(err), "%s is deleted: err=%v", s.name, err)
	}
	for _, rb := range []*rbacv1.RoleBinding{liveRB, unlabeled} {
		got, err := e.Kube.RbacV1().RoleBindings(framework.ArgoCDNamespace).Get(ctx, rb.Name, metav1.GetOptions{})
		if assert.NoError(t, err, "%s is kept", rb.Name) {
			assert.Equal(t, rb.UID, got.UID, "%s is the same binding", rb.Name)
		}
	}
	assert.Empty(t, e.ControllerLogLines(t, started, framework.LogMessage(sweptMessage,
		"rolebinding", framework.ArgoCDNamespace+"/"+liveRB.Name)), "the live binding is not deleted")
}

// readerBinding returns the one reader RoleBinding in argocd for ns.
func readerBinding(t *testing.T, e *framework.Env, ns string) *rbacv1.RoleBinding {
	t.Helper()
	names := readerBindings(t, e, ns)
	require.Len(t, names, 1, "one reader binding for %s in argocd", ns)
	rb, err := e.Kube.RbacV1().RoleBindings(framework.ArgoCDNamespace).Get(context.Background(), names[0], metav1.GetOptions{})
	require.NoError(t, err)
	return rb
}

// sameShape is a reader RoleBinding like rb, with labels, for the
// kardinal-graph service account of graphNS.
func sameShape(rb *rbacv1.RoleBinding, graphNS string, labels map[string]string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: rb.RoleRef.Name + "-" + graphNS, Namespace: rb.Namespace, Labels: labels},
		RoleRef:    rb.RoleRef,
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: rb.Subjects[0].Name, Namespace: graphNS}},
	}
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

// readerBindings lists the RoleBindings in argocd that the controller created
// for the kardinal-graph service account of ns.
func readerBindings(t *testing.T, e *framework.Env, ns string) []string {
	t.Helper()
	list, err := e.Kube.RbacV1().RoleBindings(framework.ArgoCDNamespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/managed-by=kardinal-promoter"})
	require.NoError(t, err)
	var out []string
	for _, rb := range list.Items {
		for _, s := range rb.Subjects {
			if s.Kind == "ServiceAccount" && s.Namespace == ns {
				out = append(out, rb.Name)
			}
		}
	}
	return out
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
