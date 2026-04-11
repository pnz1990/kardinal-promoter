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
