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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(policyPipeline("nginx-demo", "test")).Build()

	var buf bytes.Buffer
	err := createBundleFn(&buf, c, "default", "nginx-demo", createBundleOptions{Images: []string{"nginx:1.25"}, Type: "image"})
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
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(policyPipeline("nginx-demo", "test")).Build()

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
		err := createBundleFn(&buf, c, "default", "nginx-demo", createBundleOptions{Images: []string{tc.image}, Type: "image"})
		if tc.wantErr {
			require.Error(t, err, "image %q must be rejected", tc.image)
		} else {
			require.NoError(t, err, "image %q must be accepted", tc.image)
		}
	}
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
		ObjectMeta: metav1.ObjectMeta{
			Name: "freeze-nginx-demo", Namespace: "default",
			Labels: map[string]string{"kardinal.io/freeze": "true"},
		},
		Spec: v1alpha1.PolicyGateSpec{Expression: "false"},
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

// TestHistory_RollbackAction (C09b-cli-13): a step of a rollback Bundle shows
// action=rollback. The builder labels PromotionSteps with pipeline, bundle and
// environment only, so the action comes from the Bundle's provenance.
func TestHistory_RollbackAction(t *testing.T) {
	s := cliTestScheme(t)
	stepFor := func(bundle string) *v1alpha1.PromotionStep {
		return &v1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{
				Name:      bundle + "-prod",
				Namespace: "default",
				Labels: map[string]string{
					"kardinal.io/pipeline": "nginx-demo", "kardinal.io/bundle": bundle, "kardinal.io/environment": "prod",
				},
			},
			Spec:   v1alpha1.PromotionStepSpec{PipelineName: "nginx-demo", BundleName: bundle, Environment: "prod", StepType: "open-pr"},
			Status: v1alpha1.PromotionStepStatus{State: "Verified"},
		}
	}
	rollback := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-rollback-x1", Namespace: "default"},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo",
			Provenance: &v1alpha1.BundleProvenance{RollbackOf: "nginx-demo-v1"}},
	}
	labelled := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-rollback-x2", Namespace: "default",
			Labels: map[string]string{"kardinal.io/rollback": "true"}},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
	}
	promote := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
	}
	objs := []sigs_client.Object{rollback, labelled, promote,
		stepFor("nginx-demo-rollback-x1"), stepFor("nginx-demo-rollback-x2"), stepFor("nginx-demo-v1")}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()

	var buf bytes.Buffer
	require.NoError(t, historyFn(&buf, c, "default", "nginx-demo", "", 20))

	actions := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n")[1:] {
		f := strings.Fields(line)
		actions[f[0]] = f[1]
	}
	assert.Equal(t, map[string]string{
		"nginx-demo-rollback-x1": "rollback",
		"nginx-demo-rollback-x2": "rollback",
		"nginx-demo-v1":          "promote",
	}, actions)
}

// TestHistory_Duration (E2E-13 / C09b-cli-13): DURATION is creation to
// Verified (or to the last completed step), not the step's age.
func TestHistory_Duration(t *testing.T) {
	created := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *metav1.Time { t := metav1.NewTime(created.Add(d)); return &t }
	verified := func(d time.Duration) []metav1.Condition {
		return []metav1.Condition{{Type: "Verified", Status: metav1.ConditionTrue, LastTransitionTime: *at(d)}}
	}
	steps := []v1alpha1.StepStatus{
		{Name: "git-clone", CompletedAt: at(10 * time.Second)},
		{Name: "health-check", CompletedAt: at(4 * time.Minute)},
	}
	cases := []struct {
		name   string
		status v1alpha1.PromotionStepStatus
		want   string
	}{
		{"verified condition", v1alpha1.PromotionStepStatus{State: "Verified", Conditions: verified(30 * time.Second), Steps: steps}, "30s"},
		{"verified, steps only", v1alpha1.PromotionStepStatus{State: "Verified", Steps: steps}, "4m"},
		{"failed", v1alpha1.PromotionStepStatus{State: "Failed", Steps: steps[:1]}, "10s"},
		{"verified, no times", v1alpha1.PromotionStepStatus{State: "Verified"}, "--"},
		{"running", v1alpha1.PromotionStepStatus{State: "WaitingForMerge", Steps: steps[:1]}, "..."},
		{"rolling back ended at the alarm", v1alpha1.PromotionStepStatus{State: "RollingBack", Steps: steps}, "4m"},
		{"rolling back, no times", v1alpha1.PromotionStepStatus{State: "RollingBack"}, "--"},
		{"pending", v1alpha1.PromotionStepStatus{}, "..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			step := v1alpha1.PromotionStep{
				ObjectMeta: metav1.ObjectMeta{Name: "s", CreationTimestamp: metav1.NewTime(created)},
				Spec:       v1alpha1.PromotionStepSpec{BundleName: "b", Environment: "prod"},
				Status:     tc.status,
			}
			rows := buildHistoryRows([]v1alpha1.PromotionStep{step}, nil, "", 0)
			require.Len(t, rows, 1)
			assert.Equal(t, tc.want, rows[0].Duration)
		})
	}
}

// TestHistory_Order: rows are newest first, and steps created in the same
// second are ordered by name, whatever order the API returns them in. The
// limit keeps the first rows of that order.
func TestHistory_Order(t *testing.T) {
	created := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	step := func(name string, age time.Duration) v1alpha1.PromotionStep {
		return v1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(created.Add(-age))},
			Spec:       v1alpha1.PromotionStepSpec{BundleName: name, Environment: "prod"},
		}
	}
	steps := []v1alpha1.PromotionStep{
		step("b-old", time.Minute), step("c-new", 0), step("a-new", 0), step("b-new", 0), step("a-old", time.Minute),
	}
	bundles := func(rows []HistoryRow) []string {
		var out []string
		for _, r := range rows {
			out = append(out, r.Bundle)
		}
		return out
	}
	want := []string{"a-new", "b-new", "c-new", "a-old", "b-old"}
	for i := range steps {
		// Every rotation of the input gives the same order.
		rotated := append(append([]v1alpha1.PromotionStep{}, steps[i:]...), steps[:i]...)
		assert.Equal(t, want, bundles(buildHistoryRows(rotated, nil, "", 0)), "input rotated by %d", i)
	}
	assert.Equal(t, want[:2], bundles(buildHistoryRows(steps, nil, "", 2)))
}

// TestSplitImageRef verifies image reference parsing. A digest is returned as
// the digest, never as the tag (C09a-cli-02).
func TestSplitImageRef(t *testing.T) {
	tests := []struct {
		img    string
		repo   string
		tag    string
		digest string
	}{
		{"nginx:1.25", "nginx", "1.25", ""},
		{"ghcr.io/myorg/app:v2.0.0", "ghcr.io/myorg/app", "v2.0.0", ""},
		{"nginx", "nginx", "", ""},
		{"nginx@sha256:abc123", "nginx", "", "sha256:abc123"},
		{"ghcr.io/myorg/app:v2@sha256:abc123", "ghcr.io/myorg/app", "v2", "sha256:abc123"},
		{"registry:5000/app", "registry:5000/app", "", ""},
		{"registry:5000/app:v1", "registry:5000/app", "v1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.img, func(t *testing.T) {
			repo, tag, digest := splitImageRef(tt.img)
			assert.Equal(t, tt.repo, repo)
			assert.Equal(t, tt.tag, tag)
			assert.Equal(t, tt.digest, digest)
		})
	}
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

// TestPolicyTest_MissingFile verifies error on missing file.
func TestPolicyTest_MissingFile(t *testing.T) {
	var buf bytes.Buffer
	err := policyTestFn(&buf, "/nonexistent/path/gate.yaml", time.Now())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent")
}

// TestApprove_IsDeprecatedNoOp covers C09a-cli-03 / E2E-19: approve never
// bypassed anything, so it now fails with a pointer to override and leaves
// the Bundle untouched.
func TestApprove_IsDeprecatedNoOp(t *testing.T) {
	for _, args := range [][]string{{"nginx-demo-v1-29-0", "--env", "prod"}, {"nginx-demo-v1-29-0"}} {
		cmd := newApproveCmd()
		cmd.SetArgs(args)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		err := cmd.Execute()
		require.ErrorIs(t, err, errApproveRemoved)
		assert.Contains(t, err.Error(), "kardinal override")
		assert.NotEmpty(t, cmd.Deprecated)
	}
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

// TestGetBundles_FallbackReturnsItsOwnError (C09b-cli-23): when the
// field-selector list and the unfiltered fallback both fail, the error is the
// fallback's, with the first failure as context.
func TestGetBundles_FallbackReturnsItsOwnError(t *testing.T) {
	calls := 0
	c := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		List: func(context.Context, sigs_client.WithWatch, sigs_client.ObjectList, ...sigs_client.ListOption) error {
			calls++
			if calls == 1 {
				return errors.New("field label not supported: spec.pipeline")
			}
			return errors.New("bundles.kardinal.io is forbidden")
		},
	}).Build()

	err := getBundlesFn(&bytes.Buffer{}, c, "default", []string{"demo"}, false)
	require.Error(t, err)
	assert.Equal(t, "list bundles: bundles.kardinal.io is forbidden (field-selector list: field label not supported: spec.pipeline)",
		err.Error())
}

// TestImageRepoPattern (C09a-cli-04): repositories with a registry port are
// valid. (repo:tag@digest also needs the C09a-cli-02 split, lifecycle area.)
func TestImageRepoPattern(t *testing.T) {
	cases := []struct {
		repo string
		want bool
	}{
		{"nginx", true},
		{"docker.io/library/nginx", true},
		{"ghcr.io/pnz1990/kardinal-test-app", true},
		{"localhost:5000/kardinal-test-app", true},
		{"registry.internal:8443/org/app", true},
		{"my-registry.example.com/team__a/app.v2", true},
		{"not valid @@@", false},
		{"ghcr.io/org/app:v1", false},
		{"ghcr.io/Org/App", false},
		{"-bad", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.repo, func(t *testing.T) {
			assert.Equal(t, tc.want, imageRepoPattern.MatchString(tc.repo))
		})
	}

	c := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithObjects(policyPipeline("demo", "test")).Build()
	var buf bytes.Buffer
	require.NoError(t, createBundleFn(&buf, c, "default", "demo", createBundleOptions{Images: []string{"localhost:5000/kardinal-test-app:sha-abc1234"}, Type: "image"}))
	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundles))
	require.Len(t, bundles.Items, 1)
	assert.Equal(t, v1alpha1.ImageRef{Repository: "localhost:5000/kardinal-test-app", Tag: "sha-abc1234"}, bundles.Items[0].Spec.Images[0])
}

// TestCreateBundle_DryRun_ListsEnvironmentsAndGates (C09a-cli-05): the preview
// lists the Pipeline's environments in order with the gates the controller
// would attach, including org gates from platform-policies.
func TestCreateBundle_DryRun_ListsEnvironmentsAndGates(t *testing.T) {
	c := policyClient(t,
		policyPipeline("demo", "test", "uat", "prod"),
		policyGate("no-weekend-deploys", "platform-policies", "prod", "!schedule.isWeekend", "kardinal.io/scope", "org"),
		policyGate("team-soak", "default", "uat", "upstream.test.soakMinutes >= 5"),
	)
	var buf bytes.Buffer
	require.NoError(t, createBundleDryRun(&buf, c, "default", "demo", createBundleOptions{Images: []string{"ghcr.io/org/app:sha-abc1234"}, Type: "image"}))
	assert.Contains(t, buf.String(), "Environments in promotion order:\n"+
		"  • test\n"+
		"  • uat (gates: team-soak)\n"+
		"  • prod (gates: no-weekend-deploys)\n", buf.String())
}
