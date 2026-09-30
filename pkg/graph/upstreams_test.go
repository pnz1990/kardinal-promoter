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

// E2E-R18: an environment counts as reached only when every direct upstream
// has the bundle's step Verified, the same rule the Graph gates the
// environment's PromotionStep on.
func TestUpstreamsVerified(t *testing.T) {
	sequential := []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "uat"}, {Name: "prod"}}
	fanIn := []kardinalv1alpha1.EnvironmentSpec{
		{Name: "test"},
		{Name: "prod-eu", DependsOn: []string{"test"}},
		{Name: "prod-us", DependsOn: []string{"test"}},
		{Name: "global", DependsOn: []string{"prod-eu", "prod-us"}},
	}
	regional := []kardinalv1alpha1.EnvironmentSpec{
		{Name: "test", Regions: []string{"us", "eu"}},
		{Name: "prod"},
	}
	step := func(bundle, env, state string) kardinalv1alpha1.PromotionStep {
		return kardinalv1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Name: bundle + "-" + env, Namespace: "default"},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: bundle, Environment: env},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: state},
		}
	}
	tests := []struct {
		name   string
		envs   []kardinalv1alpha1.EnvironmentSpec
		intent *kardinalv1alpha1.BundleIntent
		steps  []kardinalv1alpha1.PromotionStep
		env    string
		want   bool
	}{
		{name: "root is always reached", envs: sequential, env: "test", want: true},
		{
			name:  "upstream still health checking",
			envs:  sequential,
			steps: []kardinalv1alpha1.PromotionStep{step("app-v1", "test", "HealthChecking")},
			env:   "prod",
			want:  false,
		},
		{
			name:  "upstream has no step yet",
			envs:  sequential,
			steps: []kardinalv1alpha1.PromotionStep{step("app-v1", "test", "Verified")},
			env:   "prod",
			want:  false,
		},
		{
			name:  "upstream Verified",
			envs:  sequential,
			steps: []kardinalv1alpha1.PromotionStep{step("app-v1", "test", "Verified"), step("app-v1", "uat", "Verified")},
			env:   "prod",
			want:  true,
		},
		{
			name:  "another bundle's Verified step does not count",
			envs:  sequential,
			steps: []kardinalv1alpha1.PromotionStep{step("app-v0", "uat", "Verified")},
			env:   "prod",
			want:  false,
		},
		{
			name:   "skipped env is bridged to its upstream",
			envs:   sequential,
			intent: &kardinalv1alpha1.BundleIntent{SkipEnvironments: []string{"uat"}},
			steps:  []kardinalv1alpha1.PromotionStep{step("app-v1", "test", "Verified")},
			env:    "prod",
			want:   true,
		},
		{
			name:  "fan-in needs every parent Verified",
			envs:  fanIn,
			steps: []kardinalv1alpha1.PromotionStep{step("app-v1", "prod-eu", "Verified"), step("app-v1", "prod-us", "Promoting")},
			env:   "global",
			want:  false,
		},
		{
			name:  "multi-region upstream needs every region",
			envs:  regional,
			steps: []kardinalv1alpha1.PromotionStep{step("app-v1", "test", "Verified")},
			env:   "prod",
			want:  false,
		},
		{
			name:   "env beyond target is not reached",
			envs:   sequential,
			intent: &kardinalv1alpha1.BundleIntent{TargetEnvironment: "uat"},
			steps:  []kardinalv1alpha1.PromotionStep{step("app-v1", "test", "Verified"), step("app-v1", "uat", "Verified")},
			env:    "prod",
			want:   false,
		},
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
			assert.Equal(t, tt.want, UpstreamsVerified(p, b, tt.env, tt.steps))
		})
	}
}
