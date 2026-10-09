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
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
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
	require.NoError(t, cmd.FormatPipelineTableFull(&buf, pipelines, stepBundles(steps), steps, nil, false))
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
	require.NoError(t, cmd.FormatPipelineTableFull(&buf, pipelines, nil, nil, nil, false))
	out := buf.String()

	row := tableRow(t, out, "my-pipeline")
	assert.Equal(t, map[string]string{
		"PIPELINE": "my-pipeline", "BUNDLE": "-", "DEV": "-", "PROD": "-", "AGE": "5m",
	}, row)
}

// stepBundles returns one Bundle per namespace and bundle name in steps,
// created with the bundle's first step: the table describes Bundles that
// exist, so fixtures that only list steps need their Bundles. The phase
// follows the steps: Failed when one failed, Verified when all are, else
// Promoting.
func stepBundles(steps []v1alpha1.PromotionStep) []v1alpha1.Bundle {
	var out []v1alpha1.Bundle
	idx := map[string]int{}
	for _, s := range steps {
		key := s.Namespace + "/" + s.Spec.BundleName
		i, ok := idx[key]
		if !ok {
			i = len(out)
			idx[key] = i
			out = append(out, v1alpha1.Bundle{
				ObjectMeta: metav1.ObjectMeta{Name: s.Spec.BundleName, Namespace: s.Namespace, CreationTimestamp: s.CreationTimestamp},
				Spec:       v1alpha1.BundleSpec{Pipeline: s.Spec.PipelineName, Type: "image"},
				Status:     v1alpha1.BundleStatus{Phase: "Verified"},
			})
		}
		b := &out[i]
		if s.CreationTimestamp.Before(&b.CreationTimestamp) {
			b.CreationTimestamp = s.CreationTimestamp
		}
		switch {
		case s.Status.State == "Failed" || b.Status.Phase == "Failed":
			b.Status.Phase = "Failed"
		case s.Status.State != "Verified":
			b.Status.Phase = "Promoting"
		}
	}
	return out
}

// tableRow returns the cells of the row whose first cell is first, keyed by
// the header. Cells must not contain spaces.
func tableRow(t *testing.T, out, first string) map[string]string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	header := strings.Fields(lines[0])
	for _, l := range lines[1:] {
		cells := strings.Fields(l)
		if len(cells) == 0 || cells[0] != first {
			continue
		}
		require.Len(t, cells, len(header), "row %q vs header %q", l, lines[0])
		row := map[string]string{}
		for i, h := range header {
			row[h] = cells[i]
		}
		return row
	}
	t.Fatalf("no row %q in:\n%s", first, out)
	return nil
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
	require.NoError(t, cmd.FormatPipelineTableFull(&buf, pipelines, stepBundles(steps), steps, nil, false))
	out := buf.String()

	// Union columns: TEST, PROD from app-a, STAGING from app-b.
	assert.Contains(t, out, "TEST")
	assert.Contains(t, out, "STAGING")
	assert.Contains(t, out, "PROD")

	// app-a has no staging environment: its STAGING cell is "-".
	a := tableRow(t, out, "app-a")
	assert.Equal(t, "Verified", a["TEST"])
	assert.Equal(t, "-", a["STAGING"])
	assert.Equal(t, "-", a["PROD"])
	b := tableRow(t, out, "app-b")
	assert.Equal(t, "-", b["TEST"])
	assert.Equal(t, "Verified", b["STAGING"])
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
	require.NoError(t, cmd.FormatPipelineTableFull(&buf, pipelines, nil, nil, nil, false))
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
	require.NoError(t, cmd.FormatPipelineTableFull(&buf, pipelines, nil, nil, nil, false))
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
	require.NoError(t, cmd.FormatPipelineTableFull(&buf, pipelines, stepBundles(steps), steps, nil, false))
	out := buf.String()

	// The table MUST show the new (Promoting) bundle, not the old (Verified) bundle.
	assert.Contains(t, out, "my-app-new",
		"table must show the active Promoting bundle, not the old Verified one")
	assert.NotContains(t, out, "my-app-old",
		"the old Verified bundle should not appear when a newer Promoting bundle exists")
}

// E2E-R17: BUNDLE is the pipeline's current Bundle (lifecycle.CurrentBundle,
// the UI's activeBundleName) and every environment cell describes that
// Bundle: its step state there, Waiting while it is in flight and still to
// come, a dash when it is terminal or does not promote the environment. An
// older Bundle's state is never shown next to it.
func TestFormatPipelineTable_CurrentBundleState(t *testing.T) {
	now := time.Now()
	old, recent, newest := now.Add(-2*time.Hour), now.Add(-10*time.Minute), now.Add(-time.Minute)
	pipelines := []v1alpha1.Pipeline{{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default", CreationTimestamp: metav1.NewTime(old)},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{
			{Name: "dev"}, {Name: "stage"}, {Name: "prod"},
		}},
	}}
	bundle := func(name, phase string, created time.Time, skip ...string) v1alpha1.Bundle {
		b := v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: metav1.NewTime(created)},
			Spec:       v1alpha1.BundleSpec{Pipeline: "app", Type: "image"},
		}
		if len(skip) > 0 {
			b.Spec.Intent = &v1alpha1.BundleIntent{SkipEnvironments: skip}
		}
		b.Status.Phase = phase
		return b
	}
	step := func(b, env, state string, created time.Time) v1alpha1.PromotionStep {
		return v1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Name: b + "-" + env, Namespace: "default", CreationTimestamp: metav1.NewTime(created)},
			Spec:       v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: b, Environment: env},
			Status:     v1alpha1.PromotionStepStatus{State: state},
		}
	}
	b1Everywhere := func(more ...v1alpha1.PromotionStep) []v1alpha1.PromotionStep {
		return append([]v1alpha1.PromotionStep{
			step("app-b1", "dev", "Verified", old), step("app-b1", "stage", "Verified", old),
			step("app-b1", "prod", "Verified", old),
		}, more...)
	}

	tests := []struct {
		name    string
		bundles []v1alpha1.Bundle
		steps   []v1alpha1.PromotionStep
		want    map[string]string
	}{
		{
			// scenA.log: B2 failed at the first env; the table said B1 and
			// Verified there while status said Failed.
			name:    "newer bundle failed at the first env",
			bundles: []v1alpha1.Bundle{bundle("app-b1", "Verified", old), bundle("app-b2", "Failed", recent)},
			steps:   b1Everywhere(step("app-b2", "dev", "Failed", recent)),
			want:    map[string]string{"BUNDLE": "app-b2", "DEV": "Failed", "STAGE": "-", "PROD": "-"},
		},
		{
			// j1-happy.log: B2 held at prod by a not-ready gate; the older
			// bundle's Verified was shown next to BUNDLE B2.
			name:    "bundle held at prod, older bundle Verified there",
			bundles: []v1alpha1.Bundle{bundle("app-b1", "Verified", old), bundle("app-b2", "Promoting", recent)},
			steps: b1Everywhere(
				step("app-b2", "dev", "Verified", recent), step("app-b2", "stage", "Verified", recent)),
			want: map[string]string{"BUNDLE": "app-b2", "DEV": "Verified", "STAGE": "Verified", "PROD": "Waiting"},
		},
		{
			name:    "bundle health checking in the first env, ungated envs after it",
			bundles: []v1alpha1.Bundle{bundle("app-b1", "Verified", old), bundle("app-b2", "Promoting", recent)},
			steps:   b1Everywhere(step("app-b2", "dev", "HealthChecking", recent)),
			want:    map[string]string{"BUNDLE": "app-b2", "DEV": "HealthChecking", "STAGE": "Waiting", "PROD": "Waiting"},
		},
		{
			// The per-environment rule would name app-b2 (it has steps) while
			// the UI's activeBundleName is app-b3, the newest bundle.
			name: "newest bundle with no step or gate yet",
			bundles: []v1alpha1.Bundle{bundle("app-b1", "Verified", old), bundle("app-b2", "Promoting", recent),
				bundle("app-b3", "Available", newest)},
			steps: b1Everywhere(step("app-b2", "dev", "Promoting", recent)),
			want:  map[string]string{"BUNDLE": "app-b3", "DEV": "Waiting", "STAGE": "Waiting", "PROD": "Waiting"},
		},
		{
			name:    "held bundle over a Superseded bundle",
			bundles: []v1alpha1.Bundle{bundle("app-b1", "Superseded", old), bundle("app-b2", "Promoting", recent)},
			steps: b1Everywhere(
				step("app-b2", "dev", "Verified", recent), step("app-b2", "stage", "Verified", recent)),
			want: map[string]string{"BUNDLE": "app-b2", "DEV": "Verified", "STAGE": "Verified", "PROD": "Waiting"},
		},
		{
			name:    "unstarted step of the current bundle is Pending",
			bundles: []v1alpha1.Bundle{bundle("app-b1", "Verified", old), bundle("app-b2", "Promoting", recent)},
			steps:   b1Everywhere(step("app-b2", "dev", "", recent)),
			want:    map[string]string{"BUNDLE": "app-b2", "DEV": "Pending", "STAGE": "Waiting", "PROD": "Waiting"},
		},
		{
			name:    "skipped env shows a dash",
			bundles: []v1alpha1.Bundle{bundle("app-b1", "Verified", old), bundle("app-b2", "Promoting", recent, "stage")},
			steps:   b1Everywhere(step("app-b2", "dev", "Verified", recent)),
			want:    map[string]string{"BUNDLE": "app-b2", "DEV": "Verified", "STAGE": "-", "PROD": "Waiting"},
		},
		{
			name:    "steps of a deleted bundle are ignored",
			bundles: []v1alpha1.Bundle{bundle("app-b2", "Promoting", recent)},
			steps:   b1Everywhere(step("app-b2", "dev", "Promoting", recent)),
			want:    map[string]string{"BUNDLE": "app-b2", "DEV": "Promoting", "STAGE": "Waiting", "PROD": "Waiting"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			require.NoError(t, cmd.FormatPipelineTableFull(&buf, pipelines, tt.bundles, tt.steps, nil, false))
			row := tableRow(t, buf.String(), "app")
			for col, want := range tt.want {
				assert.Equal(t, want, row[col], "column %s in:\n%s", col, buf.String())
			}
			if b := lifecycle.CurrentBundle(tt.bundles); assert.NotNil(t, b) {
				assert.Equal(t, b.Name, row["BUNDLE"], "BUNDLE is the UI's activeBundleName")
			}
		})
	}
}

// TestFormatStepsTable_OnePerEnvWhenMultipleBundles verifies that when multiple
// bundles have steps for the same environment, only the most active step is shown.
func TestFormatStepsTable_OnePerEnvWhenMultipleBundles(t *testing.T) {
	now := time.Now()
	old := metav1.NewTime(now.Add(-1 * time.Hour))
	newTime := metav1.NewTime(now.Add(-30 * time.Second))

	steps := []v1alpha1.PromotionStep{
		// Old bundle — Verified in test
		{
			ObjectMeta: metav1.ObjectMeta{Name: "old-test", CreationTimestamp: old},
			Spec:       v1alpha1.PromotionStepSpec{Environment: "test", StepType: "kustomize-set-image", BundleName: "old-bundle"},
			Status:     v1alpha1.PromotionStepStatus{State: "Verified"},
		},
		// New bundle — Promoting in test (higher priority)
		{
			ObjectMeta: metav1.ObjectMeta{Name: "new-test", CreationTimestamp: newTime},
			Spec:       v1alpha1.PromotionStepSpec{Environment: "test", StepType: "kustomize-set-image", BundleName: "new-bundle"},
			Status:     v1alpha1.PromotionStepStatus{State: "Promoting"},
		},
		// Old bundle — Verified in prod
		{
			ObjectMeta: metav1.ObjectMeta{Name: "old-prod", CreationTimestamp: old},
			Spec:       v1alpha1.PromotionStepSpec{Environment: "prod", StepType: "kustomize-set-image", BundleName: "old-bundle"},
			Status:     v1alpha1.PromotionStepStatus{State: "Verified"},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, cmd.FormatStepsTable(&buf, steps))
	out := buf.String()

	lines := strings.Split(strings.TrimSpace(out), "\n")
	// Header + 2 env rows (test, prod) = 3 lines. NOT 4 (no duplicate test row).
	assert.Len(t, lines, 3, "should show one row per environment, not one per bundle step")

	// The test env row must show Promoting (new bundle), not Verified (old bundle).
	for _, line := range lines[1:] {
		if strings.HasPrefix(strings.TrimSpace(line), "test") {
			assert.Contains(t, line, "Promoting",
				"test env row must show the active Promoting step, not old Verified")
		}
	}
}

// TestFormatPipelineTableFull_ShowNamespace verifies that --all-namespaces
// adds a NAMESPACE column to the output.
func TestFormatPipelineTableFull_ShowNamespace(t *testing.T) {
	now := time.Now()
	pipelines := []v1alpha1.Pipeline{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "app-a",
				Namespace:         "team-alpha",
				CreationTimestamp: metav1.NewTime(now.Add(-5 * time.Minute)),
			},
			Spec: v1alpha1.PipelineSpec{
				Environments: []v1alpha1.EnvironmentSpec{
					{Name: "prod"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "app-b",
				Namespace:         "team-beta",
				CreationTimestamp: metav1.NewTime(now.Add(-3 * time.Minute)),
			},
			Spec: v1alpha1.PipelineSpec{
				Environments: []v1alpha1.EnvironmentSpec{
					{Name: "prod"},
				},
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, cmd.FormatPipelineTableFull(&buf, pipelines, nil, nil, nil, true))
	out := buf.String()

	assert.Contains(t, out, "NAMESPACE", "header must include NAMESPACE column when showNamespace=true")
	assert.Contains(t, out, "team-alpha", "row must include pipeline's namespace")
	assert.Contains(t, out, "team-beta", "row must include pipeline's namespace")
}

// TestFormatPipelineTableFull_NoNamespaceByDefault verifies that the
// pipeline table does NOT include a NAMESPACE column.
func TestFormatPipelineTableFull_NoNamespaceByDefault(t *testing.T) {
	now := time.Now()
	pipelines := []v1alpha1.Pipeline{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "app-a",
				Namespace:         "my-ns",
				CreationTimestamp: metav1.NewTime(now.Add(-5 * time.Minute)),
			},
			Spec: v1alpha1.PipelineSpec{
				Environments: []v1alpha1.EnvironmentSpec{{Name: "prod"}},
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, cmd.FormatPipelineTableFull(&buf, pipelines, nil, nil, nil, false))
	out := buf.String()

	assert.NotContains(t, out, "NAMESPACE", "default table must not include NAMESPACE column")
	assert.NotContains(t, out, "my-ns", "namespace must not appear in default table")
}

// TestFormatBundleErrors_FailedBundle_ShowsError verifies that a Failed bundle
// results in an ERROR: line containing the pipeline name and condition message.
func TestFormatBundleErrors_FailedBundle_ShowsError(t *testing.T) {
	bundles := []v1alpha1.Bundle{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1"},
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
		},
	}

	var buf bytes.Buffer
	require.NoError(t, cmd.FormatBundleErrors(&buf, bundles, false))
	out := buf.String()

	assert.Contains(t, out, "ERROR:", "output must contain ERROR: prefix")
	assert.Contains(t, out, "my-app", "output must contain pipeline name")
	assert.Contains(t, out, `dependsOn unknown environment "staging"`, "output must contain the condition message")
}

// TestFormatBundleErrors_NoFailedBundles_NoOutput verifies that when no bundles
// are in Failed phase, nothing is written.
func TestFormatBundleErrors_NoFailedBundles_NoOutput(t *testing.T) {
	bundles := []v1alpha1.Bundle{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1"},
			Spec:       v1alpha1.BundleSpec{Pipeline: "my-app", Type: "image"},
			Status:     v1alpha1.BundleStatus{Phase: "Verified"},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, cmd.FormatBundleErrors(&buf, bundles, false))
	assert.Empty(t, buf.String(), "no output expected when no bundles are Failed")
}

// TestFormatBundleErrors_EmptyList_NoOutput verifies that an empty bundle list
// produces no output.
func TestFormatBundleErrors_EmptyList_NoOutput(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, cmd.FormatBundleErrors(&buf, nil, false))
	assert.Empty(t, buf.String(), "no output expected for empty bundle list")
}

// TestFormatBundleErrors_CircularDependency verifies that a CircularDependency
// condition is surfaced with its specific message.
func TestFormatBundleErrors_CircularDependency(t *testing.T) {
	bundles := []v1alpha1.Bundle{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "loop-app-v1"},
			Spec:       v1alpha1.BundleSpec{Pipeline: "loop-app", Type: "image"},
			Status: v1alpha1.BundleStatus{
				Phase: "Failed",
				Conditions: []metav1.Condition{
					{
						Type:    "InvalidSpec",
						Status:  metav1.ConditionTrue,
						Reason:  "CircularDependency",
						Message: "pipeline has circular dependsOn: prod → staging → prod",
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, cmd.FormatBundleErrors(&buf, bundles, false))
	out := buf.String()

	assert.Contains(t, out, "ERROR:", "output must contain ERROR: prefix")
	assert.Contains(t, out, "loop-app", "output must contain pipeline name")
	assert.Contains(t, out, "circular dependsOn", "output must contain CircularDependency message")
}

// TestFormatBundleErrors_DuplicatePipeline_Deduplicates verifies that multiple
// Failed bundles for the same pipeline produce only one error line.
func TestFormatBundleErrors_DuplicatePipeline_Deduplicates(t *testing.T) {
	bundles := []v1alpha1.Bundle{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1"},
			Spec:       v1alpha1.BundleSpec{Pipeline: "my-app", Type: "image"},
			Status: v1alpha1.BundleStatus{
				Phase: "Failed",
				Conditions: []metav1.Condition{
					{Type: "Failed", Status: metav1.ConditionTrue, Reason: "TranslationError", Message: "err v1"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "my-app-v2"},
			Spec:       v1alpha1.BundleSpec{Pipeline: "my-app", Type: "image"},
			Status: v1alpha1.BundleStatus{
				Phase: "Failed",
				Conditions: []metav1.Condition{
					{Type: "Failed", Status: metav1.ConditionTrue, Reason: "TranslationError", Message: "err v2"},
				},
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, cmd.FormatBundleErrors(&buf, bundles, false))
	out := buf.String()

	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Len(t, lines, 1, "only one error line per pipeline (deduplication)")
	assert.Contains(t, out, "my-app", "pipeline name must appear in deduplicated output")
}

// C09a-cli-16: the most recent Failed bundle per namespace/pipeline wins, and
// -A shows the namespace.
func TestFormatBundleErrors_MostRecentPerNamespacedPipeline(t *testing.T) {
	now := time.Now()
	failed := func(ns, name string, age time.Duration, reason, msg string) v1alpha1.Bundle {
		return v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(now.Add(-age))},
			Spec:       v1alpha1.BundleSpec{Pipeline: "web"},
			Status: v1alpha1.BundleStatus{Phase: "Failed", Conditions: []metav1.Condition{
				{Type: "Failed", Status: metav1.ConditionTrue, Reason: reason, Message: msg},
			}},
		}
	}
	bundles := []v1alpha1.Bundle{
		failed("team-a", "web-aaaaa", 2*time.Hour, "TranslationError", "OLD error (already fixed)"),
		failed("team-a", "web-zzzzz", time.Minute, "TranslationError", "NEW error"),
		failed("team-b", "web-bbbbb", time.Hour, "TranslationError", "team-b error"),
	}
	bundles[2].Status.Conditions = append(bundles[2].Status.Conditions, metav1.Condition{
		Type: "Failed", Status: metav1.ConditionTrue, Reason: "CircularDependency", Message: "cycle uat -> prod -> uat",
	})

	var buf bytes.Buffer
	require.NoError(t, cmd.FormatBundleErrors(&buf, bundles, true))
	assert.Equal(t, "ERROR: pipeline team-a/web: NEW error\n"+
		"ERROR: pipeline team-b/web: cycle uat -> prod -> uat\n", buf.String())

	buf.Reset()
	require.NoError(t, cmd.FormatBundleErrors(&buf, bundles[:2], false))
	assert.Equal(t, "ERROR: pipeline web: NEW error\n", buf.String())
}

// Only cause conditions (InvalidSpec, Failed) give the message; a True
// GraphSynced condition listed first does not hide the failure.
func TestFormatBundleErrors_OnlyCauseConditions(t *testing.T) {
	cond := func(typ, reason, msg string) metav1.Condition {
		return metav1.Condition{Type: typ, Status: metav1.ConditionTrue, Reason: reason, Message: msg}
	}
	cases := []struct {
		name  string
		conds []metav1.Condition
		want  string
	}{
		{"step failed", []metav1.Condition{
			cond("GraphSynced", "Synced", "graph is current"),
			cond("Failed", "StepFailed", "prod: health check failed"),
		}, "prod: health check failed"},
		{"invalid spec before failed", []metav1.Condition{
			cond("GraphSynced", "Synced", "graph is current"),
			cond("Failed", "GraphRejected", "graph rejected"),
			cond("InvalidSpec", "PipelineNotFound", `pipeline "web" not found`),
		}, `pipeline "web" not found`},
		{"no cause condition", []metav1.Condition{
			cond("GraphSynced", "Synced", "graph is current"),
		}, "promotion failed — run `kubectl describe bundle web-1` for details"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := v1alpha1.Bundle{
				ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "default"},
				Spec:       v1alpha1.BundleSpec{Pipeline: "web"},
				Status:     v1alpha1.BundleStatus{Phase: "Failed", Conditions: tc.conds},
			}
			var buf bytes.Buffer
			require.NoError(t, cmd.FormatBundleErrors(&buf, []v1alpha1.Bundle{b}, false))
			assert.Equal(t, "ERROR: pipeline web: "+tc.want+"\n", buf.String())
		})
	}
}

// C09a-cli-07: with -A, same-named pipelines in different namespaces keep their
// own bundle, environment state and subscription count.
func TestFormatPipelineTableFull_AllNamespacesNoCrossTalk(t *testing.T) {
	now := time.Now()
	pipe := func(ns string) v1alpha1.Pipeline {
		return v1alpha1.Pipeline{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns, CreationTimestamp: metav1.NewTime(now)},
			Spec:       v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "prod"}}},
		}
	}
	step := func(ns, bundle, state string) v1alpha1.PromotionStep {
		return v1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Name: bundle + "-prod", Namespace: ns, CreationTimestamp: metav1.NewTime(now)},
			Spec:       v1alpha1.PromotionStepSpec{PipelineName: "web", BundleName: bundle, Environment: "prod"},
			Status:     v1alpha1.PromotionStepStatus{State: state},
		}
	}
	sub := v1alpha1.Subscription{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "team-a"},
		Spec:       v1alpha1.SubscriptionSpec{Pipeline: "web"},
		Status:     v1alpha1.SubscriptionStatus{Phase: "Watching"},
	}

	steps := []v1alpha1.PromotionStep{step("team-a", "web-a1", "Verified"), step("team-b", "web-b1", "Failed")}
	var buf bytes.Buffer
	require.NoError(t, cmd.FormatPipelineTableFull(&buf,
		[]v1alpha1.Pipeline{pipe("team-a"), pipe("team-b")},
		stepBundles(steps), steps,
		[]v1alpha1.Subscription{sub}, true))

	rows := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n")[1:] {
		f := strings.Fields(line)
		rows[f[0]] = f[1:5]
	}
	assert.Equal(t, []string{"web", "web-a1", "Verified", "1"}, rows["team-a"])
	assert.Equal(t, []string{"web", "web-b1", "Failed", "0"}, rows["team-b"])
}

// C09a-cli-12: every PromotionStep state has a priority; in-flight states
// (including RollingBack) beat Pending, which beats Verified. A newer
// AbortedByAlarm (post-merge) step beats an older Verified one; Failed does not.
func TestStepStatePriority_AllStates(t *testing.T) {
	now := time.Now()
	cases := []struct {
		newer string
		want  string
	}{
		{"RollingBack", "RollingBack"},
		{"AbortedByAlarm", "AbortedByAlarm"},
		{"Failed", "Verified"},
		{"Promoting", "Promoting"},
		{"WaitingForMerge", "WaitingForMerge"},
		{"HealthChecking", "HealthChecking"},
		{"Pending", "Pending"},
		{"Verified", "Verified"},
	}
	for _, tc := range cases {
		t.Run(tc.newer, func(t *testing.T) {
			steps := []v1alpha1.PromotionStep{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "old", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))},
					Spec:       v1alpha1.PromotionStepSpec{Environment: "prod", StepType: "old-step"},
					Status:     v1alpha1.PromotionStepStatus{State: "Verified"},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "new", CreationTimestamp: metav1.NewTime(now)},
					Spec:       v1alpha1.PromotionStepSpec{Environment: "prod", StepType: "new-step"},
					Status:     v1alpha1.PromotionStepStatus{State: tc.newer},
				},
			}
			var buf bytes.Buffer
			require.NoError(t, cmd.FormatStepsTable(&buf, steps))
			assert.Contains(t, buf.String(), " "+tc.want+" ", buf.String())
		})
	}
}

// Bug 13 of the delivery spike: a step that applied onHealthFailure: rollback
// stays RollingBack for good. Once the rollback Bundle's newer step is
// Verified, that step is what the environment runs and what the views show.
func TestStepStatePriority_RollingBackIsNotInFlight(t *testing.T) {
	now := time.Now()
	step := func(name, state string, age time.Duration) v1alpha1.PromotionStep {
		return v1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(now.Add(-age))},
			Spec:       v1alpha1.PromotionStepSpec{Environment: "prod", StepType: name + "-step"},
			Status:     v1alpha1.PromotionStepStatus{State: state},
		}
	}
	for _, tc := range []struct{ rollback, want string }{
		{"Verified", "rollback-step"},
		{"Promoting", "rollback-step"},
	} {
		t.Run(tc.rollback, func(t *testing.T) {
			var buf bytes.Buffer
			require.NoError(t, cmd.FormatStepsTable(&buf, []v1alpha1.PromotionStep{
				step("failed", "RollingBack", time.Hour), step("rollback", tc.rollback, time.Minute)}))
			assert.Contains(t, buf.String(), tc.want, buf.String())
			assert.NotContains(t, buf.String(), "RollingBack", buf.String())
		})
	}
}

// E2E-R16: a Failed bundle's error is shown only while no newer bundle of the
// same pipeline that is not Superseded exists: a newer bundle that is
// promoting or Verified makes the failure history, not the pipeline's state.
func TestFormatBundleErrors_NewerBundleHidesOlderFailure(t *testing.T) {
	now := time.Now()
	bundle := func(ns, pipeline, name, phase string, age time.Duration) v1alpha1.Bundle {
		b := v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(now.Add(-age))},
			Spec:       v1alpha1.BundleSpec{Pipeline: pipeline, Type: "image"},
			Status:     v1alpha1.BundleStatus{Phase: phase},
		}
		if phase == "Failed" {
			b.Status.Conditions = []metav1.Condition{{Type: "Failed", Status: metav1.ConditionTrue, Reason: "StepFailed",
				Message: "environment prod: PR #28 was closed without merging"}}
		}
		return b
	}
	failed := bundle("default", "kardinal-test-app", "kardinal-test-app-rollback-bkgwk", "Failed", 13*time.Minute)
	lifecycle.StampCreatedAt(&failed, failed.CreationTimestamp.Time)
	// sameSecond is created in the same second as failed; the
	// kardinal.io/created-at annotation orders the two (lifecycle.CompareCreation).
	sameSecond := func(phase string, after time.Duration) v1alpha1.Bundle {
		b := bundle("default", "kardinal-test-app", "kardinal-test-app-9smn4", phase, 0)
		b.CreationTimestamp = failed.CreationTimestamp
		lifecycle.StampCreatedAt(&b, failed.CreationTimestamp.Add(after))
		return b
	}
	const wantErr = "ERROR: pipeline kardinal-test-app: environment prod: PR #28 was closed without merging\n"
	cases := []struct {
		name  string
		other v1alpha1.Bundle
		want  string
	}{
		{"newer verified bundle", bundle("default", "kardinal-test-app", "kardinal-test-app-9smn4", "Verified", 3*time.Minute), ""},
		{"newer promoting bundle", bundle("default", "kardinal-test-app", "kardinal-test-app-9smn4", "Promoting", 3*time.Minute), ""},
		{"newer bundle not started", bundle("default", "kardinal-test-app", "kardinal-test-app-9smn4", "", 3*time.Minute), ""},
		{"newer superseded bundle", bundle("default", "kardinal-test-app", "kardinal-test-app-9smn4", "Superseded", 3*time.Minute), wantErr},
		{"older verified bundle", bundle("default", "kardinal-test-app", "kardinal-test-app-9tptr", "Verified", time.Hour), wantErr},
		{"newer bundle of another pipeline", bundle("default", "other", "other-9smn4", "Verified", 3*time.Minute), wantErr},
		{"newer bundle in another namespace", bundle("team-b", "kardinal-test-app", "kardinal-test-app-9smn4", "Verified", 3*time.Minute), wantErr},
		{"newer bundle in the same second", sameSecond("Verified", 400*time.Millisecond), ""},
		{"older bundle in the same second", sameSecond("Verified", -400*time.Millisecond), wantErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			require.NoError(t, cmd.FormatBundleErrors(&buf, []v1alpha1.Bundle{failed, tc.other}, false))
			assert.Equal(t, tc.want, buf.String())
		})
	}
}

// TestFormatPipelineTable_Fleet (D1): a fleet environment is one column,
// counting its targets' states; once every target is Verified it says so.
//
// Covers FLEET-05.
func TestFormatPipelineTable_Fleet(t *testing.T) {
	p := v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "edge"}}
	p.Spec.Environments = []v1alpha1.EnvironmentSpec{{Name: "test"},
		{Name: "prod", Fleet: &v1alpha1.FleetSpec{MaxConcurrent: 2, Targets: []v1alpha1.FleetTarget{{Name: "a"}, {Name: "b"}, {Name: "c"}}}}}
	step := func(env, state string) v1alpha1.PromotionStep {
		return v1alpha1.PromotionStep{Spec: v1alpha1.PromotionStepSpec{PipelineName: "edge", Environment: env, BundleName: "edge-1"},
			Status: v1alpha1.PromotionStepStatus{State: state}}
	}
	render := func(steps ...v1alpha1.PromotionStep) string {
		var buf bytes.Buffer
		require.NoError(t, cmd.FormatPipelineTableFull(&buf, []v1alpha1.Pipeline{p}, stepBundles(steps), steps, nil, false))
		return buf.String()
	}
	out := render(step("test", "Verified"), step("prod-a", "Verified"), step("prod-b", "Failed"), step("prod-c", "Promoting"))
	assert.Contains(t, out, "PROD")
	assert.NotContains(t, out, "PROD-A", "targets are not columns of their own")
	assert.Contains(t, out, "1/3 Verified, 1 Failed")
	out = render(step("test", "Verified"), step("prod-a", "Verified"), step("prod-b", "Verified"), step("prod-c", "Verified"))
	assert.NotContains(t, out, "/3")
}
