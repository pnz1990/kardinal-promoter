// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func TestDirectUpstreams(t *testing.T) {
	sequential := []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "uat"}, {Name: "prod"}}
	fanIn := []kardinalv1alpha1.EnvironmentSpec{
		{Name: "test"},
		{Name: "prod-eu", DependsOn: []string{"test"}},
		{Name: "prod-us", DependsOn: []string{"test"}},
		{Name: "global", DependsOn: []string{"prod-eu", "prod-us"}},
	}

	tests := []struct {
		name    string
		envs    []kardinalv1alpha1.EnvironmentSpec
		intent  *kardinalv1alpha1.BundleIntent
		env     string
		want    []string
		wantErr bool
	}{
		{name: "sequential prod depends on uat only", envs: sequential, env: "prod", want: []string{"uat"}},
		{name: "root has no upstream", envs: sequential, env: "test", want: nil},
		{name: "fan-in returns both parents", envs: fanIn, env: "global", want: []string{"prod-eu", "prod-us"}},
		{name: "sibling is not an upstream", envs: fanIn, env: "prod-us", want: []string{"test"}},
		{
			name:   "skipped env is bridged",
			envs:   sequential,
			intent: &kardinalv1alpha1.BundleIntent{SkipEnvironments: []string{"uat"}},
			env:    "prod",
			want:   []string{"test"},
		},
		{
			name:    "env beyond target is an error",
			envs:    sequential,
			intent:  &kardinalv1alpha1.BundleIntent{TargetEnvironment: "uat"},
			env:     "prod",
			wantErr: true,
		},
		{name: "unknown env is an error", envs: sequential, env: "nope", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &kardinalv1alpha1.Pipeline{
				ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
				Spec:       kardinalv1alpha1.PipelineSpec{Environments: tt.envs},
			}
			b := &kardinalv1alpha1.Bundle{
				ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default"},
				Spec:       kardinalv1alpha1.BundleSpec{Pipeline: "app", Intent: tt.intent},
			}
			got, err := DirectUpstreams(p, b, tt.env)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.want, got)
		})
	}
}
