// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// TestRenderStep: the controller's render step asks for the render, waits
// for the RenderRun the Graph mirrors onto the step, and takes its result;
// a failed render fails the step for good.
func TestRenderStep(t *testing.T) {
	step, err := parentsteps.Lookup(parentsteps.RenderStepName)
	require.NoError(t, err)
	state := func(seq []string, live ...v1alpha1.LiveRenderRun) *parentsteps.StepState {
		return &parentsteps.StepState{Outputs: map[string]string{}, Sequence: seq, LiveRenders: live,
			Git: parentsteps.GitConfig{Branch: "env/prod"}}
	}
	auto := []string{"render", "health-check"}
	pr := []string{"render", "open-pr", "wait-for-merge", "health-check"}

	s := state(pr)
	res, err := step.Execute(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepPending, res.Status)
	assert.Equal(t, map[string]string{"renderRequested": "true", "renderPullRequest": "true"}, res.Outputs,
		"where to push follows the step's list")
	s.Outputs = res.Outputs

	res, err = step.Execute(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepPending, res.Status)
	assert.Contains(t, res.Message, "waiting for the Graph to create the RenderRun")

	for _, phase := range []string{"", "Pending", "Running"} {
		s.LiveRenders = []v1alpha1.LiveRenderRun{{Name: "rr", Phase: phase}}
		res, err = step.Execute(context.Background(), s)
		require.NoError(t, err)
		assert.Equal(t, parentsteps.StepPending, res.Status, phase)
		assert.NotZero(t, res.RequeueAfter)
	}

	result := &v1alpha1.RenderRunResult{CommitSHA: "c0ffee", Branch: "kardinal/b/prod", DryCommit: "d00d", Renderer: "helm",
		Objects: 3, MarkerDigest: "abc"}
	s.LiveRenders = []v1alpha1.LiveRenderRun{{Name: "rr", Phase: "Succeeded", Result: result}}
	res, err = step.Execute(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, res.Status)
	assert.Equal(t, "kardinal/b/prod", res.Outputs["branch"], "open-pr opens the PR from the branch the Job pushed")
	assert.Empty(t, res.Outputs["commitSHA"], "a pr-review step health checks the merge commit")
	assert.Equal(t, "false", res.Outputs["noChanges"])
	assert.Equal(t, "d00d", res.Outputs["dryCommit"])

	a := state(auto, v1alpha1.LiveRenderRun{Name: "rr", Phase: "Succeeded", Result: &v1alpha1.RenderRunResult{CommitSHA: "c0ffee", Branch: "env/prod"}})
	a.Outputs["renderRequested"] = "true"
	res, err = step.Execute(context.Background(), a)
	require.NoError(t, err)
	assert.Equal(t, "c0ffee", res.Outputs["commitSHA"], "an auto step health checks the pushed render")

	a.LiveRenders = []v1alpha1.LiveRenderRun{{Name: "rr", Phase: "Succeeded", Result: &v1alpha1.RenderRunResult{NoChanges: true}}}
	res, err = step.Execute(context.Background(), a)
	require.NoError(t, err)
	assert.Equal(t, "true", res.Outputs["noChanges"], "open-pr and wait-for-merge skip an unchanged render")

	a.LiveRenders = []v1alpha1.LiveRenderRun{{Name: "rr", Phase: "Failed", Message: "drift: x changed"}}
	res, err = step.Execute(context.Background(), a)
	require.Error(t, err)
	assert.True(t, errors.Is(err, parentsteps.ErrPermanent))
	assert.Equal(t, "render failed (RenderRun rr): drift: x changed", res.Message)

	a.LiveRenders = []v1alpha1.LiveRenderRun{{Name: "rr", Phase: "Succeeded"}}
	_, err = step.Execute(context.Background(), a)
	assert.Error(t, err, "a success without a result is refused")
}

// headGit reports a branch head, as git ls-remote would.
type headGit struct {
	scm.GitClient
	head string
	err  error
}

func (g headGit) RemoteBranchHead(context.Context, string, string, scm.GitAuth) (string, error) {
	return g.head, g.err
}

// TestRenderStep_ChecksTheRemoteHead (QA round 2 on #1515, M2): the commit
// the render Job reported must be the head of the branch it named on the
// remote; a report that does not hold fails the step for good, and an
// ls-remote error is retried.
func TestRenderStep_ChecksTheRemoteHead(t *testing.T) {
	step, err := parentsteps.Lookup(parentsteps.RenderStepName)
	require.NoError(t, err)
	commit := strings.Repeat("c", 40)
	run := func(g scm.GitClient) (parentsteps.StepResult, error) {
		st := &parentsteps.StepState{Outputs: map[string]string{"renderRequested": "true"}, GitClient: g,
			Sequence: []string{"render", "health-check"}, Git: parentsteps.GitConfig{URL: "https://git.example.com/r.git", Branch: "env/prod"},
			LiveRenders: []v1alpha1.LiveRenderRun{{Name: "rr", Phase: "Succeeded",
				Result: &v1alpha1.RenderRunResult{CommitSHA: commit, Branch: "env/prod"}}}}
		return step.Execute(context.Background(), st)
	}
	res, err := run(headGit{head: commit})
	require.NoError(t, err)
	assert.Equal(t, commit, res.Outputs["commitSHA"])

	res, err = run(headGit{head: strings.Repeat("e", 40)})
	require.Error(t, err)
	assert.True(t, errors.Is(err, parentsteps.ErrPermanent))
	assert.Contains(t, res.Message, "but the branch is at \"eeeeeeee\"")

	_, err = run(headGit{err: errors.New("connection refused")})
	require.Error(t, err)
	assert.False(t, errors.Is(err, parentsteps.ErrPermanent), "an ls-remote error is retried")
}

// TestRenderStep_NoChangesChecksTheHead (QA round 3 on #1515): a result
// that pushed nothing must name the rendered branch's head, checked with git
// ls-remote, and a marker digest kardinal recorded for the environment; a
// report that skips the push cannot skip the checks.
func TestRenderStep_NoChangesChecksTheHead(t *testing.T) {
	step, err := parentsteps.Lookup(parentsteps.RenderStepName)
	require.NoError(t, err)
	commit := strings.Repeat("c", 40)
	run := func(head string, known []string, res v1alpha1.RenderRunResult) (parentsteps.StepResult, error) {
		res.NoChanges = true
		st := &parentsteps.StepState{Outputs: map[string]string{"renderRequested": "true"}, GitClient: headGit{head: head},
			Sequence: []string{"render", "health-check"}, Git: parentsteps.GitConfig{URL: "https://git.example.com/r.git", Branch: "env/prod"},
			LiveRenders: []v1alpha1.LiveRenderRun{{Name: "rr", Phase: "Succeeded", KnownMarkerDigests: known, Result: &res}}}
		return step.Execute(context.Background(), st)
	}
	tests := []struct {
		name    string
		head    string
		known   []string
		result  v1alpha1.RenderRunResult
		wantErr string
	}{
		{name: "head and known marker", head: commit, known: []string{"m0", "m1"},
			result: v1alpha1.RenderRunResult{CommitSHA: commit, MarkerDigest: "m1"}},
		{name: "no recorded renders yet", head: commit, result: v1alpha1.RenderRunResult{CommitSHA: commit, MarkerDigest: "m1"}},
		{name: "unknown marker", head: commit, known: []string{"m0"},
			result: v1alpha1.RenderRunResult{CommitSHA: commit, MarkerDigest: "m9"}, wantErr: "is not one of kardinal's recorded renders"},
		{name: "branch moved", head: strings.Repeat("e", 40), known: []string{"m1"},
			result: v1alpha1.RenderRunResult{CommitSHA: commit, MarkerDigest: "m1"}, wantErr: "but the branch is at \"eeeeeeee\""},
		{name: "no commit reported", head: commit, known: []string{"m1"},
			result: v1alpha1.RenderRunResult{MarkerDigest: "m1"}, wantErr: "but the branch is at"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := run(tc.head, tc.known, tc.result)
			if tc.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, "true", res.Outputs["noChanges"])
				assert.Empty(t, res.Outputs["commitSHA"], "an unchanged render sets no commit to health check")
				return
			}
			require.Error(t, err)
			assert.True(t, errors.Is(err, parentsteps.ErrPermanent))
			assert.Contains(t, res.Message, tc.wantErr)
		})
	}
}

// authGit records the GitAuth the layout: branch calls are made with.
type authGit struct {
	scm.GitClient
	head  string
	auths map[string]scm.GitAuth
}

func (g *authGit) RemoteBranchHead(_ context.Context, _, _ string, auth scm.GitAuth) (string, error) {
	g.auths["RemoteBranchHead"] = auth
	return g.head, nil
}

func (g *authGit) CloneOrInit(_ context.Context, _, _, _ string, auth scm.GitAuth, _ int) (bool, error) {
	g.auths["CloneOrInit"] = auth
	return false, errors.New("stop after the clone")
}

func (g *authGit) HeadCommit(context.Context, string) (string, error) { return g.head, nil }

// TestRenderBranch_SSHPipelineAuth (#1515, after #1491): a layout: branch
// Pipeline with an ssh spec.git.url clones (or creates) its rendered branch
// and reads its head with the git Secret's ssh key and known_hosts, not a
// token alone. The transport itself is TestGoGitClient_RenderedBranchOverSSH.
//
// Covers REND-SSH-02.
func TestRenderBranch_SSHPipelineAuth(t *testing.T) {
	git := parentsteps.GitConfig{URL: "ssh://git@git.example.com/acme/web.git", Branch: "env/prod", SourceBranch: "main",
		SSHPrivateKey: []byte("key"), SSHKnownHosts: []byte("git.example.com ssh-ed25519 AAAA")}
	want := git.Auth()
	commit := strings.Repeat("c", 40)

	g := &authGit{head: commit, auths: map[string]scm.GitAuth{}}
	clone, err := parentsteps.Lookup("git-clone")
	require.NoError(t, err)
	t.Setenv(parentsteps.RenderJobEnv, "1")
	_, err = clone.Execute(context.Background(), &parentsteps.StepState{
		PipelineName: "web", BundleName: "web-v2", WorkDir: filepath.Join(t.TempDir(), "w"),
		Render:      &parentsteps.RenderContext{Namespace: "team"},
		Environment: v1alpha1.EnvironmentSpec{Name: "prod", Path: "environments/prod", Layout: "branch"},
		Bundle:      v1alpha1.BundleSpec{Type: "image", Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/org/web", Tag: "2"}}},
		Git:         git, GitClient: g, Outputs: map[string]string{},
	})
	require.ErrorContains(t, err, "stop after the clone")

	render, err := parentsteps.Lookup(parentsteps.RenderStepName)
	require.NoError(t, err)
	_, err = render.Execute(context.Background(), &parentsteps.StepState{Outputs: map[string]string{"renderRequested": "true"},
		GitClient: g, Sequence: []string{"render", "health-check"}, Git: git,
		LiveRenders: []v1alpha1.LiveRenderRun{{Name: "rr", Phase: "Succeeded",
			Result: &v1alpha1.RenderRunResult{CommitSHA: commit, Branch: "env/prod"}}}})
	require.NoError(t, err)

	for _, call := range []string{"CloneOrInit", "RemoteBranchHead"} {
		assert.Equal(t, want, g.auths[call], call)
	}
}
