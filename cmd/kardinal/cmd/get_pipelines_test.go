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

package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func buildGetPipelinesScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}

// TestGetPipelines_WatchFlagRegistered verifies that the --watch / -w flags
// are correctly registered on the `get pipelines` command.
func TestGetPipelines_WatchFlagRegistered(t *testing.T) {
	cmd := newGetPipelinesCmd()
	f := cmd.Flags().Lookup("watch")
	require.NotNil(t, f, "--watch flag must be registered on 'get pipelines'")
	assert.Equal(t, "false", f.DefValue, "--watch must default to false")

	// Shorthand
	sf := cmd.Flags().ShorthandLookup("w")
	require.NotNil(t, sf, "-w shorthand must be registered on 'get pipelines'")
}

// TestGetSteps_WatchFlagRegistered verifies that the --watch / -w flags
// are correctly registered on the `get steps` command.
func TestGetSteps_WatchFlagRegistered(t *testing.T) {
	cmd := newGetStepsCmd()
	f := cmd.Flags().Lookup("watch")
	require.NotNil(t, f, "--watch flag must be registered on 'get steps'")
	assert.Equal(t, "false", f.DefValue, "--watch must default to false")

	sf := cmd.Flags().ShorthandLookup("w")
	require.NotNil(t, sf, "-w shorthand must be registered on 'get steps'")
}

// TestGetPipelinesOnce_TableOutput verifies that getPipelinesOnce produces
// the expected pipeline table output (same as the non-watch path).
func TestGetPipelinesOnce_TableOutput(t *testing.T) {
	s := buildGetPipelinesScheme(t)

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-app",
			Namespace: "default",
		},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "prod"},
			},
		},
	}
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-app-test",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline":    "my-app",
				"kardinal.io/environment": "test",
				"kardinal.io/bundle":      "bundle-abc",
			},
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "my-app",
			Environment:  "test",
			BundleName:   "bundle-abc",
		},
		Status: v1alpha1.PromotionStepStatus{
			State: "Verified",
		},
	}

	fc := fake.NewClientBuilder().WithScheme(s).WithObjects(pipeline, step).Build()

	var buf bytes.Buffer
	err := getPipelinesOnce(&buf, fc, "default", nil, false)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "my-app", "pipeline name must appear in output")
	// The table must have at least a header row
	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Greater(t, len(lines), 1, "output must have at least a header and one data row")
}

// TestGetStepsOnce_TableOutput verifies that getStepsOnce produces
// the expected steps table output (same as the non-watch path).
func TestGetStepsOnce_TableOutput(t *testing.T) {
	s := buildGetPipelinesScheme(t)

	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-app-test",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline":    "my-app",
				"kardinal.io/environment": "test",
				"kardinal.io/bundle":      "bundle-abc",
			},
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "my-app",
			Environment:  "test",
			BundleName:   "bundle-abc",
		},
		Status: v1alpha1.PromotionStepStatus{
			State: "Verified",
		},
	}

	fc := fake.NewClientBuilder().WithScheme(s).WithObjects(step).Build()

	var buf bytes.Buffer
	err := getStepsOnce(&buf, fc, "default", "my-app")
	require.NoError(t, err)

	out := buf.String()
	// Steps table must have at least headers
	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Greater(t, len(lines), 0, "output must have at least one line")
}

// TestGetPipelinesOnce_FailedBundle_ShowsError verifies that when a Bundle is in
// Failed phase with a TranslationError condition, getPipelinesOnce appends an
// ERROR: line after the pipeline table.
func TestGetPipelinesOnce_FailedBundle_ShowsError(t *testing.T) {
	s := buildGetPipelinesScheme(t)

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-app",
			Namespace: "default",
		},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "prod"},
			},
		},
	}

	failedBundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-app-v1",
			Namespace: "default",
		},
		Spec: v1alpha1.BundleSpec{
			Pipeline: "my-app",
			Type:     "image",
		},
		Status: v1alpha1.BundleStatus{
			Phase: "Failed",
			Conditions: []metav1.Condition{
				{
					Type:    "Failed",
					Status:  metav1.ConditionTrue,
					Reason:  "TranslationError",
					Message: `build: environment "prod" dependsOn unknown environment "staging"`,
				},
			},
		},
	}

	fc := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(pipeline, failedBundle).
		WithStatusSubresource(failedBundle).
		Build()

	var buf bytes.Buffer
	err := getPipelinesOnce(&buf, fc, "default", nil, false)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "my-app", "pipeline name must appear in table output")
	assert.Contains(t, out, "ERROR:", "ERROR: prefix must appear when a bundle is Failed")
	assert.Contains(t, out, `dependsOn unknown environment "staging"`,
		"condition message must appear in error output")
}

// TestGetPipelinesOnce_HealthyBundles_NoErrorSection verifies that when all bundles
// are healthy, no ERROR: section is printed.
func TestGetPipelinesOnce_HealthyBundles_NoErrorSection(t *testing.T) {
	s := buildGetPipelinesScheme(t)

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-app",
			Namespace: "default",
		},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}},
		},
	}

	goodBundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-app-v1",
			Namespace: "default",
		},
		Spec:   v1alpha1.BundleSpec{Pipeline: "my-app", Type: "image"},
		Status: v1alpha1.BundleStatus{Phase: "Verified"},
	}

	fc := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(pipeline, goodBundle).
		WithStatusSubresource(goodBundle).
		Build()

	var buf bytes.Buffer
	err := getPipelinesOnce(&buf, fc, "default", nil, false)
	require.NoError(t, err)

	out := buf.String()
	assert.NotContains(t, out, "ERROR:", "no ERROR: section expected when bundles are healthy")
}
