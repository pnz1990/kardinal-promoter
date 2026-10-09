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
	sigyaml "sigs.k8s.io/yaml"
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

// helmBin is bin/e2e/helm when make e2e-tools installed it, the helm version
// CI runs (hack/tool-versions.env; versions word errors differently), and
// helm on PATH otherwise.
func helmBin(t *testing.T) string {
	t.Helper()
	pinned := filepath.Join(repoRoot(t), "bin", "e2e", "helm")
	if fi, err := os.Stat(pinned); err == nil && fi.Mode()&0o111 != 0 {
		return pinned
	}
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
	assert.Contains(t, rendered, "--graph-reader-namespaces=argocd,flux-system")
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
	// Reader RoleBindings no Graph needs any more are pruned; the sweep lists
	// them.
	assert.Contains(t, rendered, `resources: ["rolebindings"]
    verbs: ["get", "list", "create", "update", "delete"]`)
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
	ports := map[int64]bool{}
	for _, d := range renderChart(t, "kardinal-promoter") {
		if d["kind"] != "Deployment" {
			continue
		}
		containers, _ := dig(d, "spec", "template", "spec", "containers").([]interface{})
		for _, c := range containers {
			cps, _ := dig(c, "ports").([]interface{})
			for _, p := range cps {
				if n, ok := dig(p, "containerPort").(float64); ok {
					ports[int64(n)] = true
				}
			}
		}
	}
	assert.True(t, ports[8080], "the Deployment must declare containerPort 8080 (metrics), got %v", ports)
	assert.True(t, ports[8081], "the Deployment must declare containerPort 8081 (health), got %v", ports)
}

// TestHelmTemplateGraphFlagsNameRenderedObjects renders the chart under two
// release names and checks that the controller's --graph-*-clusterrole flags
// name ClusterRoles the chart creates. `make install` uses the release name
// kardinal, whose object names differ from kardinal-promoter. (The Graph
// ServiceAccount is created by the controller in each Graph's namespace.)
func TestHelmTemplateGraphFlagsNameRenderedObjects(t *testing.T) {
	for _, release := range []string{"kardinal-promoter", "kardinal"} {
		t.Run(release, func(t *testing.T) {
			have := map[string]bool{}
			var args []interface{}
			for _, d := range renderChart(t, release) {
				kind, _ := d["kind"].(string)
				name, _ := dig(d, "metadata", "name").(string)
				have[kind+"/"+name] = true
				if kind == "Deployment" {
					containers, _ := dig(d, "spec", "template", "spec", "containers").([]interface{})
					for _, c := range containers {
						a, _ := dig(c, "args").([]interface{})
						args = append(args, a...)
					}
				}
			}
			flags := map[string]string{
				"--graph-applier-clusterrole=": "ClusterRole/",
				"--graph-reader-clusterrole=":  "ClusterRole/",
			}
			found := 0
			for _, a := range args {
				arg, _ := a.(string)
				for flag, kind := range flags {
					if strings.HasPrefix(arg, flag) {
						found++
						assert.True(t, have[kind+strings.TrimPrefix(arg, flag)], "%s names an object the chart does not render", arg)
					}
				}
			}
			assert.Equal(t, len(flags), found, "the controller must get both --graph-*-clusterrole flags")
		})
	}
}

// renderChart runs helm template for the chart and returns the documents.
func renderChart(t *testing.T, release string, extra ...string) []map[string]interface{} {
	t.Helper()
	helm := helmBin(t)
	chartDir := filepath.Join(repoRoot(t), "chart", "kardinal-promoter")
	out, err := exec.Command(helm, append([]string{"template", release, chartDir}, extra...)...).Output()
	require.NoError(t, err, "helm template must succeed")
	var docs []map[string]interface{}
	for _, raw := range strings.Split(string(out), "\n---") {
		var d map[string]interface{}
		require.NoError(t, sigyaml.Unmarshal([]byte(raw), &d))
		if d != nil {
			docs = append(docs, d)
		}
	}
	require.NotEmpty(t, docs)
	return docs
}

// dig returns the value at a map path, or nil.
func dig(v interface{}, keys ...string) interface{} {
	for _, k := range keys {
		m, ok := v.(map[string]interface{})
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
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
	alerts := 0
	for _, d := range renderChart(t, "kardinal-promoter", "--set", "prometheusRule.enabled=true") {
		if d["kind"] != "PrometheusRule" {
			continue
		}
		groups, _ := dig(d, "spec", "groups").([]interface{})
		for _, g := range groups {
			rules, _ := dig(g, "rules").([]interface{})
			for _, r := range rules {
				alert, _ := dig(r, "alert").(string)
				if alert == "" {
					continue
				}
				alerts++
				url, _ := dig(r, "annotations", "runbook_url").(string)
				assert.NotEmpty(t, url, "alert %s must have a runbook_url annotation", alert)
			}
		}
	}
	assert.GreaterOrEqual(t, alerts, 5, "expected the five alerts above")
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

// userRoles returns the user ClusterRoles of a render by name suffix.
func userRoles(docs []map[string]interface{}, fullname string) map[string]map[string]interface{} {
	out := map[string]map[string]interface{}{}
	for _, d := range docs {
		if d["kind"] != "ClusterRole" {
			continue
		}
		name, _ := dig(d, "metadata", "name").(string)
		for _, r := range []string{"viewer", "promoter", "approver", "admin-extra", "admin"} {
			if name == fullname+"-"+r {
				out[r] = d
			}
		}
	}
	return out
}

// rulesGrant reports whether rules grant verb on group/resource.
func rulesGrant(d map[string]interface{}, group, resource, verb string) bool {
	rules, _ := d["rules"].([]interface{})
	for _, r := range rules {
		in := func(key, want string) bool {
			vals, _ := dig(r, key).([]interface{})
			for _, v := range vals {
				if v == want || v == "*" {
					return true
				}
			}
			return false
		}
		if in("apiGroups", group) && in("resources", resource) && in("verbs", verb) {
			return true
		}
	}
	return false
}

// TestHelmTemplateUserRoles verifies rbac.userRoles: the four ClusterRoles
// and admin-extra with the documented grants, the admin aggregating this
// release's roles only, the aggregation into view/edit/admin and its opt-out,
// and nothing when disabled.
func TestHelmTemplateUserRoles(t *testing.T) {
	roles := userRoles(renderChart(t, "kardinal-promoter"), "kardinal-promoter")
	require.Len(t, roles, 5)
	viewer, promoter, approver, admin := roles["viewer"], roles["promoter"], roles["approver"], roles["admin"]
	for _, kind := range []string{"pipelines", "bundles", "policygates", "promotionsteps", "auditevents"} {
		for _, r := range []map[string]interface{}{viewer, promoter, approver} {
			assert.True(t, rulesGrant(r, "kardinal.io", kind, "list"), "%v lists %s", dig(r, "metadata", "name"), kind)
		}
	}
	assert.True(t, rulesGrant(viewer, "", "events", "list"))
	assert.False(t, rulesGrant(viewer, "kardinal.io", "bundles", "create"), "the viewer cannot create")
	assert.True(t, rulesGrant(promoter, "kardinal.io", "bundles", "create"))
	assert.True(t, rulesGrant(promoter, "kardinal.io", "pipelines", "update"))
	assert.False(t, rulesGrant(promoter, "kardinal.io", "policygates", "update"), "a promoter cannot approve")
	assert.True(t, rulesGrant(approver, "kardinal.io", "policygates", "update"))
	assert.False(t, rulesGrant(approver, "kardinal.io", "bundles", "create"), "an approver cannot promote")
	assert.True(t, rulesGrant(roles["admin-extra"], "kardinal.io", "pipelines", "delete"))
	assert.Equal(t, map[string]interface{}{"kardinal.io/aggregate-to-admin": "true", "app.kubernetes.io/instance": "kardinal-promoter"},
		dig(admin, "aggregationRule", "clusterRoleSelectors").([]interface{})[0].(map[string]interface{})["matchLabels"])
	assert.Equal(t, "true", dig(viewer, "metadata", "labels", "rbac.authorization.k8s.io/aggregate-to-view"))
	assert.Equal(t, "true", dig(promoter, "metadata", "labels", "rbac.authorization.k8s.io/aggregate-to-edit"))
	assert.Equal(t, "true", dig(roles["admin-extra"], "metadata", "labels", "rbac.authorization.k8s.io/aggregate-to-admin"))

	noAgg := userRoles(renderChart(t, "kardinal-promoter", "--set", "rbac.userRoles.aggregateToDefaultRoles=false"), "kardinal-promoter")
	for name, r := range noAgg {
		labels, _ := dig(r, "metadata", "labels").(map[string]interface{})
		for k := range labels {
			assert.NotContains(t, k, "rbac.authorization.k8s.io/aggregate-to-", "%s", name)
		}
	}
	assert.Empty(t, userRoles(renderChart(t, "kardinal-promoter", "--set", "rbac.userRoles.enabled=false"), "kardinal-promoter"))
}

// TestHelmTemplateBundleAPITokenReview verifies bundleAPI.tokenReview: the
// flag and the TokenReview/SubjectAccessReview grants.
func TestHelmTemplateBundleAPITokenReview(t *testing.T) {
	helm := helmBin(t)
	chartDir := filepath.Join(repoRoot(t), "chart", "kardinal-promoter")
	out, err := exec.Command(helm, "template", "kardinal-promoter", chartDir).CombinedOutput()
	require.NoError(t, err, string(out))
	assert.NotContains(t, string(out), "--bundle-api-tokenreview-auth")
	assert.NotContains(t, string(out), "tokenreviews")
	out, err = exec.Command(helm, "template", "kardinal-promoter", chartDir, "--set", "bundleAPI.tokenReview=true").CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "- --bundle-api-tokenreview-auth=true\n")
	assert.Contains(t, string(out), `resources: ["tokenreviews"]`)
	assert.Contains(t, string(out), `resources: ["subjectaccessreviews"]`)
}
