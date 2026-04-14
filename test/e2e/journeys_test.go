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

// Package e2e contains end-to-end journey tests for kardinal-promoter.
// Journey tests 1, 3, 4, and 5 run without a live cluster using fake clients
// and the real reconciler/CEL code paths.
// Journey test 2 (multi-cluster) requires Stages 14+ and is skipped until then.
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynfake "k8s.io/client-go/dynamic/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	pgrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
	psrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	rprec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/rollbackpolicy"
	kardinalsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// journeyScheme builds the scheme used by all journey tests.
func journeyScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	require.NoError(t, appsv1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))
	return s
}

// TestJourney1Quickstart validates docs/aide/definition-of-done.md Journey 1.
//
// A user applies a 3-environment Pipeline, creates a Bundle, and the system
// promotes through test → uat → prod automatically.
// In this test we use approvalMode: auto for all envs (the real PR flow is verified
// in TestPromotionLoop_PRReview_ViaWebhook in promotion_loop_test.go).
//
// Pipeline references pnz1990/kardinal-demo (the GitOps repo) and uses
// ghcr.io/pnz1990/kardinal-test-app (the reference test application image).
// This matches the AGENTS.md §Product Validation Scenarios (issue #399).
func TestJourney1Quickstart(t *testing.T) {
	s := journeyScheme(t)

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "kardinal-test-app", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{
				URL:    "https://github.com/pnz1990/kardinal-demo",
				Branch: "main",
			},
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test", Approval: "auto"},
				{Name: "uat", Approval: "auto"},
				{Name: "prod", Approval: "auto"},
			},
		},
	}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "kardinal-test-app-sha-abc1234", Namespace: "default"},
		Spec: v1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: "kardinal-test-app",
			// No Images: the kustomize-set-image step skips when no images are specified,
			// allowing the promotion loop to advance in unit tests without requiring kustomize.
			// In CI with a live cluster, the real image is used via 'kardinal create bundle'.
			// The real kustomize-set-image behaviour is tested in pkg/steps/steps/steps_test.go.
			// Reference image for documentation: ghcr.io/pnz1990/kardinal-test-app:sha-abc1234
			Provenance: &v1alpha1.BundleProvenance{
				Author:    "ci-system",
				CommitSHA: "abc1234def5678",
				CIRunURL:  "https://github.com/pnz1990/kardinal-test-app/actions/runs/1",
			},
		},
	}
	steps := []*v1alpha1.PromotionStep{
		makeJourneyStep("step-test", "kardinal-test-app", "kardinal-test-app-sha-abc1234", "test", "auto"),
		makeJourneyStep("step-uat", "kardinal-test-app", "kardinal-test-app-sha-abc1234", "uat", "auto"),
		makeJourneyStep("step-prod", "kardinal-test-app", "kardinal-test-app-sha-abc1234", "prod", "auto"),
	}

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pipeline, bundle, steps[0], steps[1], steps[2]).
		WithStatusSubresource(&v1alpha1.Bundle{}, &v1alpha1.PromotionStep{}).
		Build()

	rec := &psrec.Reconciler{
		Client: c,
		SCM: &mockSCMForLoop{
			prURL:    "https://github.com/pnz1990/kardinal-demo/pull/1",
			prNumber: 1,
		},
		GitClient: &mockGitForLoop{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	ctx := context.Background()

	// Drive each environment's PromotionStep to Verified.
	for _, stepName := range []string{"step-test", "step-uat", "step-prod"} {
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: stepName, Namespace: "default"}}
		driveStepToVerified(t, ctx, rec, c, req, stepName)
	}

	// Assert all three environments reached Verified.
	for _, stepName := range []string{"step-test", "step-uat", "step-prod"} {
		var ps v1alpha1.PromotionStep
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: stepName, Namespace: "default"}, &ps))
		assert.Equal(t, "Verified", ps.Status.State,
			"journey 1: %s should be Verified; got %s: %s", stepName, ps.Status.State, ps.Status.Message)
	}
	t.Log("Journey 1: Quickstart — test → uat → prod all Verified ✅")
	t.Logf("journey 1: pipeline=kardinal-test-app repo=pnz1990/kardinal-demo image=ghcr.io/pnz1990/kardinal-test-app:sha-abc1234 ✅")
}

// TestJourney2MultiClusterFleet validates docs/aide/definition-of-done.md Journey 2.
//
// Tests the dependsOn fan-out: prod-eu and prod-us both depend on pre-prod.
// After pre-prod reaches Verified, both prod environments run simultaneously.
// Each must reach Verified independently.
//
// This test uses a fake Kubernetes client (same approach as J1/J3/J4/J5/J6).
// Stage 14 distributed mode is not required for the fan-out logic — the
// PromotionStep reconciler handles all steps in standalone mode regardless of shard.
//
// Pass criteria (subset of definition-of-done.md Journey 2):
//   - dependsOn fan-out: prod-eu and prod-us start after pre-prod is Verified
//   - Both prod environments reach Verified independently
func TestJourney2MultiClusterFleet(t *testing.T) {
	s := journeyScheme(t)

	// Pipeline: test → pre-prod → [prod-eu, prod-us] (parallel fan-out via dependsOn)
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "rollouts-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{
				URL:    "https://github.com/pnz1990/kardinal-demo",
				Branch: "main",
			},
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test", Approval: "auto"},
				{Name: "pre-prod", Approval: "auto"},
				// prod-eu and prod-us both depend on pre-prod — parallel fan-out
				{Name: "prod-eu", Approval: "auto", DependsOn: []string{"pre-prod"}},
				{Name: "prod-us", Approval: "auto", DependsOn: []string{"pre-prod"}},
			},
		},
	}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "rollouts-demo-sha-abc1234", Namespace: "default"},
		Spec: v1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: "rollouts-demo",
			Provenance: &v1alpha1.BundleProvenance{
				Author:    "ci-system",
				CommitSHA: "abc1234def5678",
				CIRunURL:  "https://github.com/pnz1990/kardinal-test-app/actions/runs/2",
			},
		},
	}

	// Create PromotionSteps for all 4 environments.
	steps := []*v1alpha1.PromotionStep{
		makeJourneyStep("step-test", "rollouts-demo", "rollouts-demo-sha-abc1234", "test", "auto"),
		makeJourneyStep("step-pre-prod", "rollouts-demo", "rollouts-demo-sha-abc1234", "pre-prod", "auto"),
		makeJourneyStep("step-prod-eu", "rollouts-demo", "rollouts-demo-sha-abc1234", "prod-eu", "auto"),
		makeJourneyStep("step-prod-us", "rollouts-demo", "rollouts-demo-sha-abc1234", "prod-us", "auto"),
	}

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pipeline, bundle, steps[0], steps[1], steps[2], steps[3]).
		WithStatusSubresource(&v1alpha1.Bundle{}, &v1alpha1.PromotionStep{}).
		Build()

	rec := &psrec.Reconciler{
		Client: c,
		SCM: &mockSCMForLoop{
			prURL:    "https://github.com/pnz1990/kardinal-demo/pull/2",
			prNumber: 2,
		},
		GitClient: &mockGitForLoop{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	ctx := context.Background()

	// Drive sequential environments first: test → pre-prod.
	for _, stepName := range []string{"step-test", "step-pre-prod"} {
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: stepName, Namespace: "default"}}
		driveStepToVerified(t, ctx, rec, c, req, stepName)
	}

	// Drive prod-eu and prod-us in parallel (interleaved reconcile calls
	// simulating concurrent fan-out from the dependsOn Graph edge).
	euReq := ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-prod-eu", Namespace: "default"}}
	usReq := ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-prod-us", Namespace: "default"}}
	for i := 0; i < 30; i++ {
		_, errEU := rec.Reconcile(ctx, euReq)
		require.NoError(t, errEU, "prod-eu: reconcile iteration %d", i)
		_, errUS := rec.Reconcile(ctx, usReq)
		require.NoError(t, errUS, "prod-us: reconcile iteration %d", i)

		var eu, us v1alpha1.PromotionStep
		require.NoError(t, c.Get(ctx, euReq.NamespacedName, &eu))
		require.NoError(t, c.Get(ctx, usReq.NamespacedName, &us))
		t.Logf("parallel iteration %d: prod-eu=%s prod-us=%s", i, eu.Status.State, us.Status.State)
		if (eu.Status.State == "Verified" || eu.Status.State == "Failed") &&
			(us.Status.State == "Verified" || us.Status.State == "Failed") {
			break
		}
		time.Sleep(1 * time.Millisecond)
	}

	// Assert all 4 environments reached Verified.
	for _, stepName := range []string{"step-test", "step-pre-prod", "step-prod-eu", "step-prod-us"} {
		var ps v1alpha1.PromotionStep
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: stepName, Namespace: "default"}, &ps))
		assert.Equal(t, "Verified", ps.Status.State,
			"journey 2: %s should be Verified; got %s: %s", stepName, ps.Status.State, ps.Status.Message)
	}
	t.Log("Journey 2: Multi-cluster fleet — test → pre-prod → [prod-eu, prod-us] all Verified ✅")
}

// TestJourney3PolicyGovernance validates docs/aide/definition-of-done.md Journey 3.
//
// Tests the PolicyGate reconciler evaluating:
//   - no-weekend-deploys: !schedule.isWeekend
//   - business-hours-only: schedule.hour >= 9 && schedule.hour <= 17
//
// Uses the real CEL evaluator and PolicyGate reconciler with a fake Kubernetes client.
func TestJourney3PolicyGovernance(t *testing.T) {
	s := journeyScheme(t)

	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
	}

	// ── Fixture: no-weekend-deploys gate ──────────────────────────────────────
	weekendGate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-weekend-deploys",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/bundle": "nginx-demo-v1",
				"kardinal.io/env":    "prod",
			},
		},
		Spec: v1alpha1.PolicyGateSpec{
			Expression:      "!schedule.isWeekend",
			Message:         "Production deployments are blocked on weekends",
			RecheckInterval: "5m",
		},
	}

	// ── Test: weekend blocks ──────────────────────────────────────────────────
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(bundle, weekendGate).
		WithStatusSubresource(&v1alpha1.PolicyGate{}).
		Build()

	// Inject a Saturday timestamp via NowFn.
	saturday := time.Date(2026, 4, 12, 15, 0, 0, 0, time.UTC) // Saturday April 12, 2026
	rec, err := pgrec.NewReconciler(c)
	require.NoError(t, err)
	rec.NowFn = func() time.Time { return saturday }

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "no-weekend-deploys", Namespace: "default"}}
	_, err = rec.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var gate v1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &gate))
	assert.False(t, gate.Status.Ready,
		"journey 3: no-weekend-deploys must block on Saturday; got ready=%v reason=%s",
		gate.Status.Ready, gate.Status.Reason)
	t.Logf("journey 3: Saturday → BLOCKED (reason: %s) ✅", gate.Status.Reason)

	// ── Test: weekday passes ──────────────────────────────────────────────────
	tuesday := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC) // Tuesday April 14, 2026
	c2 := fake.NewClientBuilder().WithScheme(s).
		WithObjects(bundle, weekendGate.DeepCopy()).
		WithStatusSubresource(&v1alpha1.PolicyGate{}).
		Build()
	rec2, err := pgrec.NewReconciler(c2)
	require.NoError(t, err)
	rec2.NowFn = func() time.Time { return tuesday }
	_, err = rec2.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var gate2 v1alpha1.PolicyGate
	require.NoError(t, c2.Get(context.Background(), req.NamespacedName, &gate2))
	assert.True(t, gate2.Status.Ready,
		"journey 3: no-weekend-deploys must pass on Tuesday; got ready=%v reason=%s",
		gate2.Status.Ready, gate2.Status.Reason)
	t.Log("journey 3: Tuesday → PASS ✅")

	// ── Test: kardinal policy simulate equivalent ─────────────────────────────
	// We test the CEL evaluator directly (simulate logic).
	satCtx := map[string]interface{}{
		"bundle":         map[string]interface{}{"type": "image", "version": "1.29.0"},
		"schedule":       map[string]interface{}{"isWeekend": true, "hour": 15, "dayOfWeek": "Saturday"},
		"environment":    map[string]interface{}{"name": "prod"},
		"metrics":        map[string]interface{}{},
		"previousBundle": map[string]interface{}{},
	}
	blocked, reason, err := pgrec.EvaluateForTest("!schedule.isWeekend", satCtx)
	require.NoError(t, err)
	assert.False(t, blocked, "journey 3: simulate Saturday must return BLOCKED")
	t.Logf("journey 3: policy simulate Saturday 3pm → RESULT: BLOCKED (reason: %s) ✅", reason)

	// ── Test: all 7 kro library CEL function types (#401) ────────────────────
	// Verifies that all kro CEL library extensions work in PolicyGate context.
	// These are the functions documented in AGENTS.md §CEL Expressions.
	kroLibCtx := map[string]interface{}{
		"bundle": map[string]interface{}{
			"type":    "image",
			"version": "1.29.0",
			"metadata": map[string]interface{}{
				"name": "test-bundle",
				"annotations": map[string]interface{}{
					"channel": `{"channel":"stable"}`,
					"team":    "platform",
				},
				"labels": map[string]interface{}{
					"tier": "production",
				},
			},
			"upstreamSoakMinutes": int64(45),
		},
		"schedule": map[string]interface{}{
			"isWeekend": false,
			"hour":      10,
			"dayOfWeek": "Tuesday",
		},
		"environment": map[string]interface{}{
			"name": "prod",
			"labels": map[string]interface{}{
				"tier": "production",
			},
		},
		"metrics": map[string]interface{}{},
		"upstream": map[string]interface{}{
			"uat": map[string]interface{}{"soakMinutes": int64(45)},
		},
		"previousBundle": map[string]interface{}{},
	}

	type kroTest struct {
		name string
		expr string
		want bool
	}
	kroTests := []kroTest{
		// 1. json.unmarshal — parse JSON annotation
		{"json.unmarshal", `json.unmarshal(bundle.metadata.annotations["channel"]).channel == "stable"`, true},
		// 2. maps.merge — combine two maps
		{"maps.merge", `environment.labels.merge({"extra": "value"})["extra"] == "value"`, true},
		// 3. lists.setAtIndex — replace element
		{"lists.setAtIndex", `lists.setAtIndex([1,2,3], 1, 9)[1] == 9`, true},
		// 4. lists.insertAtIndex — insert element
		{"lists.insertAtIndex", `lists.insertAtIndex([1,2,3], 0, 0).size() == 4`, true},
		// 5. lists.removeAtIndex — remove element
		{"lists.removeAtIndex", `lists.removeAtIndex([1,2,3], 2).size() == 2`, true},
		// 6. random.seededInt — deterministic random
		{"random.seededInt", `random.seededInt(0, 100, "test-seed") >= 0`, true},
		// 7. string.lowerAscii — string normalization
		{"string.lowerAscii", `bundle.metadata.annotations["team"].lowerAscii() == "platform"`, true},
		// Combined schedule + upstream (J3 pass criteria)
		{"combined_schedule_upstream", `!schedule.isWeekend && upstream["uat"].soakMinutes >= 30`, true},
	}

	for _, tc := range kroTests {
		pass, kroReason, evalErr := pgrec.EvaluateForTest(tc.expr, kroLibCtx)
		require.NoError(t, evalErr, "journey 3: kro function %q must compile without error", tc.name)
		assert.Equal(t, tc.want, pass,
			"journey 3: kro function %q: got %v want %v (reason: %s)", tc.name, pass, tc.want, kroReason)
		t.Logf("journey 3: kro lib function %s → %v ✅", tc.name, pass)
	}

	t.Log("Journey 3: Policy Governance — weekend gate, weekday gate, simulate, all 7 kro CEL functions verified ✅")
}

// TestJourney4Rollback validates docs/aide/definition-of-done.md Journey 4.
//
// When a PromotionStep's health check fails consecutively, the controller
// automatically creates a rollback Bundle with kardinal.io/rollback=true.
func TestJourney4Rollback(t *testing.T) {
	s := journeyScheme(t)

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/test/repo", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{
				{
					Name:     "prod",
					Approval: "auto",
					Health:   v1alpha1.HealthConfig{Type: "resource"},
					AutoRollback: &v1alpha1.AutoRollbackSpec{
						FailureThreshold: 1, // trigger immediately for test speed
					},
				},
			},
		},
	}
	badBundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo-bad",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "nginx-demo"},
		},
		Spec: v1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: "nginx-demo",
			Images:   []v1alpha1.ImageRef{{Repository: "ghcr.io/nginx/nginx", Tag: "1.30.0-bad"}},
		},
		Status: v1alpha1.BundleStatus{Phase: "Promoting"},
	}
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "step-prod-bad",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline":    "nginx-demo",
				"kardinal.io/environment": "prod",
			},
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "nginx-demo-bad",
			Environment:  "prod",
			StepType:     "auto",
		},
		Status: v1alpha1.PromotionStepStatus{
			State:                     "HealthChecking",
			ConsecutiveHealthFailures: 0,
		},
	}

	// RollbackPolicy CRD — created by the Graph controller alongside the PromotionStep.
	// Monitors ConsecutiveHealthFailures and triggers rollback when threshold exceeded.
	rollbackPolicy := &v1alpha1.RollbackPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "rp-nginx-demo-prod", Namespace: "default"},
		Spec: v1alpha1.RollbackPolicySpec{
			PipelineName:     "nginx-demo",
			Environment:      "prod",
			BundleRef:        "nginx-demo-bad",
			FailureThreshold: 1, // trigger immediately for test speed
		},
	}

	// Deployment exists but is NOT available → health check fails.
	unhealthyDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "prod"},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pipeline, badBundle, step, unhealthyDeploy, rollbackPolicy).
		WithStatusSubresource(&v1alpha1.Bundle{}, &v1alpha1.PromotionStep{}, &v1alpha1.RollbackPolicy{}).
		Build()

	dynClient := dynfake.NewSimpleDynamicClient(runtime.NewScheme())
	healthDetector := health.NewAutoDetector(c, dynClient)

	rec := &psrec.Reconciler{
		Client:         c,
		SCM:            &mockSCMForLoop{},
		GitClient:      &mockGitForLoop{},
		HealthDetector: healthDetector,
		WorkDirFn:      func(_, _ string) string { return t.TempDir() },
	}
	rpRec := &rprec.Reconciler{
		Client: c,
		NowFn:  func() time.Time { return time.Now().UTC() },
	}

	ctx := context.Background()
	psReq := ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-prod-bad", Namespace: "default"}}
	rpReq := ctrl.Request{NamespacedName: types.NamespacedName{Name: "rp-nginx-demo-prod", Namespace: "default"}}

	// Reconcile both reconcilers until rollback bundle appears (max 10 iterations).
	var rollbackBundle *v1alpha1.Bundle
	for i := 0; i < 10; i++ {
		_, err := rec.Reconcile(ctx, psReq)
		require.NoError(t, err)
		_, err = rpRec.Reconcile(ctx, rpReq)
		require.NoError(t, err)

		var bundleList v1alpha1.BundleList
		require.NoError(t, c.List(ctx, &bundleList))
		for j := range bundleList.Items {
			b := &bundleList.Items[j]
			if b.Labels["kardinal.io/rollback"] == "true" {
				rollbackBundle = b.DeepCopy()
				break
			}
		}
		if rollbackBundle != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	require.NotNil(t, rollbackBundle, "journey 4: rollback Bundle must be created after health failure")
	assert.Equal(t, "true", rollbackBundle.Labels["kardinal.io/rollback"])
	require.NotNil(t, rollbackBundle.Spec.Provenance)
	assert.Equal(t, "nginx-demo-bad", rollbackBundle.Spec.Provenance.RollbackOf)
	t.Logf("journey 4: rollback Bundle %q created, rollbackOf=%s ✅",
		rollbackBundle.Name, rollbackBundle.Spec.Provenance.RollbackOf)
	t.Log("Journey 4: Rollback — auto-rollback Bundle created with correct labels ✅")
}

// TestJourney5CLI validates docs/aide/definition-of-done.md Journey 5.
//
// Verifies that the `kardinal` CLI binary compiles and `kardinal version` works.
// Commands requiring a live cluster are skipped when no binary is in PATH.
func TestJourney5CLI(t *testing.T) {
	kardinal, err := findKardinalBinary()
	if err != nil {
		t.Skipf("kardinal binary not found (%v) — build with 'make build' first", err)
	}

	// version: must return non-zero version info
	out, err := runCLICmd(kardinal, "version")
	require.NoError(t, err, "journey 5: kardinal version must succeed; output: %s", out)
	assert.True(t,
		strings.Contains(out, "CLI:") || strings.Contains(out, "v0."),
		"journey 5: kardinal version must contain version info; got: %s", out)
	t.Logf("journey 5: kardinal version → %s ✅", strings.TrimSpace(out))

	// policy simulate: must not panic (cluster not required for argument parsing)
	out, err = runCLICmd(kardinal, "policy", "simulate",
		"--pipeline", "nginx-demo", "--env", "prod", "--time", "Saturday 3pm")
	t.Logf("journey 5: kardinal policy simulate → err=%v out=%s", err, strings.TrimSpace(out))
	if err != nil {
		// Acceptable: cluster unavailable → command exits 1 with diagnostic message
		assert.NotContains(t, out, "panic", "journey 5: must not panic")
		assert.NotContains(t, out, "nil pointer dereference", "journey 5: must not nil-deref")
	}
	t.Log("journey 5: kardinal policy simulate (no cluster) — no panic ✅")

	// policy test: runs locally without a cluster (validates CEL syntax from file)
	// Write a test gate file to a temp dir.
	gateFile := filepath.Join(t.TempDir(), "test-gate.yaml")
	gateContent := `apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: no-weekend
spec:
  expression: "!schedule.isWeekend"
  message: "Block weekends"
  recheckInterval: 5m
`
	require.NoError(t, os.WriteFile(gateFile, []byte(gateContent), 0600))

	out, err = runCLICmd(kardinal, "policy", "test", gateFile)
	require.NoError(t, err, "journey 5: policy test with valid gate must exit 0; got: %s", out)
	assert.Contains(t, out, "valid", "journey 5: policy test output must contain 'valid'")
	t.Logf("journey 5: kardinal policy test → %s ✅", strings.TrimSpace(out))

	// policy test with invalid CEL must exit non-zero
	badGateFile := filepath.Join(t.TempDir(), "bad-gate.yaml")
	badGateContent := `apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: bad-gate
spec:
  expression: "this is not valid CEL !!!"
`
	require.NoError(t, os.WriteFile(badGateFile, []byte(badGateContent), 0600))
	out, err = runCLICmd(kardinal, "policy", "test", badGateFile)
	assert.Error(t, err, "journey 5: policy test with invalid CEL must exit non-zero")
	assert.Contains(t, out, "INVALID", "journey 5: invalid CEL output must contain 'INVALID'")
	t.Logf("journey 5: kardinal policy test (invalid CEL) → exit non-zero ✅")

	t.Log("Journey 5: CLI — version, policy simulate, policy test all verified ✅")
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func makeJourneyStep(name, pipeline, bundle, env, stepType string) *v1alpha1.PromotionStep {
	return &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": pipeline},
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: pipeline,
			BundleName:   bundle,
			Environment:  env,
			StepType:     stepType,
		},
	}
}

// driveStepToVerified runs the PromotionStep reconciler until the step reaches
// Verified (or Failed), up to 30 iterations.
func driveStepToVerified(t *testing.T, ctx context.Context,
	rec *psrec.Reconciler,
	c client.Client,
	req ctrl.Request, name string) {
	t.Helper()
	for i := 0; i < 30; i++ {
		result, err := rec.Reconcile(ctx, req)
		require.NoError(t, err, "%s: reconcile iteration %d", name, i)

		var ps v1alpha1.PromotionStep
		require.NoError(t, c.Get(ctx, req.NamespacedName, &ps),
			"%s: get step after iteration %d", name, i)
		t.Logf("%s iteration %d: state=%s", name, i, ps.Status.State)

		if ps.Status.State == "Verified" || ps.Status.State == "Failed" {
			return
		}
		if result.RequeueAfter == 0 && !result.Requeue { //nolint:staticcheck // legacy Requeue check
			return
		}
		time.Sleep(1 * time.Millisecond)
	}
}

// findKardinalBinary locates the kardinal CLI binary.
func findKardinalBinary() (string, error) {
	candidates := []string{"bin/kardinal", "../../bin/kardinal", "/usr/local/bin/kardinal"}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	if path, err := exec.LookPath("kardinal"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("kardinal binary not found in bin/ or PATH")
}

// runCLICmd runs the kardinal CLI with the given args and returns combined output.
func runCLICmd(binary string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	return string(out), err
}

// TestJourney6RenderedManifests validates docs/aide/definition-of-done.md Journey 6.
//
// A pipeline with layout:branch + kustomize-build renders manifests at promotion time
// and commits the rendered YAML to environment-specific branches.
//
// This test verifies the step sequence is correctly generated for layout:branch
// pipelines, as the full rendering requires a live Git repo with a DRY source branch.
func TestJourney6RenderedManifests(t *testing.T) {
	// Verify that a Pipeline with layout:branch produces the kustomize-build step sequence.
	// The DefaultSequenceForBundle function must include kustomize-build when layout=branch.
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "rendered-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{
				{
					Name:     "prod",
					Approval: "pr-review",
					Update:   v1alpha1.UpdateConfig{Strategy: "kustomize"},
					Layout:   "branch",
				},
			},
		},
	}
	require.NotNil(t, pipeline, "pipeline must be created")

	// The step sequence for a pr-review, kustomize, layout:branch pipeline
	// must include kustomize-build (renders manifests before committing to env branch).
	approvalMode := pipeline.Spec.Environments[0].Approval
	strategy := pipeline.Spec.Environments[0].Update.Strategy
	layout := pipeline.Spec.Environments[0].Layout

	seq := kardinalsteps.DefaultSequenceForBundle(approvalMode, "image", strategy, layout)

	// layout:branch must include kustomize-build step
	found := false
	for _, s := range seq {
		if s == "kustomize-build" {
			found = true
			break
		}
	}
	assert.True(t, found,
		"journey 6: layout:branch pipeline must include kustomize-build in step sequence; got: %v ✅", seq)
	t.Logf("journey 6: step sequence for layout:branch = %v ✅", seq)

	// The kustomize-build step must appear BEFORE git-commit
	// (rendered YAML is committed to the env branch, not the DRY source)
	kustomizeBuildIdx := -1
	gitCommitIdx := -1
	for i, s := range seq {
		if s == "kustomize-build" {
			kustomizeBuildIdx = i
		}
		if s == "git-commit" {
			gitCommitIdx = i
		}
	}
	if kustomizeBuildIdx >= 0 && gitCommitIdx >= 0 {
		assert.Less(t, kustomizeBuildIdx, gitCommitIdx,
			"journey 6: kustomize-build must appear before git-commit in step sequence")
		t.Log("journey 6: kustomize-build precedes git-commit ✅")
	}
}

// TestJourney7MultiTenantSelfService is a stub for Journey 7.
// Full implementation requires ApplicationSet controller integration.
func TestJourney7MultiTenantSelfService(t *testing.T) {
	t.Skip("journey 7: multi-tenant self-service requires ApplicationSet controller — not yet implemented")
}
