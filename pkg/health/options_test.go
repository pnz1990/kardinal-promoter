// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

// TestOptionsForEnv proves C01-graph-07, C03-promotionstep-04/05/19,
// C06-scm-health-09, C08-api-config-06 and C13b-design-06: the documented
// health overrides reach the CheckOptions, Flagger is filled, and an omitted
// health.type means the resource adapter.
func TestOptionsForEnv(t *testing.T) {
	tests := []struct {
		name  string
		env   v1alpha1.EnvironmentSpec
		check func(t *testing.T, o health.CheckOptions)
	}{
		{name: "defaults", env: v1alpha1.EnvironmentSpec{Name: "prod"},
			check: func(t *testing.T, o health.CheckOptions) {
				assert.Equal(t, "resource", o.Type, "omitted health.type is the resource adapter")
				assert.Equal(t, health.ResourceConfig{Name: "web", Namespace: "prod", Condition: "Available"}, o.Resource)
				assert.Equal(t, health.ArgoCDConfig{Name: "web-prod", Namespace: "argocd"}, o.ArgoCD)
				assert.Equal(t, health.FluxConfig{Name: "web-prod", Namespace: "flux-system"}, o.Flux)
				assert.Equal(t, health.ArgoRolloutsConfig{Name: "web", Namespace: "prod"}, o.ArgoRollouts)
				assert.Equal(t, health.FlaggerConfig{Name: "web", Namespace: "prod"}, o.Flagger)
			}},
		{name: "resource and labelSelector overrides", env: v1alpha1.EnvironmentSpec{Name: "prod",
			Health: v1alpha1.HealthConfig{Type: "resource",
				Resource:      &v1alpha1.ResourceRef{Kind: "Deployment", Name: "frontend", Namespace: "shop", Condition: "Ready"},
				LabelSelector: map[string]string{"tier": "web"}}},
			check: func(t *testing.T, o health.CheckOptions) {
				assert.Equal(t, health.ResourceConfig{Name: "frontend", Namespace: "shop", Condition: "Ready",
					LabelSelector: map[string]string{"tier": "web"}}, o.Resource)
			}},
		{name: "argocd name override", env: v1alpha1.EnvironmentSpec{Name: "prod",
			Health: v1alpha1.HealthConfig{Type: "argocd", ArgoCD: &v1alpha1.HealthTargetRef{Name: "shop-prod"}}},
			check: func(t *testing.T, o health.CheckOptions) {
				assert.Equal(t, "argocd", o.Type)
				assert.Equal(t, health.ArgoCDConfig{Name: "shop-prod", Namespace: "argocd"}, o.ArgoCD)
			}},
		{name: "flux, rollouts and flagger overrides", env: v1alpha1.EnvironmentSpec{Name: "prod",
			Health: v1alpha1.HealthConfig{Type: "flux",
				Flux:         &v1alpha1.HealthTargetRef{Name: "apps", Namespace: "gitops"},
				ArgoRollouts: &v1alpha1.HealthTargetRef{Name: "web-rollout"},
				Flagger:      &v1alpha1.HealthTargetRef{Name: "web-canary", Namespace: "canaries"}}},
			check: func(t *testing.T, o health.CheckOptions) {
				assert.Equal(t, health.FluxConfig{Name: "apps", Namespace: "gitops"}, o.Flux)
				assert.Equal(t, health.ArgoRolloutsConfig{Name: "web-rollout", Namespace: "prod"}, o.ArgoRollouts)
				assert.Equal(t, health.FlaggerConfig{Name: "web-canary", Namespace: "canaries"}, o.Flagger)
			}},
		{name: "delivery delegate selects the adapter", env: v1alpha1.EnvironmentSpec{Name: "prod",
			Delivery: v1alpha1.DeliveryConfig{Delegate: "flagger"}, Health: v1alpha1.HealthConfig{Type: "resource"}},
			check: func(t *testing.T, o health.CheckOptions) { assert.Equal(t, "flagger", o.Type) }},
		{name: "delegate none keeps health.type", env: v1alpha1.EnvironmentSpec{Name: "prod",
			Delivery: v1alpha1.DeliveryConfig{Delegate: "none"}, Health: v1alpha1.HealthConfig{Type: "argocd"}},
			check: func(t *testing.T, o health.CheckOptions) { assert.Equal(t, "argocd", o.Type) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { tt.check(t, health.OptionsForEnv("web", tt.env)) })
	}
}
