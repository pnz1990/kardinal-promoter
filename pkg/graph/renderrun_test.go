// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// renderPipeline is test → prod; prod renders to env/prod, pr-review.
func renderPipeline() *kardinalv1alpha1.Pipeline {
	p := makeLinearPipeline("app", "test", "prod")
	p.Spec.Git.SecretRef = &kardinalv1alpha1.SecretRef{Name: "git-creds"}
	p.Spec.Environments[1].Layout = "branch"
	p.Spec.Environments[1].Approval = "pr-review"
	p.Spec.Environments[1].Render = &kardinalv1alpha1.RenderConfig{OnDrift: "fail"}
	return p
}

// TestBuilder_RenderRunNode (#1447): a layout: branch environment gets a
// RenderRun node whose name resolves once its step asks for the render, a
// mirror node writing the RenderRun back onto the step, and the Graph gets
// the refSteps and refRenderRuns read-back refs. Other environments do not.
func TestBuilder_RenderRunNode(t *testing.T) {
	b := makeBundle("app-v1", "app")
	b.Spec.Images[0].Tag = "1.2.3"
	b.Spec.Provenance = &kardinalv1alpha1.BundleProvenance{Author: "ci ${USER}"}
	b.Spec.Images[0].Repository = "ghcr.io/org/${weird}"
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: renderPipeline(), Bundle: b})
	require.NoError(t, err)
	g := res.Graph
	for _, id := range []string{"refSteps", "refRenderRuns", "render0prod", "live0prod"} {
		assert.True(t, hasNode(g, id), "node %s", id)
	}
	for _, id := range []string{"render0test", "live0test", "refHookRuns"} {
		assert.False(t, hasNode(g, id), "no node %s", id)
	}

	n := hookNode(t, g, "render0prod")
	assert.Equal(t, "RenderRun", n.Template["kind"])
	meta := n.Template["metadata"].(map[string]interface{})
	name := meta["name"].(string)
	assert.Contains(t, name, `s.?status.?renderRequestedAt.hasValue()`, "created once the step reached its render step")
	assert.Contains(t, name, `bundle.status.phase != "Superseded"`)
	assert.Contains(t, name, graph.RenderRunName("app", "app-v1", "prod"))
	spec := n.Template["spec"].(map[string]interface{})
	git := spec["git"].(map[string]interface{})
	assert.Equal(t, "git-creds", git["secretName"])
	assert.Equal(t, "main", git["sourceBranch"])
	assert.Equal(t, "env/prod", git["renderedBranch"])
	assert.Contains(t, git["pullRequest"], `renderPullRequest`, "the step's recorded list decides where to push")
	assert.Equal(t, "environments/prod", spec["path"])
	bundle := spec["bundle"].(map[string]interface{})
	img := bundle["images"].([]interface{})[0].(map[string]interface{})
	assert.Equal(t, "1.2.3", img["tag"])
	assert.True(t, strings.HasPrefix(img["repository"].(string), `${"`), "a ${ in the Bundle is a literal, not CEL: %s", img["repository"])
	assert.NotContains(t, bundle, "provenance", "only what a render needs: no provenance, no zero timestamps")
	assert.NotContains(t, bundle, "intent")
	assert.Equal(t, "fail", spec["render"].(map[string]interface{})["onDrift"])

	live := hookNode(t, g, "live0prod")
	l := live.Patch["spec"].(map[string]interface{})["live"].(map[string]interface{})
	assert.Contains(t, l["renders"], "refRenderRuns.filter(r, r.metadata.name in [\""+graph.RenderRunName("app", "app-v1", "prod")+"\"]")
	assert.Contains(t, l["renders"], `kro.run/node-id`, "only a RenderRun kro applied")
	assert.Contains(t, l["renders"], `r.spec.environment == "prod"`)
	assert.NotContains(t, l, "hooks")
}

// TestBuilder_RenderAndHooksShareTheMirror: an environment with hooks and
// layout: branch has one mirror node writing both.
func TestBuilder_RenderAndHooksShareTheMirror(t *testing.T) {
	p := renderPipeline()
	p.Spec.Environments[1].Hooks = []kardinalv1alpha1.HookSpec{hook("migrate", "pre", hookJob)}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app")})
	require.NoError(t, err)
	live := hookNode(t, res.Graph, "live0prod")
	l := live.Patch["spec"].(map[string]interface{})["live"].(map[string]interface{})
	assert.Contains(t, l, "hooks")
	assert.Contains(t, l, "renders")
	count := 0
	for _, n := range res.Graph.Spec.Nodes {
		if n.ID == "refSteps" {
			count++
		}
	}
	assert.Equal(t, 1, count, "one refSteps")
}

// TestBuilder_RenderRunRollback: a rollback's RenderRun names the Bundle it
// restores.
func TestBuilder_RenderRunRollback(t *testing.T) {
	b := makeBundle("app-rb", "app")
	b.Spec.Provenance = &kardinalv1alpha1.BundleProvenance{RollbackOf: "app-v1"}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: renderPipeline(), Bundle: b})
	require.NoError(t, err)
	spec := hookNode(t, res.Graph, "render0prod").Template["spec"].(map[string]interface{})
	assert.Equal(t, "app-v1", spec["bundle"].(map[string]interface{})["rollbackOf"])
}

// TestRenderRunName is a valid Job name, unique per Pipeline, Bundle and
// environment.
func TestRenderRunName(t *testing.T) {
	a := graph.RenderRunName("app", "app-v1", "prod")
	assert.LessOrEqual(t, len(a), 63)
	assert.NotEqual(t, a, graph.RenderRunName("app", "app-v1", "staging"))
	long := graph.RenderRunName(strings.Repeat("p", 60), strings.Repeat("b", 60), "prod")
	assert.LessOrEqual(t, len(long), 63)
}

// TestCompact_RefusesRendersAndHooks: the compact shape builds no RenderRun,
// HookRun or live mirror nodes, so a Pipeline with layout: branch or hooks
// is refused in it (naming the feature) and reported by CompactUnsupported;
// the node shape builds both.
func TestCompact_RefusesRendersAndHooks(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pipeline *kardinalv1alpha1.Pipeline
		feature  string
	}{
		{name: "layout branch", pipeline: renderPipeline(), feature: "rendered manifests (layout: branch)"},
		{name: "hooks", pipeline: hookPipeline(), feature: "pre- and post-deploy hooks (spec.environments[].hooks)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, []string{tc.feature}, graph.CompactUnsupported(graph.BuildInput{Pipeline: tc.pipeline}))
			build := func(shape string) error {
				p := tc.pipeline.DeepCopy()
				p.Annotations = map[string]string{graph.AnnotationGraphShape: shape}
				_, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app")})
				return err
			}
			require.NoError(t, build(graph.GraphShapeNodes))
			err := build(graph.GraphShapeCompact)
			require.ErrorIs(t, err, graph.ErrInvalid)
			assert.Contains(t, err.Error(), "does not support "+tc.feature)
		})
	}
	assert.Empty(t, graph.CompactUnsupported(graph.BuildInput{Pipeline: makeLinearPipeline("app", "test", "prod")}))
}

// TestBuilder_LiveRendersMirrorsKnownDigests: the mirror's renders
// expression copies each RenderRun's status.knownMarkerDigests onto the step
// (the render step checks a noChanges result against them), and an empty
// list when the RenderRun has none yet.
func TestBuilder_LiveRendersMirrorsKnownDigests(t *testing.T) {
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: renderPipeline(), Bundle: makeBundle("app-v1", "app")})
	require.NoError(t, err)
	l := hookNode(t, res.Graph, "live0prod").Patch["spec"].(map[string]interface{})["live"].(map[string]interface{})
	expr := l["renders"].(string)
	rendered := graph.RenderRunName("app", "app-v1", "prod")
	run := func(name string, status map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{"metadata": map[string]interface{}{"name": name,
			"labels": map[string]interface{}{"kro.run/node-id": "render0prod"}},
			"spec": map[string]interface{}{"environment": "prod"}, "status": status}
	}
	out, err := celEval(t, expr, map[string]interface{}{"refRenderRuns": []interface{}{
		run(rendered, map[string]interface{}{"phase": "Succeeded", "knownMarkerDigests": []interface{}{"m1", "m0"},
			"result": map[string]interface{}{"commitSHA": "c", "noChanges": true}}),
		run("forged-by-hand", map[string]interface{}{"phase": "Succeeded"}),
	}})
	require.NoError(t, err)
	renders := out.([]interface{})
	require.Len(t, renders, 1, "only the RenderRun this Graph rendered")
	first := renders[0].(map[string]interface{})
	assert.Equal(t, []interface{}{"m1", "m0"}, first["knownMarkerDigests"])
	assert.Equal(t, "c", first["result"].(map[string]interface{})["commitSHA"])
	assert.Equal(t, true, first["result"].(map[string]interface{})["noChanges"])
	out, err = celEval(t, expr, map[string]interface{}{"refRenderRuns": []interface{}{run(rendered, map[string]interface{}{})}})
	require.NoError(t, err)
	second := out.([]interface{})[0].(map[string]interface{})
	assert.Equal(t, []interface{}{}, second["knownMarkerDigests"], "none yet")
	assert.Equal(t, "Pending", second["phase"])
}
