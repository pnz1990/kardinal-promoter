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
		// CI sets KARDINAL_REQUIRE_HELM so a missing helm fails the job
		// instead of skipping every chart test.
		if os.Getenv("KARDINAL_REQUIRE_HELM") != "" {
			t.Fatalf("KARDINAL_REQUIRE_HELM is set but helm is not on PATH: %v", err)
		}
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
		"graph-rbac.yaml",
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
	// kro is a prerequisite, not bundled: no Graph controller or CRDs.
	assert.NotContains(t, rendered, "kind: CustomResourceDefinition", "kro CRDs must not be bundled")
	// The controller manages kro.run Graphs and the Graph identity.
	assert.Contains(t, rendered, `apiGroups: ["kro.run"]`, "controller must manage kro.run graphs")
	assert.Contains(t, rendered, "--graph-service-account=kardinal-graph")
	assert.Contains(t, rendered, "--graph-applier-clusterrole=kardinal-promoter-graph-applier")
	assert.Contains(t, rendered, "--graph-reader-clusterrole=kardinal-promoter-graph-reader")
}

// TestHelmTemplateGraphRBAC verifies the Graph ServiceAccount's ClusterRoles,
// the kro aggregation role, and that the controller's bind grant is limited to
// the two Graph ClusterRoles.
func TestHelmTemplateGraphRBAC(t *testing.T) {
	helm := helmBin(t)
	root := repoRoot(t)
	chartDir := filepath.Join(root, "chart", "kardinal-promoter")

	out, err := exec.Command(helm, "template", "kardinal-promoter", chartDir).CombinedOutput()
	require.NoError(t, err, "helm template must succeed:\n%s", string(out))
	rendered := string(out)
	assert.Contains(t, rendered, "name: kardinal-promoter-graph-applier")
	assert.Contains(t, rendered, "name: kardinal-promoter-graph-reader")
	assert.Contains(t, rendered, `rbac.kro.run/aggregate-to-controller: "true"`)
	assert.Contains(t, rendered, `verbs: ["bind"]
    resourceNames:
      - kardinal-promoter-graph-applier
      - kardinal-promoter-graph-reader`)

	out, err = exec.Command(helm, "template", "kardinal-promoter", chartDir,
		"--set", "graph.aggregateToKro=false").CombinedOutput()
	require.NoError(t, err, "helm template must succeed:\n%s", string(out))
	assert.NotContains(t, string(out), "rbac.kro.run/aggregate-to-controller")
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

// TestHelmTemplateUIAllowedHosts verifies --ui-allowed-hosts carries the
// controller Service's DNS names plus ui.allowedHosts, and that the schema
// rejects an entry with a scheme (the controller would refuse to start).
func TestHelmTemplateUIAllowedHosts(t *testing.T) {
	helm := helmBin(t)
	root := repoRoot(t)
	chartDir := filepath.Join(root, "chart", "kardinal-promoter")

	out, err := exec.Command(helm, "template", "kardinal-promoter", chartDir,
		"--namespace", "kardinal-system").CombinedOutput()
	require.NoError(t, err, "helm template must succeed:\n%s", string(out))
	assert.Contains(t, string(out), "--ui-allowed-hosts=kardinal-promoter,kardinal-promoter.kardinal-system,"+
		"kardinal-promoter.kardinal-system.svc,kardinal-promoter.kardinal-system.svc.cluster.local\n")

	out, err = exec.Command(helm, "template", "kardinal-promoter", chartDir, "--namespace", "kardinal-system",
		"--set", "ui.allowedHosts={kardinal.example.com}").CombinedOutput()
	require.NoError(t, err, "helm template must succeed:\n%s", string(out))
	assert.Contains(t, string(out), "kardinal-promoter.kardinal-system.svc.cluster.local,kardinal.example.com\n")

	out, err = exec.Command(helm, "template", "kardinal-promoter", chartDir,
		"--set", "ui.allowedHosts={https://kardinal.example.com}").CombinedOutput()
	assert.Error(t, err, "a scheme in ui.allowedHosts must fail the schema:\n%s", string(out))
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
	assert.Contains(t, content, "golang:1.26", "builder stage must use golang:1.26")
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

func TestHelmTemplatePrometheusRuleDisabledByDefault(t *testing.T) {
	helm := helmBin(t)
	root := repoRoot(t)
	chartDir := filepath.Join(root, "chart", "kardinal-promoter")

	// PrometheusRule must NOT be created by default (requires Prometheus Operator)
	cmd := exec.Command(helm, "template", "kardinal-promoter", chartDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "helm template must succeed:\n%s", string(out))

	rendered := string(out)
	assert.NotContains(t, rendered, "kind: PrometheusRule",
		"PrometheusRule must NOT be rendered by default (disabled)")
}

func TestHelmTemplatePrometheusRuleEnabledWhenConfigured(t *testing.T) {
	helm := helmBin(t)
	root := repoRoot(t)
	chartDir := filepath.Join(root, "chart", "kardinal-promoter")

	// PrometheusRule must be created when enabled
	cmd := exec.Command(helm, "template", "kardinal-promoter", chartDir,
		"--set", "prometheusRule.enabled=true")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "helm template with prometheusRule.enabled=true must succeed:\n%s", string(out))

	rendered := string(out)
	assert.Contains(t, rendered, "kind: PrometheusRule",
		"PrometheusRule must be rendered when prometheusRule.enabled=true")
	assert.Contains(t, rendered, "monitoring.coreos.com/v1",
		"PrometheusRule must use monitoring.coreos.com/v1 apiVersion")
	assert.Contains(t, rendered, "KardinalControllerDown",
		"PrometheusRule must include KardinalControllerDown alert")
	assert.Contains(t, rendered, "KardinalHighReconcileErrors",
		"PrometheusRule must include KardinalHighReconcileErrors alert")
	assert.Contains(t, rendered, "KardinalBundleReconcilerStalled",
		"PrometheusRule must include KardinalBundleReconcilerStalled alert")
	assert.Contains(t, rendered, "KardinalWorkQueueBacklog",
		"PrometheusRule must include KardinalWorkQueueBacklog alert")
	assert.Contains(t, rendered, "KardinalPolicyGateReconcileSlow",
		"PrometheusRule must include KardinalPolicyGateReconcileSlow alert")
	// Every alert must have a runbook_url
	assert.Contains(t, rendered, "runbook_url",
		"All alerts must include runbook_url annotation")
}

func TestHelmTemplatePrometheusRuleAdditionalLabels(t *testing.T) {
	helm := helmBin(t)
	root := repoRoot(t)
	chartDir := filepath.Join(root, "chart", "kardinal-promoter")

	// Additional labels must be propagated to the PrometheusRule
	cmd := exec.Command(helm, "template", "kardinal-promoter", chartDir,
		"--set", "prometheusRule.enabled=true",
		"--set", "prometheusRule.additionalLabels.release=kube-prometheus-stack")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "helm template with additionalLabels must succeed:\n%s", string(out))

	rendered := string(out)
	assert.Contains(t, rendered, "release: kube-prometheus-stack",
		"additionalLabels must be propagated to the PrometheusRule metadata")
}
