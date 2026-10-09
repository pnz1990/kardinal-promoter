//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// graphList is an empty list of kro Graphs.
func graphList() *unstructured.UnstructuredList {
	gl := &unstructured.UnstructuredList{}
	gl.SetGroupVersionKind(graph.GraphGVK.GroupVersion().WithKind(graph.GraphGVK.Kind + "List"))
	return gl
}

// waitRetired waits for bundle's Graph to be retired (#1492): GraphRetired is
// True with reason Retired, the Graph is gone, kro has deleted the Bundle's
// PromotionSteps, and status.retiredSteps has one Verified record per env.
func waitRetired(t *testing.T, e *framework.Env, ns, bundle string, envs ...string) {
	t.Helper()
	framework.Eventually(t, 3*time.Minute, bundle+" retired", func(ctx context.Context) (bool, string) {
		var b v1alpha1.Bundle
		if err := e.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: bundle}, &b); err != nil {
			return false, err.Error()
		}
		c := meta.FindStatusCondition(b.Status.Conditions, "GraphRetired")
		if c == nil || c.Reason != "Retired" {
			return false, fmt.Sprintf("GraphRetired %+v", c)
		}
		gl := graphList()
		if err := e.Client.List(ctx, gl, client.InNamespace(ns), client.MatchingLabels{"kardinal.io/bundle": bundle}); err != nil {
			return false, err.Error()
		}
		if len(gl.Items) > 0 {
			return false, "the Graph still exists"
		}
		var steps v1alpha1.PromotionStepList
		if err := e.Client.List(ctx, &steps, client.InNamespace(ns), client.MatchingLabels{"kardinal.io/bundle": bundle}); err != nil {
			return false, err.Error()
		}
		if len(steps.Items) > 0 {
			return false, fmt.Sprintf("%d PromotionStep(s) not deleted yet", len(steps.Items))
		}
		got := map[string]string{}
		for _, r := range b.Status.RetiredSteps {
			got[r.Environment] = r.State
		}
		want := map[string]string{}
		for _, env := range envs {
			want[env] = "Verified"
		}
		return assert.ObjectsAreEqual(want, got), fmt.Sprintf("retiredSteps %v", got)
	})
}

// TestGraph_RetiresFinishedBundles checks Graph retirement (#1492) on a real
// kro: the Graph of a finished Bundle is deleted after the Pipeline's
// kardinal.io/graph-retire-after delay, kro deletes its PromotionSteps, and
// the Bundle keeps them in status.retiredSteps. Rollback, history and the
// Pipeline phase then work from those records: with both Bundles retired,
// kardinal rollback rolls prod back from b2 to b1, and the rollback Bundle
// promotes.
//
// Covers GRAPH-RETIRE-01.
func TestGraph_RetiresFinishedBundles(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	p := a.pipeline(nil)
	p.Annotations = map[string]string{"kardinal.io/graph-retire-after": "20s"}
	a.apply(t, p)

	b1 := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	rbVerified(t, a, b1, "test", "prod")
	b2 := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	rbVerified(t, a, b2, "test", "prod")
	waitRetired(t, e, a.ns, b1, "test", "prod")
	waitRetired(t, e, a.ns, b2, "test", "prod")

	// The Pipeline phase and history come from the records.
	framework.Eventually(t, time.Minute, "Pipeline Ready from retired steps", func(ctx context.Context) (bool, string) {
		var pl v1alpha1.Pipeline
		if err := e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: pipelineName}, &pl); err != nil {
			return false, err.Error()
		}
		return pl.Status.Phase == "Ready", "phase " + pl.Status.Phase
	})
	var got []string
	for _, r := range rbHistory(t, a) {
		got = append(got, r["BUNDLE"]+"/"+r["ENV"])
	}
	assert.ElementsMatch(t, []string{b2 + "/prod", b2 + "/test", b1 + "/prod", b1 + "/test"}, got)

	// Rollback reads which Bundle is deployed, and which was Verified before,
	// from the records.
	_, rb := rbRollback(t, a, "prod")
	rbAssertBundle(t, e, a.ns, rb, "prod", b2, b1, cliUser(t), "")
	e.WaitStepState(t, a.ns, pipelineName, rb, "prod", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, rb, "Verified", time.Minute)
	assertEnvAt(t, a, "prod", fixtures.V2)

	// A retired Bundle is final: a Pipeline change does not rebuild its Graph.
	var pl v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: pipelineName}, &pl))
	pl.Spec.HistoryLimit = 40
	require.NoError(t, e.Client.Update(ctx, &pl))
	framework.Consistently(t, 15*time.Second, "no Graph rebuilt for "+b1, func(ctx context.Context) (bool, string) {
		gl := graphList()
		if err := e.Client.List(ctx, gl, client.InNamespace(a.ns), client.MatchingLabels{"kardinal.io/bundle": b1}); err != nil {
			return false, err.Error()
		}
		return len(gl.Items) == 0, fmt.Sprintf("%d Graph(s)", len(gl.Items))
	})
}
