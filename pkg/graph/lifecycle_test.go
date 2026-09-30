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

func TestPromotedEnvironments(t *testing.T) {
	fanOut := makeLinearPipeline("p", "test", "uat", "prod-eu", "prod-us")
	fanOut.Spec.Environments[2].DependsOn = []string{"uat"}
	fanOut.Spec.Environments[3].DependsOn = []string{"uat"}

	tests := []struct {
		name     string
		pipeline *kardinalv1alpha1.Pipeline
		intent   *kardinalv1alpha1.BundleIntent
		want     []string
		wantErr  string
	}{
		{name: "no intent keeps every environment in order",
			pipeline: makeLinearPipeline("p", "test", "uat", "prod"),
			want:     []string{"test", "uat", "prod"}},
		{name: "targetEnvironment stops at the target",
			pipeline: makeLinearPipeline("p", "test", "uat", "prod"),
			intent:   &kardinalv1alpha1.BundleIntent{TargetEnvironment: "uat"},
			want:     []string{"test", "uat"}},
		{name: "targetEnvironment keeps only the path to the target",
			pipeline: fanOut,
			intent:   &kardinalv1alpha1.BundleIntent{TargetEnvironment: "prod-eu"},
			want:     []string{"test", "uat", "prod-eu"}},
		{name: "skipEnvironments drops the skipped environment",
			pipeline: makeLinearPipeline("p", "test", "uat", "prod"),
			intent:   &kardinalv1alpha1.BundleIntent{SkipEnvironments: []string{"uat"}},
			want:     []string{"test", "prod"}},
		{name: "unknown target is an error",
			pipeline: makeLinearPipeline("p", "test"),
			intent:   &kardinalv1alpha1.BundleIntent{TargetEnvironment: "nope"},
			wantErr:  "unknown target environment"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := makeBundle("b1", "p")
			b.Spec.Intent = tc.intent
			got, err := graph.PromotedEnvironments(tc.pipeline, b)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestEnvironmentUpstreams(t *testing.T) {
	fanIn := makeLinearPipeline("p", "eu", "us", "prod")
	fanIn.Spec.Environments[1].DependsOn = nil
	fanIn.Spec.Environments[2].DependsOn = []string{"us", "eu"}
	waves := makeLinearPipeline("p", "canary", "w1", "w2")
	waves.Spec.Environments[1].Wave = 1
	waves.Spec.Environments[2].Wave = 2

	tests := []struct {
		name     string
		pipeline *kardinalv1alpha1.Pipeline
		env      string
		want     []string
		wantErr  string
	}{
		{name: "root has no upstream", pipeline: makeLinearPipeline("p", "test", "prod"), env: "test", want: []string{}},
		{name: "list order is the default edge", pipeline: makeLinearPipeline("p", "test", "uat", "prod"), env: "prod", want: []string{"uat"}},
		{name: "explicit dependsOn, sorted", pipeline: fanIn, env: "prod", want: []string{"eu", "us"}},
		{name: "wave edges", pipeline: waves, env: "w2", want: []string{"w1"}},
		{name: "unknown environment", pipeline: makeLinearPipeline("p", "test"), env: "prod", wantErr: "has no environment"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := graph.EnvironmentUpstreams(tc.pipeline, tc.env)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSinkEnvironments(t *testing.T) {
	fanOut := makeLinearPipeline("p", "test", "uat", "prod", "perf")
	fanOut.Spec.Environments[2].DependsOn = []string{"uat"}
	fanOut.Spec.Environments[3].DependsOn = []string{"uat"}
	cycle := makeLinearPipeline("p", "a", "b")
	cycle.Spec.Environments[0].DependsOn = []string{"b"}
	cycle.Spec.Environments[1].DependsOn = []string{"a"}

	tests := []struct {
		name     string
		pipeline *kardinalv1alpha1.Pipeline
		want     []string
		wantErr  string
	}{
		{name: "linear pipeline ends at the last environment",
			pipeline: makeLinearPipeline("p", "test", "uat", "prod"), want: []string{"prod"}},
		{name: "a fan-out has one sink per leaf, not the last list entry",
			pipeline: fanOut, want: []string{"prod", "perf"}},
		{name: "a cycle is an error", pipeline: cycle, wantErr: "circular dependency"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := graph.SinkEnvironments(tc.pipeline)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.ElementsMatch(t, tc.want, got)
		})
	}
}
