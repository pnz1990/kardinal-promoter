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

// TestBuilder_ScmProvider: the resolved provider is a static field of every
// PromotionStep template, in the node shape and in the compact shape (one
// forEach template), and none is written without one.
func TestBuilder_ScmProvider(t *testing.T) {
	id := &kardinalv1alpha1.ScmProviderIdentity{Kind: kardinalv1alpha1.KindScmProvider, Name: "gitlab", UID: "u-1"}
	for _, tt := range []struct {
		name    string
		id      *kardinalv1alpha1.ScmProviderIdentity
		compact bool
		steps   int
	}{{"with providerRef", id, false, 2}, {"without providerRef", nil, false, 2},
		{"compact with providerRef", id, true, 1}, {"compact without providerRef", nil, true, 1}} {
		t.Run(tt.name, func(t *testing.T) {
			b := graph.NewBuilder()
			if tt.compact {
				b.CompactAbove = 0
			}
			res, err := b.Build(graph.BuildInput{
				Pipeline:    makeLinearPipeline("app", "test", "prod"),
				Bundle:      makeBundle("app-v1", "app"),
				ScmProvider: tt.id,
			})
			require.NoError(t, err)
			assertKroValid(t, res.Graph)
			steps := 0
			for _, n := range res.Graph.Spec.Nodes {
				if n.Template == nil || n.Template["kind"] != "PromotionStep" {
					continue
				}
				steps++
				spec := n.Template["spec"].(map[string]interface{})
				if tt.id == nil {
					assert.NotContains(t, spec, "scmProvider", n.ID)
					continue
				}
				assert.Equal(t, map[string]interface{}{"kind": "ScmProvider", "name": "gitlab", "uid": "u-1"}, spec["scmProvider"], n.ID)
			}
			assert.Equal(t, tt.steps, steps)
		})
	}
}
