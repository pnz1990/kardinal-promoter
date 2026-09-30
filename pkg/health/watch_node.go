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

// WatchNodeSpec describes a kro Graph ref node for health verification.
//
// The translator uses this struct to emit a ref node in the Graph spec instead of
// calling the Go health adapter at reconcile time. This moves health verification
// into the Graph layer (HE-1, HE-2, HE-3 from docs/design/11-graph-purity-tech-debt.md).
//
// The node variable name in ReadyWhen is always "healthNode" — the translator assigns
// a unique Graph node ID (e.g. "healthProd") and must substitute "healthNode" with
// the actual ID (or with "each" for a selector collection) when generating the
// Graph spec.
type WatchNodeSpec struct {
	// APIVersion is the Kubernetes API version of the resource to watch.
	// Example: "apps/v1", "argoproj.io/v1alpha1".
	APIVersion string

	// Kind is the Kubernetes Kind of the resource to watch.
	// Example: "Deployment", "Application", "Kustomization".
	Kind string

	// Name is the resource name to watch. Empty for selector (collection) nodes.
	Name string

	// Namespace is the resource namespace to watch.
	Namespace string

	// LabelSelector is the label selector for collection nodes.
	// When non-empty, UseWatchKind is true and Name is ignored.
	// The translator emits a ref node with metadata.selector (a collection)
	// instead of metadata.name (a single object).
	LabelSelector map[string]string

	// UseWatchKind indicates that a selector (collection) ref node should be
	// emitted instead of a named ref node.
	UseWatchKind bool

	// ReadyWhen is a CEL expression (without the ${} wrapper) evaluated against
	// the watched resource. The node variable placeholder is "healthNode".
	//
	// For named nodes (UseWatchKind=false) the translator substitutes the Graph
	// node ID:
	//   "healthNode.status.conditions.exists(c, c.type == 'Available' && c.status == 'True')"
	//
	// For collection nodes (UseWatchKind=true) kro evaluates readyWhen once per
	// element with the element bound to "each", so the translator substitutes
	// "each"; the collection is ready when every element is.
	ReadyWhen string

	// HealthType is the adapter type that produced this spec.
	// Preserved for debugging and documentation purposes.
	HealthType string
}

// WatchNodeTemplate returns a Graph ref node spec for the given health type
// and configuration. The translator calls this function to get the ref node identity
// and readyWhen CEL expression for a Pipeline environment's health check.
//
// This function is pure and has no side effects. It is safe to call from any context.
//
// The ReadyWhen expression uses "healthNode" as the node variable placeholder. The
// translator must replace "healthNode" with the actual Graph node ID (e.g. "healthProd")
// before emitting the Graph spec.
func WatchNodeTemplate(healthType string, opts CheckOptions) (WatchNodeSpec, error) {
	switch healthType {
	case "resource":
		if len(opts.Resource.LabelSelector) > 0 {
			return watchNodeResourceWatchKind(opts.Resource), nil
		}
		return watchNodeResource(opts.Resource), nil
	case "argocd":
		return watchNodeArgoCD(opts.ArgoCD), nil
	case "flux":
		return watchNodeFlux(opts.Flux), nil
	case "argoRollouts":
		return watchNodeArgoRollouts(opts.ArgoRollouts)
	case "flagger":
		return watchNodeFlagger(opts.Flagger)
	case "":
		return WatchNodeSpec{}, fmt.Errorf(
			"health.type is required: set health.type to one of [resource, argocd, flux, argoRollouts, flagger]")
	default:
		return WatchNodeSpec{}, fmt.Errorf(
			"unknown health.type %q: must be one of [resource, argocd, flux, argoRollouts, flagger]",
			healthType)
	}
}

// deploymentReadyWhen mirrors DeploymentAdapter.Check (without the image
// check, which needs the Bundle): the condition is True and the rollout of the
// current generation is complete, as `kubectl rollout status` decides it.
// Status counters are omitted from the object when zero, hence the has() guards.
func deploymentReadyWhen(condition string) string {
	return fmt.Sprintf(
		"healthNode.status.conditions.exists(c, c.type == %q && c.status == 'True') && "+
			"has(healthNode.status.observedGeneration) && "+
			"healthNode.status.observedGeneration >= healthNode.metadata.generation && "+
			"!healthNode.status.conditions.exists(c, c.type == 'Progressing' && has(c.reason) && "+
			"c.reason == 'ProgressDeadlineExceeded') && "+
			"has(healthNode.status.updatedReplicas) && "+
			"healthNode.status.updatedReplicas == healthNode.spec.replicas && "+
			"healthNode.status.replicas == healthNode.status.updatedReplicas && "+
			"has(healthNode.status.availableReplicas) && "+
			"healthNode.status.availableReplicas == healthNode.status.updatedReplicas",
		condition)
}

// watchNodeResource builds a Watch node spec for a single named Kubernetes Deployment.
//
// readyWhen: see deploymentReadyWhen.
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
		ReadyWhen:  deploymentReadyWhen(condition),
		HealthType: "resource",
	}
}

// watchNodeResourceWatchKind builds a collection node spec for the Deployments
// matched by label selector.
//
// readyWhen: every matched Deployment must satisfy deploymentReadyWhen.
// UseWatchKind=true causes the translator to emit a ref node with
// metadata.selector instead of metadata.name. kro evaluates readyWhen per
// element, so the expression is written for a single Deployment.
func watchNodeResourceWatchKind(cfg ResourceConfig) WatchNodeSpec {
	condition := cfg.Condition
	if condition == "" {
		condition = "Available"
	}
	return WatchNodeSpec{
		APIVersion:    "apps/v1",
		Kind:          "Deployment",
		Namespace:     cfg.Namespace,
		LabelSelector: cfg.LabelSelector,
		UseWatchKind:  true,
		// Per-element expression: the translator binds healthNode to "each".
		ReadyWhen:  deploymentReadyWhen(condition),
		HealthType: "resource",
	}
}

// watchNodeArgoCD builds a Watch node spec for an Argo CD Application.
//
// readyWhen: health=Healthy AND sync=Synced AND no sync operation is running
// or has failed, as ArgoCDAdapter.Check decides it. status.operationState is
// absent until the first sync operation.
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
			"healthNode.status.sync.status == 'Synced' && " +
			"(!has(healthNode.status.operationState) || " +
			"healthNode.status.operationState.phase == 'Succeeded')",
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
// OptionsForEnv always sets the namespace, so an empty one is an error.
func watchNodeArgoRollouts(cfg ArgoRolloutsConfig) (WatchNodeSpec, error) {
	if cfg.Namespace == "" {
		return WatchNodeSpec{}, fmt.Errorf("argoRollouts health: Rollout %q has no namespace", cfg.Name)
	}
	return WatchNodeSpec{
		APIVersion: "argoproj.io/v1alpha1",
		Kind:       "Rollout",
		Name:       cfg.Name,
		Namespace:  cfg.Namespace,
		ReadyWhen:  "healthNode.status.phase == 'Healthy'",
		HealthType: "argoRollouts",
	}, nil
}

// watchNodeFlagger builds a Watch node spec for a Flagger Canary.
//
// readyWhen: status.phase == "Succeeded".
// OptionsForEnv always sets the namespace, so an empty one is an error.
func watchNodeFlagger(cfg FlaggerConfig) (WatchNodeSpec, error) {
	if cfg.Namespace == "" {
		return WatchNodeSpec{}, fmt.Errorf("flagger health: Canary %q has no namespace", cfg.Name)
	}
	return WatchNodeSpec{
		APIVersion: "flagger.app/v1beta1",
		Kind:       "Canary",
		Name:       cfg.Name,
		Namespace:  cfg.Namespace,
		ReadyWhen:  "healthNode.status.phase == 'Succeeded'",
		HealthType: "flagger",
	}, nil
}
