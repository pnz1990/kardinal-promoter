// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// sizedGraph builds the Graph for a linear Pipeline of n environments with
// gates PolicyGates each.
func sizedGraph(t *testing.T, n, gates int) *graph.Graph {
	t.Helper()
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("region%03d", i)
	}
	var pgs []kardinalv1alpha1.PolicyGate
	for _, e := range names {
		for g := 0; g < gates; g++ {
			pgs = append(pgs, makePolicyGate(fmt.Sprintf("%s-gate%d", e, g), "default", e,
				`!schedule.isWeekend && upstream.uat.soakMinutes >= 30 && bundle.provenance.author != "dependabot[bot]"`))
		}
	}
	res, err := graph.NewBuilder().Build(graph.BuildInput{
		Pipeline:    makeLinearPipeline("payments-platform", names...),
		Bundle:      makeBundle("payments-platform-v1-29-0-abc1234", "payments-platform"),
		PolicyGates: pgs,
	})
	require.NoError(t, err)
	return res.Graph
}

func TestEstimateSize(t *testing.T) {
	g := sizedGraph(t, 3, 1)
	data, err := json.Marshal(g)
	require.NoError(t, err)
	templates := 0
	for _, n := range g.Spec.Nodes {
		if n.Template != nil {
			templates++
		}
	}
	size, err := graph.EstimateSize(g)
	require.NoError(t, err)
	// 3 PromotionSteps, 3 gate instances, 3 PRStatuses.
	assert.Equal(t, 9, graph.ObjectCount(g))
	assert.Equal(t, len(data)+9*260, size)
	assert.Less(t, templates, 9, "collections create several objects from one node")

	size, err = graph.EstimateSize(nil)
	require.NoError(t, err)
	assert.Zero(t, size)
}

func TestCheckSize(t *testing.T) {
	tests := []struct {
		name    string
		envs    int
		gates   int
		wantErr bool
	}{
		{name: "small pipeline", envs: 3, gates: 2},
		{name: "150 environments, 3 gates each", envs: 150, gates: 3},
		{name: "300 environments, 3 gates each", envs: 300, gates: 3, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := graph.CheckSize(sizedGraph(t, tc.envs, tc.gates))
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, graph.ErrInvalid, "the size depends only on the input: a retry fails the same way")
			assert.True(t, strings.HasPrefix(err.Error(), "graph size: the Graph for this Bundle would be about "), err.Error())
			assert.Contains(t, err.Error(), "split the Pipeline")
		})
	}
	assert.NoError(t, graph.CheckSize(nil))
}

// TestObjectCount checks that EstimateSize counts every object a collection
// creates, not one per node: 3 PromotionSteps, 6 gate instances and 3
// PRStatuses from 3 + 2 collection nodes.
func TestObjectCount(t *testing.T) {
	g := sizedGraph(t, 3, 2)
	assert.Equal(t, 12, graph.ObjectCount(g))
	collections := 0
	for _, n := range g.Spec.Nodes {
		if len(n.ForEach) > 0 {
			collections++
		}
	}
	assert.Equal(t, 2, collections, "PolicyGates and PRStatuses")
}
