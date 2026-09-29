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

package promotionstep

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestExtractRepo verifies that GitHub PR URLs are parsed into "owner/repo" format.
func TestExtractRepo(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "typical GitHub PR URL",
			input:    "https://github.com/myorg/gitops/pull/42",
			expected: "myorg/gitops",
		},
		{
			name:     "http scheme",
			input:    "http://github.com/myorg/gitops/pull/123",
			expected: "myorg/gitops",
		},
		{
			name:     "no pull path",
			input:    "https://github.com/owner/repo",
			expected: "owner/repo",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "only host",
			input:    "https://github.com",
			expected: "",
		},
		{
			name:     "one path segment",
			input:    "https://github.com/owner",
			expected: "",
		},
		{
			name:     "GitHub Enterprise URL",
			input:    "https://github.example.com/corp/platform-gitops/pull/7",
			expected: "corp/platform-gitops",
		},
		// C06-scm-health-03: non-GitHub shapes and git remote URLs.
		{
			name:     "git remote with .git suffix",
			input:    "https://github.com/owner/repo.git",
			expected: "owner/repo",
		},
		{
			name:     "GitLab subgroup MR",
			input:    "https://gitlab.com/group/sub/proj/-/merge_requests/3",
			expected: "group/sub/proj",
		},
		{
			name:     "GitLab subgroup remote",
			input:    "https://gitlab.com/group/sub/proj.git",
			expected: "group/sub/proj",
		},
		{
			name:     "Azure DevOps PR",
			input:    "https://dev.azure.com/org/proj/_git/repo/pullrequest/7",
			expected: "org/proj/repo",
		},
		{
			name:     "Azure DevOps remote",
			input:    "https://dev.azure.com/org/proj/_git/repo",
			expected: "org/proj/repo",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractRepo(tc.input)
			assert.Equal(t, tc.expected, got)
		})
	}
}

// TestExtractPRNumber covers the PR URL shape of every provider
// (C06-scm-health-03).
func TestExtractPRNumber(t *testing.T) {
	cases := []struct {
		input string
		want  int
	}{
		{"https://github.com/o/r/pull/42", 42},
		{"https://gitlab.com/g/s/p/-/merge_requests/3", 3},
		{"https://bitbucket.org/ws/r/pull-requests/5", 5},
		{"https://codeberg.org/o/r/pulls/9", 9},
		{"https://dev.azure.com/org/proj/_git/repo/pullrequest/7", 7},
		{"https://github.com/o/r", 0},
		{"", 0},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, extractPRNumber(tc.input), tc.input)
	}
}

// TestWorkDir_PerPromotionStep proves sibling environments of one Bundle and
// same-named Pipelines in two namespaces get distinct checkouts
// (C05-steps-01, C05-steps-02).
func TestWorkDir_PerPromotionStep(t *testing.T) {
	r := &Reconciler{}
	mk := func(ns, env string) *v1alpha1.PromotionStep {
		return &v1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "app-v2-" + env},
			Spec:       v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "app-v2", Environment: env},
		}
	}
	base := r.workDir(mk("team-a", "prod-eu"))
	assert.NotEqual(t, base, r.workDir(mk("team-a", "prod-us")), "sibling environment")
	assert.NotEqual(t, base, r.workDir(mk("team-b", "prod-eu")), "other namespace")
	assert.Equal(t, base, r.workDir(mk("team-a", "prod-eu")), "stable across reconciles")
}

// TestCleanWorkDir_IgnoresStatusWorkDir proves cleanWorkDir removes the
// computed checkout, not whatever path status.workDir holds.
func TestCleanWorkDir_IgnoresStatusWorkDir(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "victim")
	require.NoError(t, os.MkdirAll(victim, 0o755))
	own := filepath.Join(root, "own")
	require.NoError(t, os.MkdirAll(own, 0o755))

	r := &Reconciler{WorkDirFn: func(_, _ string) string { return own }}
	ps := &v1alpha1.PromotionStep{Status: v1alpha1.PromotionStepStatus{WorkDir: victim}}
	r.cleanWorkDir(zerolog.Nop(), ps)

	assert.DirExists(t, victim, "a tampered status.workDir must not be deleted")
	assert.NoDirExists(t, own)
}

// TestAppendCondition_NewCondition verifies that a new condition is appended.
func TestAppendCondition_NewCondition(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	conditions := appendCondition(nil, "Ready", metav1.ConditionTrue, "Verified", "all steps done", now)
	require.Len(t, conditions, 1)
	assert.Equal(t, "Ready", conditions[0].Type)
	assert.Equal(t, metav1.ConditionTrue, conditions[0].Status)
	assert.Equal(t, "Verified", conditions[0].Reason)
	assert.Equal(t, "all steps done", conditions[0].Message)
}

// TestAppendCondition_UpdatesExisting verifies that an existing condition is updated.
func TestAppendCondition_UpdatesExisting(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	existing := []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Pending", Message: "not started"},
	}
	updated := appendCondition(existing, "Ready", metav1.ConditionTrue, "Verified", "done", now)
	require.Len(t, updated, 1, "existing condition must be updated, not duplicated")
	assert.Equal(t, metav1.ConditionTrue, updated[0].Status)
	assert.Equal(t, "Verified", updated[0].Reason)
	assert.Equal(t, "done", updated[0].Message)
}

// TestAppendCondition_MultipleConditions verifies that distinct conditions accumulate.
func TestAppendCondition_MultipleConditions(t *testing.T) {
	now := time.Now()
	var conditions []metav1.Condition
	conditions = appendCondition(conditions, "Ready", metav1.ConditionFalse, "Pending", "waiting", now)
	conditions = appendCondition(conditions, "Failed", metav1.ConditionTrue, "StepError", "git push failed", now)
	require.Len(t, conditions, 2)
	assert.Equal(t, "Ready", conditions[0].Type)
	assert.Equal(t, "Failed", conditions[1].Type)
}

// TestAppendCondition_UnknownObservedGeneration verifies that ObservedGeneration defaults to 0.
func TestAppendCondition_ObservedGeneration(t *testing.T) {
	now := time.Now()
	conditions := appendCondition(nil, "Ready", metav1.ConditionTrue, "Verified", "ok", now)
	assert.Equal(t, int64(0), conditions[0].ObservedGeneration)
}

// TestInitStepStatuses verifies that all steps start as Pending.
func TestInitStepStatuses(t *testing.T) {
	seq := []string{"git-clone", "kustomize-set-image", "open-pr"}
	ss := initStepStatuses(seq)

	require.Len(t, ss, 3)
	for i, s := range ss {
		assert.Equal(t, seq[i], s.Name, "step %d name", i)
		assert.Equal(t, v1alpha1.StepExecutionPending, s.State, "step %d state", i)
		assert.Nil(t, s.StartedAt, "step %d startedAt", i)
		assert.Nil(t, s.CompletedAt, "step %d completedAt", i)
	}
}

// TestUpdateStepStatuses_CurrentStepInProgress verifies that the active step
// is marked InProgress and prior steps are Completed.
func TestUpdateStepStatuses_CurrentStepInProgress(t *testing.T) {
	seq := []string{"git-clone", "open-pr", "health-check"}
	ps := &v1alpha1.PromotionStep{
		Status: v1alpha1.PromotionStepStatus{
			Steps: initStepStatuses(seq),
		},
	}

	// ExecuteFrom returns nextIdx=1 (step 0 ran, step 1 is now in progress)
	updateStepStatuses(ps, seq, 1, false, "")

	assert.Equal(t, v1alpha1.StepExecutionCompleted, ps.Status.Steps[0].State, "step 0 must be Completed")
	assert.Equal(t, v1alpha1.StepExecutionInProgress, ps.Status.Steps[1].State, "step 1 must be InProgress")
	assert.Equal(t, v1alpha1.StepExecutionPending, ps.Status.Steps[2].State, "step 2 must be Pending")
	assert.NotNil(t, ps.Status.Steps[1].StartedAt, "InProgress step must have startedAt")
}

// TestUpdateStepStatuses_Failed verifies that the failing step is marked Failed.
func TestUpdateStepStatuses_Failed(t *testing.T) {
	seq := []string{"git-clone", "open-pr"}
	ps := &v1alpha1.PromotionStep{
		Status: v1alpha1.PromotionStepStatus{
			Steps: initStepStatuses(seq),
		},
	}

	// ExecuteFrom returned nextIdx=1, failed=true (step 1 failed)
	updateStepStatuses(ps, seq, 1, true, "push rejected")

	assert.Equal(t, v1alpha1.StepExecutionCompleted, ps.Status.Steps[0].State, "step 0 must be Completed")
	assert.Equal(t, v1alpha1.StepExecutionFailed, ps.Status.Steps[1].State, "step 1 must be Failed")
	assert.Equal(t, "push rejected", ps.Status.Steps[1].Message, "failure message propagated")
	assert.NotNil(t, ps.Status.Steps[1].CompletedAt, "Failed step must have completedAt")
}

// TestUpdateStepStatuses_AllComplete verifies all steps are Completed when nextIdx==len(seq).
func TestUpdateStepStatuses_AllComplete(t *testing.T) {
	seq := []string{"git-clone", "open-pr"}
	ps := &v1alpha1.PromotionStep{
		Status: v1alpha1.PromotionStepStatus{
			Steps: initStepStatuses(seq),
		},
	}

	// ExecuteFrom returned nextIdx=2 (all complete)
	updateStepStatuses(ps, seq, 2, false, "")

	for i, s := range ps.Status.Steps {
		assert.Equal(t, v1alpha1.StepExecutionCompleted, s.State, "step %d must be Completed", i)
	}
}

// TestUpdateStepStatuses_Idempotent verifies that calling updateStepStatuses again
// does not flip a Completed step back to another state.
func TestUpdateStepStatuses_Idempotent(t *testing.T) {
	seq := []string{"git-clone", "open-pr"}
	ps := &v1alpha1.PromotionStep{
		Status: v1alpha1.PromotionStepStatus{
			Steps: initStepStatuses(seq),
		},
	}

	// First call: step 0 completed, step 1 in progress.
	updateStepStatuses(ps, seq, 1, false, "")
	firstCompletedAt := ps.Status.Steps[0].CompletedAt

	// Second call with same index: step 0 must stay Completed with same timestamp.
	updateStepStatuses(ps, seq, 1, false, "")
	assert.Equal(t, v1alpha1.StepExecutionCompleted, ps.Status.Steps[0].State, "idempotent: step 0 stays Completed")
	assert.Equal(t, firstCompletedAt, ps.Status.Steps[0].CompletedAt, "idempotent: completedAt unchanged")
}

// newTestScheme creates a runtime.Scheme with all required types registered.
func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	return scheme
}

// TestPRStatusMapper verifies that prStatusMapper re-enqueues the PromotionStep
// that owns the changed PRStatus. This validates the Watch registration logic
// added in #644 to eliminate polling in the WaitingForMerge state.
func TestPRStatusMapper(t *testing.T) {
	scheme := newTestScheme(t)
	ctx := context.Background()

	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-1", Namespace: "default"},
		Spec:       v1alpha1.PromotionStepSpec{PRStatusRef: "prstatus-1"},
	}
	otherStep := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-2", Namespace: "default"},
		Spec:       v1alpha1.PromotionStepSpec{PRStatusRef: "prstatus-other"},
	}
	prs := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: "prstatus-1", Namespace: "default"},
	}

	fakeClient := fakeclient.NewClientBuilder().WithScheme(scheme).
		WithObjects(step, otherStep, prs).
		Build()

	r := &Reconciler{Client: fakeClient}
	reqs := r.prStatusMapper(ctx, prs)

	require.Len(t, reqs, 1, "must enqueue exactly the owning PromotionStep")
	assert.Equal(t, "step-1", reqs[0].Name)
	assert.Equal(t, "default", reqs[0].Namespace)
}

// TestPRStatusMapper_UnmatchedPRStatus verifies that a PRStatus with no owning
// PromotionStep produces no re-enqueue requests.
func TestPRStatusMapper_UnmatchedPRStatus(t *testing.T) {
	scheme := newTestScheme(t)
	ctx := context.Background()

	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-1", Namespace: "default"},
		Spec:       v1alpha1.PromotionStepSpec{PRStatusRef: "prstatus-different"},
	}
	prs := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: "prstatus-1", Namespace: "default"},
	}

	fakeClient := fakeclient.NewClientBuilder().WithScheme(scheme).
		WithObjects(step, prs).
		Build()

	r := &Reconciler{Client: fakeClient}
	reqs := r.prStatusMapper(ctx, prs)
	assert.Empty(t, reqs, "no PromotionStep owns this PRStatus")
}

// TestPolicyGateMapper verifies that policyGateMapper re-enqueues all
// PromotionSteps in the same namespace as the changed PolicyGate.
func TestPolicyGateMapper(t *testing.T) {
	scheme := newTestScheme(t)
	ctx := context.Background()

	step1 := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-1", Namespace: "default"},
	}
	step2 := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-2", Namespace: "default"},
	}
	otherNSStep := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-3", Namespace: "other-ns"},
	}
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "gate-1", Namespace: "default"},
	}

	fakeClient := fakeclient.NewClientBuilder().WithScheme(scheme).
		WithObjects(step1, step2, otherNSStep, gate).
		Build()

	r := &Reconciler{Client: fakeClient}
	reqs := r.policyGateMapper(ctx, gate)

	// Must enqueue only steps in the same namespace
	require.Len(t, reqs, 2, "must enqueue all PromotionSteps in the same namespace")
	names := map[string]bool{}
	for _, req := range reqs {
		assert.Equal(t, "default", req.Namespace)
		names[req.Name] = true
	}
	assert.True(t, names["step-1"])
	assert.True(t, names["step-2"])
	assert.False(t, names["step-3"], "steps in other namespaces must not be enqueued")
}

// TestCollectGateResults verifies the PR body gets one row per required gate,
// named after the template, with Pass/Fail from status.ready, and that an
// unreadable gate is skipped rather than failing the step.
func TestCollectGateResults(t *testing.T) {
	scheme := newTestScheme(t)
	evaluated := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	pass := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name: "require-uat-soak-platform-policies-prod--app-abc", Namespace: "default",
			Labels: map[string]string{"kardinal.io/gate-name": "require-uat-soak"},
		},
		Status: v1alpha1.PolicyGateStatus{Ready: true, Reason: "soak >= 30 = true", LastEvaluatedAt: &evaluated},
	}
	fail := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "no-weekend--app-abc", Namespace: "default"},
		Status:     v1alpha1.PolicyGateStatus{Ready: false, Reason: "isWeekend"},
	}
	c := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(pass, fail).Build()
	r := &Reconciler{Client: c}

	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "app-abc-prod", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{RequiredGates: []string{
			pass.Name, fail.Name, "missing-gate",
		}},
	}
	got := r.collectGateResults(context.Background(), zerolog.Nop(), ps)
	require.Len(t, got, 2)
	assert.Equal(t, "require-uat-soak", got[0].GateName)
	assert.Equal(t, "Pass", got[0].Result)
	assert.True(t, evaluated.Time.Equal(got[0].EvaluatedAt.Time), "evaluatedAt must come from the gate")
	assert.Equal(t, "no-weekend--app-abc", got[1].GateName)
	assert.Equal(t, "Fail", got[1].Result)
	assert.Equal(t, "isWeekend", got[1].Reason)
	assert.False(t, got[1].EvaluatedAt.IsZero())

	assert.Nil(t, r.collectGateResults(context.Background(), zerolog.Nop(),
		&v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Namespace: "default"}}))
}

func TestUpstreamEnvironments(t *testing.T) {
	b := &v1alpha1.Bundle{Status: v1alpha1.BundleStatus{Environments: []v1alpha1.EnvironmentStatus{
		{Name: "test", Phase: "Verified"}, {Name: "uat", Phase: "Verified"}, {Name: "prod", Phase: "Pending"},
	}}}
	got := upstreamEnvironments(b, "prod")
	require.Len(t, got, 2)
	assert.Equal(t, "test", got[0].Name)
	assert.Equal(t, "uat", got[1].Name)
	assert.Nil(t, upstreamEnvironments(nil, "prod"))
}
