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
	"os"
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

// TestCreateBundle_RejectsMalformedImage verifies that image references with
// invalid characters are rejected before creating a Bundle (#283).
func TestCreateBundle_RejectsMalformedImage(t *testing.T) {
	s := cliTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	tests := []struct {
		image   string
		wantErr bool
	}{
		{"ghcr.io/pnz1990/app:sha-abc1234", false},
		{"nginx:1.29", false},
		{"nginx", false},
		{"!!! bad", true},
		{"space bad", true},
	}
	for _, tc := range tests {
		var buf bytes.Buffer
		err := createBundleFn(&buf, c, "default", "nginx-demo", []string{tc.image}, "image")
		if tc.wantErr {
			require.Error(t, err, "image %q must be rejected", tc.image)
		} else {
			require.NoError(t, err, "image %q must be accepted", tc.image)
		}
	}
}

// TestRollback_CreatesBundleWithRollbackOf verifies rollback bundle creation.
// The rollback command now queries PromotionStep history (not Bundle.Phase) to find
// the last Verified bundle for the target environment (#264).
func TestRollback_CreatesBundleWithRollbackOf(t *testing.T) {
	s := cliTestScheme(t)
	// Original bundle is Superseded (normal in production after multiple deploys).
	verifiedBundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     v1alpha1.BundleStatus{Phase: "Superseded"},
	}
	// Verified PromotionStep for prod — this is what rollback now uses.
	prodStep := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo-v1-prod",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline":    "nginx-demo",
				"kardinal.io/environment": "prod",
			},
		},
		Spec:   v1alpha1.PromotionStepSpec{PipelineName: "nginx-demo", Environment: "prod", BundleName: "nginx-demo-v1"},
		Status: v1alpha1.PromotionStepStatus{State: "Verified"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(verifiedBundle, prodStep).WithStatusSubresource(verifiedBundle, prodStep).Build()

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

// TestPolicySimulate_BlockedOnWeekend verifies simulation returns BLOCKED on Saturday,
// and that the "Next window" field is shown (issue #318 regression test).
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
	// #318: Next window must appear when gate blocks on a weekend simulation
	assert.Contains(t, out, "Next window: Monday", "BLOCKED weekend gate must show Next window field")
	assert.Contains(t, out, "UTC", "Next window must include UTC timezone")
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

// TestPolicySimulate_KroCELFunctionsAvailable verifies that kro CEL library functions
// (json.*, maps.*, lists.*) are available in policy simulate (#243).
func TestPolicySimulate_KroCELFunctionsAvailable(t *testing.T) {
	s := cliTestScheme(t)
	jsonGate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "json-gate",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/applies-to": "prod"},
		},
		Spec: v1alpha1.PolicyGateSpec{
			Expression: `json.unmarshal("{\"ready\": true}").ready == true`,
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(jsonGate).Build()

	var buf bytes.Buffer
	err := policySimulateFn(&buf, c, "default", "nginx-demo", "prod", "Tuesday 10am", 0)
	require.NoError(t, err, "policy simulate with json.unmarshal must not error")

	out := buf.String()
	assert.Contains(t, out, "PASS", "json.unmarshal expression evaluating true must PASS")
	assert.NotContains(t, out, "compile error",
		"kro library functions must be registered; no compile error expected")
}

// TestRollback_CopiesTypeFromOriginalBundle verifies that the rollback bundle
// copies Type from the original Verified bundle, not hardcoding "image" (CLI-5 fix).
func TestRollback_CopiesTypeFromOriginalBundle(t *testing.T) {
	s := cliTestScheme(t)
	// Bundle is Superseded (realistic production state).
	verifiedBundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-cfg-v1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "config", Pipeline: "nginx-demo"},
		Status:     v1alpha1.BundleStatus{Phase: "Superseded"},
	}
	// Verified PromotionStep points to this bundle.
	prodStep := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo-cfg-v1-prod",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline":    "nginx-demo",
				"kardinal.io/environment": "prod",
			},
		},
		Spec:   v1alpha1.PromotionStepSpec{PipelineName: "nginx-demo", Environment: "prod", BundleName: "nginx-demo-cfg-v1"},
		Status: v1alpha1.PromotionStepStatus{State: "Verified"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(verifiedBundle, prodStep).WithStatusSubresource(verifiedBundle, prodStep).Build()

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

// TestRollback_WorksWhenAllBundlesSuperseded verifies that rollback succeeds even when
// all Bundle objects have Phase=Superseded — the realistic production state (#264).
func TestRollback_WorksWhenAllBundlesSuperseded(t *testing.T) {
	s := cliTestScheme(t)
	// All bundles are Superseded (as in production after multiple deploys).
	oldBundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-old", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     v1alpha1.BundleStatus{Phase: "Superseded"},
	}
	newBundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-new", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     v1alpha1.BundleStatus{Phase: "Superseded"},
	}
	// Old bundle has a Verified prod step; new bundle is still Promoting.
	oldProdStep := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nginx-demo-old-prod",
			Namespace:         "default",
			CreationTimestamp: metav1.Now(),
			Labels: map[string]string{
				"kardinal.io/pipeline":    "nginx-demo",
				"kardinal.io/environment": "prod",
			},
		},
		Spec:   v1alpha1.PromotionStepSpec{PipelineName: "nginx-demo", Environment: "prod", BundleName: "nginx-demo-old"},
		Status: v1alpha1.PromotionStepStatus{State: "Verified"},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(oldBundle, newBundle, oldProdStep).
		WithStatusSubresource(oldBundle, newBundle, oldProdStep).
		Build()

	var buf bytes.Buffer
	err := rollbackFn(&buf, c, "default", "nginx-demo", "prod", "", false)
	require.NoError(t, err, "rollback must succeed even when all bundles are Superseded")

	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundles))

	var rb *v1alpha1.Bundle
	for i := range bundles.Items {
		if bundles.Items[i].Labels["kardinal.io/rollback"] == "true" {
			rb = &bundles.Items[i]
		}
	}
	require.NotNil(t, rb, "rollback bundle must be created")
	assert.Equal(t, "nginx-demo-old", rb.Spec.Provenance.RollbackOf,
		"rollback must target the bundle with the most recent Verified prod PromotionStep")
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

// TestPolicyList_FiltersGraphInstances verifies that Graph-managed per-bundle
// PolicyGate instances are excluded from policy list (#285).
func TestPolicyList_FiltersGraphInstances(t *testing.T) {
	s := cliTestScheme(t)
	// Template gate — should be shown.
	templateGate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-weekend-deploys",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/scope":      "org",
				"kardinal.io/applies-to": "prod",
			},
		},
		Spec: v1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend"},
	}
	// Graph instance with kro label — must be filtered out.
	instanceGate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-weekend-deploys--nginx-demo-v1",
			Namespace: "default",
			Labels: map[string]string{
				"internal.kro.run/graph-name": "nginx-demo-nginx-demo-v1",
				"kardinal.io/bundle":          "nginx-demo-v1",
			},
		},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(templateGate, instanceGate).Build()

	var buf bytes.Buffer
	err := policyListFn(&buf, c, "default", "")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "no-weekend-deploys", "template gate must be shown")
	assert.NotContains(t, out, "nginx-demo-v1", "Graph instance must be filtered out")
}

// TestVersionOutput_ThreeLines verifies that versionFn outputs CLI, Controller, and Graph lines.
func TestVersionOutput_ThreeLines(t *testing.T) {
	tests := []struct {
		name        string
		controllerV string
		graphV      string
		wantLines   []string
	}{
		{
			name:        "all versions known",
			controllerV: "v0.2.0",
			graphV:      "v0.9.1",
			wantLines:   []string{"CLI:", "Controller: v0.2.0", "Graph:      v0.9.1"},
		},
		{
			name:        "graph version unknown",
			controllerV: "v0.2.0",
			graphV:      "",
			wantLines:   []string{"CLI:", "Controller: v0.2.0", "Graph:      (unknown)"},
		},
		{
			name:        "controller version unknown",
			controllerV: "",
			graphV:      "",
			wantLines:   []string{"CLI:", "Controller: unknown", "Graph:      (unknown)"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := versionFn(&buf, tc.controllerV, tc.graphV)
			require.NoError(t, err)
			out := buf.String()
			for _, want := range tc.wantLines {
				assert.Contains(t, out, want, "output should contain %q", want)
			}
		})
	}
}

// TestPolicyTest_ValidExpression verifies that a gate with valid CEL is reported PASS.
func TestPolicyTest_ValidExpression(t *testing.T) {
	// Write a temp YAML file with a valid PolicyGate.
	content := `apiVersion: promotions.kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: allow-all
spec:
  expression: "true"
`
	tmpFile := t.TempDir() + "/gate.yaml"
	require.NoError(t, os.WriteFile(tmpFile, []byte(content), 0600))

	var buf bytes.Buffer
	err := policyTestFn(&buf, tmpFile)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "allow-all")
	assert.Contains(t, out, "Syntax: valid")
	assert.Contains(t, out, "PASS")
}

// TestPolicyTest_InvalidExpression verifies that a gate with invalid CEL
// returns a non-nil error (for CI gating) and shows INVALID in output.
func TestPolicyTest_InvalidExpression(t *testing.T) {
	content := `apiVersion: promotions.kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: bad-gate
spec:
  expression: "schedule.notExistingFn()"
`
	tmpFile := t.TempDir() + "/bad.yaml"
	require.NoError(t, os.WriteFile(tmpFile, []byte(content), 0600))

	var buf bytes.Buffer
	// Syntax errors return non-nil error for CI gating.
	err := policyTestFn(&buf, tmpFile)
	require.Error(t, err, "CEL syntax error must return non-nil error")

	out := buf.String()
	assert.Contains(t, out, "bad-gate")
	assert.True(t,
		contains(out, "INVALID") || contains(out, "ERROR") || contains(out, "FAIL"),
		"output should show validation failure: %s", out)
}

// TestPolicyTest_WeekendExpression verifies schedule.isWeekend expression evaluates.
func TestPolicyTest_WeekendExpression(t *testing.T) {
	content := `apiVersion: promotions.kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: no-weekend
spec:
  expression: "!schedule.isWeekend"
`
	tmpFile := t.TempDir() + "/weekend.yaml"
	require.NoError(t, os.WriteFile(tmpFile, []byte(content), 0600))

	var buf bytes.Buffer
	err := policyTestFn(&buf, tmpFile)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "no-weekend")
	assert.Contains(t, out, "Syntax: valid")
	// Result is either PASS or FAIL depending on current day — either is valid.
	assert.True(t,
		contains(out, "PASS") || contains(out, "FAIL"),
		"output should show PASS or FAIL: %s", out)
}

// TestPolicyTest_MissingFile verifies error on missing file.
func TestPolicyTest_MissingFile(t *testing.T) {
	var buf bytes.Buffer
	err := policyTestFn(&buf, "/nonexistent/path/gate.yaml")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent")
}

// contains is a helper for multi-assertion checks.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

// TestApprove_PatchesBundleWithLabel verifies that approveFn adds kardinal.io/approved label.
func TestApprove_PatchesBundleWithLabel(t *testing.T) {
	s := cliTestScheme(t)
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1-29-0", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(bundle).Build()

	var buf bytes.Buffer
	err := approveFn(&buf, c, "default", "nginx-demo-v1-29-0", "prod")
	require.NoError(t, err)

	// Verify label was applied.
	var updated v1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo-v1-29-0", Namespace: "default"}, &updated))
	assert.Equal(t, "true", updated.Labels["kardinal.io/approved"])
	assert.Equal(t, "prod", updated.Labels["kardinal.io/approved-for"])

	// Verify output.
	assert.Contains(t, buf.String(), "nginx-demo-v1-29-0")
	assert.Contains(t, buf.String(), "approved")
}

// TestApprove_WithoutEnv verifies approveFn without --env flag.
func TestApprove_WithoutEnv(t *testing.T) {
	s := cliTestScheme(t)
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1-28-0", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(bundle).Build()

	var buf bytes.Buffer
	err := approveFn(&buf, c, "default", "nginx-demo-v1-28-0", "")
	require.NoError(t, err)

	var updated v1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo-v1-28-0", Namespace: "default"}, &updated))
	assert.Equal(t, "true", updated.Labels["kardinal.io/approved"])
	assert.Empty(t, updated.Labels["kardinal.io/approved-for"])
}

// TestApprove_BundleNotFound verifies error when bundle does not exist.
func TestApprove_BundleNotFound(t *testing.T) {
	s := cliTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	var buf bytes.Buffer
	err := approveFn(&buf, c, "default", "nonexistent-bundle", "prod")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent-bundle")
}

// --- #406: pause/resume lifecycle tests ---

// TestPauseResume_FreezeGateCreatedAndDeleted verifies the full pause/resume lifecycle
// from the CLI perspective (issue #406):
// 1. pauseFn creates a freeze-<pipeline> PolicyGate with expression "false"
// 2. resumeFn deletes the freeze gate
// 3. The pipeline's Spec.Paused is set/unset correctly
func TestPauseResume_FreezeGateCreatedAndDeleted(t *testing.T) {
	s := cliTestScheme(t)
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git:          v1alpha1.PipelineGit{URL: "https://github.com/test/repo"},
			Environments: []v1alpha1.EnvironmentSpec{{Name: "prod"}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pipeline).Build()

	// Step 1: pause
	var pauseBuf bytes.Buffer
	require.NoError(t, pauseFn(&pauseBuf, c, "default", "nginx-demo"),
		"pause must succeed")
	assert.Contains(t, pauseBuf.String(), "paused")

	// Verify Pipeline.Spec.Paused = true
	var paused v1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo", Namespace: "default"}, &paused))
	assert.True(t, paused.Spec.Paused, "Pipeline.Spec.Paused must be true after pause")

	// Verify freeze gate exists with expression "false"
	var freezeGate v1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "freeze-nginx-demo", Namespace: "default"}, &freezeGate),
		"freeze gate must be created by pauseFn")
	assert.Equal(t, "false", freezeGate.Spec.Expression,
		"freeze gate expression must be 'false' to block all promotions")
	assert.Equal(t, "true", freezeGate.Labels["kardinal.io/freeze"],
		"freeze gate must have kardinal.io/freeze=true label")

	// Step 2: resume
	var resumeBuf bytes.Buffer
	require.NoError(t, resumeFn(&resumeBuf, c, "default", "nginx-demo"),
		"resume must succeed")
	assert.Contains(t, resumeBuf.String(), "resumed")

	// Verify Pipeline.Spec.Paused = false
	var resumed v1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo", Namespace: "default"}, &resumed))
	assert.False(t, resumed.Spec.Paused, "Pipeline.Spec.Paused must be false after resume")

	// Verify freeze gate is deleted
	var deletedGate v1alpha1.PolicyGate
	err := c.Get(context.Background(),
		types.NamespacedName{Name: "freeze-nginx-demo", Namespace: "default"}, &deletedGate)
	assert.True(t, apierrors.IsNotFound(err),
		"freeze gate must be deleted by resumeFn; got: %v", err)
}

// TestPause_PipelineNotFound verifies pauseFn returns an error when the pipeline does not exist.
func TestPause_PipelineNotFound(t *testing.T) {
	s := cliTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	var buf bytes.Buffer
	err := pauseFn(&buf, c, "default", "nonexistent-pipeline")
	assert.Error(t, err, "pause must fail when pipeline does not exist")
	assert.Contains(t, err.Error(), "nonexistent-pipeline")
}

// TestResume_PipelineNotFound verifies resumeFn returns an error when the pipeline does not exist.
func TestResume_PipelineNotFound(t *testing.T) {
	s := cliTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	var buf bytes.Buffer
	err := resumeFn(&buf, c, "default", "nonexistent-pipeline")
	assert.Error(t, err, "resume must fail when pipeline does not exist")
	assert.Contains(t, err.Error(), "nonexistent-pipeline")
}
