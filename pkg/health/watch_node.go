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

package health

import (
	"fmt"
)

// WatchNodeSpec describes a krocodile Watch-reference node template for health verification.
//
// The translator uses this struct to emit a Watch node in the Graph spec instead of
// calling the Go health adapter at reconcile time. This moves health verification
// into the Graph layer (HE-1, HE-2, HE-3 from docs/design/11-graph-purity-tech-debt.md).
//
// The node variable name in ReadyWhen is always "healthNode" — the translator assigns
// a unique Graph node ID (e.g. "healthProd") and must substitute "healthNode" with
// the actual ID when generating the Graph spec.
type WatchNodeSpec struct {
	// APIVersion is the Kubernetes API version of the resource to watch.
	// Example: "apps/v1", "argoproj.io/v1alpha1".
	APIVersion string

	// Kind is the Kubernetes Kind of the resource to watch.
	// Example: "Deployment", "Application", "Kustomization".
	Kind string

	// Name is the resource name to watch.
	Name string

	// Namespace is the resource namespace to watch.
	Namespace string

	// ReadyWhen is a krocodile CEL expression evaluated against the watched resource.
	// The node variable placeholder is "healthNode". The translator must substitute
	// "healthNode" with the actual Graph node ID before emitting the Graph spec.
	//
	// Example:
	//   "healthNode.status.conditions.exists(c, c.type == 'Available' && c.status == 'True')"
	ReadyWhen string

	// HealthType is the adapter type that produced this spec.
	// Preserved for debugging and documentation purposes.
	HealthType string
}

// WatchNodeTemplate returns a krocodile Watch-reference node spec for the given health type
// and configuration. The translator calls this function to get the Watch node template
// and readyWhen CEL expression for a Pipeline environment's health check.
//
// This function is pure and has no side effects. It is safe to call from any context.
//
// The ReadyWhen expression uses "healthNode" as the node variable placeholder. The
// translator must replace "healthNode" with the actual Graph node ID (e.g. "health_prod")
// before emitting the Graph spec.
func WatchNodeTemplate(healthType string, opts CheckOptions) (WatchNodeSpec, error) {
	switch healthType {
	case "resource":
		return watchNodeResource(opts.Resource), nil
	case "argocd":
		return watchNodeArgoCD(opts.ArgoCD), nil
	case "flux":
		return watchNodeFlux(opts.Flux), nil
	case "argoRollouts":
		return watchNodeArgoRollouts(opts.ArgoRollouts), nil
	case "flagger":
		return watchNodeFlagger(opts.Flagger), nil
	case "":
		return WatchNodeSpec{}, fmt.Errorf(
			"health.type is required: set health.type to one of [resource, argocd, flux, argoRollouts, flagger]")
	default:
		return WatchNodeSpec{}, fmt.Errorf(
			"unknown health.type %q: must be one of [resource, argocd, flux, argoRollouts, flagger]",
			healthType)
	}
}

// watchNodeResource builds a Watch node spec for a Kubernetes Deployment.
//
// readyWhen: the Available condition must be True.
// HE-1 in docs/design/11-graph-purity-tech-debt.md.
func watchNodeResource(cfg ResourceConfig) WatchNodeSpec {
	condition := cfg.Condition
	if condition == "" {
		condition = "Available"
	}
	return WatchNodeSpec{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Name:       cfg.Name,
		Namespace:  cfg.Namespace,
		ReadyWhen: fmt.Sprintf(
			"healthNode.status.conditions.exists(c, c.type == %q && c.status == 'True')",
			condition,
		),
		HealthType: "resource",
	}
}

// watchNodeArgoCD builds a Watch node spec for an Argo CD Application.
//
// readyWhen: health=Healthy AND sync=Synced.
// HE-2 in docs/design/11-graph-purity-tech-debt.md.
func watchNodeArgoCD(cfg ArgoCDConfig) WatchNodeSpec {
	ns := cfg.Namespace
	if ns == "" {
		ns = "argocd"
	}
	return WatchNodeSpec{
		APIVersion: "argoproj.io/v1alpha1",
		Kind:       "Application",
		Name:       cfg.Name,
		Namespace:  ns,
		ReadyWhen: "healthNode.status.health.status == 'Healthy' && " +
			"healthNode.status.sync.status == 'Synced'",
		HealthType: "argocd",
	}
}

// watchNodeFlux builds a Watch node spec for a Flux Kustomization.
//
// readyWhen: the Ready condition is True AND observedGeneration matches generation
// (confirming the latest revision has been reconciled).
// HE-3 in docs/design/11-graph-purity-tech-debt.md.
func watchNodeFlux(cfg FluxConfig) WatchNodeSpec {
	ns := cfg.Namespace
	if ns == "" {
		ns = "flux-system"
	}
	return WatchNodeSpec{
		APIVersion: "kustomize.toolkit.fluxcd.io/v1",
		Kind:       "Kustomization",
		Name:       cfg.Name,
		Namespace:  ns,
		ReadyWhen: "healthNode.status.conditions.exists(c, c.type == 'Ready' && c.status == 'True') && " +
			"healthNode.status.observedGeneration == healthNode.metadata.generation",
		HealthType: "flux",
	}
}

// watchNodeArgoRollouts builds a Watch node spec for an Argo Rollouts Rollout.
//
// readyWhen: status.phase == "Healthy".
func watchNodeArgoRollouts(cfg ArgoRolloutsConfig) WatchNodeSpec {
	ns := cfg.Namespace
	if ns == "" {
		ns = "default"
	}
	return WatchNodeSpec{
		APIVersion: "argoproj.io/v1alpha1",
		Kind:       "Rollout",
		Name:       cfg.Name,
		Namespace:  ns,
		ReadyWhen:  "healthNode.status.phase == 'Healthy'",
		HealthType: "argoRollouts",
	}
}

// watchNodeFlagger builds a Watch node spec for a Flagger Canary.
//
// readyWhen: status.phase == "Succeeded".
func watchNodeFlagger(cfg FlaggerConfig) WatchNodeSpec {
	ns := cfg.Namespace
	if ns == "" {
		ns = "default"
	}
	return WatchNodeSpec{
		APIVersion: "flagger.app/v1beta1",
		Kind:       "Canary",
		Name:       cfg.Name,
		Namespace:  ns,
		ReadyWhen:  "healthNode.status.phase == 'Succeeded'",
		HealthType: "flagger",
	}
}
