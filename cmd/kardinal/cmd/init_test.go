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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInitFn_GeneratesValidPipelineYAML verifies that initFn generates YAML
// with the required apiVersion/kind and all provided environments.
func TestInitFn_GeneratesValidPipelineYAML(t *testing.T) {
	cfg := &InitConfig{
		AppName:        "nginx-demo",
		Namespace:      "default",
		Environments:   []string{"test", "uat", "prod"},
		GitURL:         "https://github.com/myorg/gitops",
		Branch:         "main",
		UpdateStrategy: "kustomize",
	}

	var buf bytes.Buffer
	err := initFn(&buf, cfg)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "apiVersion: kardinal.io/v1alpha1")
	assert.Contains(t, out, "kind: Pipeline")
	assert.Contains(t, out, "name: nginx-demo")
	assert.Contains(t, out, "namespace: default")
	assert.Contains(t, out, "url: https://github.com/myorg/gitops")
	assert.Contains(t, out, "branch: main")
	assert.Contains(t, out, "strategy: kustomize")
}

// TestInitFn_AllEnvironments verifies that all environments appear in the output.
func TestInitFn_AllEnvironments(t *testing.T) {
	cfg := &InitConfig{
		AppName:        "my-app",
		Namespace:      "my-ns",
		Environments:   []string{"dev", "staging", "prod"},
		GitURL:         "https://github.com/test/repo",
		Branch:         "main",
		UpdateStrategy: "kustomize",
	}

	var buf bytes.Buffer
	err := initFn(&buf, cfg)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "name: dev")
	assert.Contains(t, out, "name: staging")
	assert.Contains(t, out, "name: prod")
}

// TestInitFn_DefaultApprovalModes verifies that the last environment gets
// pr-review and others get auto by default.
func TestInitFn_DefaultApprovalModes(t *testing.T) {
	cfg := &InitConfig{
		AppName:        "nginx-demo",
		Namespace:      "default",
		Environments:   []string{"test", "uat", "prod"},
		GitURL:         "https://github.com/myorg/gitops",
		Branch:         "main",
		UpdateStrategy: "kustomize",
	}

	var buf bytes.Buffer
	err := initFn(&buf, cfg)
	require.NoError(t, err)

	out := buf.String()
	// prod should have pr-review, others auto
	prodIdx := strings.Index(out, "name: prod")
	testIdx := strings.Index(out, "name: test")
	require.True(t, prodIdx >= 0, "prod env not found")
	require.True(t, testIdx >= 0, "test env not found")

	// Count occurrences of approval mode values
	assert.Contains(t, out, "approval: pr-review")
	assert.Contains(t, out, "approval: auto")
}

// TestInitFn_CredentialSecretRef verifies that a credentials secret ref is included.
func TestInitFn_CredentialSecretRef(t *testing.T) {
	cfg := &InitConfig{
		AppName:        "nginx-demo",
		Namespace:      "default",
		Environments:   []string{"test", "prod"},
		GitURL:         "https://github.com/myorg/gitops",
		Branch:         "main",
		UpdateStrategy: "kustomize",
	}

	var buf bytes.Buffer
	err := initFn(&buf, cfg)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "github-token", "credentials secret ref must reference github-token")
}

// TestInitFn_SingleEnv verifies that a single environment works.
func TestInitFn_SingleEnv(t *testing.T) {
	cfg := &InitConfig{
		AppName:        "simple-app",
		Namespace:      "ops",
		Environments:   []string{"prod"},
		GitURL:         "https://github.com/test/repo",
		Branch:         "main",
		UpdateStrategy: "kustomize",
	}

	var buf bytes.Buffer
	err := initFn(&buf, cfg)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "name: prod")
	assert.Contains(t, out, "approval: pr-review", "single env should always get pr-review")
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
			wantTag:  "sha256:abc123",
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
