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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func cliTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}

// TestCreateBundle_CreatesBundle verifies that createBundleFn creates a Bundle CRD.
func TestCreateBundle_CreatesBundle(t *testing.T) {
	s := cliTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	var buf bytes.Buffer
	err := createBundleFn(&buf, c, "default", "nginx-demo", []string{"nginx:1.25"}, "image")
	require.NoError(t, err)

	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundles))
	require.Len(t, bundles.Items, 1)
	assert.Equal(t, "nginx-demo", bundles.Items[0].Spec.Pipeline)
	assert.Equal(t, "image", bundles.Items[0].Spec.Type)
	assert.Equal(t, "nginx", bundles.Items[0].Spec.Images[0].Repository)
	assert.Equal(t, "1.25", bundles.Items[0].Spec.Images[0].Tag)

	assert.Contains(t, buf.String(), "Bundle")
	assert.Contains(t, buf.String(), "nginx-demo")
}

// TestRollback_CreatesBundleWithRollbackOf verifies rollback bundle creation.
func TestRollback_CreatesBundleWithRollbackOf(t *testing.T) {
	s := cliTestScheme(t)
	verifiedBundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     v1alpha1.BundleStatus{Phase: "Verified"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(verifiedBundle).WithStatusSubresource(verifiedBundle).Build()

	var buf bytes.Buffer
	err := rollbackFn(&buf, c, "default", "nginx-demo", "prod", "", false)
	require.NoError(t, err)

	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundles))
	require.Len(t, bundles.Items, 2, "should have original + rollback bundle")

	var rb *v1alpha1.Bundle
	for i := range bundles.Items {
		if bundles.Items[i].Labels["kardinal.io/rollback"] == "true" {
			rb = &bundles.Items[i]
		}
	}
	require.NotNil(t, rb, "rollback bundle not created")
	assert.Equal(t, "nginx-demo-v1", rb.Spec.Provenance.RollbackOf)
	assert.Equal(t, "prod", rb.Spec.Intent.TargetEnvironment)
}

// TestPause_PatchesPipelinePaused verifies that pauseFn patches Pipeline.spec.paused=true
// and creates a freeze PolicyGate (Graph-observable pause enforcement, PS-2 / BU-2 fix).
func TestPause_PatchesPipelinePaused(t *testing.T) {
	s := cliTestScheme(t)
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git:          v1alpha1.PipelineGit{URL: "https://github.com/test/repo"},
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pipeline).Build()

	var buf bytes.Buffer
	err := pauseFn(&buf, c, "default", "nginx-demo")
	require.NoError(t, err)

	var updated v1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo", Namespace: "default"}, &updated))
	assert.True(t, updated.Spec.Paused)
	assert.Contains(t, buf.String(), "paused")

	// Freeze gate must have been created.
	var gate v1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "freeze-nginx-demo", Namespace: "default"}, &gate))
	assert.Equal(t, "false", gate.Spec.Expression, "freeze gate must always evaluate to false")
	assert.Equal(t, "true", gate.Labels["kardinal.io/freeze"])
}

// TestPause_Idempotent verifies that pauseFn is safe to call multiple times
// (AlreadyExists on the freeze gate is silently swallowed).
func TestPause_Idempotent(t *testing.T) {
	s := cliTestScheme(t)
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git:          v1alpha1.PipelineGit{URL: "https://github.com/test/repo"},
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pipeline).Build()

	var buf bytes.Buffer
	require.NoError(t, pauseFn(&buf, c, "default", "nginx-demo"))
	// Second call must succeed without error (gate already exists).
	require.NoError(t, pauseFn(&buf, c, "default", "nginx-demo"), "second pauseFn must be idempotent")
}

// TestResume_UnpausesPipeline verifies that resumeFn patches Pipeline.spec.paused=false
// and deletes the freeze PolicyGate (PS-2 / BU-2 fix).
func TestResume_UnpausesPipeline(t *testing.T) {
	s := cliTestScheme(t)
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git:          v1alpha1.PipelineGit{URL: "https://github.com/test/repo"},
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}},
			Paused:       true,
		},
	}
	freezeGate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "freeze-nginx-demo", Namespace: "default"},
		Spec:       v1alpha1.PolicyGateSpec{Expression: "false"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pipeline, freezeGate).Build()

	var buf bytes.Buffer
	err := resumeFn(&buf, c, "default", "nginx-demo")
	require.NoError(t, err)

	var updated v1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo", Namespace: "default"}, &updated))
	assert.False(t, updated.Spec.Paused)
	assert.Contains(t, buf.String(), "resumed")

	// Freeze gate must have been deleted.
	var gate v1alpha1.PolicyGate
	err = c.Get(context.Background(), types.NamespacedName{Name: "freeze-nginx-demo", Namespace: "default"}, &gate)
	assert.True(t, apierrors.IsNotFound(err), "freeze gate must be deleted on resume")
}

// TestResume_Idempotent verifies that resumeFn is safe when the freeze gate is absent
// (e.g. manual deletion or first resume after an older-format pause).
func TestResume_Idempotent(t *testing.T) {
	s := cliTestScheme(t)
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git:          v1alpha1.PipelineGit{URL: "https://github.com/test/repo"},
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}},
			Paused:       true,
		},
	}
	// No freeze gate in the cluster — should still succeed.
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pipeline).Build()

	var buf bytes.Buffer
	require.NoError(t, resumeFn(&buf, c, "default", "nginx-demo"), "resumeFn must succeed even if freeze gate is absent")
}

// TestPolicyList_ShowsGates verifies that policyListFn shows PolicyGate rows.
func TestPolicyList_ShowsGates(t *testing.T) {
	s := cliTestScheme(t)
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-weekend-deploys",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/scope":      "org",
				"kardinal.io/applies-to": "prod",
			},
		},
		Spec: v1alpha1.PolicyGateSpec{
			Expression:      "!schedule.isWeekend",
			RecheckInterval: "5m",
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gate).Build()

	var buf bytes.Buffer
	err := policyListFn(&buf, c, "default", "")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "no-weekend-deploys")
	assert.Contains(t, out, "org")
	assert.Contains(t, out, "prod")
	assert.Contains(t, out, "5m")
}

// TestPolicySimulate_BlockedOnWeekend verifies simulation returns BLOCKED on Saturday.
func TestPolicySimulate_BlockedOnWeekend(t *testing.T) {
	s := cliTestScheme(t)
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-weekend-deploys",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/applies-to": "prod"},
		},
		Spec: v1alpha1.PolicyGateSpec{
			Expression: "!schedule.isWeekend",
			Message:    "Production deployments are blocked on weekends",
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gate).Build()

	var buf bytes.Buffer
	err := policySimulateFn(&buf, c, "default", "nginx-demo", "prod", "Saturday 3pm", 0)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "BLOCKED")
	assert.Contains(t, out, "no-weekend-deploys")
}

// TestPolicySimulate_PassOnWeekday verifies simulation returns PASS on Tuesday.
func TestPolicySimulate_PassOnWeekday(t *testing.T) {
	s := cliTestScheme(t)
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-weekend-deploys",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/applies-to": "prod"},
		},
		Spec: v1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gate).Build()

	var buf bytes.Buffer
	err := policySimulateFn(&buf, c, "default", "nginx-demo", "prod", "Tuesday 10am", 0)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "PASS")
}

// TestHistory_ListsPromotionSteps verifies that historyFn lists promotion steps for a pipeline.
func TestHistory_ListsPromotionSteps(t *testing.T) {
	s := cliTestScheme(t)
	step1 := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo-v1-dev",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "nginx-demo"},
		},
		Spec:   v1alpha1.PromotionStepSpec{PipelineName: "nginx-demo", BundleName: "nginx-demo-v1", Environment: "dev", StepType: "open-pr"},
		Status: v1alpha1.PromotionStepStatus{State: "Verified", PRURL: "https://github.com/org/repo/pull/10"},
	}
	step2 := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo-v2-dev",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "nginx-demo"},
		},
		Spec:   v1alpha1.PromotionStepSpec{PipelineName: "nginx-demo", BundleName: "nginx-demo-v2", Environment: "dev", StepType: "open-pr"},
		Status: v1alpha1.PromotionStepStatus{State: "Promoting"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(step1, step2).WithStatusSubresource(step1, step2).Build()

	var buf bytes.Buffer
	err := historyFn(&buf, c, "default", "nginx-demo", "", 20)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "nginx-demo-v1", "should show bundle v1")
	assert.Contains(t, out, "nginx-demo-v2", "should show bundle v2")
	assert.Contains(t, out, "BUNDLE", "should have header")
	assert.Contains(t, out, "ACTION", "should have ACTION column")
	assert.Contains(t, out, "ENV", "should have ENV column")
	assert.Contains(t, out, "#10", "should show PR number")
}

// TestHistory_EnvFilter verifies that env filter works.
func TestHistory_EnvFilter(t *testing.T) {
	s := cliTestScheme(t)
	stepDev := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo-v1-dev",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "nginx-demo"},
		},
		Spec:   v1alpha1.PromotionStepSpec{PipelineName: "nginx-demo", BundleName: "nginx-demo-v1", Environment: "dev", StepType: "open-pr"},
		Status: v1alpha1.PromotionStepStatus{State: "Verified"},
	}
	stepProd := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo-v1-prod",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "nginx-demo"},
		},
		Spec:   v1alpha1.PromotionStepSpec{PipelineName: "nginx-demo", BundleName: "nginx-demo-v1", Environment: "prod", StepType: "open-pr"},
		Status: v1alpha1.PromotionStepStatus{State: "Verified"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(stepDev, stepProd).WithStatusSubresource(stepDev, stepProd).Build()

	var buf bytes.Buffer
	err := historyFn(&buf, c, "default", "nginx-demo", "prod", 20)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "prod", "should contain prod env")
	assert.NotContains(t, out, "dev", "should not contain dev when filtered to prod")
}

// TestHistory_RollbackAction verifies that rollback steps show action=rollback.
func TestHistory_RollbackAction(t *testing.T) {
	s := cliTestScheme(t)
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo-v1-rollback-prod",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline": "nginx-demo",
				"kardinal.io/rollback": "true",
			},
		},
		Spec:   v1alpha1.PromotionStepSpec{PipelineName: "nginx-demo", BundleName: "nginx-demo-v1", Environment: "prod", StepType: "open-pr"},
		Status: v1alpha1.PromotionStepStatus{State: "Verified"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(step).WithStatusSubresource(step).Build()

	var buf bytes.Buffer
	err := historyFn(&buf, c, "default", "nginx-demo", "", 20)
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "rollback", "rollback step should show action=rollback")
}

// TestPolicySimulate_GlobalGateAppliedToAllEnvs verifies that gates without applies-to
// are included in simulation regardless of environment (CLI-3 fix).
func TestPolicySimulate_GlobalGateAppliedToAllEnvs(t *testing.T) {
	s := cliTestScheme(t)
	// Gate with NO applies-to label — should apply to every env.
	globalGate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "global-freeze",
			Namespace: "default",
			Labels:    map[string]string{},
		},
		Spec: v1alpha1.PolicyGateSpec{
			Expression: "!schedule.isWeekend",
			Message:    "Global freeze on weekends",
		},
	}
	// Gate with applies-to=prod — should NOT apply when env=staging.
	prodGate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "prod-only",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/applies-to": "prod"},
		},
		Spec: v1alpha1.PolicyGateSpec{
			Expression: "!schedule.isWeekend",
			Message:    "Prod-only gate",
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(globalGate, prodGate).Build()

	// Simulate for staging on a Saturday — global gate should block, prod gate should be excluded.
	var buf bytes.Buffer
	err := policySimulateFn(&buf, c, "default", "nginx-demo", "staging", "Saturday 3pm", 0)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "global-freeze", "global gate (no applies-to) must be evaluated")
	assert.NotContains(t, out, "prod-only", "prod-only gate must not appear for staging env")
	assert.Contains(t, out, "BLOCKED")
}

// TestPolicySimulate_EnvSpecificGateExcluded verifies that applies-to gates are
// excluded when the env does not match (CLI-3: applies-to label check in simulate loop).
func TestPolicySimulate_EnvSpecificGateExcluded(t *testing.T) {
	s := cliTestScheme(t)
	stagingGate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "staging-gate",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/applies-to": "staging"},
		},
		Spec: v1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(stagingGate).Build()

	// Simulate for prod — staging gate must not appear.
	var buf bytes.Buffer
	err := policySimulateFn(&buf, c, "default", "nginx-demo", "prod", "Saturday 3pm", 0)
	require.NoError(t, err)

	out := buf.String()
	assert.NotContains(t, out, "staging-gate", "staging gate must be excluded for prod env")
	assert.Contains(t, out, "PASS", "no applicable gates means PASS")
}

// TestRollback_CopiesTypeFromOriginalBundle verifies that the rollback bundle
// copies Type from the original Verified bundle, not hardcoding "image" (CLI-5 fix).
func TestRollback_CopiesTypeFromOriginalBundle(t *testing.T) {
	s := cliTestScheme(t)
	// Verified bundle with type=config; no special label needed — rollbackFn filters by Spec.Pipeline.
	verifiedBundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-cfg-v1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "config", Pipeline: "nginx-demo"},
		Status:     v1alpha1.BundleStatus{Phase: "Verified"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(verifiedBundle).WithStatusSubresource(verifiedBundle).Build()

	var buf bytes.Buffer
	err := rollbackFn(&buf, c, "default", "nginx-demo", "prod", "", false)
	require.NoError(t, err)

	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundles))
	require.Len(t, bundles.Items, 2)

	var rb *v1alpha1.Bundle
	for i := range bundles.Items {
		if bundles.Items[i].Labels["kardinal.io/rollback"] == "true" {
			rb = &bundles.Items[i]
		}
	}
	require.NotNil(t, rb)
	assert.Equal(t, "config", rb.Spec.Type, "rollback bundle must copy type from the original bundle")
	assert.Equal(t, "nginx-demo-cfg-v1", rb.Spec.Provenance.RollbackOf)
}

// TestRollback_CopiesTypeWhenExplicitToBundle verifies type is copied when --to is specified.
func TestRollback_CopiesTypeWhenExplicitToBundle(t *testing.T) {
	s := cliTestScheme(t)
	targetBundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-mixed-v3", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "mixed", Pipeline: "nginx-demo"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(targetBundle).Build()

	var buf bytes.Buffer
	err := rollbackFn(&buf, c, "default", "nginx-demo", "prod", "nginx-demo-mixed-v3", false)
	require.NoError(t, err)

	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundles))

	var rb *v1alpha1.Bundle
	for i := range bundles.Items {
		if bundles.Items[i].Labels["kardinal.io/rollback"] == "true" {
			rb = &bundles.Items[i]
		}
	}
	require.NotNil(t, rb)
	assert.Equal(t, "mixed", rb.Spec.Type, "rollback bundle must copy type from target bundle")
}

// TestSplitImageRef verifies image reference parsing.
func TestSplitImageRef(t *testing.T) {
	tests := []struct {
		img  string
		repo string
		tag  string
	}{
		{"nginx:1.25", "nginx", "1.25"},
		{"ghcr.io/myorg/app:v2.0.0", "ghcr.io/myorg/app", "v2.0.0"},
		{"nginx", "nginx", ""},
		{"nginx@sha256:abc123", "nginx", "sha256:abc123"},
	}
	for _, tt := range tests {
		t.Run(tt.img, func(t *testing.T) {
			repo, tag := splitImageRef(tt.img)
			assert.Equal(t, tt.repo, repo)
			assert.Equal(t, tt.tag, tag)
		})
	}
}

// TestPolicyList_ShowsPendingState verifies that unevaluated gates show "Pending".
func TestPolicyList_ShowsPendingState(t *testing.T) {
	s := cliTestScheme(t)
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unevaluated-gate",
			Namespace: "default",
		},
		Spec: v1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend"},
		// Status zero-value: no LastEvaluatedAt
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gate).Build()

	var buf bytes.Buffer
	err := policyListFn(&buf, c, "default", "")
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "Pending", "unevaluated gate must show Pending")
}
