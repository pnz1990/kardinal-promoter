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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/cmd/kardinal/cmd"
)

func buildSchemeForStatus() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)
	return s
}

// TestStatusPipelineWriter_NoSteps verifies that the output says "No active promotions"
// when there are no PromotionSteps.
func TestStatusPipelineWriter_NoSteps(t *testing.T) {
	scheme := buildSchemeForStatus()

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pipeline", Namespace: "default"},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pipeline).
		Build()

	var buf bytes.Buffer
	err := cmd.StatusPipelineWriterForTest(&buf, fakeClient, "default", "my-pipeline")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No active promotions.")
}

// TestStatusPipelineWriter_NotFound verifies that a missing pipeline returns an error.
func TestStatusPipelineWriter_NotFound(t *testing.T) {
	scheme := buildSchemeForStatus()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	var buf bytes.Buffer
	err := cmd.StatusPipelineWriterForTest(&buf, fakeClient, "default", "missing-pipeline")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestStatusPipelineWriter_ActiveStep verifies that an in-flight PromotionStep
// is shown with a ▶ marker and the active step name.
func TestStatusPipelineWriter_ActiveStep(t *testing.T) {
	scheme := buildSchemeForStatus()

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
	}

	now := time.Now()
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nginx-demo-prod-bundle-abc",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: now.Add(-5 * time.Minute)},
			Labels: map[string]string{
				"kardinal.io/pipeline": "nginx-demo",
			},
		},
		Spec: v1alpha1.PromotionStepSpec{
			Environment: "prod",
			BundleName:  "bundle-abc",
		},
		Status: v1alpha1.PromotionStepStatus{
			State:  "WaitingForMerge",
			PRURL:  "https://github.com/org/repo/pull/42",
			Message: "waiting for PR merge",
			Steps: []v1alpha1.StepStatus{
				{Name: "git-clone", State: "Completed"},
				{Name: "open-pr", State: "Running"},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pipeline, step).
		Build()

	var buf bytes.Buffer
	err := cmd.StatusPipelineWriterForTest(&buf, fakeClient, "default", "nginx-demo")
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Pipeline: nginx-demo")
	assert.Contains(t, output, "Active bundle(s): bundle-abc")
	assert.Contains(t, output, "WaitingForMerge")
	assert.Contains(t, output, "▶")
	assert.Contains(t, output, "open-pr") // active step name
	assert.Contains(t, output, "pull/42") // PR URL (truncated)
}

// TestStatusPipelineWriter_BlockingGate verifies that a blocking PolicyGate is shown.
func TestStatusPipelineWriter_BlockingGate(t *testing.T) {
	scheme := buildSchemeForStatus()

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
	}

	now := time.Now()
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nginx-demo-prod-bundle-abc",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: now.Add(-2 * time.Hour)},
			Labels: map[string]string{
				"kardinal.io/pipeline": "nginx-demo",
			},
		},
		Spec: v1alpha1.PromotionStepSpec{
			Environment: "prod",
			BundleName:  "bundle-abc",
		},
		Status: v1alpha1.PromotionStepStatus{
			State: "Promoting",
		},
	}

	checked := metav1.Time{Time: now.Add(-30 * time.Second)}
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-weekend-deploys",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline":    "nginx-demo",
				"kardinal.io/environment": "prod",
			},
		},
		Spec: v1alpha1.PolicyGateSpec{
			Expression: "!schedule.isWeekend",
		},
		Status: v1alpha1.PolicyGateStatus{
			Ready:           false,
			Reason:          "BLOCKED: schedule.isWeekend is true",
			LastEvaluatedAt: &checked,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pipeline, step, gate).
		Build()

	var buf bytes.Buffer
	err := cmd.StatusPipelineWriterForTest(&buf, fakeClient, "default", "nginx-demo")
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Blocking Policy Gates")
	assert.Contains(t, output, "no-weekend-deploys")
	assert.Contains(t, output, "!schedule.isWeekend")
	assert.Contains(t, output, "prod")
}

// TestStatusPipelineWriter_TerminalSteps verifies the "all steps terminal" hint.
func TestStatusPipelineWriter_TerminalSteps(t *testing.T) {
	scheme := buildSchemeForStatus()

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
	}

	now := time.Now()
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nginx-demo-prod-bundle-abc",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: now.Add(-1 * time.Hour)},
			Labels: map[string]string{
				"kardinal.io/pipeline": "nginx-demo",
			},
		},
		Spec: v1alpha1.PromotionStepSpec{
			Environment: "prod",
			BundleName:  "bundle-abc",
		},
		Status: v1alpha1.PromotionStepStatus{
			State: "Verified",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pipeline, step).
		Build()

	var buf bytes.Buffer
	err := cmd.StatusPipelineWriterForTest(&buf, fakeClient, "default", "nginx-demo")
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Verified")
	assert.Contains(t, output, "terminal state")
}

// Compile-time check: StatusPipelineWriterForTest must accept the right signature.
var _ = func() {
	var w *bytes.Buffer
	var c sigs_client.Client
	_ = cmd.StatusPipelineWriterForTest(w, c, "", "")
}
