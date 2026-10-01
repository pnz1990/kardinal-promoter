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
// finalizer must not stall the namespace either: the controller closes the PR
// and lets the step go. The reader RoleBinding in argocd goes with the
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
	require.NotEmpty(t, readerBindings(t, e, a.ns), "the Graph reads argocd through a reader binding")

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
	framework.Eventually(t, time.Minute, "no reader binding for "+a.ns+" in argocd", func(context.Context) (bool, string) {
		left := readerBindings(t, e, a.ns)
		return len(left) == 0, fmt.Sprintf("left: %v", left)
	})
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
	a.apply(t, a.pipeline(nil))
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
