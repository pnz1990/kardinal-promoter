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

package steps_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
)

func runStep(t *testing.T, name string, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	t.Helper()
	step, err := parentsteps.Lookup(name)
	require.NoError(t, err)
	return step.Execute(context.Background(), state)
}

// TestGitCloneStep_Hardening covers the clone fixes: a fresh clone every run
// (C05-steps-12), the token passed to the client and never in a message
// (C05-steps-08, C05-steps-26), the config source clone for config and mixed
// Bundles (C05-steps-03) and the loud layout: branch rejection (C05-steps-10).
func TestGitCloneStep_Hardening(t *testing.T) {
	const secretURL = "https://x-access-token:ghp_SECRET@github.com/owner/repo"
	tests := []struct {
		name  string
		setup func(state *parentsteps.StepState, git *mockGitClient)
		check func(t *testing.T, state *parentsteps.StepState, git *mockGitClient, res parentsteps.StepResult, err error)
	}{
		{
			name: "stale work dir is removed before cloning",
			setup: func(state *parentsteps.StepState, _ *mockGitClient) {
				require.NoError(t, os.MkdirAll(filepath.Join(state.WorkDir, ".git"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(state.WorkDir, "stale.yaml"), []byte("x"), 0o644))
			},
			check: func(t *testing.T, state *parentsteps.StepState, git *mockGitClient, res parentsteps.StepResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, parentsteps.StepSuccess, res.Status)
				assert.Equal(t, 1, git.cloneCalls, "an existing .git must not skip the clone")
				assert.NoFileExists(t, filepath.Join(state.WorkDir, "stale.yaml"))
			},
		},
		{
			name: "token goes to the client, not into the URL",
			check: func(t *testing.T, state *parentsteps.StepState, git *mockGitClient, res parentsteps.StepResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, state.Git.URL, git.cloneURL)
				assert.Equal(t, "tok", git.cloneToken)
				assert.NotContains(t, res.Message, "tok@")
			},
		},
		{
			name: "clone error is redacted",
			setup: func(state *parentsteps.StepState, git *mockGitClient) {
				state.Git.URL = secretURL
				git.cloneErr = fmt.Errorf("authentication required for %s", secretURL)
			},
			check: func(t *testing.T, _ *parentsteps.StepState, _ *mockGitClient, res parentsteps.StepResult, err error) {
				require.Error(t, err)
				assert.Equal(t, parentsteps.StepFailed, res.Status)
				assert.NotContains(t, res.Message, "ghp_SECRET")
				assert.NotContains(t, err.Error(), "ghp_SECRET")
				assert.Contains(t, res.Message, "https://github.com/owner/repo")
			},
		},
		// An error that url.Parse takes for a relative URL (no colon before
		// its first slash) is redacted as text, not percent-encoded with the
		// credentials kept.
		{
			name: "clone error is redacted as text",
			setup: func(_ *parentsteps.StepState, git *mockGitClient) {
				git.cloneErr = errors.New("access to owner/repo is blocked; see " + secretURL)
			},
			check: func(t *testing.T, _ *parentsteps.StepState, _ *mockGitClient, res parentsteps.StepResult, err error) {
				require.Error(t, err)
				assert.Equal(t, "access to owner/repo is blocked; see https://github.com/owner/repo", res.Message)
				assert.Equal(t, res.Message, err.Error())
			},
		},
		{
			name: "config source clone error is redacted as text",
			setup: func(state *parentsteps.StepState, git *mockGitClient) {
				state.Bundle.Type = "config"
				state.Bundle.ConfigRef = &v1alpha1.ConfigRef{GitRepo: "https://github.com/org/config", CommitSHA: "abc123"}
				git.cloneAtErr = errors.New("access to org/config is blocked; see " + secretURL)
			},
			check: func(t *testing.T, _ *parentsteps.StepState, _ *mockGitClient, res parentsteps.StepResult, err error) {
				require.Error(t, err)
				assert.Equal(t, "config source: access to org/config is blocked; see https://github.com/owner/repo", res.Message)
				assert.Equal(t, res.Message, err.Error())
			},
		},
		{
			name: "config bundle checks out configRef next to the work dir",
			setup: func(state *parentsteps.StepState, _ *mockGitClient) {
				state.Bundle.Type = "config"
				state.Bundle.ConfigRef = &v1alpha1.ConfigRef{GitRepo: "https://github.com/org/config", CommitSHA: "abc123"}
			},
			check: func(t *testing.T, state *parentsteps.StepState, git *mockGitClient, res parentsteps.StepResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, 1, git.cloneAtCalls)
				assert.Equal(t, "https://github.com/org/config", git.cloneAtURL)
				assert.Equal(t, "abc123", git.cloneAtSHA)
				assert.Equal(t, parentsteps.ConfigSourceDir(state.WorkDir), git.cloneAtDir)
				assert.Equal(t, "tok", git.cloneAtToken, "same origin: the pipeline token is used")
				assert.Equal(t, git.cloneAtDir, res.Outputs["configSourceDir"])
			},
		},
		{
			name: "mixed bundle checks out configRef too",
			setup: func(state *parentsteps.StepState, _ *mockGitClient) {
				state.Bundle.Type = "mixed"
				state.Bundle.ConfigRef = &v1alpha1.ConfigRef{GitRepo: "https://github.com/org/config", CommitSHA: "abc123"}
			},
			check: func(t *testing.T, state *parentsteps.StepState, git *mockGitClient, res parentsteps.StepResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, 1, git.cloneAtCalls)
				assert.Equal(t, "abc123", git.cloneAtSHA)
				assert.Equal(t, parentsteps.ConfigSourceDir(state.WorkDir), git.cloneAtDir)
				assert.Equal(t, git.cloneAtDir, res.Outputs["configSourceDir"])
			},
		},
		{
			name: "image bundle never checks out a configRef",
			setup: func(state *parentsteps.StepState, _ *mockGitClient) {
				state.Bundle.ConfigRef = &v1alpha1.ConfigRef{GitRepo: "https://github.com/org/config", CommitSHA: "abc123"}
			},
			check: func(t *testing.T, _ *parentsteps.StepState, git *mockGitClient, res parentsteps.StepResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, 0, git.cloneAtCalls)
				assert.Empty(t, res.Outputs["configSourceDir"])
			},
		},
		{
			name: "config source on another host gets no token",
			setup: func(state *parentsteps.StepState, _ *mockGitClient) {
				state.Bundle.Type = "config"
				state.Bundle.ConfigRef = &v1alpha1.ConfigRef{GitRepo: "https://evil.example/org/config", CommitSHA: "abc123"}
			},
			check: func(t *testing.T, _ *parentsteps.StepState, git *mockGitClient, _ parentsteps.StepResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, 1, git.cloneAtCalls)
				assert.Empty(t, git.cloneAtToken)
			},
		},
		{
			name: "config source over plain http gets no token",
			setup: func(state *parentsteps.StepState, _ *mockGitClient) {
				state.Bundle.Type = "config"
				state.Bundle.ConfigRef = &v1alpha1.ConfigRef{GitRepo: "http://github.com/owner/config", CommitSHA: "abc123"}
			},
			check: func(t *testing.T, _ *parentsteps.StepState, git *mockGitClient, _ parentsteps.StepResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, 1, git.cloneAtCalls)
				assert.Empty(t, git.cloneAtToken)
			},
		},
		{
			name: "config source on another port gets no token",
			setup: func(state *parentsteps.StepState, _ *mockGitClient) {
				state.Bundle.Type = "config"
				state.Bundle.ConfigRef = &v1alpha1.ConfigRef{GitRepo: "https://github.com:8443/owner/config", CommitSHA: "abc123"}
			},
			check: func(t *testing.T, _ *parentsteps.StepState, git *mockGitClient, _ parentsteps.StepResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, 1, git.cloneAtCalls)
				assert.Empty(t, git.cloneAtToken)
			},
		},
		{
			name: "ssh pipeline: the ssh key goes to the clone and to a config source on the same ssh host only",
			setup: func(state *parentsteps.StepState, _ *mockGitClient) {
				state.Git.URL = "ssh://git@git.example.com/owner/repo.git"
				state.Git.Token = ""
				state.Git.SSHPrivateKey, state.Git.SSHKnownHosts = []byte("KEY"), []byte("git.example.com ssh-ed25519 AAAA")
				state.Bundle.Type = "config"
				state.Bundle.ConfigRef = &v1alpha1.ConfigRef{GitRepo: "git@git.example.com:owner/config.git", CommitSHA: "abc123"}
			},
			check: func(t *testing.T, state *parentsteps.StepState, git *mockGitClient, _ parentsteps.StepResult, err error) {
				require.NoError(t, err)
				want := scm.GitAuth{SSHPrivateKey: []byte("KEY"), SSHKnownHosts: []byte("git.example.com ssh-ed25519 AAAA")}
				assert.Equal(t, want, git.cloneAuth)
				assert.Equal(t, want, git.cloneAtAuth, "same ssh host")
			},
		},
		{
			name: "ssh pipeline: no key for a config source on another ssh host",
			setup: func(state *parentsteps.StepState, _ *mockGitClient) {
				state.Git.URL = "ssh://git@git.example.com/owner/repo.git"
				state.Git.SSHPrivateKey, state.Git.SSHKnownHosts = []byte("KEY"), []byte("k")
				state.Bundle.Type = "config"
				state.Bundle.ConfigRef = &v1alpha1.ConfigRef{GitRepo: "git@evil.example:owner/config.git", CommitSHA: "abc123"}
			},
			check: func(t *testing.T, _ *parentsteps.StepState, git *mockGitClient, _ parentsteps.StepResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, scm.GitAuth{}, git.cloneAtAuth)
			},
		},
		{
			name: "config source defaults to the pipeline repo",
			setup: func(state *parentsteps.StepState, _ *mockGitClient) {
				state.Bundle.Type = "config"
				state.Bundle.ConfigRef = &v1alpha1.ConfigRef{CommitSHA: "abc123"}
			},
			check: func(t *testing.T, state *parentsteps.StepState, git *mockGitClient, _ parentsteps.StepResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, state.Git.URL, git.cloneAtURL)
			},
		},
		{
			name: "environment layout branch needs a branch-capable git client",
			setup: func(state *parentsteps.StepState, _ *mockGitClient) {
				state.Environment.Layout = "branch"
			},
			check: func(t *testing.T, _ *parentsteps.StepState, git *mockGitClient, res parentsteps.StepResult, err error) {
				assert.ErrorIs(t, err, parentsteps.ErrPermanent)
				assert.Equal(t, parentsteps.StepFailed, res.Status)
				assert.Contains(t, res.Message, "layout: branch needs a git client that can create branches")
				assert.Equal(t, 0, git.cloneCalls)
			},
		},
		{
			name: "pipeline layout branch needs a branch-capable git client",
			setup: func(state *parentsteps.StepState, _ *mockGitClient) {
				state.Pipeline.Git.Layout = "branch"
			},
			check: func(t *testing.T, _ *parentsteps.StepState, git *mockGitClient, res parentsteps.StepResult, err error) {
				assert.ErrorIs(t, err, parentsteps.ErrPermanent)
				assert.Equal(t, parentsteps.StepFailed, res.Status)
				assert.Equal(t, 0, git.cloneCalls)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			git := &mockGitClient{}
			state := makeState(t, git, nil)
			if tc.setup != nil {
				tc.setup(state, git)
			}
			res, err := runStep(t, "git-clone", state)
			tc.check(t, state, git, res, err)
		})
	}
}

// TestGitCommitStep_NoChanges proves an unchanged tree is reported, not
// committed as an empty commit (E2E-04).
func TestGitCommitStep_NoChanges(t *testing.T) {
	tests := []struct {
		name        string
		commitErr   error
		wantStatus  parentsteps.StepStatus
		wantNoChg   string
		wantErr     bool
		wantMessage string
	}{
		{name: "changes committed", wantStatus: parentsteps.StepSuccess, wantNoChg: "false", wantMessage: "committed"},
		{name: "nothing to commit", commitErr: scm.ErrNothingToCommit, wantStatus: parentsteps.StepSuccess, wantNoChg: "true", wantMessage: "no changes"},
		{name: "wrapped nothing to commit", commitErr: fmt.Errorf("commit: %w", scm.ErrNothingToCommit), wantStatus: parentsteps.StepSuccess, wantNoChg: "true", wantMessage: "no changes"},
		{name: "commit error", commitErr: errors.New("disk full"), wantStatus: parentsteps.StepFailed, wantErr: true, wantMessage: "disk full"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			git := &mockGitClient{commitErr: tc.commitErr}
			res, err := runStep(t, "git-commit", makeState(t, git, nil))
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantStatus, res.Status)
			assert.Equal(t, tc.wantNoChg, res.Outputs["noChanges"])
			assert.Contains(t, res.Message, tc.wantMessage)
		})
	}
}

// TestGitPushStep_Modes covers force-push of the kardinal-owned PR branch
// (a re-run after a restart must not fail non-fast-forward), the restart on
// a moved base branch for auto environments (C05-steps-12) and the skip when
// nothing changed (E2E-04). The branch follows from the sequence being run
// (approval when the step started), not the live approval: an approval edited
// mid-promotion takes effect from the next Bundle.
func TestGitPushStep_Modes(t *testing.T) {
	nonFF := fmt.Errorf("push: %w", scm.ErrNonFastForward)
	tests := []struct {
		name       string
		approval   string // the approval the sequence was built for
		live       string // the Pipeline's approval now; approval when empty
		noChanges  string
		layout     string
		pushErrs   []error
		wantStatus parentsteps.StepStatus
		wantErr    bool
		wantPushes int
		wantBranch string
		wantForce  bool
		wantMsg    string
	}{
		{name: "pr-review force-pushes its own branch", approval: "pr-review", wantStatus: parentsteps.StepSuccess,
			wantPushes: 1, wantBranch: "kardinal/nginx-demo-v1-29-0/prod", wantForce: true},
		{name: "auto pushes the base branch without force", approval: "auto", wantStatus: parentsteps.StepSuccess,
			wantPushes: 1, wantBranch: "main", wantForce: false},
		{name: "pr-review edited to auto still pushes its own branch", approval: "pr-review", live: "auto",
			wantStatus: parentsteps.StepSuccess, wantPushes: 1, wantBranch: "kardinal/nginx-demo-v1-29-0/prod", wantForce: true},
		{name: "auto edited to pr-review still pushes the base branch", approval: "auto", live: "pr-review",
			wantStatus: parentsteps.StepSuccess, wantPushes: 1, wantBranch: "main", wantForce: false},
		{name: "auto non-fast-forward restarts the sequence", approval: "auto", pushErrs: []error{nonFF},
			wantStatus: parentsteps.StepRestart, wantPushes: 1, wantBranch: "main", wantMsg: "fresh clone"},
		{name: "pr-review push error fails", approval: "pr-review", pushErrs: []error{errors.New("denied")},
			wantStatus: parentsteps.StepFailed, wantErr: true, wantPushes: 1, wantBranch: "kardinal/nginx-demo-v1-29-0/prod", wantForce: true},
		{name: "nothing changed: no push", approval: "auto", noChanges: "true", wantStatus: parentsteps.StepSuccess,
			wantPushes: 0, wantMsg: "nothing to push"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			git := &mockGitClient{pushErrs: tc.pushErrs}
			state := makeState(t, git, nil)
			state.Sequence = parentsteps.DefaultSequenceForBundle(tc.approval, "image", "", "")
			state.Environment.Approval = tc.approval
			if tc.live != "" {
				state.Environment.Approval = tc.live
			}
			state.Environment.Layout = tc.layout
			if tc.noChanges != "" {
				state.Outputs["noChanges"] = tc.noChanges
			}
			res, err := runStep(t, "git-push", state)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantStatus, res.Status)
			assert.Equal(t, tc.wantPushes, git.pushCalls)
			if tc.wantPushes > 0 {
				assert.Equal(t, tc.wantBranch, git.pushBranch)
				assert.Equal(t, tc.wantForce, git.pushForce)
			}
			assert.Contains(t, res.Message, tc.wantMsg)
			assert.Empty(t, res.Outputs[steps.OutputPushedSHA], "this git client cannot report the commit")
		})
	}
}

// headGitClient is a mockGitClient that reports HEAD.
type headGitClient struct {
	mockGitClient
	sha string
}

func (h *headGitClient) HeadCommit(_ context.Context, _ string) (string, error) { return h.sha, nil }

// TestGitPushStep_RecordsPushedSHA: a successful push records the commit it
// pushed (HEAD of the work directory) as the pushedSHA output, which the
// step's status persists (the kardinal/gates status is set on it, #1452).
// A failed push records none.
func TestGitPushStep_RecordsPushedSHA(t *testing.T) {
	git := &headGitClient{sha: "abc123"}
	state := makeState(t, &git.mockGitClient, nil)
	state.GitClient = git
	state.Sequence = parentsteps.DefaultSequenceForBundle("pr-review", "image", "", "")
	state.Environment.Approval = "pr-review"
	res, err := runStep(t, "git-push", state)
	require.NoError(t, err)
	assert.Equal(t, "abc123", res.Outputs[steps.OutputPushedSHA])

	git = &headGitClient{sha: "abc123", mockGitClient: mockGitClient{pushErrs: []error{errors.New("denied")}}}
	state = makeState(t, &git.mockGitClient, nil)
	state.GitClient = git
	state.Sequence = parentsteps.DefaultSequenceForBundle("pr-review", "image", "", "")
	state.Environment.Approval = "pr-review"
	res, err = runStep(t, "git-push", state)
	require.Error(t, err)
	assert.Empty(t, res.Outputs[steps.OutputPushedSHA])
}

// TestPRBranch covers the one definition of the branch kardinal promotes
// through: git-push pushes PRBranch(namespace, bundle, env),
// kardinal/<first 8 hex of sha256(namespace)>/<bundle>/<env>, open-pr opens
// the PR from it when git-push reported no branch, and both are under
// PRBranchPrefix, the only prefix the PromotionStep reconciler deletes a
// branch under. The same Bundle name in two namespaces gets two branches; a
// promotion that already pushed a branch of the old form,
// kardinal/<bundle>/<env>, keeps it (#1504 QA).
func TestPRBranch(t *testing.T) {
	want := "kardinal/37a8eec1/nginx-demo-v1-29-0/prod" // sha256("default")
	assert.Equal(t, want, steps.PRBranch("default", "nginx-demo-v1-29-0", "prod"))
	assert.NotEqual(t, want, steps.PRBranch("team-b", "nginx-demo-v1-29-0", "prod"))
	assert.Equal(t, "kardinal/nginx-demo-v1-29-0/prod", steps.PRBranch("", "nginx-demo-v1-29-0", "prod"))
	assert.True(t, strings.HasPrefix(want, steps.PRBranchPrefix))

	git := &mockGitClient{}
	state := makeState(t, git, nil)
	state.Namespace = "default"
	state.Sequence = parentsteps.DefaultSequenceForBundle("pr-review", "image", "", "")
	state.Environment.Approval = "pr-review"
	res, err := runStep(t, "git-push", state)
	require.NoError(t, err)
	assert.Equal(t, want, git.pushBranch, "git-push pushes the PR branch")
	assert.Equal(t, want, res.Outputs["branch"])

	legacy := "kardinal/nginx-demo-v1-29-0/prod"
	git = &mockGitClient{}
	state = makeState(t, git, nil)
	state.Namespace = "default"
	state.Outputs["branch"] = legacy
	state.Sequence = parentsteps.DefaultSequenceForBundle("pr-review", "image", "", "")
	_, err = runStep(t, "git-push", state)
	require.NoError(t, err)
	assert.Equal(t, legacy, git.pushBranch, "an in-flight PR keeps its branch")

	scmP := &mockSCMProvider{prURL: "https://example/pr/7", prNumber: 7}
	state = makeState(t, &mockGitClient{}, scmP)
	state.Namespace = "default"
	delete(state.Outputs, "branch")
	_, err = runStep(t, "open-pr", state)
	require.NoError(t, err)
	assert.Equal(t, []string{want}, scmP.heads, "open-pr falls back to the PR branch")
}

// TestPromotionSequence_NoChangesSkipsPR runs the pr-review sequence against a
// tree that already has the target version: no push, no PR, and the sequence
// completes (E2E-04).
func TestPromotionSequence_NoChangesSkipsPR(t *testing.T) {
	git := &mockGitClient{commitErr: scm.ErrNothingToCommit}
	scmP := &mockSCMProvider{prURL: "https://github.com/owner/repo/pull/1", prNumber: 1}
	state := makeState(t, git, scmP)

	engine := parentsteps.NewEngine([]string{"git-clone", "git-commit", "git-push", "open-pr", "wait-for-merge"})
	next, res, err := engine.ExecuteFrom(context.Background(), state, 0)
	require.NoError(t, err)
	assert.Equal(t, 5, next)
	assert.Equal(t, parentsteps.StepSuccess, res.Status)
	assert.Equal(t, 0, git.pushCalls)
	assert.Equal(t, 0, scmP.openPRCalls)
}

// TestOpenPRStep_Providers covers the repository sent to non-GitHub providers
// (C06-scm-health-02, C06-scm-health-03), the no-changes skip and the label
// failure report.
func TestOpenPRStep_Providers(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		noChanges  bool
		labelsErr  error
		wantStatus parentsteps.StepStatus
		wantErr    bool
		wantRepo   string
		wantOpens  int
		wantMsg    string
	}{
		{name: "github", url: "https://github.com/owner/repo.git", wantStatus: parentsteps.StepSuccess, wantRepo: "owner/repo", wantOpens: 1},
		{name: "github enterprise", url: "https://ghe.corp.example/team/gitops", wantStatus: parentsteps.StepSuccess, wantRepo: "team/gitops", wantOpens: 1},
		{name: "gitlab subgroup", url: "https://gitlab.com/group/sub/proj.git", wantStatus: parentsteps.StepSuccess, wantRepo: "group/sub/proj", wantOpens: 1},
		{name: "azure devops", url: "https://dev.azure.com/org/proj/_git/repo", wantStatus: parentsteps.StepSuccess, wantRepo: "org/proj/repo", wantOpens: 1},
		{name: "unparseable URL fails without calling the provider", url: "https://github.com/owner", wantStatus: parentsteps.StepFailed, wantErr: true, wantOpens: 0},
		{name: "nothing changed: no PR", url: "https://github.com/owner/repo", noChanges: true, wantStatus: parentsteps.StepSuccess, wantOpens: 0, wantMsg: "no PR opened"},
		{name: "label failure is reported", url: "https://github.com/owner/repo", labelsErr: errors.New("403 labels"),
			wantStatus: parentsteps.StepSuccess, wantRepo: "owner/repo", wantOpens: 1, wantMsg: "adding labels failed: 403 labels"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scmP := &mockSCMProvider{prURL: "https://example/pr/7", prNumber: 7, labelsErr: tc.labelsErr}
			state := makeState(t, &mockGitClient{}, scmP)
			state.Pipeline.Git.URL = tc.url
			state.Outputs["branch"] = "kardinal/nginx-demo-v1-29-0/prod"
			if tc.noChanges {
				state.Outputs["noChanges"] = "true"
			}
			res, err := runStep(t, "open-pr", state)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantStatus, res.Status)
			assert.Equal(t, tc.wantOpens, scmP.openPRCalls)
			if tc.wantRepo != "" {
				require.NotEmpty(t, scmP.repos)
				for _, r := range scmP.repos {
					assert.Equal(t, tc.wantRepo, r)
				}
			}
			assert.Contains(t, res.Message, tc.wantMsg)
			if tc.labelsErr != nil {
				assert.Equal(t, tc.labelsErr.Error(), res.Outputs["prLabelsError"],
					"the output keeps the label error once wait-for-merge's message replaces this one (B71)")
			} else {
				assert.NotContains(t, res.Outputs, "prLabelsError")
			}
		})
	}
}

// TestOpenPRStep_LabelsErrorCleared covers B71: a PR whose labels worked
// clears the label error an earlier PR of the step left in the outputs.
func TestOpenPRStep_LabelsErrorCleared(t *testing.T) {
	scmP := &mockSCMProvider{prURL: "https://example/pr/8", prNumber: 8}
	state := makeState(t, &mockGitClient{}, scmP)
	state.Pipeline.Git.URL = "https://github.com/owner/repo"
	state.Outputs["branch"] = "kardinal/nginx-demo-v1-29-0/prod"
	state.Outputs["prLabelsError"] = "403 labels"
	res, err := runStep(t, "open-pr", state)
	require.NoError(t, err)
	v, ok := res.Outputs["prLabelsError"]
	assert.True(t, ok, "the output is set, to empty")
	assert.Empty(t, v)
}

// TestWaitForMergeStep_Transient proves an SCM API error keeps the step
// Pending with a retry instead of failing the promotion, and that the repo
// is parsed per provider (C05-steps-18, C06-scm-health-02).
func TestWaitForMergeStep_Transient(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		getPRErr   error
		noChanges  bool
		wantStatus parentsteps.StepStatus
		wantRepo   string
		wantAfter  time.Duration
	}{
		{name: "scm error stays pending", url: "https://github.com/owner/repo", getPRErr: errors.New("502 bad gateway"),
			wantStatus: parentsteps.StepPending, wantRepo: "owner/repo", wantAfter: 30 * time.Second},
		{name: "gitlab subgroup repo", url: "https://gitlab.com/group/sub/proj", wantStatus: parentsteps.StepPending, wantRepo: "group/sub/proj"},
		{name: "nothing changed: nothing to wait for", url: "https://github.com/owner/repo", noChanges: true, wantStatus: parentsteps.StepSuccess},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scmP := &mockSCMProvider{open: true, getPRErr: tc.getPRErr}
			state := makeState(t, &mockGitClient{}, scmP)
			state.Pipeline.Git.URL = tc.url
			state.Outputs["prNumber"] = "42"
			if tc.noChanges {
				state.Outputs["noChanges"] = "true"
				delete(state.Outputs, "prNumber")
			}
			res, err := runStep(t, "wait-for-merge", state)
			require.NoError(t, err)
			assert.Equal(t, tc.wantStatus, res.Status)
			if tc.wantRepo != "" {
				assert.Equal(t, []string{tc.wantRepo}, scmP.repos)
			}
			if tc.wantAfter > 0 {
				assert.Equal(t, tc.wantAfter, res.RequeueAfter)
			}
		})
	}
}

// TestWaitForMergeStep_PermanentSCMErrors: a rejected token, a token without
// access or a missing repository or PR cannot be fixed by polling again, so
// the step fails at once with a permanent error that says why. Rate limits,
// 5xx, network errors and an open circuit breaker stay Pending.
func TestWaitForMergeStep_PermanentSCMErrors(t *testing.T) {
	apiErr := func(status int, transient bool) error {
		return fmt.Errorf("get PR status owner/repo#42: %w", &scm.APIError{Provider: "GitHub", Method: "GET",
			Path: "/repos/owner/repo/pulls/42", StatusCode: status, Body: `{"message":"x"}`, Transient: transient})
	}
	tests := []struct {
		name     string
		getPRErr error
		wantMsg  string // empty: the step stays Pending
	}{
		{name: "401 token rejected", getPRErr: apiErr(401, false), wantMsg: "HTTP 401: the SCM token was rejected"},
		{name: "403 token lacks access", getPRErr: apiErr(403, false), wantMsg: "HTTP 403: the SCM token has no access"},
		{name: "404 not found", getPRErr: apiErr(404, false), wantMsg: "HTTP 404: the repository or PR does not exist"},
		{name: "410 gone", getPRErr: apiErr(410, false), wantMsg: "HTTP 410: the repository or PR does not exist"},
		{name: "403 rate limit", getPRErr: apiErr(403, true)},
		{name: "429", getPRErr: apiErr(429, true)},
		{name: "500", getPRErr: apiErr(500, true)},
		{name: "502", getPRErr: apiErr(502, true)},
		{name: "timeout", getPRErr: fmt.Errorf("execute request: %w", context.DeadlineExceeded)},
		{name: "circuit open", getPRErr: fmt.Errorf("github scm: %w", &scm.ErrCircuitOpen{RetryAfter: time.Now().Add(time.Minute)})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state := makeState(t, &mockGitClient{}, &mockSCMProvider{open: true, getPRErr: tc.getPRErr})
			state.Pipeline.Git.URL = "https://github.com/owner/repo"
			state.Outputs["prNumber"] = "42"

			res, err := runStep(t, "wait-for-merge", state)
			if tc.wantMsg == "" {
				require.NoError(t, err)
				assert.Equal(t, parentsteps.StepPending, res.Status)
				assert.Equal(t, 30*time.Second, res.RequeueAfter)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, parentsteps.ErrPermanent)
			assert.ErrorIs(t, err, tc.getPRErr, "the SCM error stays in the chain")
			assert.Equal(t, parentsteps.StepFailed, res.Status)
			assert.Contains(t, res.Message, "PR #42")
			assert.Contains(t, res.Message, tc.wantMsg)
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}
