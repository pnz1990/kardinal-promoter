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

// E2E-R14: one list of the reserved fields that make every Bundle fail, shared
// by the Pipeline reconciler and "kardinal validate".
func TestUnimplementedFields(t *testing.T) {
	tests := []struct {
		name string
		git  kardinalv1alpha1.PipelineGit
		env  kardinalv1alpha1.EnvironmentSpec
		want []string // substrings, one per message, in order
	}{
		{name: "nothing reserved", env: kardinalv1alpha1.EnvironmentSpec{Name: "test", Layout: "directory"}},
		{name: "one region has no effect", env: kardinalv1alpha1.EnvironmentSpec{Name: "test", Regions: []string{"us-east-1"}}},
		{name: "steps", env: kardinalv1alpha1.EnvironmentSpec{Name: "test",
			Steps: []kardinalv1alpha1.StepSpec{{Uses: "git-clone"}}},
			want: []string{`environment "test" declares 1 steps; spec.environments[].steps is not implemented`}},
		{name: "promotionTemplate", env: kardinalv1alpha1.EnvironmentSpec{Name: "test",
			PromotionTemplate: &kardinalv1alpha1.PromotionTemplateRef{Name: "tmpl"}},
			want: []string{`references PromotionTemplate "tmpl"`}},
		{name: "autoRollback", env: kardinalv1alpha1.EnvironmentSpec{Name: "test",
			AutoRollback: &kardinalv1alpha1.AutoRollbackSpec{}},
			want: []string{`environment "test": environments[].autoRollback is not implemented`}},
		{name: "regions fan-out", env: kardinalv1alpha1.EnvironmentSpec{Name: "test",
			Regions: []string{"us-east-1", "eu-west-1"}},
			want: []string{`environment "test": environments[].regions fan-out is not implemented`}},
		{name: "environment layout branch", env: kardinalv1alpha1.EnvironmentSpec{Name: "test", Layout: "branch"},
			want: []string{`environment "test": layout: branch is not implemented`}},
		{name: "pipeline layout branch", git: kardinalv1alpha1.PipelineGit{Layout: "branch"},
			env:  kardinalv1alpha1.EnvironmentSpec{Name: "test"},
			want: []string{"spec.git.layout: branch is not implemented"}},
		{name: "health.cluster", env: kardinalv1alpha1.EnvironmentSpec{Name: "test",
			Health: kardinalv1alpha1.HealthConfig{Cluster: "prod-eu"}},
			want: []string{`environment "test": health.cluster is not supported`}},
		{name: "every field is reported", git: kardinalv1alpha1.PipelineGit{Layout: "branch"},
			env: kardinalv1alpha1.EnvironmentSpec{Name: "test", Layout: "branch",
				Steps: []kardinalv1alpha1.StepSpec{{Uses: "git-clone"}}, Regions: []string{"a", "b"}},
			want: []string{"spec.git.layout", "steps is not implemented", "regions fan-out", `"test": layout: branch`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := pipelineOf("app", tc.env)
			p.Spec.Git = tc.git
			got := graph.UnimplementedFields(p)
			require.Len(t, got, len(tc.want), "%q", got)
			for i, w := range tc.want {
				assert.Contains(t, got[i], w)
			}
		})
	}
}
