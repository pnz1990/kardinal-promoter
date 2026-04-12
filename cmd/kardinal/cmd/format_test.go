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

package cmd_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/cmd/kardinal/cmd"
)

func TestFormatPipelineTable(t *testing.T) {
	now := time.Now()
	pipelines := []v1alpha1.Pipeline{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "nginx-demo",
				CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Minute)),
			},
			Spec: v1alpha1.PipelineSpec{
				Environments: []v1alpha1.EnvironmentSpec{
					{Name: "test"},
					{Name: "uat"},
					{Name: "prod"},
				},
			},
		},
	}
	steps := []v1alpha1.PromotionStep{
		{
			Spec: v1alpha1.PromotionStepSpec{
				PipelineName: "nginx-demo",
				Environment:  "test",
				BundleName:   "nginx-demo-v1-29-0",
				StepType:     "health-check",
			},
			Status: v1alpha1.PromotionStepStatus{State: "Verified"},
		},
		{
			Spec: v1alpha1.PromotionStepSpec{
				PipelineName: "nginx-demo",
				Environment:  "uat",
				BundleName:   "nginx-demo-v1-29-0",
				StepType:     "health-check",
			},
			Status: v1alpha1.PromotionStepStatus{State: "Promoting"},
		},
		{
			Spec: v1alpha1.PromotionStepSpec{
				PipelineName: "nginx-demo",
				Environment:  "prod",
				BundleName:   "nginx-demo-v1-29-0",
				StepType:     "health-check",
			},
			Status: v1alpha1.PromotionStepStatus{State: "Pending"},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, cmd.FormatPipelineTable(&buf, pipelines, steps))
	out := buf.String()

	// Header must have per-environment columns.
	assert.Contains(t, out, "PIPELINE")
	assert.Contains(t, out, "BUNDLE")
	assert.Contains(t, out, "TEST")
	assert.Contains(t, out, "UAT")
	assert.Contains(t, out, "PROD")
	assert.Contains(t, out, "AGE")

	// Must NOT have old columns.
	assert.NotContains(t, out, "PHASE")
	assert.NotContains(t, out, "ENVIRONMENTS")
	assert.NotContains(t, out, "PAUSED")

	// Row data.
	assert.Contains(t, out, "nginx-demo")
	assert.Contains(t, out, "nginx-demo-v1-29-0")
	assert.Contains(t, out, "Verified")
	assert.Contains(t, out, "Promoting")
	assert.Contains(t, out, "Pending")
}

// TestFormatPipelineTable_NoSteps verifies that an empty step list shows "-" for all env columns.
func TestFormatPipelineTable_NoSteps(t *testing.T) {
	now := time.Now()
	pipelines := []v1alpha1.Pipeline{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "my-pipeline",
				CreationTimestamp: metav1.NewTime(now.Add(-5 * time.Minute)),
			},
			Spec: v1alpha1.PipelineSpec{
				Environments: []v1alpha1.EnvironmentSpec{
					{Name: "dev"},
					{Name: "prod"},
				},
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, cmd.FormatPipelineTable(&buf, pipelines, nil))
	out := buf.String()

	assert.Contains(t, out, "DEV")
	assert.Contains(t, out, "PROD")
	assert.Contains(t, out, "my-pipeline")
	// Without steps both env columns and bundle should show "-".
	// Count occurrences of "-" — at least 3 (bundle + 2 envs).
	assert.GreaterOrEqual(t, strings.Count(out, "-"), 3)
}

// TestFormatPipelineTable_MultiPipeline verifies that multiple pipelines with different
// environments produce a union-column table with "-" for absent environments.
func TestFormatPipelineTable_MultiPipeline(t *testing.T) {
	now := time.Now()
	pipelines := []v1alpha1.Pipeline{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "app-a",
				CreationTimestamp: metav1.NewTime(now.Add(-10 * time.Minute)),
			},
			Spec: v1alpha1.PipelineSpec{
				Environments: []v1alpha1.EnvironmentSpec{
					{Name: "test"},
					{Name: "prod"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "app-b",
				CreationTimestamp: metav1.NewTime(now.Add(-5 * time.Minute)),
			},
			Spec: v1alpha1.PipelineSpec{
				Environments: []v1alpha1.EnvironmentSpec{
					{Name: "test"},
					{Name: "staging"},
					{Name: "prod"},
				},
			},
		},
	}
	steps := []v1alpha1.PromotionStep{
		{
			Spec: v1alpha1.PromotionStepSpec{
				PipelineName: "app-a",
				Environment:  "test",
				BundleName:   "app-a-v1-0-0",
				StepType:     "health-check",
			},
			Status: v1alpha1.PromotionStepStatus{State: "Verified"},
		},
		{
			Spec: v1alpha1.PromotionStepSpec{
				PipelineName: "app-b",
				Environment:  "staging",
				BundleName:   "app-b-v2-0-0",
				StepType:     "health-check",
			},
			Status: v1alpha1.PromotionStepStatus{State: "Verified"},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, cmd.FormatPipelineTable(&buf, pipelines, steps))
	out := buf.String()

	// Union columns: TEST, PROD from app-a, STAGING from app-b.
	assert.Contains(t, out, "TEST")
	assert.Contains(t, out, "STAGING")
	assert.Contains(t, out, "PROD")

	// app-a has no staging column — its staging cell should be "-".
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.GreaterOrEqual(t, len(lines), 3, "need header + 2 data rows")
	// Find the app-a row.
	for _, line := range lines[1:] { // skip header
		if strings.HasPrefix(strings.TrimSpace(line), "app-a") {
			assert.Contains(t, line, "-", "app-a row must have '-' for missing staging column")
		}
	}
}

func TestFormatBundleTable(t *testing.T) {
	now := time.Now()
	bundles := []v1alpha1.Bundle{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "nginx-demo-v1-29-0",
				CreationTimestamp: metav1.NewTime(now.Add(-45 * time.Second)),
			},
			Spec: v1alpha1.BundleSpec{
				Type:     "image",
				Pipeline: "nginx-demo",
			},
			Status: v1alpha1.BundleStatus{
				Phase: "Promoting",
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, cmd.FormatBundleTable(&buf, bundles))
	out := buf.String()

	assert.Contains(t, out, "BUNDLE")
	assert.Contains(t, out, "TYPE")
	assert.Contains(t, out, "PHASE")
	assert.Contains(t, out, "AGE")
	assert.Contains(t, out, "nginx-demo-v1-29-0")
	assert.Contains(t, out, "image")
	assert.Contains(t, out, "Promoting")
}

func TestFormatStepsTable(t *testing.T) {
	now := time.Now()
	steps := []v1alpha1.PromotionStep{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "nginx-demo-v1-29-0-test",
				CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Minute)),
			},
			Spec: v1alpha1.PromotionStepSpec{
				Environment: "test",
				StepType:    "kustomize-set-image",
			},
			Status: v1alpha1.PromotionStepStatus{
				State:   "Pending",
				Message: "",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "nginx-demo-v1-29-0-prod-gate",
				CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Minute)),
			},
			Spec: v1alpha1.PromotionStepSpec{
				Environment: "prod",
				StepType:    "PolicyGate",
			},
			Status: v1alpha1.PromotionStepStatus{
				State:   "Pending",
				Message: "no-weekend-deploys",
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, cmd.FormatStepsTable(&buf, steps))
	out := buf.String()

	assert.Contains(t, out, "ENVIRONMENT")
	assert.Contains(t, out, "STEP-TYPE")
	assert.Contains(t, out, "STATE")
	assert.Contains(t, out, "MESSAGE")
	assert.Contains(t, out, "test")
	assert.Contains(t, out, "kustomize-set-image")
	assert.Contains(t, out, "Pending")
	assert.Contains(t, out, "no-weekend-deploys")
}

func TestPolicyGatePhase(t *testing.T) {
	now := metav1.Now()
	tests := []struct {
		name string
		gate v1alpha1.PolicyGate
		want string
	}{
		{
			name: "ready=true gives Pass",
			gate: v1alpha1.PolicyGate{
				Status: v1alpha1.PolicyGateStatus{
					Ready:           true,
					LastEvaluatedAt: &now,
				},
			},
			want: "Pass",
		},
		{
			name: "ready=false with lastEvaluatedAt gives Block",
			gate: v1alpha1.PolicyGate{
				Status: v1alpha1.PolicyGateStatus{
					Ready:           false,
					LastEvaluatedAt: &now,
				},
			},
			want: "Block",
		},
		{
			name: "ready=false with no lastEvaluatedAt gives Pending",
			gate: v1alpha1.PolicyGate{
				Status: v1alpha1.PolicyGateStatus{
					Ready:           false,
					LastEvaluatedAt: nil,
				},
			},
			want: "Pending",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, cmd.PolicyGatePhase(tc.gate))
		})
	}
}

func TestHumanAge(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		contains string
	}{
		{"seconds", 45 * time.Second, "s"},
		{"minutes", 3 * time.Minute, "m"},
		{"hours", 2 * time.Hour, "h"},
		{"days", 25 * time.Hour, "d"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := cmd.HumanAge(time.Now().Add(-tc.duration))
			require.True(t, strings.Contains(result, tc.contains),
				"expected %q to contain %q, got %q", tc.name, tc.contains, result)
		})
	}
}

func TestWriteJSON_ProducesValidJSON(t *testing.T) {
	data := []struct {
		Name  string `json:"name"`
		Phase string `json:"phase"`
	}{
		{Name: "my-app", Phase: "Promoting"},
	}
	var buf bytes.Buffer
	err := cmd.WriteJSON(&buf, data)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, `"name": "my-app"`)
	assert.Contains(t, out, `"phase": "Promoting"`)
}

func TestWriteYAML_ProducesValidYAML(t *testing.T) {
	data := []struct {
		Name  string `json:"name" yaml:"name"`
		Phase string `json:"phase" yaml:"phase"`
	}{
		{Name: "my-app", Phase: "Verified"},
	}
	var buf bytes.Buffer
	err := cmd.WriteYAML(&buf, data)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "name: my-app")
	assert.Contains(t, out, "phase: Verified")
}

func TestOutputFormat_DefaultIsTable(t *testing.T) {
	// Default global output is "", which means table.
	assert.Equal(t, "", cmd.OutputFormat())
}

// TestFormatPipelineTable_PausedBadge verifies that a paused pipeline shows [PAUSED] in the name.
func TestFormatPipelineTable_PausedBadge(t *testing.T) {
	now := time.Now()
	pipelines := []v1alpha1.Pipeline{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "my-pipeline",
				CreationTimestamp: metav1.NewTime(now.Add(-5 * time.Minute)),
			},
			Spec: v1alpha1.PipelineSpec{
				Paused: true,
				Environments: []v1alpha1.EnvironmentSpec{
					{Name: "test"},
					{Name: "prod"},
				},
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, cmd.FormatPipelineTable(&buf, pipelines, nil))
	out := buf.String()

	assert.Contains(t, out, "my-pipeline [PAUSED]", "paused pipeline must show [PAUSED] badge in name")
}

// TestFormatPipelineTable_NonPausedNoBadge verifies that a non-paused pipeline does NOT show [PAUSED].
func TestFormatPipelineTable_NonPausedNoBadge(t *testing.T) {
	now := time.Now()
	pipelines := []v1alpha1.Pipeline{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "my-pipeline",
				CreationTimestamp: metav1.NewTime(now.Add(-5 * time.Minute)),
			},
			Spec: v1alpha1.PipelineSpec{
				Paused: false,
				Environments: []v1alpha1.EnvironmentSpec{
					{Name: "test"},
				},
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, cmd.FormatPipelineTable(&buf, pipelines, nil))
	out := buf.String()

	assert.NotContains(t, out, "[PAUSED]", "non-paused pipeline must not show [PAUSED] badge")
}

// TestFormatPipelineTable_ActiveBundlePrefersPromoting verifies that when both a Verified
// step (old bundle) and a Promoting step (new bundle) exist, the Promoting bundle is shown.
func TestFormatPipelineTable_ActiveBundlePrefersPromoting(t *testing.T) {
	now := time.Now()
	pipelines := []v1alpha1.Pipeline{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "my-app",
				CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Hour)),
			},
			Spec: v1alpha1.PipelineSpec{
				Environments: []v1alpha1.EnvironmentSpec{
					{Name: "test"},
					{Name: "prod"},
				},
			},
		},
	}
	// Old bundle: Verified in test, prod; created 1 hour ago.
	oldTime := metav1.NewTime(now.Add(-1 * time.Hour))
	// New bundle: Promoting in test; created 30s ago.
	newTime := metav1.NewTime(now.Add(-30 * time.Second))

	steps := []v1alpha1.PromotionStep{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "my-app-old-test",
				CreationTimestamp: oldTime,
			},
			Spec: v1alpha1.PromotionStepSpec{
				PipelineName: "my-app",
				Environment:  "test",
				BundleName:   "my-app-old",
			},
			Status: v1alpha1.PromotionStepStatus{State: "Verified"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "my-app-old-prod",
				CreationTimestamp: oldTime,
			},
			Spec: v1alpha1.PromotionStepSpec{
				PipelineName: "my-app",
				Environment:  "prod",
				BundleName:   "my-app-old",
			},
			Status: v1alpha1.PromotionStepStatus{State: "Verified"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "my-app-new-test",
				CreationTimestamp: newTime,
			},
			Spec: v1alpha1.PromotionStepSpec{
				PipelineName: "my-app",
				Environment:  "test",
				BundleName:   "my-app-new",
			},
			Status: v1alpha1.PromotionStepStatus{State: "Promoting"},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, cmd.FormatPipelineTable(&buf, pipelines, steps))
	out := buf.String()

	// The table MUST show the new (Promoting) bundle, not the old (Verified) bundle.
	assert.Contains(t, out, "my-app-new",
		"table must show the active Promoting bundle, not the old Verified one")
	assert.NotContains(t, out, "my-app-old",
		"the old Verified bundle should not appear when a newer Promoting bundle exists")
}
