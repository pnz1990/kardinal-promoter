// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health

import (
	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// DefaultType is the adapter used when an environment sets neither
// health.type nor delivery.delegate.
const DefaultType = "resource"

// EffectiveType returns the adapter type that verifies env:
// delivery.delegate when it is set (and not "none"), else health.type,
// else DefaultType.
func EffectiveType(env v1alpha1.EnvironmentSpec) string {
	if d := env.Delivery.Delegate; d != "" && d != "none" {
		return d
	}
	if env.Health.Type != "" {
		return env.Health.Type
	}
	return DefaultType
}

// OptionsForEnv builds the CheckOptions for one Pipeline environment. It is the
// single source of the health target names, used both by the PromotionStep
// reconciler (which decides Verified) and by the translator (which emits the
// Graph health ref nodes), so the two can never check different objects.
//
// Defaults, each overridable from spec.environments[].health:
//   - resource:     Deployment <pipeline> in namespace <env>, condition Available
//     (health.resource.name/namespace/condition; health.labelSelector selects
//     every matching Deployment in the namespace instead of one by name)
//   - argocd:       Application <pipeline>-<env> in namespace argocd (health.argocd)
//   - flux:         Kustomization <pipeline>-<env> in namespace flux-system (health.flux)
//   - argoRollouts: Rollout <pipeline> in namespace <env> (health.argoRollouts)
//   - flagger:      Canary <pipeline> in namespace <env> (health.flagger)
func OptionsForEnv(pipelineName string, env v1alpha1.EnvironmentSpec) CheckOptions {
	h := env.Health
	opts := CheckOptions{
		Type: EffectiveType(env),
		Resource: ResourceConfig{
			Name:          pipelineName,
			Namespace:     env.Name,
			Condition:     "Available",
			LabelSelector: h.LabelSelector,
		},
		ArgoCD:       ArgoCDConfig{Name: pipelineName + "-" + env.Name, Namespace: "argocd"},
		Flux:         FluxConfig{Name: pipelineName + "-" + env.Name, Namespace: "flux-system"},
		ArgoRollouts: ArgoRolloutsConfig{Name: pipelineName, Namespace: env.Name},
		Flagger:      FlaggerConfig{Name: pipelineName, Namespace: env.Name},
	}
	if r := h.Resource; r != nil {
		override(&opts.Resource.Name, r.Name)
		override(&opts.Resource.Namespace, r.Namespace)
		override(&opts.Resource.Condition, r.Condition)
	}
	if t := h.ArgoCD; t != nil {
		override(&opts.ArgoCD.Name, t.Name)
		override(&opts.ArgoCD.Namespace, t.Namespace)
	}
	if t := h.Flux; t != nil {
		override(&opts.Flux.Name, t.Name)
		override(&opts.Flux.Namespace, t.Namespace)
	}
	if t := h.ArgoRollouts; t != nil {
		override(&opts.ArgoRollouts.Name, t.Name)
		override(&opts.ArgoRollouts.Namespace, t.Namespace)
	}
	if t := h.Flagger; t != nil {
		override(&opts.Flagger.Name, t.Name)
		override(&opts.Flagger.Namespace, t.Namespace)
	}
	return opts
}

func override(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}
