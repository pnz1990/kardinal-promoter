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
					{Name: "dev"},
					{Name: "uat"},
					{Name: "prod"},
				},
				Paused: false,
			},
			Status: v1alpha1.PipelineStatus{
				Phase: "Ready",
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, cmd.FormatPipelineTable(&buf, pipelines))
	out := buf.String()

	assert.Contains(t, out, "PIPELINE")
	assert.Contains(t, out, "PHASE")
	assert.Contains(t, out, "ENVIRONMENTS")
	assert.Contains(t, out, "PAUSED")
	assert.Contains(t, out, "AGE")
	assert.Contains(t, out, "nginx-demo")
	assert.Contains(t, out, "Ready")
	assert.Contains(t, out, "3")
	assert.Contains(t, out, "false")
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
