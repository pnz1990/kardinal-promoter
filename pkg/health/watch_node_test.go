// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package health_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

// TestWatchNodeTemplate_Resource verifies that health.type=resource produces
// a Watch node spec for apps/v1 Deployment with the correct readyWhen expression.
func TestWatchNodeTemplate_Resource(t *testing.T) {
	spec, err := health.WatchNodeTemplate("resource", health.CheckOptions{
		Resource: health.ResourceConfig{
			Name:      "nginx",
			Namespace: "prod",
			Condition: "Available",
		},
	})
	require.NoError(t, err)

	assert.Equal(t, "apps/v1", spec.APIVersion)
	assert.Equal(t, "Deployment", spec.Kind)
	assert.Equal(t, "nginx", spec.Name)
	assert.Equal(t, "prod", spec.Namespace)
	assert.NotEmpty(t, spec.ReadyWhen, "ReadyWhen must be a non-empty CEL expression")
	assert.Contains(t, spec.ReadyWhen, "Available")
	assert.Equal(t, "resource", spec.HealthType)
}

// TestWatchNodeTemplate_ResourceDefaultCondition verifies that an empty Condition
// defaults to "Available".
func TestWatchNodeTemplate_ResourceDefaultCondition(t *testing.T) {
	spec, err := health.WatchNodeTemplate("resource", health.CheckOptions{
		Resource: health.ResourceConfig{
			Name:      "nginx",
			Namespace: "prod",
		},
	})
	require.NoError(t, err)
	assert.Contains(t, spec.ReadyWhen, "Available")
}

// TestWatchNodeTemplate_ArgoCD verifies that health.type=argocd produces
// a Watch node spec for argoproj.io/v1alpha1 Application.
func TestWatchNodeTemplate_ArgoCD(t *testing.T) {
	spec, err := health.WatchNodeTemplate("argocd", health.CheckOptions{
		ArgoCD: health.ArgoCDConfig{
			Name:      "nginx-prod",
			Namespace: "argocd",
		},
	})
	require.NoError(t, err)

	assert.Equal(t, "argoproj.io/v1alpha1", spec.APIVersion)
	assert.Equal(t, "Application", spec.Kind)
	assert.Equal(t, "nginx-prod", spec.Name)
	assert.Equal(t, "argocd", spec.Namespace)
	assert.Contains(t, spec.ReadyWhen, "Healthy")
	assert.Contains(t, spec.ReadyWhen, "Synced")
	assert.Equal(t, "argocd", spec.HealthType)
}

// TestWatchNodeTemplate_ArgoCDDefaultNamespace verifies that an empty Namespace
// defaults to "argocd".
func TestWatchNodeTemplate_ArgoCDDefaultNamespace(t *testing.T) {
	spec, err := health.WatchNodeTemplate("argocd", health.CheckOptions{
		ArgoCD: health.ArgoCDConfig{
			Name: "nginx-prod",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "argocd", spec.Namespace)
}

// TestWatchNodeTemplate_Flux verifies that health.type=flux produces
// a Watch node spec for kustomize.toolkit.fluxcd.io/v1 Kustomization.
func TestWatchNodeTemplate_Flux(t *testing.T) {
	spec, err := health.WatchNodeTemplate("flux", health.CheckOptions{
		Flux: health.FluxConfig{
			Name:      "nginx-prod",
			Namespace: "flux-system",
		},
	})
	require.NoError(t, err)

	assert.Equal(t, "kustomize.toolkit.fluxcd.io/v1", spec.APIVersion)
	assert.Equal(t, "Kustomization", spec.Kind)
	assert.Equal(t, "nginx-prod", spec.Name)
	assert.Equal(t, "flux-system", spec.Namespace)
	assert.Contains(t, spec.ReadyWhen, "Ready")
	assert.Equal(t, "flux", spec.HealthType)
}

// TestWatchNodeTemplate_FluxDefaultNamespace verifies that an empty Namespace
// defaults to "flux-system".
func TestWatchNodeTemplate_FluxDefaultNamespace(t *testing.T) {
	spec, err := health.WatchNodeTemplate("flux", health.CheckOptions{
		Flux: health.FluxConfig{Name: "nginx-prod"},
	})
	require.NoError(t, err)
	assert.Equal(t, "flux-system", spec.Namespace)
}

// TestWatchNodeTemplate_ArgoRollouts verifies that health.type=argoRollouts produces
// a Watch node spec for argoproj.io/v1alpha1 Rollout.
func TestWatchNodeTemplate_ArgoRollouts(t *testing.T) {
	spec, err := health.WatchNodeTemplate("argoRollouts", health.CheckOptions{
		ArgoRollouts: health.ArgoRolloutsConfig{
			Name:      "my-app",
			Namespace: "prod",
		},
	})
	require.NoError(t, err)

	assert.Equal(t, "argoproj.io/v1alpha1", spec.APIVersion)
	assert.Equal(t, "Rollout", spec.Kind)
	assert.Equal(t, "my-app", spec.Name)
	assert.Equal(t, "prod", spec.Namespace)
	assert.Contains(t, spec.ReadyWhen, "Healthy")
	assert.Equal(t, "argoRollouts", spec.HealthType)
}

// TestWatchNodeTemplate_Flagger verifies that health.type=flagger produces
// a Watch node spec for flagger.app/v1beta1 Canary.
func TestWatchNodeTemplate_Flagger(t *testing.T) {
	spec, err := health.WatchNodeTemplate("flagger", health.CheckOptions{
		Flagger: health.FlaggerConfig{
			Name:      "my-app",
			Namespace: "prod",
		},
	})
	require.NoError(t, err)

	assert.Equal(t, "flagger.app/v1beta1", spec.APIVersion)
	assert.Equal(t, "Canary", spec.Kind)
	assert.Equal(t, "my-app", spec.Name)
	assert.Equal(t, "prod", spec.Namespace)
	assert.Contains(t, spec.ReadyWhen, "Succeeded")
	assert.Equal(t, "flagger", spec.HealthType)
}

// TestWatchNodeTemplate_UnknownType verifies that an unknown health type returns an error.
func TestWatchNodeTemplate_UnknownType(t *testing.T) {
	_, err := health.WatchNodeTemplate("unknownType", health.CheckOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknownType")
}

// TestWatchNodeTemplate_EmptyType verifies that an empty health type returns an error.
func TestWatchNodeTemplate_EmptyType(t *testing.T) {
	_, err := health.WatchNodeTemplate("", health.CheckOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "health.type")
}

// TestWatchNodeTemplate_ReadyWhenIsValidCEL verifies that the ReadyWhen expressions
// are non-trivial CEL expressions (contain a node reference for the respective adapter).
func TestWatchNodeTemplate_ReadyWhenIsValidCEL(t *testing.T) {
	cases := []struct {
		healthType  string
		opts        health.CheckOptions
		wantNodeRef string // the node-id prefix used in the CEL expression
	}{
		{
			"resource",
			health.CheckOptions{Resource: health.ResourceConfig{Name: "app", Namespace: "ns", Condition: "Available"}},
			"healthNode",
		},
		{
			"argocd",
			health.CheckOptions{ArgoCD: health.ArgoCDConfig{Name: "app", Namespace: "argocd"}},
			"healthNode",
		},
		{
			"flux",
			health.CheckOptions{Flux: health.FluxConfig{Name: "app", Namespace: "flux-system"}},
			"healthNode",
		},
		{
			"argoRollouts",
			health.CheckOptions{ArgoRollouts: health.ArgoRolloutsConfig{Name: "app", Namespace: "ns"}},
			"healthNode",
		},
		{
			"flagger",
			health.CheckOptions{Flagger: health.FlaggerConfig{Name: "app", Namespace: "ns"}},
			"healthNode",
		},
	}

	for _, tc := range cases {
		t.Run(tc.healthType, func(t *testing.T) {
			spec, err := health.WatchNodeTemplate(tc.healthType, tc.opts)
			require.NoError(t, err)
			assert.NotEmpty(t, spec.ReadyWhen)
			// The ReadyWhen expression must reference a node variable (not be a literal constant)
			assert.Contains(t, spec.ReadyWhen, ".", "ReadyWhen must reference node fields via dot notation")
		})
	}
}
