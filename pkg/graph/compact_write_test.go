// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// TestCompact_BatchedWrites (#2366): spec.git.writes: batched makes the Graph
// compact, with one BranchWrite for the Bundle's branch listing the
// environments it writes (the auto ones: not pr-review). Its requests are the
// Promoting steps of this BranchWrite that asked (status.writeRequest), and a
// step's spec.write names it and its spec.live.writes reads its own result
// back from the BranchWrite kro applied for this Bundle.
func TestCompact_BatchedWrites(t *testing.T) {
	p := batchedPipeline(makeLinearPipeline("app", "test", "uat", "prod"))
	p.Spec.Git.URL = "https://git.example.com/org/app.git"
	p.Spec.Environments[2].Approval = "pr-review"
	b := makeBundle("app-v1", "app")
	b.UID = "uid-1"
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: b})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)
	g := res.Graph
	assert.Equal(t, graph.GraphShapeCompact, g.Labels[graph.LabelGraphShape], "batched writes use the compact shape")
	assert.True(t, hasNode(g, "refBranchWrites"))
	name := graph.BranchWriteName("app-v1", p.Spec.Git.URL, "main")
	testStep, uatStep, prodStep := "app-app-v1-test", "app-app-v1-uat", "app-app-v1-prod"

	bw := hookNode(t, g, graph.NodeBranchWrites).Template
	assert.Equal(t, "BranchWrite", bw["kind"])
	assert.Equal(t, name, bw["metadata"].(map[string]interface{})["name"])
	spec := bw["spec"].(map[string]interface{})
	assert.Equal(t, []interface{}{
		map[string]interface{}{"name": "test", "step": testStep},
		map[string]interface{}{"name": "uat", "step": uatStep},
	}, spec["environments"], "the auto environments, not pr-review prod")
	assert.Equal(t, "main", spec["branch"])
	assert.Equal(t, p.Spec.Git.URL, spec["url"])

	step := func(name, write, state string, request int64) map[string]interface{} {
		st := map[string]interface{}{"state": state}
		if request > 0 {
			st["writeRequest"] = request
		}
		return map[string]interface{}{"metadata": map[string]interface{}{"name": name, "uid": "u-" + name},
			"spec": map[string]interface{}{"write": write}, "status": st}
	}
	requested := evalCEL(t, spec["requested"].(string), map[string]interface{}{graph.NodeStepsObserved: []interface{}{
		step(testStep, name, "Promoting", 2),
		step(uatStep, name, "Pending", 0),
		step(prodStep, "", "Promoting", 1),
		step("app-app-v1-other", "another-write", "Promoting", 1),
		step("app-app-v1-done", name, "Verified", 1),
	}}).([]interface{})
	require.Len(t, requested, 1, "only a Promoting step of this BranchWrite that asked")
	assert.Equal(t, map[string]interface{}{"step": testStep, "uid": "u-" + testStep, "request": 2}, requested[0])

	// Each DAG entry names its BranchWrite, and the step template copies it.
	for _, e := range hookNode(t, g, graph.NodePromotionDAG).Def["steps"].([]interface{}) {
		entry := e.(map[string]interface{})
		want := name
		if entry["environment"] == "prod" {
			want = ""
		}
		assert.Equal(t, want, entry["write"], entry["environment"])
	}
	steps := hookNode(t, g, graph.NodePromotionSteps).Template["spec"].(map[string]interface{})
	assert.Equal(t, "${Step.write}", steps["write"])
	expr := steps["live"].(map[string]interface{})["writes"].(string)
	applied := map[string]interface{}{"metadata": map[string]interface{}{"name": name,
		"labels": map[string]interface{}{graph.LabelKRONodeID: graph.NodeBranchWrites, graph.LabelBundleUID: "uid-1"}},
		"status": map[string]interface{}{"environments": map[string]interface{}{
			"test": map[string]interface{}{"step": testStep, "request": int64(2), "round": int64(3), "commitSHA": "abc"}}}}
	forged := map[string]interface{}{"metadata": map[string]interface{}{"name": name},
		"status": map[string]interface{}{"environments": map[string]interface{}{"test": map[string]interface{}{"commitSHA": "evil"}}}}
	for _, tc := range []struct {
		env, write string
		want       int
	}{{"test", name, 1}, {"uat", name, 0}, {"prod", "", 0}} {
		out := evalCEL(t, expr, map[string]interface{}{"refBranchWrites": []interface{}{applied, forged},
			"Step": map[string]interface{}{"environment": tc.env, "write": tc.write}}).([]interface{})
		require.Len(t, out, tc.want, "%s: its own result, from the BranchWrite kro applied", tc.env)
		if tc.want == 1 {
			r := out[0].(map[string]interface{})
			assert.Equal(t, name, r["name"])
			result := r["result"].(map[string]interface{})
			assert.Equal(t, "abc", result["commitSHA"])
			assert.EqualValues(t, 2, result["request"])
			assert.Equal(t, false, result["cancelled"], "absent fields read as their zero value")
		}
	}
}

// TestCompact_BatchedWritesOnlyWhenBatched: a Pipeline without spec.git.writes
// has no BranchWrite and no write fields; a config Bundle of a batched
// Pipeline keeps the compact shape but writes its own changes; a Bundle
// pinned to the node shape cannot batch.
func TestCompact_BatchedWritesOnlyWhenBatched(t *testing.T) {
	b := makeBundle("app-v1", "app")
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: makeLinearPipeline("app", "test", "prod"), Bundle: b})
	require.NoError(t, err)
	assert.False(t, hasNode(res.Graph, graph.NodeBranchWrites))
	assert.False(t, hasNode(res.Graph, "refBranchWrites"))

	config := makeBundle("app-v1", "app")
	config.Spec.Type = "config"
	config.Spec.Images = nil
	config.Spec.ConfigRef = &kardinalv1alpha1.ConfigRef{GitRepo: "https://git.example.com/org/config.git", CommitSHA: "0123456789abcdef0123456789abcdef01234567"}
	res, err = graph.NewBuilder().Build(graph.BuildInput{Pipeline: batchedPipeline(makeLinearPipeline("app", "test", "prod")), Bundle: config})
	require.NoError(t, err)
	assert.Equal(t, graph.GraphShapeCompact, res.Graph.Labels[graph.LabelGraphShape])
	assert.False(t, hasNode(res.Graph, graph.NodeBranchWrites), "a config Bundle is not batched")
	steps := hookNode(t, res.Graph, graph.NodePromotionSteps).Template["spec"].(map[string]interface{})
	assert.NotContains(t, steps, "write")

	_, err = graph.NewBuilder().Build(graph.BuildInput{Pipeline: batchedPipeline(makeLinearPipeline("app", "test", "prod")),
		Bundle: b, Shape: graph.GraphShapeNodes})
	require.ErrorContains(t, err, "cannot batch writes")
}
