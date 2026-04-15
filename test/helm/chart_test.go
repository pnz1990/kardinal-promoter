// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package helm contains tests that validate the Helm chart structure and
// that `helm template` produces valid Kubernetes YAML.
// These tests run in CI without a cluster (no kubectl apply --server-side).
package helm

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repoRoot walks up from the test file to find the repo root (the directory
// that contains go.mod).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (no go.mod found)")
		}
		dir = parent
	}
}

func helmBin(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not installed — skipping Helm chart tests")
	}
	return bin
}

func TestChartDirectoryExists(t *testing.T) {
	root := repoRoot(t)
	chartDir := filepath.Join(root, "chart", "kardinal-promoter")
	info, err := os.Stat(chartDir)
	require.NoError(t, err, "chart/kardinal-promoter directory must exist")
	assert.True(t, info.IsDir(), "chart/kardinal-promoter must be a directory")
}

func TestChartYamlExists(t *testing.T) {
	root := repoRoot(t)
	chartYAML := filepath.Join(root, "chart", "kardinal-promoter", "Chart.yaml")
	_, err := os.Stat(chartYAML)
	require.NoError(t, err, "Chart.yaml must exist")
}

func TestValuesYamlExists(t *testing.T) {
	root := repoRoot(t)
	valuesYAML := filepath.Join(root, "chart", "kardinal-promoter", "values.yaml")
	_, err := os.Stat(valuesYAML)
	require.NoError(t, err, "values.yaml must exist")
}

func TestRequiredTemplatesExist(t *testing.T) {
	root := repoRoot(t)
	templates := []string{
		"deployment.yaml",
		"serviceaccount.yaml",
		"clusterrole.yaml",
		"clusterrolebinding.yaml",
		"service.yaml",
		"krocodile.yaml",
		"_helpers.tpl",
	}
	for _, tmpl := range templates {
		path := filepath.Join(root, "chart", "kardinal-promoter", "templates", tmpl)
		_, err := os.Stat(path)
		assert.NoError(t, err, "template %s must exist", tmpl)
	}
}

func TestHelmLint(t *testing.T) {
	helm := helmBin(t)
	root := repoRoot(t)
	chartDir := filepath.Join(root, "chart", "kardinal-promoter")

	cmd := exec.Command(helm, "lint", chartDir)
	out, err := cmd.CombinedOutput()
	assert.NoError(t, err, "helm lint must pass:\n%s", string(out))
	assert.NotContains(t, strings.ToLower(string(out)), "error",
		"helm lint must produce no errors:\n%s", string(out))
}

func TestHelmTemplate(t *testing.T) {
	helm := helmBin(t)
	root := repoRoot(t)
	chartDir := filepath.Join(root, "chart", "kardinal-promoter")

	cmd := exec.Command(helm, "template", "kardinal-promoter", chartDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "helm template must succeed:\n%s", string(out))

	rendered := string(out)
	// Verify expected Kubernetes resources are rendered
	assert.Contains(t, rendered, "kind: Deployment", "must render a Deployment")
	assert.Contains(t, rendered, "kind: ServiceAccount", "must render a ServiceAccount")
	assert.Contains(t, rendered, "kind: ClusterRole", "must render a ClusterRole")
	assert.Contains(t, rendered, "kind: ClusterRoleBinding", "must render a ClusterRoleBinding")
	assert.Contains(t, rendered, "kind: Service", "must render a Service")
	// krocodile resources must be present when enabled (default)
	assert.Contains(t, rendered, "graph-controller", "must render krocodile Deployment")
	assert.Contains(t, rendered, "graphs.experimental.kro.run", "must render Graph CRD")
	assert.Contains(t, rendered, "graphrevisions.experimental.kro.run", "must render GraphRevision CRD")
}

func TestHelmTemplateKrocodileDisabled(t *testing.T) {
	helm := helmBin(t)
	root := repoRoot(t)
	chartDir := filepath.Join(root, "chart", "kardinal-promoter")

	// When krocodile.enabled=false, no krocodile resources should be rendered
	cmd := exec.Command(helm, "template", "kardinal-promoter", chartDir,
		"--set", "krocodile.enabled=false")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "helm template with krocodile disabled must succeed:\n%s", string(out))

	rendered := string(out)
	assert.NotContains(t, rendered, "graph-controller",
		"krocodile Deployment must NOT be rendered when disabled")
	assert.NotContains(t, rendered, "graphs.experimental.kro.run",
		"Graph CRD must NOT be rendered when disabled")
}

func TestHelmTemplateContainerImage(t *testing.T) {
	helm := helmBin(t)
	root := repoRoot(t)
	chartDir := filepath.Join(root, "chart", "kardinal-promoter")

	cmd := exec.Command(helm, "template", "kardinal-promoter", chartDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "helm template must succeed:\n%s", string(out))

	rendered := string(out)
	assert.Contains(t, rendered, "ghcr.io/pnz1990/kardinal-promoter/controller",
		"must use correct image repository")
}

func TestHelmTemplatePorts(t *testing.T) {
	helm := helmBin(t)
	root := repoRoot(t)
	chartDir := filepath.Join(root, "chart", "kardinal-promoter")

	cmd := exec.Command(helm, "template", "kardinal-promoter", chartDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "helm template must succeed:\n%s", string(out))

	rendered := string(out)
	// Metrics port 8080 and health port 8081 must be declared
	assert.Contains(t, rendered, "8080", "must declare metrics port 8080")
	assert.Contains(t, rendered, "8081", "must declare health port 8081")
}

func TestDockerignoreExists(t *testing.T) {
	root := repoRoot(t)
	dockerignore := filepath.Join(root, ".dockerignore")
	_, err := os.Stat(dockerignore)
	require.NoError(t, err, ".dockerignore must exist")
}

func TestDockerfileExists(t *testing.T) {
	root := repoRoot(t)
	dockerfile := filepath.Join(root, "Dockerfile")
	_, err := os.Stat(dockerfile)
	require.NoError(t, err, "Dockerfile must exist")
}

func TestDockerfileContent(t *testing.T) {
	root := repoRoot(t)
	dockerfile := filepath.Join(root, "Dockerfile")
	data, err := os.ReadFile(dockerfile)
	require.NoError(t, err)

	content := string(data)
	assert.Contains(t, content, "golang:1.25", "builder stage must use golang:1.25")
	// Final stage uses alpine with git+kustomize (required by promotion step engine).
	// Changed from distroless to alpine to include git and kustomize binaries.
	assert.Contains(t, content, "alpine", "final stage must use alpine image")
	assert.Contains(t, content, "65532", "final stage must use nonroot UID 65532")
	assert.Contains(t, content, "git", "final stage must install git")
	assert.Contains(t, content, "kustomize", "final stage must install kustomize")
	assert.Contains(t, content, "kardinal-controller", "must build kardinal-controller binary")
	assert.Contains(t, content, "ENTRYPOINT", "must set ENTRYPOINT")
}

func TestHelmTemplatePDBCreatedForMultiReplica(t *testing.T) {
	helm := helmBin(t)
	root := repoRoot(t)
	chartDir := filepath.Join(root, "chart", "kardinal-promoter")

	// With replicaCount=2, PDB must be created
	cmd := exec.Command(helm, "template", "kardinal-promoter", chartDir,
		"--set", "replicaCount=2",
		"--set", "pdb.enabled=true")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "helm template with replicaCount=2 must succeed:\n%s", string(out))

	rendered := string(out)
	assert.Contains(t, rendered, "kind: PodDisruptionBudget",
		"PDB must be rendered when replicaCount=2")
	assert.Contains(t, rendered, "minAvailable: 1",
		"PDB must set minAvailable: 1 by default")
}

func TestHelmTemplatePDBNotCreatedForSingleReplica(t *testing.T) {
	helm := helmBin(t)
	root := repoRoot(t)
	chartDir := filepath.Join(root, "chart", "kardinal-promoter")

	// With replicaCount=1 (default), PDB must NOT be created
	cmd := exec.Command(helm, "template", "kardinal-promoter", chartDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "helm template must succeed:\n%s", string(out))

	rendered := string(out)
	assert.NotContains(t, rendered, "kind: PodDisruptionBudget",
		"PDB must NOT be rendered when replicaCount=1 (single replica)")
}

func TestHelmTemplateTopologySpreadForMultiReplica(t *testing.T) {
	helm := helmBin(t)
	root := repoRoot(t)
	chartDir := filepath.Join(root, "chart", "kardinal-promoter")

	// With replicaCount=2, topology spread constraints must be added
	cmd := exec.Command(helm, "template", "kardinal-promoter", chartDir,
		"--set", "replicaCount=2",
		"--set", "topologySpread.enabled=true")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "helm template with replicaCount=2 must succeed:\n%s", string(out))

	rendered := string(out)
	assert.Contains(t, rendered, "topologySpreadConstraints",
		"topology spread constraints must be added when replicaCount=2")
	assert.Contains(t, rendered, "topology.kubernetes.io/zone",
		"topology spread must use zone topology key")
}
