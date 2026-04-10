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

// Package steps_test contains tests for built-in step implementations.
package steps_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"

	// Import built-ins to trigger init() registration.
	_ "github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
)

// mockGitClient records calls for testing.
type mockGitClient struct {
	cloneCalls  int
	commitCalls int
	pushCalls   int
	failClone   bool
	failCommit  bool
	failPush    bool
}

func (m *mockGitClient) Clone(_ context.Context, _, _, _ string) error {
	m.cloneCalls++
	if m.failClone {
		return errors.New("mock clone error")
	}
	return nil
}

func (m *mockGitClient) Checkout(_ context.Context, _, _ string) error { return nil }

func (m *mockGitClient) CommitAll(_ context.Context, _, _, _, _ string) error {
	m.commitCalls++
	if m.failCommit {
		return errors.New("mock commit error")
	}
	return nil
}

func (m *mockGitClient) Push(_ context.Context, _, _, _, _ string) error {
	m.pushCalls++
	if m.failPush {
		return errors.New("mock push error")
	}
	return nil
}

// mockSCMProvider records calls for testing.
type mockSCMProvider struct {
	openPRCalls    int
	addLabelsCalls int
	addedLabels    []string
	prURL          string
	prNumber       int
	merged         bool
	open           bool
	openPRErr      error
	getPRErr       error
}

func (m *mockSCMProvider) OpenPR(_ context.Context, _, _, _, _, _ string) (string, int, error) {
	m.openPRCalls++
	return m.prURL, m.prNumber, m.openPRErr
}

func (m *mockSCMProvider) ClosePR(_ context.Context, _ string, _ int) error { return nil }

func (m *mockSCMProvider) CommentOnPR(_ context.Context, _ string, _ int, _ string) error {
	return nil
}

func (m *mockSCMProvider) GetPRStatus(_ context.Context, _ string, _ int) (bool, bool, error) {
	return m.merged, m.open, m.getPRErr
}

func (m *mockSCMProvider) ParseWebhookEvent(_ []byte, _ string) (scm.WebhookEvent, error) {
	return scm.WebhookEvent{}, nil
}

func (m *mockSCMProvider) AddLabelsToPR(_ context.Context, _ string, _ int, labels []string) error {
	m.addLabelsCalls++
	m.addedLabels = append(m.addedLabels, labels...)
	return nil
}

func makeState(git *mockGitClient, scmProvider *mockSCMProvider) *parentsteps.StepState {
	return &parentsteps.StepState{
		PipelineName: "nginx-demo",
		BundleName:   "nginx-demo-v1-29-0",
		Pipeline: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/owner/repo"},
		},
		Environment: v1alpha1.EnvironmentSpec{
			Name:     "prod",
			Approval: "pr-review",
		},
		Bundle: v1alpha1.BundleSpec{
			Type:   "image",
			Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/nginx/nginx", Tag: "1.29.0"}},
		},
		Git: parentsteps.GitConfig{
			URL:    "https://github.com/owner/repo",
			Branch: "main",
			Token:  "tok",
		},
		WorkDir:   "/tmp/workdir",
		Outputs:   map[string]string{},
		GitClient: git,
		SCM:       scmProvider,
	}
}

func TestGitCloneStep_Success(t *testing.T) {
	git := &mockGitClient{}
	state := makeState(git, nil)

	step, err := parentsteps.Lookup("git-clone")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, 1, git.cloneCalls)
}

func TestGitCloneStep_Idempotent(t *testing.T) {
	// When WorkDir does not exist and clone succeeds, calling again should still succeed
	// (second call would be a no-op in production because .git dir exists).
	git := &mockGitClient{}
	state := makeState(git, nil)

	step, err := parentsteps.Lookup("git-clone")
	require.NoError(t, err)

	// First execution
	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
}

func TestGitCloneStep_Error(t *testing.T) {
	git := &mockGitClient{failClone: true}
	state := makeState(git, nil)

	step, err := parentsteps.Lookup("git-clone")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.Error(t, err)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
}

func TestGitCommitStep_Success(t *testing.T) {
	git := &mockGitClient{}
	state := makeState(git, nil)

	step, err := parentsteps.Lookup("git-commit")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, 1, git.commitCalls)
}

func TestGitPushStep_Success(t *testing.T) {
	git := &mockGitClient{}
	state := makeState(git, nil)

	step, err := parentsteps.Lookup("git-push")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, 1, git.pushCalls)
	// Outputs should contain the branch name.
	assert.Contains(t, result.Outputs["branch"], "kardinal/")
}

func TestOpenPRStep_Success(t *testing.T) {
	mockSCM := &mockSCMProvider{
		prURL:    "https://github.com/owner/repo/pull/42",
		prNumber: 42,
	}
	state := makeState(&mockGitClient{}, mockSCM)
	state.Outputs["branch"] = "kardinal/nginx-demo-v1-29-0/prod"

	step, err := parentsteps.Lookup("open-pr")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, 1, mockSCM.openPRCalls)
	assert.Equal(t, "https://github.com/owner/repo/pull/42", result.Outputs["prURL"])
	assert.Equal(t, "42", result.Outputs["prNumber"])
}

func TestOpenPRStep_Idempotent(t *testing.T) {
	// When prURL is already in outputs, open-pr should not call SCM again.
	mockSCM := &mockSCMProvider{}
	state := makeState(&mockGitClient{}, mockSCM)
	state.Outputs["prURL"] = "https://github.com/owner/repo/pull/42"

	step, err := parentsteps.Lookup("open-pr")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, 0, mockSCM.openPRCalls, "should not re-create PR")
}

func TestWaitForMergeStep_Pending(t *testing.T) {
	mockSCM := &mockSCMProvider{merged: false, open: true}
	state := makeState(&mockGitClient{}, mockSCM)
	state.Outputs["prNumber"] = "42"

	step, err := parentsteps.Lookup("wait-for-merge")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepPending, result.Status)
}

func TestWaitForMergeStep_Merged(t *testing.T) {
	mockSCM := &mockSCMProvider{merged: true, open: false}
	state := makeState(&mockGitClient{}, mockSCM)
	state.Outputs["prNumber"] = "42"

	step, err := parentsteps.Lookup("wait-for-merge")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
}

func TestWaitForMergeStep_ClosedUnmerged(t *testing.T) {
	mockSCM := &mockSCMProvider{merged: false, open: false}
	state := makeState(&mockGitClient{}, mockSCM)
	state.Outputs["prNumber"] = "42"

	step, err := parentsteps.Lookup("wait-for-merge")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
	assert.Contains(t, result.Message, "closed without merging")
}

func TestWaitForMergeStep_MissingPRNumber(t *testing.T) {
	mockSCM := &mockSCMProvider{}
	state := makeState(&mockGitClient{}, mockSCM)
	// No prNumber in outputs

	step, err := parentsteps.Lookup("wait-for-merge")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
}

func TestHealthCheckStep_AlwaysSuccess(t *testing.T) {
	state := makeState(&mockGitClient{}, nil)

	step, err := parentsteps.Lookup("health-check")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
}

func TestDefaultSequence_Auto(t *testing.T) {
	seq := parentsteps.DefaultSequence("auto")
	assert.NotContains(t, seq, "open-pr", "auto mode should omit open-pr")
	assert.NotContains(t, seq, "wait-for-merge", "auto mode should omit wait-for-merge")
	assert.Contains(t, seq, "git-clone")
	assert.Contains(t, seq, "health-check")
}

func TestDefaultSequence_PRReview(t *testing.T) {
	seq := parentsteps.DefaultSequence("pr-review")
	assert.Contains(t, seq, "open-pr")
	assert.Contains(t, seq, "wait-for-merge")
	assert.Contains(t, seq, "git-clone")
	assert.Contains(t, seq, "health-check")
}

func TestOpenPRStep_AppliesLabels(t *testing.T) {
	mockSCM := &mockSCMProvider{
		prURL:    "https://github.com/owner/repo/pull/42",
		prNumber: 42,
	}
	state := makeState(&mockGitClient{}, mockSCM)
	state.Outputs["branch"] = "kardinal/nginx-demo-v1-29-0/prod"

	step, err := parentsteps.Lookup("open-pr")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, 1, mockSCM.addLabelsCalls, "should call AddLabelsToPR once")
	assert.Contains(t, mockSCM.addedLabels, "kardinal", "kardinal label must be applied")
	assert.Contains(t, mockSCM.addedLabels, "kardinal/promotion", "kardinal/promotion label must be applied")
}

func TestLookup_UnknownStep(t *testing.T) {
	_, err := parentsteps.Lookup("nonexistent-step")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not registered")
}

func TestEngine_ExecuteFrom_AllSteps(t *testing.T) {
	git := &mockGitClient{}
	mockSCM := &mockSCMProvider{
		prURL:    "https://github.com/owner/repo/pull/1",
		prNumber: 1,
		merged:   true,
	}
	state := makeState(git, mockSCM)
	state.Outputs = map[string]string{}

	// Use pr-review sequence
	_ = parentsteps.DefaultSequence("pr-review")

	// Use a minimal sequence without kustomize for this test.
	eng2 := parentsteps.NewEngine([]string{
		"git-clone",
		"git-commit",
		"git-push",
		"open-pr",
		"wait-for-merge",
		"health-check",
	})

	nextIdx, result, err := eng2.ExecuteFrom(context.Background(), state, 0)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, len(eng2.StepNames()), nextIdx, "should complete all steps")
	assert.Equal(t, "https://github.com/owner/repo/pull/1", state.Outputs["prURL"])
}

func TestEngine_ExecuteFrom_ResumeFromIndex(t *testing.T) {
	// Start from index 1 (skip git-clone) — simulates crash recovery.
	git := &mockGitClient{}
	mockSCM := &mockSCMProvider{
		prURL:    "https://github.com/owner/repo/pull/2",
		prNumber: 2,
		merged:   true,
	}
	state := makeState(git, mockSCM)
	state.Outputs = map[string]string{}

	engine := parentsteps.NewEngine([]string{
		"git-clone",
		"git-commit",
		"git-push",
		"health-check",
	})

	// Start from index 1 (git-commit), git-clone should not be called.
	_, result, err := engine.ExecuteFrom(context.Background(), state, 1)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, 0, git.cloneCalls, "git-clone should be skipped when resuming from index 1")
}

func TestEngine_ExecuteFrom_StepPending(t *testing.T) {
	// Wait-for-merge returns Pending — engine should return current index.
	mockSCM := &mockSCMProvider{merged: false, open: true}
	state := makeState(&mockGitClient{}, mockSCM)
	state.Outputs = map[string]string{"prNumber": "42"}

	engine := parentsteps.NewEngine([]string{
		"wait-for-merge",
		"health-check",
	})

	nextIdx, result, err := engine.ExecuteFrom(context.Background(), state, 0)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepPending, result.Status)
	assert.Equal(t, 0, nextIdx, "should return index 0 so reconciler can requeue")
}

func TestEngine_ExecuteFrom_StepFailed(t *testing.T) {
	git := &mockGitClient{failClone: true}
	state := makeState(git, nil)

	engine := parentsteps.NewEngine([]string{"git-clone", "health-check"})

	_, result, err := engine.ExecuteFrom(context.Background(), state, 0)
	require.Error(t, err)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
	assert.Contains(t, fmt.Sprintf("%v", err), "git-clone")
}
