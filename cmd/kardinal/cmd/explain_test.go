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

package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func buildExplainScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}

// TestExplain_ShowsStepsAndGates verifies that the explain output contains
// both PromotionStep and PolicyGate rows with the correct columns.
func TestExplain_ShowsStepsAndGates(t *testing.T) {
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "step-prod",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "nginx-demo", "kardinal.io/environment": "prod"},
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "bundle-1",
			Environment:  "prod",
			StepType:     "pr-review",
		},
		Status: v1alpha1.PromotionStepStatus{
			State:   "WaitingForMerge",
			Message: "PR #7 open",
		},
	}
	now := metav1.Now()
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-weekend-deploys",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "nginx-demo", "kardinal.io/environment": "prod"},
		},
		Spec: v1alpha1.PolicyGateSpec{
			Expression: "!schedule.isWeekend",
			Message:    "weekend blocked",
		},
		Status: v1alpha1.PolicyGateStatus{
			Ready:           false,
			Reason:          "isWeekend == true",
			LastEvaluatedAt: &now,
		},
	}

	s := buildExplainScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(ps, gate).Build()

	var buf bytes.Buffer
	err := explainOnce(&buf, c, "default", "nginx-demo", "")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "prod")
	assert.Contains(t, out, "WaitingForMerge")
	assert.Contains(t, out, "PR #7 open")
	assert.Contains(t, out, "PolicyGate")
	assert.Contains(t, out, "no-weekend-deploys")
	assert.Contains(t, out, "Block")
	// CEL expression must be shown (#117).
	assert.Contains(t, out, "!schedule.isWeekend", "CEL expression must be shown in EXPRESSION column")
	assert.Contains(t, out, "EXPRESSION", "output header must include EXPRESSION column")
}

// TestExplain_FilterByEnv verifies that the envFilter parameter filters output.
func TestExplain_FilterByEnv(t *testing.T) {
	testStep := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "step-test",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "nginx-demo"},
		},
		Spec: v1alpha1.PromotionStepSpec{
			Environment: "test",
			StepType:    "auto",
		},
		Status: v1alpha1.PromotionStepStatus{State: "Verified"},
	}
	prodStep := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "step-prod",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "nginx-demo"},
		},
		Spec: v1alpha1.PromotionStepSpec{
			Environment: "prod",
			StepType:    "pr-review",
		},
		Status: v1alpha1.PromotionStepStatus{State: "WaitingForMerge"},
	}

	s := buildExplainScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(testStep, prodStep).Build()

	var buf bytes.Buffer
	err := explainOnce(&buf, c, "default", "nginx-demo", "prod")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "prod")
	assert.Contains(t, out, "WaitingForMerge")
	assert.NotContains(t, out, "test")
	assert.NotContains(t, out, "Verified")
}

// TestExplain_EmptyOutput verifies graceful output when no steps or gates exist.
func TestExplain_EmptyOutput(t *testing.T) {
	s := buildExplainScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	var buf bytes.Buffer
	err := explainOnce(&buf, c, "default", "nginx-demo", "")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "No steps")
}

// TestExplain_UnevaluatedGateExpression verifies that a PolicyGate not yet evaluated
// shows its CEL expression and "-" as reason.
func TestExplain_UnevaluatedGateExpression(t *testing.T) {
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "staging-soak-30m",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "nginx-demo", "kardinal.io/environment": "prod"},
		},
		Spec: v1alpha1.PolicyGateSpec{
			Expression: "bundle.upstreamSoakMinutes >= 30",
			Message:    "soak time required",
		},
		// No status set = not yet evaluated
	}

	s := buildExplainScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gate).Build()

	var buf bytes.Buffer
	err := explainOnce(&buf, c, "default", "nginx-demo", "")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "staging-soak-30m")
	assert.Contains(t, out, "bundle.upstreamSoakMinutes >= 30", "CEL expression must be shown")
	assert.Contains(t, out, "Pending", "unevaluated gate must show Pending state")
}

// TestExplain_ShowsOnlyActiveBundleRows verifies that when multiple bundles have
// PromotionSteps and PolicyGates, only the active bundle's rows are shown (#267).
func TestExplain_ShowsOnlyActiveBundleRows(t *testing.T) {
	now := metav1.Now()
	old := metav1.NewTime(now.Time.Add(-1 * 3600 * 1e9))

	// Old bundle: Verified in prod.
	oldStep := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "old-prod",
			Namespace:         "default",
			CreationTimestamp: old,
			Labels:            map[string]string{"kardinal.io/pipeline": "my-app", "kardinal.io/environment": "prod"},
		},
		Spec:   v1alpha1.PromotionStepSpec{PipelineName: "my-app", Environment: "prod", BundleName: "old-bundle", StepType: "old-step"},
		Status: v1alpha1.PromotionStepStatus{State: "Verified"},
	}
	oldGate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "old-gate",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline":    "my-app",
				"kardinal.io/environment": "prod",
				"kardinal.io/bundle":      "old-bundle",
			},
		},
		Spec:   v1alpha1.PolicyGateSpec{Expression: "true"},
		Status: v1alpha1.PolicyGateStatus{Ready: true, LastEvaluatedAt: &now},
	}

	// New bundle: Promoting in prod (higher priority).
	newStep := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "new-prod",
			Namespace:         "default",
			CreationTimestamp: now,
			Labels:            map[string]string{"kardinal.io/pipeline": "my-app", "kardinal.io/environment": "prod"},
		},
		Spec:   v1alpha1.PromotionStepSpec{PipelineName: "my-app", Environment: "prod", BundleName: "new-bundle", StepType: "new-step"},
		Status: v1alpha1.PromotionStepStatus{State: "Promoting"},
	}
	newGate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "new-gate",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline":    "my-app",
				"kardinal.io/environment": "prod",
				"kardinal.io/bundle":      "new-bundle",
			},
		},
		Spec:   v1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend"},
		Status: v1alpha1.PolicyGateStatus{Ready: false, LastEvaluatedAt: &now},
	}

	s := buildExplainScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(oldStep, oldGate, newStep, newGate).Build()

	var buf bytes.Buffer
	err := explainOnce(&buf, c, "default", "my-app", "")
	require.NoError(t, err)

	out := buf.String()
	// New bundle's step and gate should appear.
	assert.Contains(t, out, "new-step", "active bundle step must be shown")
	assert.Contains(t, out, "new-gate", "active bundle gate must be shown")
	assert.Contains(t, out, "Promoting", "active bundle step state must be shown")
	// Old bundle's step and gate should NOT appear.
	assert.NotContains(t, out, "old-step", "superseded bundle step must not appear")
	assert.NotContains(t, out, "old-gate", "superseded bundle gate must not appear")
}
