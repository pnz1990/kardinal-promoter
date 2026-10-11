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

package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func init() {
	// No unit test reaches a cluster: the host's default context may be a
	// real one. Tests that want one set initClient themselves.
	initClient = func() (sigs_client.Client, string, error) {
		return nil, "", errors.New("no cluster in unit tests")
	}
}

// TestInitFn_Minimal (C6): kardinal init with only --name and --repo writes
// examples/minimal/pipeline.yaml byte for byte; each non-default value adds
// only its own line.
//
// Covers CLI-INIT-02.
func TestInitFn_Minimal(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "minimal", "pipeline.yaml"))
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, initFn(&buf, &InitConfig{AppName: "my-app", GitURL: "https://github.com/myorg/gitops",
		Environments: []string{"test", "uat", "prod"}}))
	assert.Equal(t, string(want), buf.String(), "the golden output is examples/minimal")

	buf.Reset()
	require.NoError(t, initFn(&buf, &InitConfig{AppName: "web", Namespace: "team-a", GitURL: "https://github.com/o/r",
		Branch: "release", UpdateStrategy: "helm", ProdApproval: "auto", Secret: "git",
		Environments: []string{"dev", "prod"}, Health: map[string]string{"prod": "argocd"}}))
	assert.Equal(t, `apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata: { name: web, namespace: team-a }
spec:
  git:
    url: https://github.com/o/r
    branch: release
    secretRef: { name: git }
  environments:
    - name: dev
      update: { strategy: helm }
    - name: prod
      update: { strategy: helm }
      health: { type: argocd }
`, buf.String())
}

// runInitCmd runs kardinal init in dir with stdin and returns its stdout,
// stderr and error.
func runInitCmd(t *testing.T, dir, stdin string, args ...string) (string, string, error) {
	t.Helper()
	t.Chdir(dir)
	t.Cleanup(func() { globalOutput, globalNamespace = "", "" })
	root := NewRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"init"}, args...))
	err := root.Execute()
	return out.String(), errb.String(), err
}

// TestInitCmd_Flags (C6): --repo makes init ask nothing; the flags it
// checks, an existing file it does not overwrite without --force, and no
// input with no --repo an error that names the flags.
//
// Covers CLI-INIT-02.
func TestInitCmd_Flags(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "minimal", "pipeline.yaml"))
	require.NoError(t, err)
	dir := t.TempDir()
	out, errOut, err := runInitCmd(t, dir, "", "--name", "my-app", "--repo", "https://github.com/myorg/gitops", "--stdout")
	require.NoError(t, err)
	assert.Equal(t, string(want), out, "the golden output is examples/minimal")
	assert.Empty(t, errOut, "no questions")

	_, _, err = runInitCmd(t, dir, "", "--repo", "https://github.com/o/r")
	require.NoError(t, err)
	// The same again changes nothing and succeeds; another Pipeline needs
	// --force.
	out, _, err = runInitCmd(t, dir, "", "--repo", "https://github.com/o/r")
	require.NoError(t, err)
	assert.Contains(t, out, "Pipeline YAML in pipeline.yaml unchanged\n")
	_, _, err = runInitCmd(t, dir, "", "--repo", "https://github.com/o/r3")
	assert.EqualError(t, err, "init: pipeline.yaml exists with another Pipeline; overwrite it with --force, or write another file with -f")
	_, _, err = runInitCmd(t, dir, "", "--repo", "https://github.com/o/r2", "--force")
	require.NoError(t, err)
	content, _ := os.ReadFile(filepath.Join(dir, "pipeline.yaml"))
	assert.Contains(t, string(content), "url: https://github.com/o/r2\n")

	for args, msg := range map[string]string{
		"--repo http://github.com/o/r":              `init: --repo needs a git repository URL, https://host/owner/repo or ssh`,
		"--repo https://x/y --strategy argo":        `init: --strategy must be kustomize or helm, got "argo"`,
		"--repo https://x/y --prod-approval manual": `init: --prod-approval must be pr-review or auto, got "manual"`,
		"--repo https://x/y --envs bundle --stdout": "init: the Pipeline it would write is not valid:",
		"--repo https://x/y --apply":                "init: --apply and --create-secret need a cluster: no cluster in unit tests",
	} {
		_, _, err := runInitCmd(t, t.TempDir(), "", strings.Fields(args)...)
		require.Error(t, err, args)
		assert.Contains(t, err.Error(), msg, args)
	}

	_, _, err = runInitCmd(t, t.TempDir(), "", "--name", "web")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "give the answers as flags: kardinal init --name my-app --repo https://github.com/myorg/gitops")
}

// TestInitCmd_NoInjection (C6 QA): no flag value can add or change a field
// of the Pipeline init writes: names are DNS-1123, the repository a URL with
// no spaces, query or fragment, the branch a git branch name, and anything
// else is quoted and checked by decoding the YAML back.
//
// Covers CLI-INIT-02.
func TestInitCmd_NoInjection(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"name adds a namespace", []string{"--name", "x, namespace: kube-system"}, `init: the Pipeline name (--name) "x, namespace: kube-system" is not a valid name`},
		{"name not DNS-1123", []string{"--name", "My_App"}, `init: the Pipeline name (--name) "My_App" is not a valid name`},
		{"namespace not DNS-1123", []string{"-n", "team a"}, `init: the namespace (-n) "team a" is not a valid name`},
		{"environment closes the list", []string{"--envs", "a,b }, { name: c"}, `init: the environment (--envs) "b }" is not a valid name`},
		{"environment twice", []string{"--envs", "test,test"}, `init: the environment "test" is given twice (--envs)`},
		{"secret adds a key", []string{"--secret", "x }, url: evil"}, `init: the Secret name (--secret) "x }, url: evil" is not a valid name`},
		{"repo with a newline adds the branch", []string{"--repo", "https://github.com/o/r\nbranch: evil"}, "init: --repo needs a git repository URL"},
		{"repo cut at a comment", []string{"--repo", "https://github.com/o/r #x"}, "init: --repo needs a git repository URL"},
		{"repo with a fragment", []string{"--repo", "https://github.com/o/r#x"}, "init: --repo needs a git repository URL"},
		{"repo with a query", []string{"--repo", "https://github.com/o/r?a=b"}, "init: --repo needs a git repository URL"},
		{"repo with a password", []string{"--repo", "https://u:p@github.com/o/r"}, "init: --repo needs a git repository URL"},
		{"repo without a path", []string{"--repo", "https://github.com"}, "init: --repo needs a git repository URL"},
		{"branch adds the secret", []string{"--branch", "dev, secretRef: x"}, `init: --branch "dev, secretRef: x" is not a git branch name`},
		{"branch with a newline", []string{"--branch", "dev\nlayout: evil"}, "is not a git branch name"},
		{"branch with ..", []string{"--branch", "a..b"}, "is not a git branch name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--name", "web", "--repo", "https://github.com/o/r", "--stdout"}, tc.args...)
			out, _, err := runInitCmd(t, t.TempDir(), "", args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Empty(t, out, "nothing written")
		})
	}

	// Valid values that YAML would read otherwise are quoted, and the
	// Pipeline says exactly what was asked.
	for _, tc := range []struct{ flag, value, yaml string }{
		{"--repo", "git@github.com:o/r.git", "url: git@github.com:o/r.git"},
		{"--repo", "ssh://git@github.com/o/r", "url: ssh://git@github.com/o/r"},
		{"--branch", "release/1.0", "branch: release/1.0"},
		{"--branch", "true", `branch: "true"`},
		{"--branch", "1.10", `branch: "1.10"`},
		{"--envs", "on,off", `- name: "on"`},
	} {
		out, _, err := runInitCmd(t, t.TempDir(), "", "--name", "web", "--repo", "https://github.com/o/r", "--stdout", tc.flag, tc.value)
		require.NoError(t, err, tc.value)
		assert.Contains(t, out, tc.yaml, tc.value)
		var p v1alpha1.Pipeline
		require.NoError(t, yaml.UnmarshalStrict([]byte(out), &p))
		switch tc.flag {
		case "--repo":
			assert.Equal(t, tc.value, p.Spec.Git.URL)
		case "--branch":
			assert.Equal(t, tc.value, p.Spec.Git.Branch)
		case "--envs":
			assert.Equal(t, "on", p.Spec.Environments[0].Name)
		}
	}
}

// TestInitFn_NoNamespaceDefault (C6 QA): the namespace default is left out,
// so the wizard's default answer writes the minimal Pipeline.
func TestInitFn_NoNamespaceDefault(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, initFn(&buf, &InitConfig{AppName: "my-app", Namespace: "default", GitURL: "https://github.com/myorg/gitops",
		Environments: []string{"test", "uat", "prod"}}))
	want, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "minimal", "pipeline.yaml"))
	require.NoError(t, err)
	assert.Equal(t, string(want), buf.String())
}

// TestInitCmd_DetectSecretApply (C6): with a cluster, init finds the Argo
// CD Application or Flux Kustomization of each environment, creates the
// labelled git Secret from $GITHUB_TOKEN (else gh auth token) without
// printing it, and applies the Pipeline, replacing one only with --force.
//
// Covers CLI-INIT-02.
func TestInitCmd_DetectSecretApply(t *testing.T) {
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	app.SetName("web-prod")
	app.SetNamespace("argocd")
	ks := &unstructured.Unstructured{}
	ks.SetGroupVersionKind(schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"})
	ks.SetName("web-test")
	ks.SetNamespace("flux-system")
	c := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(app, ks).Build()
	prev := initClient
	initClient = func() (sigs_client.Client, string, error) { return c, "team-a", nil }
	t.Cleanup(func() { initClient = prev })
	t.Setenv("GITHUB_TOKEN", "s3cret-token")

	out, errOut, err := runInitCmd(t, t.TempDir(), "", "--name", "web", "--repo", "https://github.com/o/r", "--envs", "test,uat,prod",
		"--create-secret", "--apply")
	require.NoError(t, err)
	assert.Contains(t, errOut, "Found Argo CD Application argocd/web-prod: prod checks health with it\n")
	assert.Contains(t, errOut, "Found Flux Kustomization flux-system/web-test: test checks health with it\n")
	assert.Contains(t, out, "Secret team-a/github-token created from $GITHUB_TOKEN\n")
	assert.Contains(t, out, "Pipeline team-a/web created\n")
	assert.NotContains(t, out+errOut, "s3cret-token", "the token is never printed")
	assert.Contains(t, errOut, "Next: ship a version: kardinal ship web --image <ref>\n")

	var p v1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "web"}, &p))
	require.Len(t, p.Spec.Environments, 3)
	assert.Equal(t, "flux", p.Spec.Environments[0].Health.Type)
	assert.Empty(t, p.Spec.Environments[1].Health.Type)
	assert.Equal(t, "argocd", p.Spec.Environments[2].Health.Type)
	var s corev1.Secret
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "github-token"}, &s))
	assert.Equal(t, "true", s.Labels[v1alpha1.LabelSecretReferenceable])
	assert.Equal(t, "s3cret-token", s.StringData["token"])

	// Again: the Secret is kept, and the same Pipeline is unchanged; another
	// one is not replaced without --force.
	out, _, err = runInitCmd(t, t.TempDir(), "", "--name", "web", "--repo", "https://github.com/o/r", "--create-secret", "--apply")
	require.NoError(t, err)
	assert.Contains(t, out, "Pipeline team-a/web unchanged\n")
	_, _, err = runInitCmd(t, t.TempDir(), "", "--name", "web", "--repo", "https://github.com/o/r2", "--apply")
	assert.EqualError(t, err, "init: Pipeline team-a/web exists with another spec; replace it with --force")
	out, _, err = runInitCmd(t, t.TempDir(), "", "--name", "web", "--repo", "https://github.com/o/r2", "--create-secret", "--apply", "--force")
	require.NoError(t, err)
	assert.Contains(t, out, "Secret team-a/github-token exists; kept\n")
	assert.Contains(t, out, "Pipeline team-a/web replaced\n")

	// No GITHUB_TOKEN: gh auth token, and none at all is an error.
	t.Setenv("GITHUB_TOKEN", "")
	prevGH := ghToken
	t.Cleanup(func() { ghToken = prevGH })
	ghToken = func() (string, error) { return "", errors.New("not logged in") }
	_, _, err = runInitCmd(t, t.TempDir(), "", "--name", "other", "--repo", "https://github.com/o/r", "--secret", "other-token", "--create-secret", "--stdout")
	assert.EqualError(t, err, "init: --create-secret needs a git token: set GITHUB_TOKEN, or log in with gh auth login")
	ghToken = func() (string, error) { return "gh-token", nil }
	out, _, err = runInitCmd(t, t.TempDir(), "", "--name", "other", "--repo", "https://github.com/o/r", "--secret", "other-token", "--create-secret")
	require.NoError(t, err)
	assert.Contains(t, out, "Secret team-a/other-token created from gh auth token\n")
}

// TestInitScaffold_CreatesKustomizationFiles verifies that scaffoldGitOpsFn creates
// the expected kustomization.yaml files for each environment (O1, O7).
func TestInitScaffold_CreatesKustomizationFiles(t *testing.T) {
	tmpDir := t.TempDir()
	gitopsDir := filepath.Join(tmpDir, "gitops")
	envs := []string{"test", "uat", "prod"}

	var out bytes.Buffer
	err := scaffoldGitOpsFn(&out, envs, gitopsDir, "REPLACE_ME:latest")
	require.NoError(t, err)

	for _, env := range envs {
		kPath := filepath.Join(gitopsDir, "environments", env, "kustomization.yaml")
		content, err := os.ReadFile(kPath)
		require.NoError(t, err, "kustomization.yaml must exist for env %s", env)
		assert.Contains(t, string(content), "apiVersion: kustomize.config.k8s.io/v1beta1")
		assert.Contains(t, string(content), "kind: Kustomization")
		assert.Contains(t, string(content), "images:")
		assert.Contains(t, string(content), "REPLACE_ME")
	}

	summary := out.String()
	assert.Contains(t, summary, "created:")
	assert.Contains(t, summary, gitopsDir)
}

// TestInitScaffold_IdempotentOnSecondRun verifies that existing files are not
// overwritten on re-run (O1 idempotency, O7).
func TestInitScaffold_IdempotentOnSecondRun(t *testing.T) {
	tmpDir := t.TempDir()
	gitopsDir := filepath.Join(tmpDir, "gitops")
	envs := []string{"test", "prod"}
	imageRef := "REPLACE_ME:latest"

	// First run — creates files
	var out1 bytes.Buffer
	require.NoError(t, scaffoldGitOpsFn(&out1, envs, gitopsDir, imageRef))

	// Overwrite test env kustomization with custom content to verify it's not overwritten
	testKustomize := filepath.Join(gitopsDir, "environments", "test", "kustomization.yaml")
	customContent := "# custom content\napiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n"
	require.NoError(t, os.WriteFile(testKustomize, []byte(customContent), 0o644))

	// Second run — must skip existing files
	var out2 bytes.Buffer
	require.NoError(t, scaffoldGitOpsFn(&out2, envs, gitopsDir, imageRef))

	// Custom content must be preserved
	content, err := os.ReadFile(testKustomize)
	require.NoError(t, err)
	assert.Equal(t, customContent, string(content), "existing file must not be overwritten")

	// Second run output must mention "skipped"
	assert.Contains(t, out2.String(), "skipped")
}

// TestInitScaffold_DemoMode verifies that --demo uses the kardinal-test-app placeholder (O4, O7).
func TestInitScaffold_DemoMode(t *testing.T) {
	tmpDir := t.TempDir()
	gitopsDir := filepath.Join(tmpDir, "demo-gitops")
	envs := []string{"test", "uat", "prod"}

	var out bytes.Buffer
	err := scaffoldGitOpsFn(&out, envs, gitopsDir, demoImageRef)
	require.NoError(t, err)

	for _, env := range envs {
		kPath := filepath.Join(gitopsDir, "environments", env, "kustomization.yaml")
		content, err := os.ReadFile(kPath)
		require.NoError(t, err)
		assert.Contains(t, string(content), "kardinal-test-app", "demo kustomization must reference kardinal-test-app")
	}
}

// TestInitScaffold_AbsoluteAndRelativePaths verifies that both absolute and relative
// gitops-dir paths work (O5).
func TestInitScaffold_AbsoluteAndRelativePaths(t *testing.T) {
	t.Run("absolute path", func(t *testing.T) {
		tmpDir := t.TempDir()
		var out bytes.Buffer
		err := scaffoldGitOpsFn(&out, []string{"test"}, tmpDir, "img:latest")
		require.NoError(t, err)
		kPath := filepath.Join(tmpDir, "environments", "test", "kustomization.yaml")
		_, err = os.Stat(kPath)
		assert.NoError(t, err, "kustomization.yaml must be created at absolute path")
	})

	t.Run("relative path creates inside tmpdir", func(t *testing.T) {
		tmpDir := t.TempDir()
		relPath := filepath.Join(tmpDir, "rel-gitops")
		var out bytes.Buffer
		err := scaffoldGitOpsFn(&out, []string{"prod"}, relPath, "img:latest")
		require.NoError(t, err)
		kPath := filepath.Join(relPath, "environments", "prod", "kustomization.yaml")
		_, err = os.Stat(kPath)
		assert.NoError(t, err, "kustomization.yaml must be created at relative path")
	})
}

// TestBuildKustomization_ContainsRequiredFields verifies the kustomization template output.
func TestBuildKustomization_ContainsRequiredFields(t *testing.T) {
	tests := []struct {
		name     string
		imageRef string
		wantName string
		wantTag  string
	}{
		{
			name:     "tag ref",
			imageRef: "myrepo/myapp:v1.2.3",
			wantName: "myrepo/myapp",
			wantTag:  "v1.2.3",
		},
		{
			name:     "digest ref",
			imageRef: "myrepo/myapp@sha256:abc123",
			wantName: "myrepo/myapp",
			wantTag:  "",
		},
		{
			name:     "placeholder",
			imageRef: "REPLACE_ME:latest",
			wantName: "REPLACE_ME",
			wantTag:  "latest",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := buildKustomization(tc.imageRef)
			assert.Contains(t, out, "apiVersion: kustomize.config.k8s.io/v1beta1")
			assert.Contains(t, out, "kind: Kustomization")
			assert.Contains(t, out, "images:")
			assert.Contains(t, out, "name: "+tc.wantName)
			if tc.wantTag != "" {
				assert.Contains(t, out, "newTag: "+tc.wantTag)
			}
		})
	}
}

// C09b-cli-15: a registry port stays in the image name, and a digest is
// written as Kustomize's digest field, not as newTag.
func TestBuildKustomization_ImageBlock(t *testing.T) {
	cases := []struct {
		imageRef string
		want     string
	}{
		{"localhost:5000/app:v1", "  - name: localhost:5000/app\n    newTag: v1\n"},
		{"localhost:5000/app", "  - name: localhost:5000/app\n"},
		{"ghcr.io/org/app@sha256:abc", "  - name: ghcr.io/org/app\n    digest: sha256:abc\n"},
		{"localhost:5000/app:v1@sha256:abc", "  - name: localhost:5000/app\n    newTag: v1\n    digest: sha256:abc\n"},
	}
	for _, tc := range cases {
		t.Run(tc.imageRef, func(t *testing.T) {
			out := buildKustomization(tc.imageRef)
			_, images, ok := strings.Cut(out, "images:\n")
			require.True(t, ok, out)
			assert.Equal(t, tc.want, images)
		})
	}
}

// C09b-cli-15: the wizard requires a git repository URL (https or ssh) and a valid strategy,
// re-prompting on a bad answer and failing at end of input; it asks only
// for what no flag gives.
func TestRunInitWizard_Validation(t *testing.T) {
	none := func(string) bool { return false }
	t.Run("defaults only: the URL is required", func(t *testing.T) {
		err := runInitWizard(strings.NewReader("\n\n\n\n"), &bytes.Buffer{}, &InitConfig{}, none)
		require.Error(t, err)
		assert.Equal(t, "Git repository URL: a git repository URL is required (https://host/owner/repo or git@host:owner/repo.git)", err.Error())
	})
	t.Run("bad answers are re-prompted", func(t *testing.T) {
		var out bytes.Buffer
		cfg := &InitConfig{}
		err := runInitWizard(strings.NewReader(
			"web\nteam-a\ntest,prod\n\nhttp://github.com/o/r\nhttps://github.com/o/r\n\nargocd\nhelm\n"), &out, cfg, none)
		require.NoError(t, err)
		assert.Equal(t, &InitConfig{
			AppName: "web", Namespace: "team-a", Environments: []string{"test", "prod"},
			GitURL: "https://github.com/o/r", Branch: "main", UpdateStrategy: "helm",
		}, cfg)
		assert.Equal(t, 2, strings.Count(out.String(), "a git repository URL is required (https://host/owner/repo or git@host:owner/repo.git)"))
		assert.Contains(t, out.String(), `update strategy must be kustomize or helm, got "argocd"`)
	})
	t.Run("a flag's question is not asked", func(t *testing.T) {
		var out bytes.Buffer
		cfg := &InitConfig{AppName: "web", Environments: []string{"dev"}}
		set := func(f string) bool { return f == "name" || f == "envs" }
		require.NoError(t, runInitWizard(strings.NewReader("default\nhttps://github.com/o/r\nmain\nkustomize\n"), &out, cfg, set))
		assert.NotContains(t, out.String(), "Application name")
		assert.NotContains(t, out.String(), "Environments")
		assert.Equal(t, "web", cfg.AppName)
		assert.Equal(t, []string{"dev"}, cfg.Environments)
	})
}

// C09b-cli-15: --file names the output file, -o is the global format flag,
// and the Kustomize scaffold is skipped for helm.
func TestInitCmd_FileFlagAndHelmScaffold(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Cleanup(func() { globalOutput = "" })
	answers := "web\ndefault\ntest,prod\nhttps://github.com/o/r\nmain\nhelm\n"
	run := func(args ...string) (string, error) {
		root := NewRootCmd()
		var buf bytes.Buffer
		root.SetOut(&buf)
		root.SetErr(&bytes.Buffer{})
		root.SetIn(strings.NewReader(answers))
		root.SetArgs(append([]string{"init"}, args...))
		err := root.Execute()
		return buf.String(), err
	}

	out, err := run("--file", "deploy.yaml", "--scaffold-gitops", "--gitops-dir", "gitops")
	require.NoError(t, err)
	content, err := os.ReadFile(filepath.Join(dir, "deploy.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "url: https://github.com/o/r\n")
	assert.Contains(t, string(content), "update: { strategy: helm }\n")
	assert.Contains(t, out, "GitOps scaffold skipped: it writes Kustomize overlays, and the update strategy is helm\n")
	_, err = os.Stat(filepath.Join(dir, "gitops"))
	assert.True(t, os.IsNotExist(err), "no kustomization.yaml for helm")

	_, err = run("-o", "json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `-o json is not supported by "kardinal init"`)
	_, err = os.Stat(filepath.Join(dir, "json"))
	assert.True(t, os.IsNotExist(err), "-o is not a file path")
}

// --file creates the directories it names, as in the help's
// "kardinal init --file deploy/pipeline.yaml".
func TestInitCmd_FileInNewDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Cleanup(func() { globalOutput = "" })
	root := NewRootCmd()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetIn(strings.NewReader("web\ndefault\ntest,prod\nhttps://github.com/o/r\nmain\nkustomize\n"))
	root.SetArgs([]string{"init", "--file", filepath.Join("deploy", "base", "pipeline.yaml")})
	require.NoError(t, root.Execute())

	content, err := os.ReadFile(filepath.Join(dir, "deploy", "base", "pipeline.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "kind: Pipeline\n")
	assert.Contains(t, stdout.String(), "Pipeline YAML written to deploy/base/pipeline.yaml\n")
}

// --stdout replaces the Pipeline file only. --scaffold-gitops and
// --demo still write the scaffold, and stdout stays the bare YAML.
func TestInitCmd_StdoutKeepsScaffold(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Cleanup(func() { globalOutput = "" })
	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetIn(strings.NewReader("web\ndefault\ntest,prod\nhttps://github.com/o/r\nmain\nkustomize\n"))
	root.SetArgs([]string{"init", "--stdout", "--demo", "--gitops-dir", "gitops"})
	require.NoError(t, root.Execute())

	assert.True(t, strings.HasPrefix(stdout.String(), "apiVersion: kardinal.io/v1alpha1\n"), stdout.String())
	assert.NotContains(t, stdout.String(), "created:", "stdout is only the YAML")
	for _, env := range []string{"test", "prod"} {
		path := filepath.Join("gitops", "environments", env, "kustomization.yaml")
		content, err := os.ReadFile(path)
		require.NoError(t, err, "--demo scaffolds %s", env)
		assert.Contains(t, string(content), "newTag: sha-DEMO")
		assert.Contains(t, stderr.String(), "  created: "+path+"\n")
	}
	assert.Contains(t, stderr.String(), "GitOps scaffold written to gitops\n")
	_, err := os.Stat(filepath.Join(dir, "pipeline.yaml"))
	assert.True(t, os.IsNotExist(err), "--stdout writes no Pipeline file")
}

// TestInitCmd_NamespaceAndSSH (C6 follow-up): -n default is written when it
// is given (left out only when it is the wizard's default), and
// --create-secret with an ssh --repo is refused with the command that
// creates an ssh-key Secret, before anything is written.
//
// Covers CLI-INIT-02.
func TestInitCmd_NamespaceAndSSH(t *testing.T) {
	out, _, err := runInitCmd(t, t.TempDir(), "", "--name", "web", "--repo", "https://github.com/o/r", "-n", "default", "--stdout")
	require.NoError(t, err)
	assert.Contains(t, out, "metadata: { name: web, namespace: default }\n")
	out, _, err = runInitCmd(t, t.TempDir(), "", "--name", "web", "--repo", "https://github.com/o/r", "--stdout")
	require.NoError(t, err)
	assert.Contains(t, out, "metadata: { name: web }\n")

	for _, repo := range []string{"git@github.com:o/r.git", "ssh://git@git.example.com/o/r"} {
		dir := t.TempDir()
		out, _, err := runInitCmd(t, dir, "", "--name", "web", "--repo", repo, "-n", "team-a", "--secret", "deploy-key", "--create-secret")
		require.Error(t, err, repo)
		host := "github.com"
		if strings.HasPrefix(repo, "ssh://") {
			host = "git.example.com"
		}
		assert.Contains(t, err.Error(), "an ssh --repo needs an ssh key instead")
		assert.Contains(t, err.Error(), "kubectl create secret generic deploy-key -n team-a --from-file=sshPrivateKey=$HOME/.ssh/id_ed25519 "+
			`--from-literal=knownHosts="$(ssh-keyscan `+host+`)" && kubectl label secret deploy-key -n team-a kardinal.io/referenceable=true`)
		assert.Empty(t, out)
		assert.NoFileExists(t, filepath.Join(dir, "pipeline.yaml"), "nothing written")
	}
}
