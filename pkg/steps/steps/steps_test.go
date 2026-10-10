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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"

	// Import built-ins to trigger init() registration.
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
)

// mockGitClient records calls for testing.
type mockGitClient struct {
	cloneCalls   int
	cloneAtCalls int
	commitCalls  int
	pushCalls    int
	cloneURL     string
	cloneToken   string
	cloneDir     string
	cloneAtURL   string
	cloneAtSHA   string
	cloneAtDir   string
	cloneAtToken string
	cloneAtAuth  scm.GitAuth
	cloneAuth    scm.GitAuth
	pushAuth     scm.GitAuth
	pushBranch   string
	pushForce    bool
	failClone    bool
	failCommit   bool
	failPush     bool
	// cloneErr, commitErr and pushErrs override the fail* flags. pushErrs is
	// consumed one element per Push call.
	cloneErr   error
	cloneAtErr error
	commitErr  error
	pushErrs   []error
}

func (m *mockGitClient) Clone(_ context.Context, url, _, dir string, auth scm.GitAuth) error {
	token := auth.Token
	m.cloneCalls++
	m.cloneURL, m.cloneDir, m.cloneToken, m.cloneAuth = url, dir, token, auth
	if m.cloneErr != nil {
		return m.cloneErr
	}
	if m.failClone {
		return errors.New("mock clone error")
	}
	return os.MkdirAll(dir, 0o755)
}

func (m *mockGitClient) CloneAt(_ context.Context, url, sha, dir string, auth scm.GitAuth) error {
	token := auth.Token
	m.cloneAtCalls++
	m.cloneAtURL, m.cloneAtSHA, m.cloneAtDir, m.cloneAtToken, m.cloneAtAuth = url, sha, dir, token, auth
	if m.cloneAtErr != nil {
		return m.cloneAtErr
	}
	return os.MkdirAll(dir, 0o755)
}

func (m *mockGitClient) CommitAll(_ context.Context, _, _, _, _ string) error {
	m.commitCalls++
	if m.commitErr != nil {
		return m.commitErr
	}
	if m.failCommit {
		return errors.New("mock commit error")
	}
	return nil
}

func (m *mockGitClient) Push(_ context.Context, _, _, branch string, auth scm.GitAuth, force bool) error {
	m.pushCalls++
	m.pushAuth = auth
	m.pushBranch = branch
	m.pushForce = force
	if len(m.pushErrs) > 0 {
		err := m.pushErrs[0]
		m.pushErrs = m.pushErrs[1:]
		return err
	}
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
	labelsErr      error
	// repos records the repository argument of every OpenPR, GetPRStatus
	// and AddLabelsToPR call.
	repos []string
	// titles, bodies and heads record the title, body and head branch
	// arguments of every OpenPR call.
	titles []string
	heads  []string
	bodies []string
}

func (m *mockSCMProvider) OpenPR(_ context.Context, repo, title, body, head, _ string) (string, int, error) {
	m.openPRCalls++
	m.repos = append(m.repos, repo)
	m.titles = append(m.titles, title)
	m.bodies = append(m.bodies, body)
	m.heads = append(m.heads, head)
	return m.prURL, m.prNumber, m.openPRErr
}

func (m *mockSCMProvider) ClosePR(_ context.Context, _ string, _ int) error { return nil }

func (m *mockSCMProvider) CommentOnPR(_ context.Context, _ string, _ int, _ string) error {
	return nil
}

func (m *mockSCMProvider) GetPRStatus(_ context.Context, repo string, _ int) (bool, bool, error) {
	m.repos = append(m.repos, repo)
	return m.merged, m.open, m.getPRErr
}

func (m *mockSCMProvider) GetPRReviewStatus(_ context.Context, _ string, _ int) (bool, int, error) {
	return false, 0, nil
}

func (m *mockSCMProvider) ParseWebhookEvent(_ []byte, _ string) (scm.WebhookEvent, error) {
	return scm.WebhookEvent{}, nil
}

func (m *mockSCMProvider) AddLabelsToPR(_ context.Context, repo string, _ int, labels []string) error {
	m.addLabelsCalls++
	m.repos = append(m.repos, repo)
	m.addedLabels = append(m.addedLabels, labels...)
	return m.labelsErr
}

func makeState(t *testing.T, git *mockGitClient, scmProvider *mockSCMProvider) *parentsteps.StepState {
	t.Helper()
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
		Sequence: parentsteps.DefaultSequenceForBundle("pr-review", "image", "", ""),
		Git: parentsteps.GitConfig{
			URL:    "https://github.com/owner/repo",
			Branch: "main",
			Token:  "tok",
		},
		WorkDir:   filepath.Join(t.TempDir(), "work"),
		Outputs:   map[string]string{},
		GitClient: git,
		SCM:       scmProvider,
	}
}

func TestGitCloneStep_Success(t *testing.T) {
	git := &mockGitClient{}
	state := makeState(t, git, nil)

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
	state := makeState(t, git, nil)

	step, err := parentsteps.Lookup("git-clone")
	require.NoError(t, err)

	// First execution
	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
}

func TestGitCloneStep_Error(t *testing.T) {
	git := &mockGitClient{failClone: true}
	state := makeState(t, git, nil)

	step, err := parentsteps.Lookup("git-clone")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.Error(t, err)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
}

func TestGitCommitStep_Success(t *testing.T) {
	git := &mockGitClient{}
	state := makeState(t, git, nil)

	step, err := parentsteps.Lookup("git-commit")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, 1, git.commitCalls)
}

// TestGitCommitStep_NoChangesOutput: a clean tree is a no-op promotion
// (noChanges=true), except when HEAD is this promotion's own commit from an
// earlier attempt (ErrAlreadyCommitted), which is a real change.
func TestGitCommitStep_NoChangesOutput(t *testing.T) {
	tests := []struct {
		name     string
		approval string
		err      error
		want     string
	}{
		{"committed", "auto", nil, "false"},
		{"nothing to commit", "auto", scm.ErrNothingToCommit, "true"},
		{"committed by an earlier attempt, direct push", "auto", scm.ErrAlreadyCommitted, "false"},
		// A pr-review step cloned the base branch: the commit got there through
		// its merged PR, and a PR with no commits cannot be opened.
		{"same commit on the base branch, pr-review", "pr-review", scm.ErrAlreadyCommitted, "true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := makeState(t, &mockGitClient{commitErr: tt.err}, nil)
			state.Environment.Approval = tt.approval
			state.Sequence = parentsteps.DefaultSequenceForBundle(tt.approval, "image", "", "")
			step, err := parentsteps.Lookup("git-commit")
			require.NoError(t, err)
			result, err := step.Execute(context.Background(), state)
			require.NoError(t, err)
			assert.Equal(t, parentsteps.StepSuccess, result.Status)
			assert.Equal(t, tt.want, result.Outputs["noChanges"])
		})
	}
}

func TestGitPushStep_Success(t *testing.T) {
	git := &mockGitClient{}
	state := makeState(t, git, nil)

	step, err := parentsteps.Lookup("git-push")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, 1, git.pushCalls)
	// Outputs should contain the branch name.
	assert.Contains(t, result.Outputs["branch"], "kardinal/")
}

// An auto environment pushes straight to the base branch; there is no PR.
func TestGitPushStep_AutoPushesBaseBranch(t *testing.T) {
	git := &mockGitClient{}
	state := makeState(t, git, nil)
	state.Environment.Approval = "auto"
	state.Sequence = parentsteps.DefaultSequenceForBundle("auto", "image", "", "")

	step, err := parentsteps.Lookup("git-push")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, "main", git.pushBranch)
	assert.Equal(t, "main", result.Outputs["branch"])
}

func TestOpenPRStep_Success(t *testing.T) {
	mockSCM := &mockSCMProvider{
		prURL:    "https://github.com/owner/repo/pull/42",
		prNumber: 42,
	}
	state := makeState(t, &mockGitClient{}, mockSCM)
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
	state := makeState(t, &mockGitClient{}, mockSCM)
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
	state := makeState(t, &mockGitClient{}, mockSCM)
	state.Outputs["prNumber"] = "42"

	step, err := parentsteps.Lookup("wait-for-merge")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepPending, result.Status)
}

func TestWaitForMergeStep_Merged(t *testing.T) {
	mockSCM := &mockSCMProvider{merged: true, open: false}
	state := makeState(t, &mockGitClient{}, mockSCM)
	state.Outputs["prNumber"] = "42"

	step, err := parentsteps.Lookup("wait-for-merge")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
}

func TestWaitForMergeStep_ClosedUnmerged(t *testing.T) {
	mockSCM := &mockSCMProvider{merged: false, open: false}
	state := makeState(t, &mockGitClient{}, mockSCM)
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
	state := makeState(t, &mockGitClient{}, mockSCM)
	// No prNumber in outputs

	step, err := parentsteps.Lookup("wait-for-merge")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
}

func TestHealthCheckStep_AlwaysSuccess(t *testing.T) {
	state := makeState(t, &mockGitClient{}, nil)

	step, err := parentsteps.Lookup("health-check")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.NotContains(t, result.Message, "Stage", "stale stage message (C05-steps-37)")
	assert.Contains(t, result.Message, "PromotionStep reconciler")
}

func TestDefaultSequence_Auto(t *testing.T) {
	seq := parentsteps.DefaultSequenceForBundle("auto", "", "", "")
	assert.NotContains(t, seq, "open-pr", "auto mode should omit open-pr")
	assert.NotContains(t, seq, "wait-for-merge", "auto mode should omit wait-for-merge")
	assert.Contains(t, seq, "git-clone")
	assert.Contains(t, seq, "health-check")
}

func TestDefaultSequence_PRReview(t *testing.T) {
	seq := parentsteps.DefaultSequenceForBundle("pr-review", "", "", "")
	assert.Contains(t, seq, "open-pr")
	assert.Contains(t, seq, "wait-for-merge")
	assert.Contains(t, seq, "git-clone")
	assert.Contains(t, seq, "health-check")
}

// TestDefaultSequenceForBundle_ConfigBundle verifies that config bundles use config-merge
// instead of kustomize-set-image.
func TestDefaultSequenceForBundle_ConfigBundle(t *testing.T) {
	seq := parentsteps.DefaultSequenceForBundle("auto", "config", "", "")
	assert.Contains(t, seq, "config-merge", "config bundle must use config-merge")
	assert.NotContains(t, seq, "kustomize-set-image", "config bundle must not use kustomize-set-image")
	assert.NotContains(t, seq, "helm-set-image", "config bundle must not use helm-set-image")
}

// TestDefaultSequenceForBundle_MixedBundle verifies that a mixed Bundle
// merges its config commit and then updates its images in one sequence:
// before, it ran the image sequence only and its config change was dropped.
func TestDefaultSequenceForBundle_MixedBundle(t *testing.T) {
	tests := []struct {
		approval, strategy string
		want               []string
	}{
		{"auto", "", []string{"git-clone", "config-merge", "kustomize-set-image", "git-commit", "git-push", "health-check"}},
		{"auto", "helm", []string{"git-clone", "config-merge", "helm-set-image", "git-commit", "git-push", "health-check"}},
		{"pr-review", "kustomize", []string{"git-clone", "config-merge", "kustomize-set-image", "git-commit", "git-push",
			"open-pr", "wait-for-merge", "health-check"}},
		// argocd refuses mixed Bundles in argocd-set-image (#1281).
		{"auto", "argocd", []string{"argocd-set-image", "health-check"}},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, parentsteps.DefaultSequenceForBundle(tc.approval, "mixed", tc.strategy, ""),
			"approval=%q strategy=%q", tc.approval, tc.strategy)
	}
}

// TestMixedBundle_MergesConfigThenImages runs the update steps of a mixed
// Bundle's sequence over one work tree: the config commit's files are merged,
// and the Bundle's image wins over the older pin in the config commit's
// kustomization.yaml.
func TestMixedBundle_MergesConfigThenImages(t *testing.T) {
	workDir, srcDir := configMergeFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "environments", "prod", "kustomization.yaml"),
		[]byte("resources:\n- deployment.yaml\n- configmap.yaml\nimages:\n- name: ghcr.io/nginx/nginx\n  newTag: 1.27.0\n"), 0o644))
	state := configMergeState(workDir, srcDir)
	state.Bundle.Type = "mixed"
	state.Bundle.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/nginx/nginx", Tag: "1.29.0"}}

	seq := parentsteps.DefaultSequenceForBundle("auto", "mixed", "kustomize", "")
	require.Equal(t, "git-clone", seq[0])
	for _, name := range seq[1:] {
		if name == "git-commit" {
			break
		}
		res, err := runStep(t, name, state)
		require.NoError(t, err, name)
		require.Equal(t, parentsteps.StepSuccess, res.Status, "%s: %s", name, res.Message)
	}

	data, err := os.ReadFile(filepath.Join(workDir, "environments", "prod", "configmap.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "new-value", "the config change is merged")
	kust, err := os.ReadFile(filepath.Join(workDir, "environments", "prod", "kustomization.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(kust), "configmap.yaml", "the config commit's kustomization.yaml is merged")
	assert.Contains(t, string(kust), "newTag: 1.29.0", "the Bundle's image wins")
	assert.NotContains(t, string(kust), "1.27.0")
}

// TestDefaultSequenceForBundle_HelmStrategy verifies that helm update strategy uses helm-set-image.
func TestDefaultSequenceForBundle_HelmStrategy(t *testing.T) {
	seq := parentsteps.DefaultSequenceForBundle("auto", "image", "helm", "")
	assert.Contains(t, seq, "helm-set-image", "helm strategy must use helm-set-image")
	assert.NotContains(t, seq, "kustomize-set-image", "helm strategy must not use kustomize-set-image")
	assert.NotContains(t, seq, "config-merge", "image+helm must not use config-merge")
}

// TestDefaultSequenceForBundle_KustomizeDefault verifies that default (no type, no strategy) is kustomize.
func TestDefaultSequenceForBundle_KustomizeDefault(t *testing.T) {
	seq := parentsteps.DefaultSequenceForBundle("auto", "", "", "")
	assert.Contains(t, seq, "kustomize-set-image", "default must use kustomize-set-image")
}

// TestDefaultSequenceForBundle_BranchLayout: a layout: branch step waits for
// its RenderRun (the render step), then opens its PR or checks health; the
// render Job runs the clone, the image update, render-manifests, git-commit
// and git-push, and a config Bundle runs no config-merge (its configRef
// commit is the DRY commit rendered).
func TestDefaultSequenceForBundle_BranchLayout(t *testing.T) {
	assert.Equal(t, []string{"render", "health-check"}, parentsteps.DefaultSequenceForBundle("auto", "image", "kustomize", "branch"))
	assert.Equal(t, []string{"render", "open-pr", "wait-for-merge", "health-check"},
		parentsteps.DefaultSequenceForBundle("pr-review", "image", "helm", "branch"))
	assert.Equal(t, []string{"git-clone", "kustomize-set-image", "render-manifests", "git-commit", "git-push"},
		parentsteps.RenderJobSequence("image", "kustomize"))
	assert.Equal(t, []string{"git-clone", "helm-set-image", "render-manifests", "git-commit", "git-push"},
		parentsteps.RenderJobSequence("mixed", "helm"))
	assert.Equal(t, []string{"git-clone", "render-manifests", "git-commit", "git-push"},
		parentsteps.RenderJobSequence("config", ""))
}

// TestRenderManifests_Registered: the render step is registered.
func TestRenderManifests_Registered(t *testing.T) {
	_, err := parentsteps.Lookup("render-manifests")
	require.NoError(t, err)
}

func TestOpenPRStep_AppliesLabels(t *testing.T) {
	mockSCM := &mockSCMProvider{
		prURL:    "https://github.com/owner/repo/pull/42",
		prNumber: 42,
	}
	state := makeState(t, &mockGitClient{}, mockSCM)
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

// TestOpenPRStep_RollbackBundleAppliesRollbackLabel verifies that when a Bundle
// has Provenance.RollbackOf set (it is a rollback), the open-pr step applies the
// 'kardinal/rollback' label to the PR. This is required by issue #402 and
// docs/rollback.md — rollback PRs must be distinguishable from promotion PRs.
func TestOpenPRStep_RollbackBundleAppliesRollbackLabel(t *testing.T) {
	mockSCM := &mockSCMProvider{
		prURL:    "https://github.com/owner/repo/pull/7",
		prNumber: 7,
	}
	state := makeState(t, &mockGitClient{}, mockSCM)
	state.Outputs["branch"] = "kardinal/nginx-demo-v1-29-0/prod"
	// Mark bundle as a rollback: Provenance.RollbackOf set.
	state.Bundle.Provenance = &v1alpha1.BundleProvenance{
		RollbackOf: "nginx-demo-v1-29-0",
		Author:     "ci",
	}

	step, err := parentsteps.Lookup("open-pr")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, 1, mockSCM.addLabelsCalls, "should call AddLabelsToPR once")
	// Rollback PRs must have kardinal/rollback label (#402)
	assert.Contains(t, mockSCM.addedLabels, "kardinal/rollback",
		"rollback bundle must add kardinal/rollback label to PR")
	assert.Contains(t, mockSCM.addedLabels, "kardinal",
		"rollback PR must still have base kardinal label")
}

// TestOpenPRStep_NormalBundleDoesNotHaveRollbackLabel verifies that a normal
// (non-rollback) promotion PR does NOT get the kardinal/rollback label.
func TestOpenPRStep_NormalBundleDoesNotHaveRollbackLabel(t *testing.T) {
	mockSCM := &mockSCMProvider{
		prURL:    "https://github.com/owner/repo/pull/8",
		prNumber: 8,
	}
	state := makeState(t, &mockGitClient{}, mockSCM)
	state.Outputs["branch"] = "kardinal/nginx-demo-v1-30-0/prod"
	// Normal bundle: Provenance without RollbackOf.
	state.Bundle.Provenance = &v1alpha1.BundleProvenance{
		Author:    "ci",
		CommitSHA: "abc123",
	}

	step, err := parentsteps.Lookup("open-pr")
	require.NoError(t, err)

	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.NotContains(t, mockSCM.addedLabels, "kardinal/rollback",
		"normal promotion PR must NOT have kardinal/rollback label")
	assert.Contains(t, mockSCM.addedLabels, "kardinal/promotion",
		"normal promotion PR must have kardinal/promotion label")
}

// TestOpenPRStep_RollbackTitleAndLabels verifies the rollback PR title keeps
// the documented shape "[kardinal] Rollback <environment> to <rollback
// bundle> (restores <target>)", where <target> is the version the rollback
// deploys (the restored tag), not a Bundle name. Only a rollback without
// artifacts falls back to the restored Bundle's name. Rollback PRs carry
// kardinal/rollback in addition to kardinal/promotion, as docs/rollback.md and
// docs/pr-evidence.md say (E2E-R06).
func TestOpenPRStep_RollbackTitleAndLabels(t *testing.T) {
	nginx := []v1alpha1.ImageRef{{Repository: "ghcr.io/nginx/nginx", Tag: "1.29.0"}}
	configRef := &v1alpha1.ConfigRef{GitRepo: "https://github.com/owner/config", CommitSHA: "c0ffee1234567890"}
	tests := []struct {
		name       string
		bundle     string
		rollbackOf string
		bundleType string
		images     []v1alpha1.ImageRef
		configRef  *v1alpha1.ConfigRef
		wantTitle  string
		wantLabels []string
	}{
		{
			name:       "promotion",
			bundle:     "kardinal-test-app-b5mt9",
			images:     nginx,
			wantTitle:  "[kardinal] Promote kardinal-test-app-b5mt9 to prod",
			wantLabels: []string{"kardinal", "kardinal/promotion"},
		},
		{
			name:       "rollback names the restored tag",
			bundle:     "kardinal-test-app-rollback-bkgwk",
			rollbackOf: "kardinal-test-app-dq92z",
			images:     nginx,
			wantTitle:  "[kardinal] Rollback prod to kardinal-test-app-rollback-bkgwk (restores 1.29.0)",
			wantLabels: []string{"kardinal", "kardinal/promotion", "kardinal/rollback"},
		},
		{
			name:       "config rollback names the config commit",
			bundle:     "kardinal-test-app-rollback-bkgwk",
			rollbackOf: "kardinal-test-app-dq92z",
			bundleType: "config",
			configRef:  configRef,
			wantTitle:  "[kardinal] Rollback prod to kardinal-test-app-rollback-bkgwk (restores config c0ffee1)",
			wantLabels: []string{"kardinal", "kardinal/promotion", "kardinal/rollback"},
		},
		{
			name:       "mixed rollback names the tag and the config commit (B60)",
			bundle:     "kardinal-test-app-rollback-bkgwk",
			rollbackOf: "kardinal-test-app-dq92z",
			bundleType: "mixed",
			images:     nginx,
			configRef:  configRef,
			wantTitle:  "[kardinal] Rollback prod to kardinal-test-app-rollback-bkgwk (restores 1.29.0 with config c0ffee1)",
			wantLabels: []string{"kardinal", "kardinal/promotion", "kardinal/rollback"},
		},
		{
			name:       "rollback without artifacts names the restored bundle",
			bundle:     "kardinal-test-app-rollback-bkgwk",
			rollbackOf: "kardinal-test-app-dq92z",
			wantTitle:  "[kardinal] Rollback prod to kardinal-test-app-rollback-bkgwk (restores kardinal-test-app-dq92z)",
			wantLabels: []string{"kardinal", "kardinal/promotion", "kardinal/rollback"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockSCM := &mockSCMProvider{prURL: "https://github.com/owner/repo/pull/28", prNumber: 28}
			state := makeState(t, &mockGitClient{}, mockSCM)
			state.BundleName = tt.bundle
			state.Outputs["branch"] = "kardinal/" + tt.bundle + "/prod"
			if tt.bundleType != "" {
				state.Bundle.Type = tt.bundleType
			}
			state.Bundle.Images = tt.images
			state.Bundle.ConfigRef = tt.configRef
			state.Bundle.Provenance = &v1alpha1.BundleProvenance{RollbackOf: tt.rollbackOf, Author: "ci"}

			step, err := parentsteps.Lookup("open-pr")
			require.NoError(t, err)
			result, err := step.Execute(context.Background(), state)
			require.NoError(t, err)
			require.Equal(t, parentsteps.StepSuccess, result.Status)

			require.Len(t, mockSCM.titles, 1)
			assert.Equal(t, tt.wantTitle, mockSCM.titles[0])
			assert.NotContains(t, mockSCM.titles[0], "reverts")
			assert.ElementsMatch(t, tt.wantLabels, mockSCM.addedLabels)
		})
	}
}

// TestOpenPRStep_RollbackBody checks that the rollback PR body names the
// Bundle and version the rollback replaces (FROM), the Bundle and version it
// restores (TO) and who asked for it (Rolled back by: the verified creator,
// else the requested-by annotation marked unverified), from the StepState the
// reconciler fills in, and that the provenance Author is the restored build's
// author (spike bug 6).
func TestOpenPRStep_RollbackBody(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*parentsteps.StepState)
		wantNote string
	}{
		{
			name: "replaced bundle, versions and actor",
			setup: func(s *parentsteps.StepState) {
				s.RollbackFrom = "nginx-demo-v1-30-0"
				s.RollbackFromBundle = &v1alpha1.BundleSpec{Type: "image",
					Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/nginx/nginx", Tag: "1.30.0"}}}
				s.CreatedBy = "alice"
				s.RequestedBy = "mallory"
			},
			wantNote: "> **This is a rollback PR.** It restores the images of bundle nginx-demo-v1-29-0 in environment prod.\n" +
				"> Rolling back FROM: nginx-demo-v1-30-0 (1.30.0)\n" +
				"> Rolling back TO: nginx-demo-v1-29-0 (1.29.0)\n" +
				"> Rolled back by: alice\n",
		},
		{
			name: "replaced bundle deleted",
			setup: func(s *parentsteps.StepState) {
				s.RollbackFrom = "nginx-demo-v1-30-0"
				s.CreatedBy = "kardinal-controller"
				s.RequestedBy = "kardinal-controller (auto-rollback via RollbackPolicy)"
			},
			wantNote: "> Rolling back FROM: nginx-demo-v1-30-0\n" +
				"> Rolling back TO: nginx-demo-v1-29-0 (1.29.0)\n" +
				"> Rolled back by: kardinal-controller\n",
		},
		{
			name: "only the unverified requester",
			setup: func(s *parentsteps.StepState) {
				s.RequestedBy = "bob"
			},
			wantNote: "> Rolling back TO: nginx-demo-v1-29-0 (1.29.0)\n" +
				"> Rolled back by: bob (unverified)\n",
		},
		{
			name:  "nothing recorded",
			setup: func(*parentsteps.StepState) {},
			wantNote: "> Rolling back FROM: the bundle deployed in prod now\n" +
				"> Rolling back TO: nginx-demo-v1-29-0 (1.29.0)\n\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockSCM := &mockSCMProvider{prURL: "https://github.com/owner/repo/pull/29", prNumber: 29}
			state := makeState(t, &mockGitClient{}, mockSCM)
			state.BundleName = "nginx-demo-rollback-bkgwk"
			state.Outputs["branch"] = "kardinal/nginx-demo-rollback-bkgwk/prod"
			state.Bundle.Provenance = &v1alpha1.BundleProvenance{
				RollbackOf: "nginx-demo-v1-29-0", Author: "ci-bot", CommitSHA: "abc1234",
			}
			tt.setup(state)

			step, err := parentsteps.Lookup("open-pr")
			require.NoError(t, err)
			result, err := step.Execute(context.Background(), state)
			require.NoError(t, err)
			require.Equal(t, parentsteps.StepSuccess, result.Status)

			require.Len(t, mockSCM.bodies, 1)
			body := mockSCM.bodies[0]
			assert.Contains(t, body, "## ROLLBACK: nginx-demo-rollback-bkgwk -> nginx-demo/prod\n")
			assert.Contains(t, body, tt.wantNote)
			assert.NotContains(t, body, "copy of")
			assert.Equal(t, strings.Contains(tt.wantNote, "Rolled back by"), strings.Contains(body, "Rolled back by"))
			assert.Contains(t, body, "| ghcr.io/nginx/nginx | 1.29.0 | — | — | abc1234 | ci-bot |",
				"the provenance Author is the restored build's author")
			assert.NotContains(t, body, "Requested by:", "a rollback names its actor once, as Rolled back by")
			assert.NotContains(t, body, "Created by:")
		})
	}
}

// TestOpenPRStep_CreatedBy checks that a promotion PR names who created the
// Bundle under the provenance table (#1581): the verified creator
// (kardinal.io/created-by, pinned by admission) as "Created by"; without one,
// the client-written kardinal.io/requested-by, marked unverified; and that a
// hostile name cannot add Markdown structure.
func TestOpenPRStep_CreatedBy(t *testing.T) {
	tests := []struct {
		name        string
		createdBy   string
		requestedBy string
		want        string
	}{
		{name: "verified creator", createdBy: "alice@example.com",
			want: "\n\nCreated by: alice@example.com\n"},
		{name: "verified creator wins over the requester", createdBy: "alice@example.com", requestedBy: "mallory",
			want: "\n\nCreated by: alice@example.com\n"},
		{name: "requester only", requestedBy: "bob",
			want: "\n\nRequested by: bob (unverified)\n"},
		{name: "hostile creator", createdBy: "mallory\n## Approved",
			want: "\n\nCreated by: mallory ## Approved\n"},
		{name: "nothing recorded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockSCM := &mockSCMProvider{prURL: "https://github.com/owner/repo/pull/30", prNumber: 30}
			state := makeState(t, &mockGitClient{}, mockSCM)
			state.CreatedBy = tt.createdBy
			state.RequestedBy = tt.requestedBy

			step, err := parentsteps.Lookup("open-pr")
			require.NoError(t, err)
			result, err := step.Execute(context.Background(), state)
			require.NoError(t, err)
			require.Equal(t, parentsteps.StepSuccess, result.Status)

			require.Len(t, mockSCM.bodies, 1)
			body := mockSCM.bodies[0]
			if tt.want == "" {
				assert.NotContains(t, body, "Created by")
				assert.NotContains(t, body, "Requested by")
				return
			}
			assert.Contains(t, body, tt.want)
			assert.Equal(t, 1, strings.Count(body, " by: "), "one creator line")
			assert.NotContains(t, body, "\n## Approved")
		})
	}
}

// TestLookup proves only built-in steps resolve. An unknown name is a
// permanent error instead of a custom webhook step (#1282), and the removed
// verify-image and integration-test steps are no longer registered.
func TestLookup(t *testing.T) {
	tests := []struct {
		name    string
		wantErr string
	}{
		{name: "git-clone"},
		{name: "kustomize-set-image"},
		{name: "health-check"},
		{name: "nonexistent-step", wantErr: `unknown step "nonexistent-step"`},
		{name: "verify-image", wantErr: `unknown step "verify-image"`},
		{name: "integration-test", wantErr: `unknown step "integration-test"`},
		{name: "", wantErr: "empty step name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			step, err := parentsteps.Lookup(tt.name)
			if tt.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, tt.name, step.Name())
				return
			}
			require.Error(t, err)
			assert.Nil(t, step)
			assert.EqualError(t, err, tt.wantErr)
			assert.ErrorIs(t, err, parentsteps.ErrPermanent, "retrying cannot fix an unknown step name")
		})
	}
}

// TestDefaultSequencesUseRegisteredSteps: Lookup no longer turns an unknown
// name into a webhook, so every step a default sequence names must be
// registered, for every approval, bundle type, strategy and layout.
func TestDefaultSequencesUseRegisteredSteps(t *testing.T) {
	for _, approval := range []string{"", "auto", "pr-review"} {
		for _, bundleType := range []string{"", "image", "config", "mixed"} {
			for _, strategy := range []string{"", "kustomize", "helm", "argocd"} {
				for _, layout := range []string{"", "directory", "branch"} {
					for _, name := range parentsteps.DefaultSequenceForBundle(approval, bundleType, strategy, layout) {
						_, err := parentsteps.Lookup(name)
						assert.NoError(t, err, "approval=%q type=%q strategy=%q layout=%q", approval, bundleType, strategy, layout)
					}
				}
			}
		}
	}
}

// TestEngine_UnknownStepFailsWithoutExecuting proves that the engine stops at
// an unknown step name with a permanent error instead of executing anything
// for it: before #1282 the name became a webhook call. The health-check step
// before it runs as usual.
func TestEngine_UnknownStepFailsWithoutExecuting(t *testing.T) {
	state := &parentsteps.StepState{Outputs: map[string]string{}}
	next, result, err := parentsteps.NewEngine([]string{"health-check", "my-webhook"}).
		ExecuteFrom(context.Background(), state, 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, parentsteps.ErrPermanent)
	assert.Contains(t, err.Error(), `unknown step "my-webhook"`)
	assert.Equal(t, 1, next, "the engine stops at the unknown step")
	assert.Equal(t, parentsteps.StepFailed, result.Status)
}

func TestEngine_ExecuteFrom_AllSteps(t *testing.T) {
	git := &mockGitClient{}
	mockSCM := &mockSCMProvider{
		prURL:    "https://github.com/owner/repo/pull/1",
		prNumber: 1,
		merged:   true,
	}
	state := makeState(t, git, mockSCM)
	state.Outputs = map[string]string{}

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
	state := makeState(t, git, mockSCM)
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
	state := makeState(t, &mockGitClient{}, mockSCM)
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
	state := makeState(t, git, nil)

	engine := parentsteps.NewEngine([]string{"git-clone", "health-check"})

	_, result, err := engine.ExecuteFrom(context.Background(), state, 0)
	require.Error(t, err)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
	assert.Contains(t, fmt.Sprintf("%v", err), "git-clone")
}

// ─── Helm set image tests ────────────────────────────────────────────────────

// TestHelmSetImage_UpdatesTagInValues verifies that helm-set-image writes the
// correct image tag to values.yaml.
func TestHelmSetImage_UpdatesTagInValues(t *testing.T) {
	dir := t.TempDir()
	// Create environment dir and values.yaml.
	envDir := filepath.Join(dir, "environments", "prod")
	require.NoError(t, os.MkdirAll(envDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "values.yaml"), []byte("image:\n  tag: \"1.28.0\"\n"), 0o644))

	state := &parentsteps.StepState{
		WorkDir: dir,
		Environment: v1alpha1.EnvironmentSpec{
			Name:   "prod",
			Update: v1alpha1.UpdateConfig{Strategy: "helm"},
		},
		Bundle: v1alpha1.BundleSpec{
			Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/nginx/nginx", Tag: "1.29.0"}},
		},
		Outputs: map[string]string{},
	}

	step, err := parentsteps.Lookup("helm-set-image")
	require.NoError(t, err)

	result, execErr := step.Execute(context.Background(), state)
	require.NoError(t, execErr)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, "1.29.0", result.Outputs["imageTag"])

	// Verify values.yaml was updated.
	raw, err := os.ReadFile(filepath.Join(envDir, "values.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "1.29.0")
	assert.NotContains(t, string(raw), "1.28.0")
}

// TestHelmSetImage_CustomPath verifies that a custom imagePathTemplate is respected.
func TestHelmSetImage_CustomPath(t *testing.T) {
	dir := t.TempDir()
	envDir := filepath.Join(dir, "environments", "staging")
	require.NoError(t, os.MkdirAll(envDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "values.yaml"), []byte("app:\n  version: \"old\"\n"), 0o644))

	state := &parentsteps.StepState{
		WorkDir: dir,
		Environment: v1alpha1.EnvironmentSpec{
			Name: "staging",
			Update: v1alpha1.UpdateConfig{
				Strategy: "helm",
				Helm:     &v1alpha1.HelmUpdateConfig{ImagePathTemplate: ".app.version"},
			},
		},
		Bundle: v1alpha1.BundleSpec{
			Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/myorg/app", Tag: "v2.0.0"}},
		},
		Outputs: map[string]string{},
	}

	step, err := parentsteps.Lookup("helm-set-image")
	require.NoError(t, err)

	result, execErr := step.Execute(context.Background(), state)
	require.NoError(t, execErr)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)

	raw, err := os.ReadFile(filepath.Join(envDir, "values.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "v2.0.0")
}

// TestHelmSetImage_Idempotent verifies that running the step twice produces the same result.
func TestHelmSetImage_Idempotent(t *testing.T) {
	dir := t.TempDir()
	envDir := filepath.Join(dir, "environments", "test")
	require.NoError(t, os.MkdirAll(envDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "values.yaml"), []byte("image:\n  tag: \"1.28.0\"\n"), 0o644))

	state := &parentsteps.StepState{
		WorkDir:     dir,
		Environment: v1alpha1.EnvironmentSpec{Name: "test", Update: v1alpha1.UpdateConfig{Strategy: "helm"}},
		Bundle:      v1alpha1.BundleSpec{Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/nginx/nginx", Tag: "1.29.0"}}},
		Outputs:     map[string]string{},
	}

	step, err := parentsteps.Lookup("helm-set-image")
	require.NoError(t, err)

	// Run twice.
	_, err = step.Execute(context.Background(), state)
	require.NoError(t, err)
	result, err := step.Execute(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)

	raw, err := os.ReadFile(filepath.Join(envDir, "values.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "1.29.0")
}

// ─── Config merge tests ──────────────────────────────────────────────────────

// configMergeFixture builds a work tree and a config source checkout the way
// git-clone does: the source is parentsteps.ConfigSourceDir(workDir), outside
// the work tree.
func configMergeFixture(t *testing.T) (workDir, srcDir string) {
	t.Helper()
	workDir = filepath.Join(t.TempDir(), "work")
	srcDir = parentsteps.ConfigSourceDir(workDir)
	for path, content := range map[string]string{
		filepath.Join(workDir, "kustomization.yaml"):                         "resources:\n- environments/prod\n",
		filepath.Join(workDir, "environments", "prod", "kustomization.yaml"): "resources:\n- deployment.yaml\n",
		filepath.Join(workDir, "environments", "test", "kustomization.yaml"): "resources:\n- deployment.yaml\n",
		filepath.Join(srcDir, ".git", "HEAD"):                                "ref: refs/heads/main\n",
		filepath.Join(srcDir, "kustomization.yaml"):                          "resources:\n- environments/prod\n",
		filepath.Join(srcDir, "environments", "prod", "configmap.yaml"):      "data: {key: new-value}",
		filepath.Join(srcDir, "environments", "prod", "sub", "extra.yaml"):   "extra: true",
		filepath.Join(srcDir, "environments", "prod", ".git", "config"):      "nested git dir",
		filepath.Join(srcDir, "environments", "test", "configmap.yaml"):      "data: {key: test-value}",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	return workDir, srcDir
}

func configMergeState(workDir, srcDir string) *parentsteps.StepState {
	return &parentsteps.StepState{
		WorkDir:     workDir,
		Environment: v1alpha1.EnvironmentSpec{Name: "prod"},
		Bundle: v1alpha1.BundleSpec{
			Type:      "config",
			ConfigRef: &v1alpha1.ConfigRef{CommitSHA: "abc123def456", GitRepo: "https://github.com/org/repo"},
		},
		Outputs: map[string]string{"configSourceDir": srcDir},
	}
}

// TestConfigMerge_AppliesOverlay verifies that config-merge copies only the
// environment's subtree of the config commit, never .git, the repo root or a
// sibling environment (C05-steps-03, C13b-design-01).
func TestConfigMerge_AppliesOverlay(t *testing.T) {
	workDir, srcDir := configMergeFixture(t)

	step, err := parentsteps.Lookup("config-merge")
	require.NoError(t, err)
	result, execErr := step.Execute(context.Background(), configMergeState(workDir, srcDir))
	require.NoError(t, execErr)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, "2", result.Outputs["mergedFiles"])

	data, err := os.ReadFile(filepath.Join(workDir, "environments", "prod", "configmap.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "new-value")
	_, err = os.Stat(filepath.Join(workDir, "environments", "prod", "sub", "extra.yaml"))
	assert.NoError(t, err)

	// Nothing outside the env subtree, and no .git, was copied.
	for _, p := range []string{
		filepath.Join("environments", "prod", ".git"),
		filepath.Join("environments", "prod", "environments"),
		filepath.Join("environments", "prod", "kustomization.yaml.orig"),
		filepath.Join("environments", "test", "configmap.yaml"),
		".git",
	} {
		_, err := os.Stat(filepath.Join(workDir, p))
		assert.True(t, os.IsNotExist(err), "%s must not exist", p)
	}
	kust, err := os.ReadFile(filepath.Join(workDir, "environments", "prod", "kustomization.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "resources:\n- deployment.yaml\n", string(kust), "the env kustomization must not be replaced by the root one")
}

// TestConfigMerge_RejectsUnsafeSource verifies config-merge fails instead of
// copying the work tree into itself (C05-steps-03) or escaping it
// (C05-steps-13).
func TestConfigMerge_RejectsUnsafeSource(t *testing.T) {
	workDir, srcDir := configMergeFixture(t)
	cases := []struct {
		name    string
		mutate  func(s *parentsteps.StepState)
		wantMsg string
	}{
		{"config source not checked out", func(s *parentsteps.StepState) { s.WorkDir = filepath.Join(workDir, "environments") },
			"git-clone must run before config-merge"},
		{"env path climbs out", func(s *parentsteps.StepState) { s.Environment.Path = "../escape" }, "must stay inside"},
		{"absolute env path", func(s *parentsteps.StepState) { s.Environment.Path = "/etc" }, "must be relative"},
		{"env subtree missing in commit", func(s *parentsteps.StepState) { s.Environment.Name = "uat" }, "has no directory environments/uat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := configMergeState(workDir, srcDir)
			tc.mutate(state)
			step, err := parentsteps.Lookup("config-merge")
			require.NoError(t, err)
			result, execErr := step.Execute(context.Background(), state)
			assert.Error(t, execErr)
			assert.Equal(t, parentsteps.StepFailed, result.Status)
			assert.Contains(t, result.Message, tc.wantMsg)
		})
	}

	t.Run("symlink in source is not followed", func(t *testing.T) {
		workDir, srcDir := configMergeFixture(t)
		secret := filepath.Join(t.TempDir(), "secret.txt")
		require.NoError(t, os.WriteFile(secret, []byte("s3cr3t"), 0o600))
		require.NoError(t, os.Symlink(secret, filepath.Join(srcDir, "environments", "prod", "leak.yaml")))
		step, err := parentsteps.Lookup("config-merge")
		require.NoError(t, err)
		result, execErr := step.Execute(context.Background(), configMergeState(workDir, srcDir))
		require.NoError(t, execErr)
		assert.Equal(t, parentsteps.StepSuccess, result.Status)
		assert.Contains(t, result.Message, "skipped 1")
		_, statErr := os.Lstat(filepath.Join(workDir, "environments", "prod", "leak.yaml"))
		assert.True(t, os.IsNotExist(statErr))
	})
}

// TestConfigMerge_IgnoresStatusSourceDir proves the config source path is
// derived from the work dir, never read from Outputs["configSourceDir"]:
// outputs are restored from PromotionStep status, which the step must not
// trust as a filesystem path (C05-steps-03).
func TestConfigMerge_IgnoresStatusSourceDir(t *testing.T) {
	foreign := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(foreign, "environments", "prod"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(foreign, "environments", "prod", "evil.yaml"), []byte("evil: true"), 0o644))

	tests := []struct {
		name   string
		output func(workDir string) (string, bool)
	}{
		{"output not set", func(string) (string, bool) { return "", false }},
		{"output points at a foreign directory", func(string) (string, bool) { return foreign, true }},
		{"output points at the work tree", func(wd string) (string, bool) { return wd, true }},
		{"output points inside the work tree", func(wd string) (string, bool) { return filepath.Join(wd, "environments"), true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workDir, _ := configMergeFixture(t)
			state := configMergeState(workDir, "")
			delete(state.Outputs, "configSourceDir")
			if v, ok := tc.output(workDir); ok {
				state.Outputs["configSourceDir"] = v
			}
			step, err := parentsteps.Lookup("config-merge")
			require.NoError(t, err)
			result, execErr := step.Execute(context.Background(), state)
			require.NoError(t, execErr)
			assert.Equal(t, parentsteps.StepSuccess, result.Status)
			assert.Equal(t, "2", result.Outputs["mergedFiles"])

			data, err := os.ReadFile(filepath.Join(workDir, "environments", "prod", "configmap.yaml"))
			require.NoError(t, err)
			assert.Contains(t, string(data), "new-value", "copied from ConfigSourceDir(workDir)")
			assert.NoFileExists(t, filepath.Join(workDir, "environments", "prod", "evil.yaml"))
		})
	}
}

// TestConfigMerge_NoConfigRef verifies that config-merge is a no-op when
// Bundle.configRef is nil.
func TestConfigMerge_NoConfigRef(t *testing.T) {
	dir := t.TempDir()
	state := &parentsteps.StepState{
		WorkDir:     dir,
		Environment: v1alpha1.EnvironmentSpec{Name: "prod"},
		Bundle:      v1alpha1.BundleSpec{Type: "image"}, // no ConfigRef
		Outputs:     map[string]string{},
	}

	step, err := parentsteps.Lookup("config-merge")
	require.NoError(t, err)

	result, execErr := step.Execute(context.Background(), state)
	require.NoError(t, execErr)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Contains(t, result.Message, "no config ref")
}

// TestConfigMerge_Idempotent verifies that running config-merge twice on the
// same source produces the same result (no duplicate or corrupted files).
func TestConfigMerge_Idempotent(t *testing.T) {
	workDir, srcDir := configMergeFixture(t)
	state := configMergeState(workDir, srcDir)

	step, err := parentsteps.Lookup("config-merge")
	require.NoError(t, err)

	result1, err1 := step.Execute(context.Background(), state)
	require.NoError(t, err1)
	assert.Equal(t, parentsteps.StepSuccess, result1.Status)

	result2, err2 := step.Execute(context.Background(), state)
	require.NoError(t, err2)
	assert.Equal(t, parentsteps.StepSuccess, result2.Status)
	assert.Equal(t, result1.Outputs["mergedFiles"], result2.Outputs["mergedFiles"],
		"second run must merge the same number of files")

	data, err := os.ReadFile(filepath.Join(workDir, "environments", "prod", "configmap.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "data: {key: new-value}", string(data))
}

// TestKustomizeSetImageStep_NoImages verifies that an empty bundle Images list
// returns StepSuccess with "no images to update" without calling kustomize.
func TestKustomizeSetImageStep_NoImages(t *testing.T) {
	step, err := parentsteps.Lookup("kustomize-set-image")
	require.NoError(t, err, "kustomize-set-image step must be registered")

	state := &parentsteps.StepState{
		Bundle:  v1alpha1.BundleSpec{Type: "image"}, // no Images → empty
		WorkDir: t.TempDir(),
	}
	result, execErr := step.Execute(context.Background(), state)
	require.NoError(t, execErr)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, "no images to update", result.Message,
		"empty images list must return 'no images to update' without calling kustomize")
}

// TestPromoteMessage: the promotion commit names the Bundle, environment,
// Pipeline and namespace.
func TestPromoteMessage(t *testing.T) {
	assert.Equal(t, "[kardinal] Promote app-1 to prod\n\nBundle: app-1\nPipeline: app\nNamespace: team-a",
		steps.PromoteMessage("app-1", "prod", "app", "team-a"))
	assert.Equal(t, "[kardinal] Promote app-1 to prod\n\nBundle: app-1\nPipeline: app",
		steps.PromoteMessage("app-1", "prod", "app", ""), "no namespace line without one")
}
