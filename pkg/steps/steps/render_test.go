// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
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
	"base/service.yaml":                    "apiVersion: v1\nkind: Service\nmetadata:\n  name: web\nspec:\n  ports: [{port: 80}]\n",
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
	t.Setenv(parentsteps.RenderJobEnv, "1")
	env.Layout = "branch"
	return &parentsteps.StepState{
		PipelineName: "web", BundleName: bundle, WorkDir: workDir,
		Render:      &parentsteps.RenderContext{Namespace: "team"},
		Environment: env,
		Bundle:      v1alpha1.BundleSpec{Type: "image", Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/org/web", Tag: tag}}},
		Git:         parentsteps.GitConfig{URL: url, Branch: env.RenderedBranch(), SourceBranch: "main"},
		GitClient:   scm.NewGoGitClient(),
		Outputs:     map[string]string{},
	}
}

// promote runs the render Job's sequence of layout: branch (auto) and
// returns the last result.
func promote(t *testing.T, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	t.Helper()
	seq := parentsteps.RenderJobSequence(state.Bundle.Type, state.Environment.Update.Strategy)
	var res parentsteps.StepResult
	for _, name := range seq {
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
	require.Contains(t, files, "web-prod_deployment-web.yaml")
	assert.Contains(t, files["web-prod_deployment-web.yaml"], "image: ghcr.io/org/web:2.0.0")
	assert.Contains(t, files, "web-prod_service-web.yaml")
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
	assert.Contains(t, files["web-prod_deployment-web.yaml"], "ghcr.io/org/web:3.0.0")

	// The same Bundle again (a retry whose first result was lost) commits
	// nothing new: the branch head is already its render.
	_, before := branchFiles(t, url, "env/prod")
	state = renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v3", "3.0.0")
	_, err = promote(t, state)
	require.NoError(t, err)
	_, after := branchFiles(t, url, "env/prod")
	assert.Equal(t, before, after, "no new commit")
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
	dep := files["web-prod_deployment-web.yaml"]
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
	writeFiles(t, dir, map[string]string{"web-prod_deployment-web.yaml": "edited by hand\n", "CODEOWNERS": "* @team\n"})
	require.NoError(t, c.CommitAll(context.Background(), dir, "hotfix", "h", "h@example.com"))
	require.NoError(t, c.Push(context.Background(), dir, "origin", "env/prod", "", false))

	state := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v3", "3.0.0")
	res, err := promote(t, state)
	require.Error(t, err)
	assert.True(t, errors.Is(err, parentsteps.ErrPermanent))
	assert.Contains(t, res.Message, "rendered branch env/prod was changed outside kardinal: web-prod_deployment-web.yaml changed")
	files, _ := branchFiles(t, url, "env/prod")
	assert.Equal(t, "edited by hand\n", files["web-prod_deployment-web.yaml"], "nothing pushed")

	env.Render = &v1alpha1.RenderConfig{OnDrift: "overwrite"}
	state = renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v3", "3.0.0")
	_, err = promote(t, state)
	require.NoError(t, err)
	assert.Contains(t, state.Outputs["driftOverwritten"], "web-prod_deployment-web.yaml changed")
	files, _ = branchFiles(t, url, "env/prod")
	assert.Contains(t, files["web-prod_deployment-web.yaml"], "ghcr.io/org/web:3.0.0")
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
	require.Contains(t, files["web-prod_deployment-web.yaml"], "replicas: 5")

	rb := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-rollback", "2.0.0")
	rb.Bundle.Provenance = &v1alpha1.BundleProvenance{RollbackOf: "web-v2"}
	_, err = promote(t, rb)
	require.NoError(t, err)
	assert.Equal(t, first.Outputs["dryCommit"], rb.Outputs["dryCommit"], "the rollback renders web-v2's DRY commit")
	files, _ = branchFiles(t, url, "env/prod")
	assert.Contains(t, files["web-prod_deployment-web.yaml"], "replicas: 1")
	assert.Contains(t, files["web-prod_deployment-web.yaml"], "ghcr.io/org/web:2.0.0")

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
			false, "resources[]: remote reference"},
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

// pushFiles commits files (nil content deletes) to branch of url, as
// someone outside kardinal would.
func pushFiles(t *testing.T, url, branch string, files map[string]*string) {
	t.Helper()
	c := scm.NewGoGitClient()
	dir := filepath.Join(t.TempDir(), "hand")
	require.NoError(t, c.Clone(context.Background(), url, branch, dir, ""))
	for p, content := range files {
		full := filepath.Join(dir, p)
		if content == nil {
			require.NoError(t, os.Remove(full))
			continue
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o750))
		require.NoError(t, os.WriteFile(full, []byte(*content), 0o600))
	}
	require.NoError(t, c.CommitAll(context.Background(), dir, "by hand", "h", "h@example.com"))
	require.NoError(t, c.Push(context.Background(), dir, "origin", branch, "", false))
}

func strp(s string) *string { return &s }

// TestRenderBranch_OtherPipelinesBranch (QA on #1515): a rendered branch
// another Pipeline (or environment) rendered is refused, so two Pipelines
// pointed at one branch cannot overwrite each other's render.
func TestRenderBranch_OtherPipelinesBranch(t *testing.T) {
	url, _ := seedRemote(t, dryRepo)
	env := v1alpha1.EnvironmentSpec{Name: "prod", Path: "environments/prod"}
	_, err := promote(t, renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v2", "2.0.0"))
	require.NoError(t, err)
	for name, mutate := range map[string]func(*parentsteps.StepState){
		"another Pipeline":    func(s *parentsteps.StepState) { s.PipelineName = "api" },
		"another namespace":   func(s *parentsteps.StepState) { s.Render.Namespace = "other-team" },
		"another environment": func(s *parentsteps.StepState) { s.Environment.Name = "staging" },
	} {
		t.Run(name, func(t *testing.T) {
			state := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "x-v1", "3.0.0")
			mutate(state)
			state.Git.Branch = "env/prod"
			res, err := promote(t, state)
			require.Error(t, err)
			assert.True(t, errors.Is(err, parentsteps.ErrPermanent))
			if state.Render.Namespace != "team" {
				assert.Contains(t, res.Message, "rendered branch env/prod is not this environment's: it was rendered for a Pipeline in another namespace",
					"another namespace's Pipeline is not named")
				assert.NotContains(t, res.Message, "web")
				return
			}
			assert.Contains(t, res.Message, "rendered branch env/prod is not this environment's: it was rendered for Pipeline team/web environment prod")
		})
	}
	files, _ := branchFiles(t, url, "env/prod")
	assert.Contains(t, files["web-prod_deployment-web.yaml"], "ghcr.io/org/web:2.0.0", "nothing was overwritten")
}

// TestRenderBranch_UnknownManifestIsDrift (QA on #1515): a YAML or JSON
// file kardinal did not write, anywhere on the rendered branch, is drift (Argo
// CD would apply it with the render); other files are not. With
// onDrift: overwrite it is removed.
func TestRenderBranch_UnknownManifestIsDrift(t *testing.T) {
	url, _ := seedRemote(t, dryRepo)
	env := v1alpha1.EnvironmentSpec{Name: "prod", Path: "environments/prod"}
	_, err := promote(t, renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v2", "2.0.0"))
	require.NoError(t, err)
	pushFiles(t, url, "env/prod", map[string]*string{
		"backdoor.yaml":     strp("apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRoleBinding\n"),
		"nested/extra.json": strp("{}"),
		"README.md":         strp("docs\n"),
	})
	res, err := promote(t, renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v3", "3.0.0"))
	require.Error(t, err)
	assert.Contains(t, res.Message, "backdoor.yaml added outside kardinal")
	assert.Contains(t, res.Message, "nested/extra.json added outside kardinal")
	assert.NotContains(t, res.Message, "README.md")

	env.Render = &v1alpha1.RenderConfig{OnDrift: "overwrite"}
	_, err = promote(t, renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v3", "3.0.0"))
	require.NoError(t, err)
	files, _ := branchFiles(t, url, "env/prod")
	assert.NotContains(t, files, "backdoor.yaml", "overwrite removes the planted manifest")
	assert.NotContains(t, files, "nested/extra.json")
	assert.Equal(t, "docs\n", files["README.md"])
}

// TestRenderBranch_AnchoredMarker (QA on #1515): a push that edits a
// rendered file and rewrites the marker to match is not visible in the
// branch alone; with the marker digests of kardinal's earlier renders (the
// RenderRun status) it is drift.
func TestRenderBranch_AnchoredMarker(t *testing.T) {
	url, _ := seedRemote(t, dryRepo)
	env := v1alpha1.EnvironmentSpec{Name: "prod", Path: "environments/prod"}
	first := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v2", "2.0.0")
	_, err := promote(t, first)
	require.NoError(t, err)
	good := first.Outputs["markerDigest"]
	require.Len(t, good, 64)

	files, _ := branchFiles(t, url, "env/prod")
	forged := "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web, namespace: web-prod}\n# evil\n"
	marker := strings.Replace(files[".kardinal/rendered.yaml"], digestOf(files["web-prod_deployment-web.yaml"]), digestOf(forged), 1)
	pushFiles(t, url, "env/prod", map[string]*string{"web-prod_deployment-web.yaml": &forged, ".kardinal/rendered.yaml": &marker})

	state := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v3", "3.0.0")
	state.Render.KnownMarkerDigests = []string{good}
	res, err := promote(t, state)
	require.Error(t, err)
	assert.Contains(t, res.Message, ".kardinal/rendered.yaml is not one kardinal wrote")

	state = renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v3", "3.0.0")
	_, err = promote(t, state)
	assert.NoError(t, err, "without earlier digests the branch's own marker is trusted")
}

func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestRenderBranch_RollbackTrustsOnlyKardinalRenders (QA on #1515): a
// rollback renders the DRY commit of a render kardinal made: a commit whose
// trailer names the Bundle but carries a short DRY commit, or whose marker
// does not match its tree, is skipped, and a DRY commit that is not on the
// source branch is refused.
func TestRenderBranch_RollbackTrustsOnlyKardinalRenders(t *testing.T) {
	url, seed := seedRemote(t, dryRepo)
	env := v1alpha1.EnvironmentSpec{Name: "prod", Path: "environments/prod"}
	_, err := promote(t, renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v2", "2.0.0"))
	require.NoError(t, err)

	// An unreviewed DRY commit on a feature branch.
	c := scm.NewGoGitClient()
	repo, err := gogit.PlainOpen(seed)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, wt.Checkout(&gogit.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("evil"), Create: true}))
	writeFiles(t, seed, map[string]string{"base/deployment.yaml": strings.Replace(dryRepo["base/deployment.yaml"], "replicas: 1", "replicas: 99", 1)})
	require.NoError(t, c.CommitAll(context.Background(), seed, "evil", "e", "e@example.com"))
	require.NoError(t, c.Push(context.Background(), seed, "origin", "evil", "", false))
	head, err := repo.Head()
	require.NoError(t, err)
	evil := head.Hash().String()

	forge := func(t *testing.T, msg string, files map[string]*string) {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "forge")
		require.NoError(t, c.Clone(context.Background(), url, "env/prod", dir, ""))
		for p, content := range files {
			require.NoError(t, os.WriteFile(filepath.Join(dir, p), []byte(*content), 0o600))
		}
		require.NoError(t, c.CommitAll(context.Background(), dir, msg, "h", "h@example.com"))
		require.NoError(t, c.Push(context.Background(), dir, "origin", "env/prod", "", false))
	}
	rollback := func(t *testing.T) (parentsteps.StepResult, error) {
		st := renderState(t, url, filepath.Join(t.TempDir(), "w"), v1alpha1.EnvironmentSpec{Name: "prod", Path: "environments/prod",
			Render: &v1alpha1.RenderConfig{OnDrift: "overwrite"}}, "web-rb", "2.0.0")
		st.Bundle.Provenance = &v1alpha1.BundleProvenance{RollbackOf: "web-evil"}
		return promote(t, st)
	}

	t.Run("short DRY commit", func(t *testing.T) {
		forge(t, "x\n\nKardinal-Dry-Commit: "+evil[:12]+"\nKardinal-Bundle: web-evil\n", map[string]*string{"README.md": strp("x")})
		res, err := rollback(t)
		require.Error(t, err)
		assert.Contains(t, res.Message, "no render of it")
		assert.Contains(t, res.Message, "is not a full commit id")
	})
	t.Run("marker does not match", func(t *testing.T) {
		forge(t, "x\n\nKardinal-Dry-Commit: "+evil+"\nKardinal-Bundle: web-evil\n", map[string]*string{"README.md": strp("y")})
		res, err := rollback(t)
		require.Error(t, err)
		assert.Contains(t, res.Message, "its marker records Bundle \"web-v2\"")
	})
	t.Run("DRY commit not on the source branch", func(t *testing.T) {
		files, _ := branchFiles(t, url, "env/prod")
		m := files[".kardinal/rendered.yaml"]
		m = regexp.MustCompile(`(?m)^bundle: .*$`).ReplaceAllString(m, "bundle: web-evil")
		m = regexp.MustCompile(`(?m)^dryCommit: .*$`).ReplaceAllString(m, "dryCommit: "+evil)
		forge(t, "x\n\nKardinal-Dry-Commit: "+evil+"\nKardinal-Bundle: web-evil\n", map[string]*string{".kardinal/rendered.yaml": &m})
		res, err := rollback(t)
		require.Error(t, err)
		assert.True(t, errors.Is(err, parentsteps.ErrPermanent))
		assert.Contains(t, res.Message, "is not on main, so it is not rendered")
		files, _ = branchFiles(t, url, "env/prod")
		assert.NotContains(t, files["web-prod_deployment-web.yaml"], "replicas: 99")
	})
}

// TestRenderBranch_HelmRefusals (QA on #1515): a symbolic link anywhere in
// the DRY tree (to the ServiceAccount token, say) is refused before helm
// loads the chart, and a template calling a nondeterministic function is
// refused unless render.allowNondeterministic.
func TestRenderBranch_HelmRefusals(t *testing.T) {
	env := v1alpha1.EnvironmentSpec{Name: "prod", Path: "charts/web", Update: v1alpha1.UpdateConfig{Strategy: "helm"}}
	t.Run("symbolic link outside the chart", func(t *testing.T) {
		url, _ := seedRemote(t, dryRepo)
		state := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v2", "2.0.0")
		clone, err := parentsteps.Lookup("git-clone")
		require.NoError(t, err)
		_, err = clone.Execute(context.Background(), state)
		require.NoError(t, err)
		require.NoError(t, os.Symlink("/var/run/secrets/kubernetes.io/serviceaccount/token",
			filepath.Join(parentsteps.DrySourceDir(state.WorkDir), "token")))
		render, err := parentsteps.Lookup("render-manifests")
		require.NoError(t, err)
		res, err := render.Execute(context.Background(), state)
		require.Error(t, err)
		assert.Contains(t, res.Message, "symbolic link token in the DRY source is not supported")
	})
	t.Run("nondeterministic template", func(t *testing.T) {
		files := map[string]string{}
		for k, v := range dryRepo {
			files[k] = v
		}
		files["charts/web/templates/secret.yaml"] = "apiVersion: v1\nkind: Secret\nmetadata: {name: pw}\nstringData: {pw: {{ randAlphaNum 16 | quote }}}\n"
		url, _ := seedRemote(t, files)
		res, err := promote(t, renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v2", "2.0.0"))
		require.Error(t, err)
		assert.Contains(t, res.Message, "randAlphaNum changes from one render to the next")
		allow := env
		allow.Render = &v1alpha1.RenderConfig{AllowNondeterministic: true}
		_, err = promote(t, renderState(t, url, filepath.Join(t.TempDir(), "w"), allow, "web-v2", "2.0.0"))
		assert.NoError(t, err)
	})
}

// TestRenderManifests_OnlyInTheRenderJob: render-manifests refuses to run
// outside the kardinal-render Job, so the controller can never render.
func TestRenderManifests_OnlyInTheRenderJob(t *testing.T) {
	render, err := parentsteps.Lookup("render-manifests")
	require.NoError(t, err)
	state := func(withContext bool) *parentsteps.StepState {
		st := &parentsteps.StepState{Environment: v1alpha1.EnvironmentSpec{Name: "prod"}, WorkDir: t.TempDir()}
		if withContext {
			st.Render = &parentsteps.RenderContext{Namespace: "team"}
		}
		return st
	}
	const refused = "only in the kardinal-render Job, never in the controller"
	for _, tc := range []struct {
		name    string
		env     string
		context bool
	}{{"neither", "", false}, {"the Job's render context only", "", true}, {"the variable only", "1", false},
		{"another value", "true", true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(parentsteps.RenderJobEnv, tc.env)
			res, err := render.Execute(context.Background(), state(tc.context))
			require.Error(t, err)
			assert.True(t, errors.Is(err, parentsteps.ErrPermanent))
			assert.Contains(t, res.Message, refused)
		})
	}
	// Both: the guard lets it through (and the step goes on to its own
	// checks: this state is not layout: branch).
	t.Setenv(parentsteps.RenderJobEnv, "1")
	res, err := render.Execute(context.Background(), state(true))
	require.Error(t, err)
	assert.NotContains(t, res.Message, refused)
	assert.Contains(t, res.Message, "runs only with layout: branch")
}

// TestRenderBranch_DryPathTrailerDefaultPath (QA on #1515): an environment
// without spec.path renders environments/<name>, and the Kardinal-Dry-Path
// trailer and the marker say so.
func TestRenderBranch_DryPathTrailerDefaultPath(t *testing.T) {
	url, _ := seedRemote(t, dryRepo)
	state := renderState(t, url, filepath.Join(t.TempDir(), "w"), v1alpha1.EnvironmentSpec{Name: "prod"}, "web-v2", "2.0.0")
	_, err := promote(t, state)
	require.NoError(t, err)
	files, msg := branchFiles(t, url, "env/prod")
	assert.Contains(t, msg, "Kardinal-Dry-Path: environments/prod\n")
	assert.Contains(t, files[".kardinal/rendered.yaml"], "dryPath: environments/prod")
	assert.Contains(t, files[".kardinal/rendered.yaml"], "namespace: team")
}

// TestRenderBranch_LostResultMarkerAdopted (QA round 2 on #1515, M1): a
// render that pushed but whose result was lost leaves a marker no recorded
// render has. Listed as unconfirmed, its Bundle's marker is accepted when
// its files are unchanged, so the environment is not wedged; with a file
// changed it is still drift, and a marker naming another Bundle is not
// accepted.
func TestRenderBranch_LostResultMarkerAdopted(t *testing.T) {
	url, _ := seedRemote(t, dryRepo)
	env := v1alpha1.EnvironmentSpec{Name: "prod", Path: "environments/prod"}
	first := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v2", "2.0.0")
	_, err := promote(t, first)
	require.NoError(t, err)
	known := []string{first.Outputs["markerDigest"]}
	lost := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v3", "3.0.0")
	lost.Render.KnownMarkerDigests = known
	_, err = promote(t, lost) // pushed; its result never reached the RenderRun
	require.NoError(t, err)

	next := func(unconfirmed ...string) (*parentsteps.StepState, parentsteps.StepResult, error) {
		st := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v4", "4.0.0")
		st.Render.KnownMarkerDigests, st.Render.UnconfirmedBundles = known, unconfirmed
		res, err := promote(t, st)
		return st, res, err
	}
	_, res, err := next()
	require.Error(t, err, "without the unconfirmed Bundle the marker is unknown")
	assert.Contains(t, res.Message, "is not one kardinal wrote")
	_, res, err = next("web-x")
	require.Error(t, err, "another Bundle's render is not adopted")
	assert.Contains(t, res.Message, "is not one kardinal wrote")

	pushFiles(t, url, "env/prod", map[string]*string{"web-prod_deployment-web.yaml": strp("edited by hand\n")})
	_, res, err = next("web-v3")
	require.Error(t, err, "a file changed since that render is still drift")
	assert.Contains(t, res.Message, "web-prod_deployment-web.yaml changed")
}

// TestRenderBranch_LostResultMarkerAdoptedClean: the unconfirmed Bundle's
// unchanged render is adopted and the next render goes ahead.
func TestRenderBranch_LostResultMarkerAdoptedClean(t *testing.T) {
	url, _ := seedRemote(t, dryRepo)
	env := v1alpha1.EnvironmentSpec{Name: "prod", Path: "environments/prod"}
	first := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v2", "2.0.0")
	_, err := promote(t, first)
	require.NoError(t, err)
	known := []string{first.Outputs["markerDigest"]}
	lost := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v3", "3.0.0")
	lost.Render.KnownMarkerDigests = known
	_, err = promote(t, lost)
	require.NoError(t, err)
	st := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v4", "4.0.0")
	st.Render.KnownMarkerDigests, st.Render.UnconfirmedBundles = known, []string{"web-v3"}
	_, err = promote(t, st)
	require.NoError(t, err)
	assert.Equal(t, "web-v3", st.Outputs["markerAdopted"])
	files, _ := branchFiles(t, url, "env/prod")
	assert.Contains(t, files["web-prod_deployment-web.yaml"], "ghcr.io/org/web:4.0.0")
}

// TestRenderBranch_RollbackUsesKnownDigests (QA round 2 on #1515): with a
// record of kardinal's renders, a rollback trusts only a render whose marker
// is in it.
func TestRenderBranch_RollbackUsesKnownDigests(t *testing.T) {
	url, _ := seedRemote(t, dryRepo)
	env := v1alpha1.EnvironmentSpec{Name: "prod", Path: "environments/prod"}
	first := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v2", "2.0.0")
	_, err := promote(t, first)
	require.NoError(t, err)
	second := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-v3", "3.0.0")
	_, err = promote(t, second)
	require.NoError(t, err)
	rollback := func(known []string) (parentsteps.StepResult, error) {
		st := renderState(t, url, filepath.Join(t.TempDir(), "w"), env, "web-rb", "2.0.0")
		st.Bundle.Provenance = &v1alpha1.BundleProvenance{RollbackOf: "web-v2"}
		st.Render.KnownMarkerDigests = known
		return promote(t, st)
	}
	res, err := rollback([]string{strings.Repeat("0", 64), second.Outputs["markerDigest"]})
	require.Error(t, err)
	assert.Contains(t, res.Message, "its marker is not one of kardinal's recorded renders")
	_, err = rollback([]string{first.Outputs["markerDigest"], second.Outputs["markerDigest"]})
	assert.NoError(t, err, "web-v2's recorded render is trusted")
}
