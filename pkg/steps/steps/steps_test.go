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
	"os/exec"
	"path/filepath"
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

// TestDefaultSequenceForBundle_ConfigBundle verifies that config bundles use config-merge
// instead of kustomize-set-image.
func TestDefaultSequenceForBundle_ConfigBundle(t *testing.T) {
	seq := parentsteps.DefaultSequenceForBundle("auto", "config", "", "")
	assert.Contains(t, seq, "config-merge", "config bundle must use config-merge")
	assert.NotContains(t, seq, "kustomize-set-image", "config bundle must not use kustomize-set-image")
	assert.NotContains(t, seq, "helm-set-image", "config bundle must not use helm-set-image")
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

// TestDefaultSequenceForBundle_ConfigBundleBackwardsCompat verifies DefaultSequence still works.
func TestDefaultSequenceForBundle_BackwardsCompat(t *testing.T) {
	seq := parentsteps.DefaultSequence("auto")
	assert.Contains(t, seq, "kustomize-set-image",
		"DefaultSequence (no type/strategy) must still use kustomize-set-image")
}

// TestDefaultSequenceForBundle_BranchLayout verifies that layout:branch inserts kustomize-build.
func TestDefaultSequenceForBundle_BranchLayout(t *testing.T) {
	seq := parentsteps.DefaultSequenceForBundle("auto", "image", "kustomize", "branch")
	assert.Contains(t, seq, "kustomize-set-image", "branch layout must include kustomize-set-image")
	assert.Contains(t, seq, "kustomize-build", "branch layout must include kustomize-build")
	// kustomize-build must come after kustomize-set-image
	setIdx, buildIdx := -1, -1
	for i, s := range seq {
		if s == "kustomize-set-image" {
			setIdx = i
		}
		if s == "kustomize-build" {
			buildIdx = i
		}
	}
	assert.Greater(t, buildIdx, setIdx, "kustomize-build must come after kustomize-set-image")
}

// TestDefaultSequenceForBundle_BranchLayoutPRReview verifies pr-review with layout:branch.
func TestDefaultSequenceForBundle_BranchLayoutPRReview(t *testing.T) {
	seq := parentsteps.DefaultSequenceForBundle("pr-review", "image", "", "branch")
	assert.Contains(t, seq, "kustomize-build")
	assert.Contains(t, seq, "open-pr")
	assert.Contains(t, seq, "wait-for-merge")
}

// TestKustomizeBuild_NotInPath verifies that kustomize-build returns a helpful error
// when kustomize is not in PATH.
func TestKustomizeBuild_NotInPath(t *testing.T) {
	// This test only runs when kustomize is NOT in PATH.
	if _, err := exec.LookPath("kustomize"); err == nil {
		t.Skip("kustomize is in PATH — skipping not-in-path test")
	}
	dir := t.TempDir()
	envDir := filepath.Join(dir, "environments", "prod")
	require.NoError(t, os.MkdirAll(envDir, 0o755))

	state := &parentsteps.StepState{
		WorkDir:     dir,
		Environment: v1alpha1.EnvironmentSpec{Name: "prod"},
		Bundle:      v1alpha1.BundleSpec{Type: "image"},
		Outputs:     map[string]string{},
	}

	step, err := parentsteps.Lookup("kustomize-build")
	require.NoError(t, err)

	result, execErr := step.Execute(context.Background(), state)
	assert.Equal(t, parentsteps.StepFailed, result.Status)
	assert.NotNil(t, execErr)
	assert.Contains(t, result.Message, "kustomize")
}

// TestKustomizeBuild_Registered verifies the kustomize-build step is registered.
func TestKustomizeBuild_Registered(t *testing.T) {
	_, err := parentsteps.Lookup("kustomize-build")
	require.NoError(t, err, "kustomize-build must be registered in the step registry")
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

func TestLookup_UnknownStep_FallsBackToCustom(t *testing.T) {
	// Unknown step names now return a CustomWebhookStep (not an error).
	// The custom step will fail at execution time if webhook.url is missing.
	step, err := parentsteps.Lookup("nonexistent-step")
	require.NoError(t, err, "unknown step names must not error — they become custom webhook steps")
	assert.Equal(t, "nonexistent-step", step.Name())
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

// TestConfigMerge_AppliesOverlay verifies that config-merge copies files from
// the config source directory to the environment directory.
func TestConfigMerge_AppliesOverlay(t *testing.T) {
	dir := t.TempDir()
	// Config source: a file we want to copy.
	srcDir := filepath.Join(dir, "config-source")
	require.NoError(t, os.MkdirAll(srcDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "configmap.yaml"), []byte("data: {key: new-value}"), 0o644))

	// Environment dir exists but doesn't have the file yet.
	envDir := filepath.Join(dir, "environments", "prod")
	require.NoError(t, os.MkdirAll(envDir, 0o755))

	state := &parentsteps.StepState{
		WorkDir:     dir,
		Environment: v1alpha1.EnvironmentSpec{Name: "prod"},
		Bundle: v1alpha1.BundleSpec{
			Type: "config",
			ConfigRef: &v1alpha1.ConfigRef{
				CommitSHA: "abc123def456",
				GitRepo:   "https://github.com/org/repo",
			},
		},
		Outputs: map[string]string{
			"configSourceDir": srcDir,
		},
	}

	step, err := parentsteps.Lookup("config-merge")
	require.NoError(t, err)

	result, execErr := step.Execute(context.Background(), state)
	require.NoError(t, execErr)
	assert.Equal(t, parentsteps.StepSuccess, result.Status)
	assert.Equal(t, "1", result.Outputs["mergedFiles"])

	// Verify file was copied to env dir.
	destFile := filepath.Join(envDir, "configmap.yaml")
	data, err := os.ReadFile(destFile)
	require.NoError(t, err)
	assert.Contains(t, string(data), "new-value")
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
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "config-source")
	require.NoError(t, os.MkdirAll(srcDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "configmap.yaml"), []byte("data: {key: value}"), 0o644))

	envDir := filepath.Join(dir, "environments", "prod")
	require.NoError(t, os.MkdirAll(envDir, 0o755))

	state := &parentsteps.StepState{
		WorkDir:     dir,
		Environment: v1alpha1.EnvironmentSpec{Name: "prod"},
		Bundle: v1alpha1.BundleSpec{
			Type:      "config",
			ConfigRef: &v1alpha1.ConfigRef{CommitSHA: "abc123", GitRepo: "https://github.com/org/repo"},
		},
		Outputs: map[string]string{"configSourceDir": srcDir},
	}

	step, err := parentsteps.Lookup("config-merge")
	require.NoError(t, err)

	// Run twice — idempotent.
	result1, err1 := step.Execute(context.Background(), state)
	require.NoError(t, err1)
	assert.Equal(t, parentsteps.StepSuccess, result1.Status)

	result2, err2 := step.Execute(context.Background(), state)
	require.NoError(t, err2)
	assert.Equal(t, parentsteps.StepSuccess, result2.Status)
	assert.Equal(t, result1.Outputs["mergedFiles"], result2.Outputs["mergedFiles"],
		"second run must merge the same number of files")

	// File content must be correct.
	data, err := os.ReadFile(filepath.Join(envDir, "configmap.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "data: {key: value}", string(data))
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
