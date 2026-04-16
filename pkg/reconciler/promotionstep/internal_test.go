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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractRepo(tc.input)
			assert.Equal(t, tc.expected, got)
		})
	}
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
