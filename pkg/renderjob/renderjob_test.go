// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package renderjob_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/renderjob"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
	stepsimpl "github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
)

// remote is a bare repository with a kustomize overlay on main.
func remote(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	seed := t.TempDir()
	_, err := gogit.PlainInitWithOptions(seed, &gogit.PlainInitOptions{
		InitOptions: gogit.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")}})
	require.NoError(t, err)
	for p, c := range map[string]string{
		"base/kustomization.yaml":              "resources: [deployment.yaml]\n",
		"base/deployment.yaml":                 "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web}\nspec:\n  template:\n    spec:\n      containers:\n      - {name: web, image: ghcr.io/org/web:1.0.0}\n",
		"environments/prod/kustomization.yaml": "namespace: web-prod\nresources: [../../base]\n",
	} {
		require.NoError(t, os.MkdirAll(filepath.Join(seed, filepath.Dir(p)), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(seed, p), []byte(c), 0o600))
	}
	require.NoError(t, scm.NewGoGitClient().CommitAll(ctx, seed, "seed", "t", "t@example.com"))
	bare := t.TempDir()
	_, err = gogit.PlainClone(bare, true, &gogit.CloneOptions{URL: seed})
	require.NoError(t, err)
	return "file://" + bare
}

func config(url string, pr bool) renderjob.Config {
	return renderjob.Config{
		Namespace: "team", Pipeline: "web", Environment: "prod", Path: "environments/prod", BundleName: "web-v2",
		Bundle: v1alpha1.BundleSpec{Type: "image", Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/org/web", Tag: "2.0.0"}}},
		Git:    v1alpha1.RenderRunGit{URL: url, SourceBranch: "main", RenderedBranch: "env/prod", PullRequest: pr},
		Update: v1alpha1.UpdateConfig{Strategy: "kustomize"}, AuthorName: "kardinal-promoter", AuthorEmail: "k@example.com",
	}
}

// TestRun renders an environment the way the Job does: the render is pushed
// to the rendered branch (auto) or to the promotion branch (pr-review), the
// result names the commit, the DRY commit, the marker digest and the object
// count, and a second identical render pushes nothing.
func TestRun(t *testing.T) {
	t.Setenv(steps.RenderJobEnv, "1")
	ctx := context.Background()
	url := remote(t)
	git := scm.NewGoGitClient()

	res, err := renderjob.Run(ctx, config(url, false), t.TempDir(), scm.GitAuth{}, git)
	require.NoError(t, err)
	assert.Equal(t, "env/prod", res.Branch)
	assert.Len(t, res.CommitSHA, 40)
	assert.Len(t, res.DryCommit, 40)
	assert.Len(t, res.MarkerDigest, 64)
	assert.Equal(t, "kustomize", res.Renderer)
	assert.Equal(t, 1, res.Objects)
	assert.False(t, res.NoChanges)
	head, err := git.RemoteBranchHead(ctx, url, "env/prod", scm.GitAuth{})
	require.NoError(t, err)
	assert.Equal(t, res.CommitSHA, head, "ls-remote sees the pushed commit")
	none, err := git.RemoteBranchHead(ctx, url, "env/none", scm.GitAuth{})
	require.NoError(t, err)
	assert.Empty(t, none)

	again := config(url, false)
	again.KnownMarkerDigests = []string{res.MarkerDigest}
	res2, err := renderjob.Run(ctx, again, t.TempDir(), scm.GitAuth{}, git)
	require.NoError(t, err)
	// A retry of the same Bundle whose first result was lost: the branch
	// head is already its render, which the result reports again.
	assert.False(t, res2.NoChanges)
	assert.Equal(t, res.CommitSHA, res2.CommitSHA, "the same render pushes no new commit")

	// The realistic retry: the Job's Pod pushed and was retried before it
	// wrote its result, so the RenderRun's known digests lack its own marker.
	retry := config(url, false)
	retry.KnownMarkerDigests = []string{strings.Repeat("0", 64)}
	res4, err := renderjob.Run(ctx, retry, t.TempDir(), scm.GitAuth{}, git)
	require.NoError(t, err, "the retry adopts its own push")
	assert.False(t, res4.NoChanges)
	assert.Equal(t, res.CommitSHA, res4.CommitSHA, "the commit the first attempt pushed")
	assert.Equal(t, res.MarkerDigest, res4.MarkerDigest)

	// A pr-review render of the Bundle the rendered branch already holds
	// pushes nothing: the result names the rendered branch's head, which the
	// controller's render step checks with git ls-remote.
	same := config(url, true)
	same.KnownMarkerDigests = []string{res.MarkerDigest}
	resNC, err := renderjob.Run(ctx, same, t.TempDir(), scm.GitAuth{}, git)
	require.NoError(t, err)
	assert.True(t, resNC.NoChanges)
	assert.Empty(t, resNC.Branch, "nothing pushed")
	assert.Equal(t, res.CommitSHA, resNC.CommitSHA, "the rendered branch's head")
	assert.Equal(t, res.MarkerDigest, resNC.MarkerDigest)

	pr := config(url, true)
	pr.BundleName = "web-v3"
	pr.Bundle.Images[0].Tag = "3.0.0"
	pr.KnownMarkerDigests = []string{res.MarkerDigest}
	res3, err := renderjob.Run(ctx, pr, t.TempDir(), scm.GitAuth{}, git)
	require.NoError(t, err)
	prBranch := stepsimpl.PRBranch("team", "web-v3", "prod")
	assert.Equal(t, prBranch, res3.Branch, "pr-review pushes to the promotion branch")
	dir := filepath.Join(t.TempDir(), "check")
	require.NoError(t, git.Clone(ctx, url, prBranch, dir, scm.GitAuth{}))
	b, err := os.ReadFile(filepath.Join(dir, "web-prod_deployment-web.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "ghcr.io/org/web:3.0.0")

	_, err = renderjob.Run(ctx, config("file:///nonexistent", false), t.TempDir(), scm.GitAuth{}, git)
	assert.Error(t, err)
}

// TestMessage: the termination message is JSON within 4096 bytes, a long
// error cut to fit, and parses back.
func TestMessage(t *testing.T) {
	msg := renderjob.Message(renderjob.Result{Error: strings.Repeat("é", 5000)})
	assert.LessOrEqual(t, len(msg), 4096)
	assert.True(t, json.Valid(msg))
	r, err := renderjob.ParseMessage(string(msg))
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(r.Error, " (cut)"))

	drift := renderjob.Message(renderjob.Result{RenderRunResult: v1alpha1.RenderRunResult{CommitSHA: strings.Repeat("c", 40),
		DriftOverwritten: strings.Repeat("a-very-long-file-name.yaml changed; ", 300)}})
	assert.LessOrEqual(t, len(drift), 4096, "a long success message is cut too")
	r, err = renderjob.ParseMessage(string(drift))
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("c", 40), r.CommitSHA, "the result itself is kept")
	assert.True(t, strings.HasSuffix(r.DriftOverwritten, " (cut)"))

	ok := renderjob.Message(renderjob.Result{RenderRunResult: v1alpha1.RenderRunResult{CommitSHA: "abc", Objects: 2}})
	r, err = renderjob.ParseMessage(string(ok))
	require.NoError(t, err)
	assert.Equal(t, "abc", r.CommitSHA)
	assert.Equal(t, 2, r.Objects)
	_, err = renderjob.ParseMessage("")
	assert.ErrorContains(t, err, "wrote no result")
	_, err = renderjob.ParseMessage("panic: boom")
	assert.ErrorContains(t, err, "not JSON")
}
