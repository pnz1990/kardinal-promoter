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
	assert.Equal(t, "1.2.3", bundle["images"].([]interface{})[0].(map[string]interface{})["tag"])
	author := bundle["provenance"].(map[string]interface{})["author"].(string)
	assert.True(t, strings.HasPrefix(author, `${"`), "a ${ in the Bundle is a literal, not CEL: %s", author)
	assert.Equal(t, "fail", spec["render"].(map[string]interface{})["onDrift"])

	live := hookNode(t, g, "live0prod")
	l := live.Patch["spec"].(map[string]interface{})["live"].(map[string]interface{})
	assert.Contains(t, l["renders"], "refRenderRuns.filter(r, r.spec.environment == \"prod\")")
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

// TestRenderRunName is a valid Job name, unique per Pipeline, Bundle and
// environment.
func TestRenderRunName(t *testing.T) {
	a := graph.RenderRunName("app", "app-v1", "prod")
	assert.LessOrEqual(t, len(a), 63)
	assert.NotEqual(t, a, graph.RenderRunName("app", "app-v1", "staging"))
	long := graph.RenderRunName(strings.Repeat("p", 60), strings.Repeat("b", 60), "prod")
	assert.LessOrEqual(t, len(long), 63)
}
