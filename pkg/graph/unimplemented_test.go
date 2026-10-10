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

// E2E-R14: one list of the unimplemented or unsupported fields that fail a
// Bundle, shared by the Pipeline reconciler and "kardinal validate". It covers
// every refusal of promotionstep/config_check.go except git.secretRef.
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
			want: []string{`environment "test" declares 1 steps; spec.environments[].steps is not supported`}},
		{name: "promotionTemplate", env: kardinalv1alpha1.EnvironmentSpec{Name: "test",
			PromotionTemplate: &kardinalv1alpha1.PromotionTemplateRef{Name: "tmpl"}},
			want: []string{`references PromotionTemplate "tmpl"`}},
		{name: "autoRollback", env: kardinalv1alpha1.EnvironmentSpec{Name: "test",
			AutoRollback: &kardinalv1alpha1.AutoRollbackSpec{}},
			want: []string{`environment "test": environments[].autoRollback is not implemented`}},
		{name: "two regions", env: kardinalv1alpha1.EnvironmentSpec{Name: "test",
			Regions: []string{"us-east-1", "eu-west-1"}},
			want: []string{`environment "test": regions is not supported; declare one environment per region ` +
				`(prod-us, prod-eu) and use wave`}},
		// #1321: distributed mode was removed.
		{name: "shard", env: kardinalv1alpha1.EnvironmentSpec{Name: "test", Shard: "eu"},
			want: []string{`environment "test": shard is not supported: distributed mode was removed`}},
		// layout: branch is implemented (#1447).
		{name: "environment layout branch", env: kardinalv1alpha1.EnvironmentSpec{Name: "test", Layout: "branch"}},
		{name: "pipeline layout branch", git: kardinalv1alpha1.PipelineGit{Layout: "branch"},
			env: kardinalv1alpha1.EnvironmentSpec{Name: "test"}},
		{name: "health.cluster", env: kardinalv1alpha1.EnvironmentSpec{Name: "test",
			Health: kardinalv1alpha1.HealthConfig{Cluster: "prod-eu"}},
			want: []string{`environment "test": health.cluster is not supported`}},
		{name: "health.resource.kind other than Deployment", env: kardinalv1alpha1.EnvironmentSpec{Name: "test",
			Health: kardinalv1alpha1.HealthConfig{Resource: &kardinalv1alpha1.ResourceRef{Kind: "StatefulSet", Name: "db"}}},
			want: []string{`environment "test": health.resource.kind "StatefulSet" is not supported: only Deployment is checked`}},
		{name: "health.resource.kind Deployment", env: kardinalv1alpha1.EnvironmentSpec{Name: "test",
			Health: kardinalv1alpha1.HealthConfig{Resource: &kardinalv1alpha1.ResourceRef{Kind: "Deployment"}}}},
		{name: "health.resource is not read by another health type", env: kardinalv1alpha1.EnvironmentSpec{Name: "test",
			Health: kardinalv1alpha1.HealthConfig{Type: "argocd", Resource: &kardinalv1alpha1.ResourceRef{Kind: "StatefulSet"}}}},
		{name: "health.resource is not read under delivery.delegate", env: kardinalv1alpha1.EnvironmentSpec{Name: "test",
			Delivery: kardinalv1alpha1.DeliveryConfig{Delegate: "argoRollouts"},
			Health:   kardinalv1alpha1.HealthConfig{Resource: &kardinalv1alpha1.ResourceRef{Kind: "StatefulSet"}}}},
		{name: "every field is reported", git: kardinalv1alpha1.PipelineGit{Layout: "branch"},
			env: kardinalv1alpha1.EnvironmentSpec{Name: "test", Layout: "branch",
				Steps: []kardinalv1alpha1.StepSpec{{Uses: "git-clone"}}, Regions: []string{"a", "b"}, Shard: "eu"},
			want: []string{"steps is not supported", "regions is not supported", "shard is not supported"}},
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

// TestValidateRenderedBranches (#1447): a rendered branch must differ from
// the DRY source and from every other environment's, be a valid branch name,
// and argocd does not render.
func TestValidateRenderedBranches(t *testing.T) {
	branch := func(name, rendered string) kardinalv1alpha1.EnvironmentSpec {
		e := kardinalv1alpha1.EnvironmentSpec{Name: name, Layout: "branch"}
		if rendered != "" {
			e.Render = &kardinalv1alpha1.RenderConfig{Branch: rendered}
		}
		return e
	}
	tests := []struct {
		name    string
		envs    []kardinalv1alpha1.EnvironmentSpec
		wantErr string
	}{
		{name: "defaults", envs: []kardinalv1alpha1.EnvironmentSpec{branch("test", ""), branch("prod", "")}},
		{name: "directory env ignored", envs: []kardinalv1alpha1.EnvironmentSpec{{Name: "dev", Layout: "directory"}, branch("prod", "")}},
		{name: "source branch", envs: []kardinalv1alpha1.EnvironmentSpec{branch("prod", "main")}, wantErr: "is spec.git.branch, the DRY source"},
		{name: "shared branch", envs: []kardinalv1alpha1.EnvironmentSpec{branch("a", "env/x"), branch("b", "env/x")},
			wantErr: `environments "a" and "b" both render to branch "env/x"`},
		{name: "dot dot", envs: []kardinalv1alpha1.EnvironmentSpec{branch("a", "env/../main")}, wantErr: "not a valid branch name"},
		{name: "lock suffix", envs: []kardinalv1alpha1.EnvironmentSpec{branch("a", "env/a.lock")}, wantErr: "not a valid branch name"},
		{name: "argocd", envs: []kardinalv1alpha1.EnvironmentSpec{{Name: "a", Layout: "branch",
			Update: kardinalv1alpha1.UpdateConfig{Strategy: "argocd"}}}, wantErr: "update.strategy argocd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &kardinalv1alpha1.Pipeline{Spec: kardinalv1alpha1.PipelineSpec{
				Git: kardinalv1alpha1.PipelineGit{Branch: "main"}, Environments: tt.envs}}
			err := graph.ValidateRenderedBranches(p)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
