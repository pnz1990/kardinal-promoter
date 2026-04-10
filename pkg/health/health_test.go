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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

func buildScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))
	return s
}

// TestDeploymentAdapter_Healthy verifies that a Deployment with Available=True
// is reported as Healthy.
func TestDeploymentAdapter_Healthy(t *testing.T) {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx", Namespace: "prod"},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, Reason: "MinimumReplicasAvailable"},
			},
		},
	}

	s := buildScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(deploy).Build()

	adapter := health.NewDeploymentAdapter(c)
	result, err := adapter.Check(context.Background(), health.CheckOptions{
		Resource: health.ResourceConfig{
			Name:      "nginx",
			Namespace: "prod",
			Condition: "Available",
		},
	})

	require.NoError(t, err)
	assert.True(t, result.Healthy)
	assert.Contains(t, result.Reason, "Available")
}

// TestDeploymentAdapter_Degraded verifies that a Deployment with Available=False
// is reported as Degraded.
func TestDeploymentAdapter_Degraded(t *testing.T) {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx", Namespace: "prod"},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse, Message: "pods not ready"},
			},
		},
	}

	s := buildScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(deploy).Build()

	adapter := health.NewDeploymentAdapter(c)
	result, err := adapter.Check(context.Background(), health.CheckOptions{
		Resource: health.ResourceConfig{
			Name:      "nginx",
			Namespace: "prod",
			Condition: "Available",
		},
	})

	require.NoError(t, err)
	assert.False(t, result.Healthy)
	assert.Contains(t, result.Reason, "False")
}

// TestDeploymentAdapter_NotFound verifies that a missing Deployment returns Unhealthy.
func TestDeploymentAdapter_NotFound(t *testing.T) {
	s := buildScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	adapter := health.NewDeploymentAdapter(c)
	result, err := adapter.Check(context.Background(), health.CheckOptions{
		Resource: health.ResourceConfig{Name: "gone", Namespace: "prod", Condition: "Available"},
	})

	require.NoError(t, err)
	assert.False(t, result.Healthy)
	assert.Contains(t, result.Reason, "not found")
}

// TestArgoCDAdapter_Healthy verifies that an Application with health=Healthy and
// sync=Synced is reported as Healthy.
func TestArgoCDAdapter_Healthy(t *testing.T) {
	app := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata":   map[string]interface{}{"name": "nginx-prod", "namespace": "argocd"},
			"status": map[string]interface{}{
				"health": map[string]interface{}{"status": "Healthy"},
				"sync":   map[string]interface{}{"status": "Synced"},
			},
		},
	}
	app.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "argoproj.io",
		Version: "v1alpha1",
		Kind:    "Application",
	})

	dynClient := dynfake.NewSimpleDynamicClient(runtime.NewScheme(), app)

	adapter := health.NewArgoCDAdapter(dynClient)
	result, err := adapter.Check(context.Background(), health.CheckOptions{
		ArgoCD: health.ArgoCDConfig{Name: "nginx-prod", Namespace: "argocd"},
	})

	require.NoError(t, err)
	assert.True(t, result.Healthy)
	assert.Contains(t, result.Reason, "Healthy")
}

// TestArgoCDAdapter_Degraded verifies that a Degraded Application is Unhealthy.
func TestArgoCDAdapter_Degraded(t *testing.T) {
	app := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata":   map[string]interface{}{"name": "nginx-prod", "namespace": "argocd"},
			"status": map[string]interface{}{
				"health": map[string]interface{}{"status": "Degraded"},
				"sync":   map[string]interface{}{"status": "Synced"},
			},
		},
	}
	app.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	dynClient := dynfake.NewSimpleDynamicClient(runtime.NewScheme(), app)

	adapter := health.NewArgoCDAdapter(dynClient)
	result, err := adapter.Check(context.Background(), health.CheckOptions{
		ArgoCD: health.ArgoCDConfig{Name: "nginx-prod", Namespace: "argocd"},
	})

	require.NoError(t, err)
	assert.False(t, result.Healthy)
	assert.Contains(t, result.Reason, "Degraded")
}

// TestFluxAdapter_Healthy verifies that a Kustomization with Ready=True is Healthy.
func TestFluxAdapter_Healthy(t *testing.T) {
	ks := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
			"kind":       "Kustomization",
			"metadata": map[string]interface{}{
				"name":       "nginx-prod",
				"namespace":  "flux-system",
				"generation": int64(3),
			},
			"status": map[string]interface{}{
				"observedGeneration": int64(3),
				"conditions": []interface{}{
					map[string]interface{}{
						"type":   "Ready",
						"status": "True",
						"reason": "ReconciliationSucceeded",
					},
				},
			},
		},
	}
	ks.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kustomize.toolkit.fluxcd.io",
		Version: "v1",
		Kind:    "Kustomization",
	})
	dynClient := dynfake.NewSimpleDynamicClient(runtime.NewScheme(), ks)

	adapter := health.NewFluxAdapter(dynClient)
	result, err := adapter.Check(context.Background(), health.CheckOptions{
		Flux: health.FluxConfig{Name: "nginx-prod", Namespace: "flux-system"},
	})

	require.NoError(t, err)
	assert.True(t, result.Healthy)
	assert.Contains(t, result.Reason, "Ready=True")
}

// TestFluxAdapter_Progressing verifies that a Kustomization with Ready=False is Unhealthy.
func TestFluxAdapter_Progressing(t *testing.T) {
	ks := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
			"kind":       "Kustomization",
			"metadata": map[string]interface{}{
				"name":       "nginx-prod",
				"namespace":  "flux-system",
				"generation": int64(4),
			},
			"status": map[string]interface{}{
				"observedGeneration": int64(3), // behind — not yet reconciled
				"conditions": []interface{}{
					map[string]interface{}{
						"type":   "Ready",
						"status": "False",
						"reason": "ArtifactFailed",
					},
				},
			},
		},
	}
	ks.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kustomize.toolkit.fluxcd.io",
		Version: "v1",
		Kind:    "Kustomization",
	})
	dynClient := dynfake.NewSimpleDynamicClient(runtime.NewScheme(), ks)

	adapter := health.NewFluxAdapter(dynClient)
	result, err := adapter.Check(context.Background(), health.CheckOptions{
		Flux: health.FluxConfig{Name: "nginx-prod", Namespace: "flux-system"},
	})

	require.NoError(t, err)
	assert.False(t, result.Healthy)
}

// TestAutoDetector_SelectsDeploymentWhenNoGitOpsCRDs verifies fallback to Deployment adapter.
func TestAutoDetector_SelectsDeploymentWhenNoGitOpsCRDs(t *testing.T) {
	s := buildScheme(t)
	// No ArgoCD or Flux CRDs
	dynClient := dynfake.NewSimpleDynamicClient(runtime.NewScheme())
	c := fake.NewClientBuilder().WithScheme(s).Build()

	detector := health.NewAutoDetector(c, dynClient)
	adapter, err := detector.Select(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, "resource", adapter.Name())
}

// TestAutoDetector_PreferredByType verifies explicit type selection.
func TestAutoDetector_PreferredByType(t *testing.T) {
	s := buildScheme(t)
	dynClient := dynfake.NewSimpleDynamicClient(runtime.NewScheme())
	c := fake.NewClientBuilder().WithScheme(s).Build()

	detector := health.NewAutoDetector(c, dynClient)

	// Explicit "resource" type
	adapter, err := detector.Select(context.Background(), "resource")
	require.NoError(t, err)
	assert.Equal(t, "resource", adapter.Name())
}
