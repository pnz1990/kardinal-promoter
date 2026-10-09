// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	gogitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// dryRepo is a DRY source: a kustomize base and a prod overlay, and a Helm
// chart for the helm environment.
var dryRepo = map[string]string{
	"base/kustomization.yaml": "resources:\n  - deployment.yaml\n  - service.yaml\n",
	"base/deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  replicas: 1
  selector: {matchLabels: {app: web}}
  template:
    metadata: {labels: {app: web}}
    spec:
      containers:
        - name: web
          image: ghcr.io/org/web:1.0.0
`,
	"base/service.yaml": "apiVersion: v1\nkind: Service\nmetadata:\n  name: web\nspec:\n  ports: [{port: 80}]\n",
	"environments/prod/kustomization.yaml": "namespace: web-prod\nresources:\n  - ../../base\nimages:\n  - name: ghcr.io/org/web\n    newTag: 1.0.0\n",
	"charts/web/Chart.yaml":                "apiVersion: v2\nname: web\nversion: 0.1.0\n",
	"charts/web/values.yaml":               "image:\n  repository: ghcr.io/org/web\n  tag: 1.0.0\nreplicas: 1\n",
	"charts/web/prod-values.yaml":          "replicas: 3\n",
	"charts/web/templates/deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}
  namespace: {{ .Release.Namespace }}
spec:
  replicas: {{ .Values.replicas }}
  template:
    spec:
      containers:
        - name: web
          image: {{ .Values.image.repository }}:{{ .Values.image.tag }}
`,
	"charts/web/templates/hook.yaml": "apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: migrate\n  annotations:\n    helm.sh/hook: pre-install\n",
	"charts/web/templates/NOTES.txt": "installed\n",
}

// seedRemote makes a bare repository with files on main and returns its
// file:// URL and the seed checkout (to commit more DRY changes).
func seedRemote(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	ctx := context.Background()
	c := scm.NewGoGitClient()
	seed := t.TempDir()
	_, err := gogit.PlainInitWithOptions(seed, &gogit.PlainInitOptions{
		InitOptions: gogit.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")}})
	require.NoError(t, err)
	writeFiles(t, seed, files)
	require.NoError(t, c.CommitAll(ctx, seed, "seed", "t", "t@example.com"))
	remote := t.TempDir()
	_, err = gogit.PlainClone(remote, true, &gogit.CloneOptions{URL: seed})
	require.NoError(t, err)
	repo, err := gogit.PlainOpen(seed)
	require.NoError(t, err)
	_, err = repo.CreateRemote(&gogitconfig.RemoteConfig{Name: "origin", URLs: []string{"file://" + remote}})
	require.NoError(t, err)
	return "file://" + remote, seed
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		full := filepath.Join(dir, p)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o750))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o600))
	}
}

// renderState is a layout: branch promotion of image tag into env.
func renderState(t *testing.T, url, workDir string, env v1alpha1.EnvironmentSpec, bundle string, tag string) *parentsteps.StepState {
	t.Helper()
	env.Layout = "branch"
	return &parentsteps.StepState{
		PipelineName: "web", BundleName: bundle, WorkDir: workDir,
		Environment: env,
		Bundle:      v1alpha1.BundleSpec{Type: "image", Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/org/web", Tag: tag}}},
		Git:         parentsteps.GitConfig{URL: url, Branch: env.RenderedBranch(), SourceBranch: "main"},
		GitClient:   scm.NewGoGitClient(),
		Outputs:     map[string]string{},
	}
}

// promote runs the auto sequence of layout: branch and returns the last result.
func promote(t *testing.T, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	t.Helper()
	seq := parentsteps.DefaultSequenceForBundle("auto", state.Bundle.Type, state.Environment.Update.Strategy, "branch")
	var res parentsteps.StepResult
	for _, name := range seq {
		if name == "health-check" {
			break
		}
		step, err := parentsteps.Lookup(name)
		require.NoError(t, err)
		res, err = step.Execute(context.Background(), state)
		for k, v := range res.Outputs {
			state.Outputs[k] = v
		}
		if err != nil || res.Status != parentsteps.StepSuccess {
			return res, err
		}
	}
	return res, nil
}

// branchFiles clones branch of url and returns its files (without .git).
func branchFiles(t *testing.T, url, branch string) (map[string]string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "check")
	require.NoError(t, scm.NewGoGitClient().Clone(context.Background(), url, branch, dir, ""))
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			b, _ := os.ReadFile(p)
			out[filepath.ToSlash(rel)] = string(b)
		}
		return nil
	}))
	repo, err := gogit.PlainOpen(dir)
	require.NoError(t, err)
	head, err := repo.Head()
	require.NoError(t, err)
	c, err := repo.CommitObject(head.Hash())
	require.NoError(t, err)
	return out, c.Message
}

// TestRenderBranch_Kustomize (#1447): the first promotion creates env/prod,
// renders the overlay in process with the Bundle's tag, one file per object,
// commits it with the DRY commit trailer, and leaves main untouched. A second
// promotion replaces the files.
func TestRenderBranch_Kustomize(t *testing.T) {
	url, _ := seedRemote(t, dryRepo)
	env := v1alpha1.EnvironmentSpec{Name: "prod", Path: "environments/prod"}
	state := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v2", "2.0.0")
	res, err := promote(t, state)
	require.NoError(t, err, res.Message)
	assert.Equal(t, "true", state.Outputs["renderedBranchCreated"])

	files, msg := branchFiles(t, url, "env/prod")
	require.Contains(t, files, "web-prod/deployment-web.yaml")
	assert.Contains(t, files["web-prod/deployment-web.yaml"], "image: ghcr.io/org/web:2.0.0")
	assert.Contains(t, files, "web-prod/service-web.yaml")
	assert.NotContains(t, files, "kustomization.yaml", "the rendered branch holds plain manifests")
	assert.Contains(t, files[".kardinal/rendered.yaml"], "dryCommit: "+state.Outputs["dryCommit"])
	assert.Contains(t, msg, "Kardinal-Dry-Commit: "+state.Outputs["dryCommit"])
	assert.Contains(t, msg, "Kardinal-Bundle: web-v2")
	assert.Contains(t, msg, "Kardinal-Dry-Path: environments/prod")

	main, _ := branchFiles(t, url, "main")
	assert.Contains(t, main["environments/prod/kustomization.yaml"], "newTag: 1.0.0", "the DRY source is never committed to")

	state = renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v3", "3.0.0")
	res, err = promote(t, state)
	require.NoError(t, err, res.Message)
	assert.Empty(t, state.Outputs["renderedBranchCreated"])
	files, _ = branchFiles(t, url, "env/prod")
	assert.Contains(t, files["web-prod/deployment-web.yaml"], "ghcr.io/org/web:3.0.0")

	// The same Bundle again changes nothing.
	state = renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v3", "3.0.0")
	_, err = promote(t, state)
	require.NoError(t, err)
	assert.Equal(t, "true", state.Outputs["noChanges"])
}

// TestRenderBranch_Helm: a chart is rendered with helm template in process,
// with render.helm values files over values.yaml, without hooks or NOTES.
func TestRenderBranch_Helm(t *testing.T) {
	url, _ := seedRemote(t, dryRepo)
	env := v1alpha1.EnvironmentSpec{Name: "prod", Path: "charts/web",
		Update: v1alpha1.UpdateConfig{Strategy: "helm"},
		Render: &v1alpha1.RenderConfig{Helm: &v1alpha1.HelmRenderConfig{ValuesFiles: []string{"prod-values.yaml"}, Namespace: "web-prod"}}}
	state := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v2", "2.0.0")
	res, err := promote(t, state)
	require.NoError(t, err, res.Message)
	assert.Equal(t, "helm", state.Outputs["renderer"])
	files, _ := branchFiles(t, url, "env/prod")
	dep := files["web-prod/deployment-web.yaml"]
	assert.Contains(t, dep, "image: ghcr.io/org/web:2.0.0", "helm-set-image edited the DRY values before the render")
	assert.Contains(t, dep, "replicas: 3", "prod-values.yaml applies over values.yaml")
	for p := range files {
		assert.NotContains(t, p, "job-migrate", "hooks are not rendered")
		assert.NotContains(t, p, "NOTES", "NOTES.txt is not rendered")
	}
}

// TestRenderBranch_Drift: a direct push that edits a rendered file fails the
// next promotion (nothing is pushed); onDrift: overwrite renders over it.
// A file kardinal never wrote (CODEOWNERS) is not drift and is kept.
func TestRenderBranch_Drift(t *testing.T) {
	url, _ := seedRemote(t, dryRepo)
	env := v1alpha1.EnvironmentSpec{Name: "prod", Path: "environments/prod"}
	_, err := promote(t, renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v2", "2.0.0"))
	require.NoError(t, err)

	// Someone pushes to env/prod directly.
	c := scm.NewGoGitClient()
	dir := filepath.Join(t.TempDir(), "hand")
	require.NoError(t, c.Clone(context.Background(), url, "env/prod", dir, ""))
	writeFiles(t, dir, map[string]string{"web-prod/deployment-web.yaml": "edited by hand\n", "CODEOWNERS": "* @team\n"})
	require.NoError(t, c.CommitAll(context.Background(), dir, "hotfix", "h", "h@example.com"))
	require.NoError(t, c.Push(context.Background(), dir, "origin", "env/prod", "", false))

	state := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v3", "3.0.0")
	res, err := promote(t, state)
	require.Error(t, err)
	assert.True(t, errors.Is(err, parentsteps.ErrPermanent))
	assert.Contains(t, res.Message, "rendered branch env/prod was changed outside kardinal: web-prod/deployment-web.yaml changed")
	files, _ := branchFiles(t, url, "env/prod")
	assert.Equal(t, "edited by hand\n", files["web-prod/deployment-web.yaml"], "nothing pushed")

	env.Render = &v1alpha1.RenderConfig{OnDrift: "overwrite"}
	state = renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v3", "3.0.0")
	_, err = promote(t, state)
	require.NoError(t, err)
	assert.Contains(t, state.Outputs["driftOverwritten"], "web-prod/deployment-web.yaml changed")
	files, _ = branchFiles(t, url, "env/prod")
	assert.Contains(t, files["web-prod/deployment-web.yaml"], "ghcr.io/org/web:3.0.0")
	assert.Equal(t, "* @team\n", files["CODEOWNERS"], "files kardinal never wrote are kept")
}

// TestRenderBranch_RollbackRendersOldDryCommit: a rollback Bundle renders the
// DRY commit its target was rendered from, read from the commit trailers of
// the rendered branch, not the source branch head.
func TestRenderBranch_RollbackRendersOldDryCommit(t *testing.T) {
	url, seed := seedRemote(t, dryRepo)
	env := v1alpha1.EnvironmentSpec{Name: "prod", Path: "environments/prod"}
	first := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v2", "2.0.0")
	_, err := promote(t, first)
	require.NoError(t, err)

	// The DRY source changes (replicas 1 -> 5), and v3 is rendered from it.
	c := scm.NewGoGitClient()
	writeFiles(t, seed, map[string]string{"base/deployment.yaml": strings.Replace(dryRepo["base/deployment.yaml"], "replicas: 1", "replicas: 5", 1)})
	require.NoError(t, c.CommitAll(context.Background(), seed, "scale up", "t", "t@example.com"))
	require.NoError(t, c.Push(context.Background(), seed, "origin", "main", "", false))
	_, err = promote(t, renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v3", "3.0.0"))
	require.NoError(t, err)
	files, _ := branchFiles(t, url, "env/prod")
	require.Contains(t, files["web-prod/deployment-web.yaml"], "replicas: 5")

	rb := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-rollback", "2.0.0")
	rb.Bundle.Provenance = &v1alpha1.BundleProvenance{RollbackOf: "web-v2"}
	_, err = promote(t, rb)
	require.NoError(t, err)
	assert.Equal(t, first.Outputs["dryCommit"], rb.Outputs["dryCommit"], "the rollback renders web-v2's DRY commit")
	files, _ = branchFiles(t, url, "env/prod")
	assert.Contains(t, files["web-prod/deployment-web.yaml"], "replicas: 1")
	assert.Contains(t, files["web-prod/deployment-web.yaml"], "ghcr.io/org/web:2.0.0")

	unknown := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-rollback-2", "1.0.0")
	unknown.Bundle.Provenance = &v1alpha1.BundleProvenance{RollbackOf: "never-rendered"}
	res, err := promote(t, unknown)
	require.Error(t, err)
	assert.Contains(t, res.Message, "no render of it")
}

// TestRenderBranch_Refusals: remote resources, kustomize helmCharts, a
// symbolic link and a missing kustomization or chart fail for good.
func TestRenderBranch_Refusals(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		link    bool
		wantMsg string
	}{
		{"remote resource", map[string]string{"environments/prod/kustomization.yaml": "resources:\n  - https://github.com/org/repo//base?ref=v1\n"},
			false, "remote resource"},
		{"helmCharts", map[string]string{"environments/prod/kustomization.yaml": "helmCharts:\n  - name: x\n"}, false, "helmCharts is not supported"},
		{"nothing to render", map[string]string{"environments/prod/README": "x\n"}, false, "found neither"},
		{"symbolic link", map[string]string{"environments/prod/kustomization.yaml": "resources: [a.yaml]\n"}, true, "symbolic link"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"README": "dry\n"}
			for k, v := range tt.files {
				files[k] = v
			}
			url, _ := seedRemote(t, files)
			env := v1alpha1.EnvironmentSpec{Name: "prod", Path: "environments/prod"}
			state := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v2", "2.0.0")
			step, err := parentsteps.Lookup("git-clone")
			require.NoError(t, err)
			_, err = step.Execute(context.Background(), state)
			require.NoError(t, err)
			if tt.link {
				require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(parentsteps.DrySourceDir(state.WorkDir), "environments/prod/a.yaml")))
			}
			render, err := parentsteps.Lookup("render-manifests")
			require.NoError(t, err)
			res, err := render.Execute(context.Background(), state)
			require.Error(t, err)
			assert.True(t, errors.Is(err, parentsteps.ErrPermanent), "%v", err)
			assert.Contains(t, res.Message, tt.wantMsg)
		})
	}
}
