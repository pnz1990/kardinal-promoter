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

// Package health provides pluggable health adapters for promotion verification.
// Each adapter checks a different Kubernetes resource type to determine if a
// deployment is healthy after a promotion PR is merged.
//
// Phase 1 adapters: resource (Deployment), argocd (Argo CD Application), flux (Flux Kustomization).
// Phase 2 adapters: argoRollouts (Argo Rollouts Rollout), flagger (Flagger Canary).
package health

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
)

// HealthStatus is the result of a health check.
type HealthStatus struct {
	// Healthy is true when the workload is fully available.
	Healthy bool
	// Reason is a human-readable explanation.
	Reason string
	// CheckedAt records when the check was performed.
	CheckedAt time.Time
}

// CheckOptions carries the health check configuration for a specific environment.
type CheckOptions struct {
	// Type selects the adapter: "resource", "argocd", "flux", "argoRollouts", "flagger".
	// Empty means auto-detect.
	Type string

	// Resource configuration (for type: resource).
	Resource ResourceConfig

	// ArgoCD configuration (for type: argocd).
	ArgoCD ArgoCDConfig

	// Flux configuration (for type: flux).
	Flux FluxConfig

	// ArgoRollouts configuration (for type: argoRollouts).
	ArgoRollouts ArgoRolloutsConfig

	// Flagger configuration (for type: flagger).
	Flagger FlaggerConfig

	// Timeout is the maximum time to wait for health. Default: 10 minutes.
	Timeout time.Duration
}

// ResourceConfig is the health check configuration for a Kubernetes Deployment.
type ResourceConfig struct {
	// Name is the Deployment name. Defaults to pipeline name.
	Name string
	// Namespace is the Deployment namespace. Defaults to environment name.
	Namespace string
	// Condition is the condition type to check. Default: "Available".
	Condition string
}

// ArgoCDConfig is the health check configuration for an Argo CD Application.
type ArgoCDConfig struct {
	// Name is the Application name.
	Name string
	// Namespace is the Application namespace. Default: "argocd".
	Namespace string
}

// FluxConfig is the health check configuration for a Flux Kustomization.
type FluxConfig struct {
	// Name is the Kustomization name.
	Name string
	// Namespace is the Kustomization namespace. Default: "flux-system".
	Namespace string
}

// ArgoRolloutsConfig is the health check configuration for an Argo Rollouts Rollout.
type ArgoRolloutsConfig struct {
	// Name is the Rollout name. Defaults to pipeline name.
	Name string
	// Namespace is the Rollout namespace. Defaults to environment name.
	Namespace string
}

// FlaggerConfig is the health check configuration for a Flagger Canary.
type FlaggerConfig struct {
	// Name is the Canary name. Defaults to pipeline name.
	Name string
	// Namespace is the Canary namespace. Defaults to environment name.
	Namespace string
}

// Adapter is the interface for health verification backends.
// All implementations must be idempotent and safe to call repeatedly.
type Adapter interface {
	// Check returns the health status of the target workload.
	// Called repeatedly (every 10s) until Healthy, timeout, or error.
	Check(ctx context.Context, opts CheckOptions) (HealthStatus, error)

	// Name returns the adapter identifier.
	Name() string
}

// --- DeploymentAdapter ---

// DeploymentAdapter checks Kubernetes Deployment readiness conditions.
type DeploymentAdapter struct {
	client sigs_client.Client
}

// NewDeploymentAdapter constructs a DeploymentAdapter.
func NewDeploymentAdapter(c sigs_client.Client) *DeploymentAdapter {
	return &DeploymentAdapter{client: c}
}

// Name returns "resource".
func (a *DeploymentAdapter) Name() string { return "resource" }

// Check verifies the Deployment's Available condition.
func (a *DeploymentAdapter) Check(ctx context.Context, opts CheckOptions) (HealthStatus, error) {
	cfg := opts.Resource
	if cfg.Condition == "" {
		cfg.Condition = "Available"
	}

	var deploy appsv1.Deployment
	if err := a.client.Get(ctx, types.NamespacedName{
		Name:      cfg.Name,
		Namespace: cfg.Namespace,
	}, &deploy); err != nil {
		if apierrors.IsNotFound(err) {
			return HealthStatus{Healthy: false, Reason: fmt.Sprintf("Deployment %s/%s not found", cfg.Namespace, cfg.Name), CheckedAt: time.Now()}, nil
		}
		return HealthStatus{}, fmt.Errorf("get deployment %s/%s: %w", cfg.Namespace, cfg.Name, err)
	}

	for _, cond := range deploy.Status.Conditions {
		if string(cond.Type) == cfg.Condition {
			healthy := cond.Status == corev1.ConditionTrue
			reason := fmt.Sprintf("%s=%s", cond.Type, cond.Status)
			if cond.Message != "" {
				reason += ": " + cond.Message
			}
			return HealthStatus{Healthy: healthy, Reason: reason, CheckedAt: time.Now()}, nil
		}
	}

	return HealthStatus{
		Healthy:   false,
		Reason:    fmt.Sprintf("condition %q not found on Deployment %s/%s", cfg.Condition, cfg.Namespace, cfg.Name),
		CheckedAt: time.Now(),
	}, nil
}

// --- ArgoCDAdapter ---

// ArgoCDAdapter checks Argo CD Application health and sync status.
// Uses the dynamic client to avoid a compile-time dependency on the Argo CD SDK.
type ArgoCDAdapter struct {
	dynamic dynamic.Interface
}

// NewArgoCDAdapter constructs an ArgoCDAdapter.
func NewArgoCDAdapter(dynClient dynamic.Interface) *ArgoCDAdapter {
	return &ArgoCDAdapter{dynamic: dynClient}
}

// Name returns "argocd".
func (a *ArgoCDAdapter) Name() string { return "argocd" }

var argoCDApplicationGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

// Check verifies that the Argo CD Application is Healthy and Synced.
func (a *ArgoCDAdapter) Check(ctx context.Context, opts CheckOptions) (HealthStatus, error) {
	cfg := opts.ArgoCD
	if cfg.Namespace == "" {
		cfg.Namespace = "argocd"
	}

	app, err := a.dynamic.Resource(argoCDApplicationGVR).
		Namespace(cfg.Namespace).
		Get(ctx, cfg.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return HealthStatus{Healthy: false, Reason: fmt.Sprintf("Application %s/%s not found", cfg.Namespace, cfg.Name), CheckedAt: time.Now()}, nil
		}
		return HealthStatus{}, fmt.Errorf("get argo cd application %s/%s: %w", cfg.Namespace, cfg.Name, err)
	}

	healthStatus, _, _ := unstructured.NestedString(app.Object, "status", "health", "status")
	syncStatus, _, _ := unstructured.NestedString(app.Object, "status", "sync", "status")
	opPhase, _, _ := unstructured.NestedString(app.Object, "status", "operationState", "phase")

	if healthStatus == "Healthy" && syncStatus == "Synced" && (opPhase == "Succeeded" || opPhase == "") {
		return HealthStatus{
			Healthy:   true,
			Reason:    fmt.Sprintf("Healthy+Synced (opPhase=%q)", opPhase),
			CheckedAt: time.Now(),
		}, nil
	}

	return HealthStatus{
		Healthy:   false,
		Reason:    fmt.Sprintf("health=%s, sync=%s, opPhase=%s", healthStatus, syncStatus, opPhase),
		CheckedAt: time.Now(),
	}, nil
}

// --- FluxAdapter ---

// FluxAdapter checks Flux Kustomization Ready condition with generation matching.
type FluxAdapter struct {
	dynamic dynamic.Interface
}

// NewFluxAdapter constructs a FluxAdapter.
func NewFluxAdapter(dynClient dynamic.Interface) *FluxAdapter {
	return &FluxAdapter{dynamic: dynClient}
}

// Name returns "flux".
func (a *FluxAdapter) Name() string { return "flux" }

var fluxKustomizationGVR = schema.GroupVersionResource{
	Group:    "kustomize.toolkit.fluxcd.io",
	Version:  "v1",
	Resource: "kustomizations",
}

// Check verifies that the Flux Kustomization's Ready condition is True and
// that observedGeneration == generation (fully reconciled).
func (a *FluxAdapter) Check(ctx context.Context, opts CheckOptions) (HealthStatus, error) {
	cfg := opts.Flux
	if cfg.Namespace == "" {
		cfg.Namespace = "flux-system"
	}

	ks, err := a.dynamic.Resource(fluxKustomizationGVR).
		Namespace(cfg.Namespace).
		Get(ctx, cfg.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return HealthStatus{Healthy: false, Reason: fmt.Sprintf("Kustomization %s/%s not found", cfg.Namespace, cfg.Name), CheckedAt: time.Now()}, nil
		}
		return HealthStatus{}, fmt.Errorf("get flux kustomization %s/%s: %w", cfg.Namespace, cfg.Name, err)
	}

	conditions, _, _ := unstructured.NestedSlice(ks.Object, "status", "conditions")
	observedGen, _, _ := unstructured.NestedInt64(ks.Object, "status", "observedGeneration")
	generation, _, _ := unstructured.NestedInt64(ks.Object, "metadata", "generation")

	readyCond := findCondition(conditions, "Ready")
	if readyCond == nil {
		return HealthStatus{
			Healthy:   false,
			Reason:    "Ready condition not found",
			CheckedAt: time.Now(),
		}, nil
	}

	readyStatus, _ := readyCond["status"].(string)
	if readyStatus == "True" && observedGen == generation {
		return HealthStatus{
			Healthy:   true,
			Reason:    fmt.Sprintf("Ready=True, generation=%d matches", generation),
			CheckedAt: time.Now(),
		}, nil
	}

	return HealthStatus{
		Healthy:   false,
		Reason:    fmt.Sprintf("Ready=%s, observedGen=%d, generation=%d", readyStatus, observedGen, generation),
		CheckedAt: time.Now(),
	}, nil
}

// findCondition searches for a condition by type in the Flux conditions slice.
func findCondition(conditions []interface{}, condType string) map[string]interface{} {
	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		t, _ := cond["type"].(string)
		if t == condType {
			return cond
		}
	}
	return nil
}

// --- ArgoRolloutsAdapter ---

// ArgoRolloutsAdapter checks Argo Rollouts Rollout health status.
// A Rollout is healthy when status.phase == "Healthy".
// Uses the dynamic client to avoid a compile-time dependency on the Argo Rollouts SDK.
type ArgoRolloutsAdapter struct {
	dynamic dynamic.Interface
}

// NewArgoRolloutsAdapter constructs an ArgoRolloutsAdapter.
func NewArgoRolloutsAdapter(dynClient dynamic.Interface) *ArgoRolloutsAdapter {
	return &ArgoRolloutsAdapter{dynamic: dynClient}
}

// Name returns "argoRollouts".
func (a *ArgoRolloutsAdapter) Name() string { return "argoRollouts" }

var argoRolloutsGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "rollouts",
}

// Check verifies that the Argo Rollouts Rollout is in the Healthy phase.
func (a *ArgoRolloutsAdapter) Check(ctx context.Context, opts CheckOptions) (HealthStatus, error) {
	cfg := opts.ArgoRollouts
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}
	if cfg.Name == "" {
		return HealthStatus{Healthy: false, Reason: "ArgoRollouts.Name not configured", CheckedAt: time.Now()}, nil
	}

	rollout, err := a.dynamic.Resource(argoRolloutsGVR).
		Namespace(cfg.Namespace).
		Get(ctx, cfg.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return HealthStatus{
				Healthy:   false,
				Reason:    fmt.Sprintf("Rollout %s/%s not found", cfg.Namespace, cfg.Name),
				CheckedAt: time.Now(),
			}, nil
		}
		return HealthStatus{}, fmt.Errorf("get rollout %s/%s: %w", cfg.Namespace, cfg.Name, err)
	}

	phase, _, _ := unstructured.NestedString(rollout.Object, "status", "phase")
	message, _, _ := unstructured.NestedString(rollout.Object, "status", "message")

	if phase == "Healthy" {
		reason := "Rollout phase: Healthy"
		if message != "" {
			reason += " — " + message
		}
		return HealthStatus{Healthy: true, Reason: reason, CheckedAt: time.Now()}, nil
	}

	reason := fmt.Sprintf("Rollout phase: %s", phase)
	if message != "" {
		reason += " — " + message
	}
	return HealthStatus{Healthy: false, Reason: reason, CheckedAt: time.Now()}, nil
}

// --- FlaggerAdapter ---

// FlaggerAdapter checks Flagger Canary health status.
// A Canary is healthy when status.phase == "Succeeded".
// Uses the dynamic client to avoid a compile-time dependency on the Flagger SDK.
type FlaggerAdapter struct {
	dynamic dynamic.Interface
}

// NewFlaggerAdapter constructs a FlaggerAdapter.
func NewFlaggerAdapter(dynClient dynamic.Interface) *FlaggerAdapter {
	return &FlaggerAdapter{dynamic: dynClient}
}

// Name returns "flagger".
func (a *FlaggerAdapter) Name() string { return "flagger" }

var flaggerGVR = schema.GroupVersionResource{
	Group:    "flagger.app",
	Version:  "v1beta1",
	Resource: "canaries",
}

// Check verifies that the Flagger Canary is in the Succeeded phase.
func (a *FlaggerAdapter) Check(ctx context.Context, opts CheckOptions) (HealthStatus, error) {
	cfg := opts.Flagger
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}
	if cfg.Name == "" {
		return HealthStatus{Healthy: false, Reason: "Flagger.Name not configured", CheckedAt: time.Now()}, nil
	}

	canary, err := a.dynamic.Resource(flaggerGVR).
		Namespace(cfg.Namespace).
		Get(ctx, cfg.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return HealthStatus{
				Healthy:   false,
				Reason:    fmt.Sprintf("Canary %s/%s not found", cfg.Namespace, cfg.Name),
				CheckedAt: time.Now(),
			}, nil
		}
		return HealthStatus{}, fmt.Errorf("get canary %s/%s: %w", cfg.Namespace, cfg.Name, err)
	}

	phase, _, _ := unstructured.NestedString(canary.Object, "status", "phase")
	statusMsg, _, _ := unstructured.NestedString(canary.Object, "status", "lastTransitionTime")

	if phase == "Succeeded" {
		return HealthStatus{Healthy: true, Reason: "Canary phase: Succeeded", CheckedAt: time.Now()}, nil
	}

	reason := fmt.Sprintf("Canary phase: %s", phase)
	if statusMsg != "" {
		reason += fmt.Sprintf(" (lastTransition: %s)", statusMsg)
	}
	return HealthStatus{Healthy: false, Reason: reason, CheckedAt: time.Now()}, nil
}

// --- AutoDetector ---

// AutoDetector selects the appropriate health adapter based on what is available
// in the cluster. Priority: explicit type > argocd > flux > resource.
type AutoDetector struct {
	k8s     sigs_client.Client
	dynamic dynamic.Interface
}

// NewAutoDetector constructs an AutoDetector.
func NewAutoDetector(k8s sigs_client.Client, dynClient dynamic.Interface) *AutoDetector {
	return &AutoDetector{k8s: k8s, dynamic: dynClient}
}

// Select returns the adapter for the given health type.
// healthType must be one of: "resource", "argocd", "flux", "argoRollouts", "flagger".
// An empty or unknown healthType returns an error — health.type must be
// explicitly configured in Pipeline.spec.environments[*].health.type.
// Silent fallback (auto-detection via CRD probing) is removed to prevent
// misconfiguration being silently masked (HE-4 in docs/design/11-graph-purity-tech-debt.md).
func (d *AutoDetector) Select(_ context.Context, healthType string) (Adapter, error) {
	switch healthType {
	case "resource":
		return NewDeploymentAdapter(d.k8s), nil
	case "argocd":
		return NewArgoCDAdapter(d.dynamic), nil
	case "flux":
		return NewFluxAdapter(d.dynamic), nil
	case "argoRollouts":
		return NewArgoRolloutsAdapter(d.dynamic), nil
	case "flagger":
		return NewFlaggerAdapter(d.dynamic), nil
	case "":
		return nil, fmt.Errorf(
			"health.type is required in Pipeline spec environments: " +
				"set health.type to one of [resource, argocd, flux, argoRollouts, flagger]")
	default:
		return nil, fmt.Errorf(
			"unknown health.type %q: must be one of [resource, argocd, flux, argoRollouts, flagger]",
			healthType)
	}
}
