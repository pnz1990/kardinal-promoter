// Copyright 2026 The kardinal-promoter Authors.
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

package steps_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// makeArgoCDApp creates an unstructured ArgoCD Application for use in tests.
func makeArgoCDApp(namespace, name string, valuesObject map[string]interface{}) *unstructured.Unstructured {
	app := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
		},
	}
	if valuesObject != nil {
		if err := unstructured.SetNestedMap(app.Object, map[string]interface{}{
			"source": map[string]interface{}{
				"helm": map[string]interface{}{
					"valuesObject": valuesObject,
				},
			},
		}, "spec"); err != nil {
			panic(err)
		}
	}
	return app
}

// argoCDAppGVK is the GroupVersionKind for ArgoCD Applications.
var argoCDAppGVK = schema.GroupVersionKind{
	Group:   "argoproj.io",
	Version: "v1alpha1",
	Kind:    "Application",
}

// TestArgoCDSetImageStep_Registered verifies the step is in the registry.
func TestArgoCDSetImageStep_Registered(t *testing.T) {
	step, err := parentsteps.Lookup("argocd-set-image")
	require.NoError(t, err, "argocd-set-image must be registered in the step registry")
	assert.Equal(t, "argocd-set-image", step.Name())
}

// TestArgoCDSetImageStep_Success verifies the step patches the Application tag.
func TestArgoCDSetImageStep_Success(t *testing.T) {
	app := makeArgoCDApp("argocd", "my-app", map[string]interface{}{
		"image": map[string]interface{}{"tag": "1.28.0"},
	})

	scheme := newArgoCDScheme(t)
	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(app).
		Build()

	state := &parentsteps.StepState{
		K8sClient: k8s,
		Environment: v1alpha1.EnvironmentSpec{
			Name: "prod",
			Update: v1alpha1.UpdateConfig{
				Strategy: "argocd",
				ArgoCD: &v1alpha1.ArgoCDUpdateConfig{
					Application: "my-app",
					Namespace:   "argocd",
					ImageKey:    "image.tag",
				},
			},
		},
		Bundle: v1alpha1.BundleSpec{
			Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/myorg/app", Tag: "1.29.0"}},
		},
		Inputs:  map[string]string{},
		Outputs: map[string]string{},
	}

	step, err := parentsteps.Lookup("argocd-set-image")
	require.NoError(t, err)

	result, execErr := step.Execute(context.Background(), state)
	require.NoError(t, execErr)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, "1.29.0", result.Outputs["imageTag"])
	assert.Equal(t, "my-app", result.Outputs["argocdApplication"])
	assert.Contains(t, result.Message, "1.29.0")
}

// TestArgoCDSetImageStep_Idempotent verifies that running the step twice
// with the same tag returns StepSuccess without error on the second run.
func TestArgoCDSetImageStep_Idempotent(t *testing.T) {
	// The Application already has the target tag.
	app := makeArgoCDApp("argocd", "my-app", map[string]interface{}{
		"image": map[string]interface{}{"tag": "1.29.0"},
	})

	scheme := newArgoCDScheme(t)
	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(app).
		Build()

	state := &parentsteps.StepState{
		K8sClient: k8s,
		Environment: v1alpha1.EnvironmentSpec{
			Name: "prod",
			Update: v1alpha1.UpdateConfig{
				Strategy: "argocd",
				ArgoCD: &v1alpha1.ArgoCDUpdateConfig{
					Application: "my-app",
					Namespace:   "argocd",
					ImageKey:    "image.tag",
				},
			},
		},
		Bundle: v1alpha1.BundleSpec{
			Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/myorg/app", Tag: "1.29.0"}},
		},
		Inputs:  map[string]string{},
		Outputs: map[string]string{},
	}

	step, err := parentsteps.Lookup("argocd-set-image")
	require.NoError(t, err)

	// First run.
	result1, err1 := step.Execute(context.Background(), state)
	require.NoError(t, err1)
	assert.Equal(t, parentsteps.StepSuccess, result1.Status)

	// Second run — must still succeed (idempotent).
	result2, err2 := step.Execute(context.Background(), state)
	require.NoError(t, err2)
	assert.Equal(t, parentsteps.StepSuccess, result2.Status)
	assert.Equal(t, "1.29.0", result2.Outputs["imageTag"])
}

// TestArgoCDSetImageStep_NilK8sClient verifies O4: nil K8sClient → StepFailed.
func TestArgoCDSetImageStep_NilK8sClient(t *testing.T) {
	state := &parentsteps.StepState{
		K8sClient: nil,
		Environment: v1alpha1.EnvironmentSpec{
			Update: v1alpha1.UpdateConfig{
				ArgoCD: &v1alpha1.ArgoCDUpdateConfig{Application: "my-app"},
			},
		},
		Bundle:  v1alpha1.BundleSpec{Images: []v1alpha1.ImageRef{{Tag: "1.0.0"}}},
		Inputs:  map[string]string{},
		Outputs: map[string]string{},
	}

	step, err := parentsteps.Lookup("argocd-set-image")
	require.NoError(t, err)

	result, execErr := step.Execute(context.Background(), state)
	require.Error(t, execErr)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
	assert.Contains(t, result.Message, "K8sClient is required")
}

// TestArgoCDSetImageStep_MissingApplicationName verifies O5: empty application → StepFailed.
func TestArgoCDSetImageStep_MissingApplicationName(t *testing.T) {
	scheme := newArgoCDScheme(t)
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()

	state := &parentsteps.StepState{
		K8sClient: k8s,
		Environment: v1alpha1.EnvironmentSpec{
			Update: v1alpha1.UpdateConfig{
				ArgoCD: &v1alpha1.ArgoCDUpdateConfig{
					// Application is intentionally empty.
					Namespace: "argocd",
				},
			},
		},
		Bundle:  v1alpha1.BundleSpec{Images: []v1alpha1.ImageRef{{Tag: "1.0.0"}}},
		Inputs:  map[string]string{},
		Outputs: map[string]string{},
	}

	step, err := parentsteps.Lookup("argocd-set-image")
	require.NoError(t, err)

	result, execErr := step.Execute(context.Background(), state)
	require.Error(t, execErr)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
	assert.Contains(t, result.Message, "argocd.application input is required")
}

// TestArgoCDSetImageStep_ApplicationNotFound verifies O6: not found → StepFailed with "not found".
func TestArgoCDSetImageStep_ApplicationNotFound(t *testing.T) {
	scheme := newArgoCDScheme(t)
	// Build a client with no pre-existing objects.
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()

	state := &parentsteps.StepState{
		K8sClient: k8s,
		Environment: v1alpha1.EnvironmentSpec{
			Update: v1alpha1.UpdateConfig{
				ArgoCD: &v1alpha1.ArgoCDUpdateConfig{
					Application: "nonexistent-app",
					Namespace:   "argocd",
				},
			},
		},
		Bundle:  v1alpha1.BundleSpec{Images: []v1alpha1.ImageRef{{Tag: "1.0.0"}}},
		Inputs:  map[string]string{},
		Outputs: map[string]string{},
	}

	step, err := parentsteps.Lookup("argocd-set-image")
	require.NoError(t, err)

	result, execErr := step.Execute(context.Background(), state)
	require.Error(t, execErr)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
	assert.Contains(t, result.Message, "not found")
}

// TestArgoCDSetImageStep_InputsMapOverridesConfig verifies that Inputs map values
// take precedence over Environment.Update.ArgoCD config.
func TestArgoCDSetImageStep_InputsMapOverridesConfig(t *testing.T) {
	app := makeArgoCDApp("custom-ns", "override-app", map[string]interface{}{
		"app": map[string]interface{}{"version": "old"},
	})

	scheme := newArgoCDScheme(t)
	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(app).
		Build()

	state := &parentsteps.StepState{
		K8sClient: k8s,
		Environment: v1alpha1.EnvironmentSpec{
			Update: v1alpha1.UpdateConfig{
				ArgoCD: &v1alpha1.ArgoCDUpdateConfig{
					Application: "wrong-app",
					Namespace:   "wrong-ns",
					ImageKey:    "wrong.key",
				},
			},
		},
		Bundle: v1alpha1.BundleSpec{
			Images: []v1alpha1.ImageRef{{Tag: "2.0.0"}},
		},
		// Inputs override the config.
		Inputs: map[string]string{
			"argocd.application": "override-app",
			"argocd.namespace":   "custom-ns",
			"argocd.imageKey":    "app.version",
		},
		Outputs: map[string]string{},
	}

	step, err := parentsteps.Lookup("argocd-set-image")
	require.NoError(t, err)

	result, execErr := step.Execute(context.Background(), state)
	require.NoError(t, execErr)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, "2.0.0", result.Outputs["imageTag"])
	assert.Equal(t, "override-app", result.Outputs["argocdApplication"])
}

// TestDefaultSequenceForBundle_ArgoCDStrategy verifies O8:
// updateStrategy=="argocd" produces [argocd-set-image, health-check].
func TestDefaultSequenceForBundle_ArgoCDStrategy(t *testing.T) {
	seq := parentsteps.DefaultSequenceForBundle("auto", "image", "argocd", "")
	require.Equal(t, []string{"argocd-set-image", "health-check"}, seq,
		"argocd strategy must produce exactly [argocd-set-image, health-check]")

	// PR-review mode is ignored for argocd — no git workflow.
	seqPR := parentsteps.DefaultSequenceForBundle("pr-review", "image", "argocd", "")
	require.Equal(t, []string{"argocd-set-image", "health-check"}, seqPR,
		"argocd strategy must not include open-pr or wait-for-merge")

	// Must not include any git steps.
	assert.NotContains(t, seq, "git-clone")
	assert.NotContains(t, seq, "git-commit")
	assert.NotContains(t, seq, "git-push")
	assert.NotContains(t, seq, "open-pr")
	assert.NotContains(t, seq, "wait-for-merge")
}

// newArgoCDScheme returns a runtime.Scheme with the ArgoCD Application type registered
// as an unstructured resource so the fake client can handle it.
func newArgoCDScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(argoCDAppGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationList"},
		&unstructured.UnstructuredList{},
	)
	return s
}
