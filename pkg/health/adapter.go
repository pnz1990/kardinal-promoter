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
// Phase 2 adapters: argoRollouts, flagger.
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
	// Type selects the adapter: "resource", "argocd", "flux".
	// Empty means auto-detect.
	Type string

	// Resource configuration (for type: resource).
	Resource ResourceConfig

	// ArgoCD configuration (for type: argocd).
	ArgoCD ArgoCDConfig

	// Flux configuration (for type: flux).
	Flux FluxConfig

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

// Select returns the best available adapter for the given health type.
// If healthType is empty, auto-detects based on installed CRDs.
func (d *AutoDetector) Select(ctx context.Context, healthType string) (Adapter, error) {
	switch healthType {
	case "argocd":
		return NewArgoCDAdapter(d.dynamic), nil
	case "flux":
		return NewFluxAdapter(d.dynamic), nil
	case "resource", "":
		// Auto-detect: try argocd, then flux, fall back to resource.
		if healthType == "" {
			if crdAvailable(ctx, d.dynamic, "applications.argoproj.io") {
				return NewArgoCDAdapter(d.dynamic), nil
			}
			if crdAvailable(ctx, d.dynamic, "kustomizations.kustomize.toolkit.fluxcd.io") {
				return NewFluxAdapter(d.dynamic), nil
			}
		}
		return NewDeploymentAdapter(d.k8s), nil
	default:
		return NewDeploymentAdapter(d.k8s), nil
	}
}

// crdAvailable checks whether a CRD (by plural resource name) is installed in the cluster.
// It uses the dynamic client to attempt a list of the CRD resource at the apiextensions group.
func crdAvailable(ctx context.Context, dynClient dynamic.Interface, crdName string) bool {
	crdGVR := schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}
	_, err := dynClient.Resource(crdGVR).Get(ctx, crdName, metav1.GetOptions{})
	return err == nil
}
