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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func cliCoreTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)
	return s
}

// TestVersion_ThreeLinesNoCluster verifies that versionFn prints three lines
// with "unknown" for controller/graph when no client is available.
func TestVersion_ThreeLinesNoCluster(t *testing.T) {
	var buf bytes.Buffer
	err := versionFn(&buf, nil, "v0.1.0-test")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "CLI:        v0.1.0-test")
	assert.Contains(t, out, "Controller: unknown")
	assert.Contains(t, out, "Graph:      unknown")

	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Len(t, lines, 3, "version output must have exactly 3 lines")
}

// TestVersion_ReadsConfigMapVersions verifies that versionFn reads controller
// and graph versions from the kardinal-version ConfigMap.
func TestVersion_ReadsConfigMapVersions(t *testing.T) {
	s := cliCoreTestScheme(t)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kardinal-version",
			Namespace: "kardinal-system",
		},
		Data: map[string]string{
			"version": "v0.4.1",
			"graph":   "experimental.kro.run/v1alpha1",
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cm).Build()

	var buf bytes.Buffer
	err := versionFn(&buf, c, "v0.1.0-test")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "CLI:        v0.1.0-test")
	assert.Contains(t, out, "Controller: v0.4.1")
	assert.Contains(t, out, "Graph:      experimental.kro.run/v1alpha1")
}

// TestVersion_MissingConfigMapShowsUnknown verifies that missing ConfigMap
// keys show "unknown" rather than crashing.
func TestVersion_MissingConfigMapShowsUnknown(t *testing.T) {
	s := cliCoreTestScheme(t)
	// ConfigMap exists but has no "graph" key.
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kardinal-version",
			Namespace: "kardinal-system",
		},
		Data: map[string]string{
			"version": "v0.4.1",
			// "graph" key absent
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cm).Build()

	var buf bytes.Buffer
	err := versionFn(&buf, c, "v0.1.0-test")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Controller: v0.4.1")
	assert.Contains(t, out, "Graph:      unknown", "absent graph key must show 'unknown'")
}

// TestHistory_NewFormat verifies that historyFn outputs the correct column headers.
func TestHistory_NewFormat(t *testing.T) {
	s := cliTestScheme(t)
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     v1alpha1.BundleStatus{Phase: "Verified"},
	}
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1-prod", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "nginx-demo-v1",
			Environment:  "prod",
			StepType:     "health-check",
		},
		Status: v1alpha1.PromotionStepStatus{
			State:   "Verified",
			PRURL:   "https://github.com/myorg/gitops/pull/144",
			Outputs: map[string]string{"mergedBy": "alice"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(b, step).WithObjects(b, step).Build()

	var buf bytes.Buffer
	err := historyFn(&buf, c, "default", "nginx-demo")
	require.NoError(t, err)

	out := buf.String()
	// Must have the correct column headers.
	assert.Contains(t, out, "BUNDLE")
	assert.Contains(t, out, "ACTION")
	assert.Contains(t, out, "ENV")
	assert.Contains(t, out, "PR")
	assert.Contains(t, out, "APPROVER")
	assert.Contains(t, out, "DURATION")
	assert.Contains(t, out, "TIMESTAMP")

	// Must show data.
	assert.Contains(t, out, "nginx-demo-v1")
	assert.Contains(t, out, "promote")
	assert.Contains(t, out, "prod")
	assert.Contains(t, out, "#144")
	assert.Contains(t, out, "alice")
}

// TestHistory_RollbackAction verifies that history shows "rollback" action for rollback bundles.
func TestHistory_RollbackAction(t *testing.T) {
	s := cliTestScheme(t)
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-rollback-v1", Namespace: "default"},
		Spec: v1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: "nginx-demo",
			Provenance: &v1alpha1.BundleProvenance{
				RollbackOf: "nginx-demo-v2",
			},
		},
	}
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "rollback-step-prod", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "nginx-demo-rollback-v1",
			Environment:  "prod",
			StepType:     "health-check",
		},
		Status: v1alpha1.PromotionStepStatus{State: "Verified"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(b, step).WithObjects(b, step).Build()

	var buf bytes.Buffer
	err := historyFn(&buf, c, "default", "nginx-demo")
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "rollback")
}

// TestHistory_NoBundlesMessage verifies the "No bundles found" message.
func TestHistory_NoBundlesMessage(t *testing.T) {
	s := cliTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	var buf bytes.Buffer
	err := historyFn(&buf, c, "default", "my-pipeline")
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "No bundles found")
}

// TestExtractPRNumber verifies that PR numbers are parsed from GitHub URLs.
func TestExtractPRNumber(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"https://github.com/org/repo/pull/42", "#42"},
		{"https://github.com/org/repo/pull/144", "#144"},
		{"not-a-url", "not-a-url"},
		{"", ""},
	}
	for _, tc := range cases {
		got := extractPRNumber(tc.input)
		assert.Equal(t, tc.expected, got, "input: %q", tc.input)
	}
}
