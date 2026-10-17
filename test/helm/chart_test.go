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
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/compliance"
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
	t.Parallel()
	root := repoRoot(t)
	chartDir := filepath.Join(root, "chart", "kardinal-promoter")
	info, err := os.Stat(chartDir)
	require.NoError(t, err, "chart/kardinal-promoter directory must exist")
	assert.True(t, info.IsDir(), "chart/kardinal-promoter must be a directory")
}

func TestChartYamlExists(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	chartYAML := filepath.Join(root, "chart", "kardinal-promoter", "Chart.yaml")
	_, err := os.Stat(chartYAML)
	require.NoError(t, err, "Chart.yaml must exist")
}

func TestValuesYamlExists(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	valuesYAML := filepath.Join(root, "chart", "kardinal-promoter", "values.yaml")
	_, err := os.Stat(valuesYAML)
	require.NoError(t, err, "values.yaml must exist")
}

func TestRequiredTemplatesExist(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	assert.NotContains(t, rendered, "--graph-reader-", "the reader flags are gone (#1283)")
}

// TestHelmTemplateGraphRBAC verifies the Graph ServiceAccount's ClusterRole,
// the kro aggregation role, and that the controller's bind grant is limited to
// the Graph applier ClusterRole: no reader ClusterRole is shipped (#1283).
// Setting the removed graph.readerNamespaces fails the install, saying why.
func TestHelmTemplateGraphRBAC(t *testing.T) {
	t.Parallel()
	helm := helmBin(t)
	root := repoRoot(t)
	chartDir := filepath.Join(root, "chart", "kardinal-promoter")

	out, err := exec.Command(helm, "template", "kardinal-promoter", chartDir).CombinedOutput()
	require.NoError(t, err, "helm template must succeed:\n%s", string(out))
	rendered := string(out)
	assert.Contains(t, rendered, "name: kardinal-promoter-graph-applier")
	assert.NotContains(t, rendered, "graph-reader")
	assert.Contains(t, rendered, `rbac.kro.run/aggregate-to-controller: "true"`)
	assert.Contains(t, rendered, `resources: ["rolebindings"]
    verbs: ["get", "list", "create", "update", "delete"]`)
	assert.Contains(t, rendered, `verbs: ["bind"]
    resourceNames:
      - kardinal-promoter-graph-applier
`)
	for _, kind := range []string{"deployments", "applications", "rollouts", "kustomizations", "canaries"} {
		assert.NotContains(t, kroWatchRules(t, rendered), kind, "kro no longer watches health kinds for Graphs")
	}

	out, err = exec.Command(helm, "template", "kardinal-promoter", chartDir,
		"--set", "graph.readerNamespaces={argocd}").CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "graph.readerNamespaces was removed (#1283)")

	out, err = exec.Command(helm, "template", "kardinal-promoter", chartDir,
		"--set", "graph.aggregateToKro=false").CombinedOutput()
	require.NoError(t, err, "helm template must succeed:\n%s", string(out))
	assert.NotContains(t, string(out), "rbac.kro.run/aggregate-to-controller")
}

func TestHelmTemplateContainerImage(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
			assert.Equal(t, len(flags), found, "the controller must get the --graph-applier-clusterrole flag")
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
	t.Parallel()
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

// TestHelmTemplateTracing verifies the tracing values: nothing is rendered
// while tracing is off, the flags when it is on (samplingRatio 0 included),
// and the schema refuses a bad endpoint or ratio.
func TestHelmTemplateTracing(t *testing.T) {
	t.Parallel()
	helm := helmBin(t)
	chartDir := filepath.Join(repoRoot(t), "chart", "kardinal-promoter")

	out, err := exec.Command(helm, "template", "kardinal-promoter", chartDir).CombinedOutput()
	require.NoError(t, err, "helm template must succeed:\n%s", string(out))
	assert.NotContains(t, string(out), "--tracing-", "tracing is off by default")

	out, err = exec.Command(helm, "template", "kardinal-promoter", chartDir, "--set", "tracing.enabled=true",
		"--set", "tracing.endpoint=http://otel-collector.observability:4318", "--set", "tracing.samplingRatio=0").CombinedOutput()
	require.NoError(t, err, "helm template must succeed:\n%s", string(out))
	for _, arg := range []string{"- --tracing-enabled=true\n", "- --tracing-endpoint=http://otel-collector.observability:4318\n",
		"- --tracing-sampling-ratio=0\n"} {
		assert.Contains(t, string(out), arg)
	}
	assert.NotContains(t, string(out), "--tracing-insecure")

	out, err = exec.Command(helm, "template", "kardinal-promoter", chartDir, "--set", "tracing.enabled=true",
		"--set", "tracing.endpoint=otel:4318", "--set", "tracing.insecure=true").CombinedOutput()
	require.NoError(t, err, "helm template must succeed:\n%s", string(out))
	assert.Contains(t, string(out), "- --tracing-insecure=true\n")
	assert.Contains(t, string(out), "- --tracing-sampling-ratio=0.1\n", "the default ratio")

	for _, bad := range [][]string{{"tracing.endpoint=grpc://otel:4317"}, {"tracing.endpoint=http://a b"}, {"tracing.samplingRatio=2"}} {
		out, err = exec.Command(helm, "template", "kardinal-promoter", chartDir, "--set", bad[0]).CombinedOutput()
		assert.Error(t, err, "%s must fail the schema:\n%s", bad[0], string(out))
	}
}

// TestHelmTemplateEgressAllowlist verifies egress.allowlist renders
// --egress-allowlist (and nothing when empty, which keeps the default of no
// allowlist), and that the schema rejects a URL instead of a host or CIDR.
func TestHelmTemplateEgressAllowlist(t *testing.T) {
	t.Parallel()
	helm := helmBin(t)
	chartDir := filepath.Join(repoRoot(t), "chart", "kardinal-promoter")

	out, err := exec.Command(helm, "template", "kardinal-promoter", chartDir).CombinedOutput()
	require.NoError(t, err, "helm template must succeed:\n%s", string(out))
	assert.NotContains(t, string(out), "--egress-allowlist", "no allowlist by default")

	out, err = exec.Command(helm, "template", "kardinal-promoter", chartDir,
		"--set", "egress.allowlist={hooks.slack.com,*.webhook.office.com,10.0.0.0/8,fd00::/8}").CombinedOutput()
	require.NoError(t, err, "helm template must succeed:\n%s", string(out))
	assert.Contains(t, string(out), "- --egress-allowlist=hooks.slack.com,*.webhook.office.com,10.0.0.0/8,fd00::/8\n")

	for _, bad := range []string{"https://hooks.slack.com", "hooks.slack.com/path", "a b", "*"} {
		out, err = exec.Command(helm, "template", "kardinal-promoter", chartDir,
			"--set-json", `egress.allowlist=["`+bad+`"]`).CombinedOutput()
		assert.Error(t, err, "%q in egress.allowlist must fail the schema:\n%s", bad, string(out))
	}
}

func TestDockerignoreExists(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dockerignore := filepath.Join(root, ".dockerignore")
	_, err := os.Stat(dockerignore)
	require.NoError(t, err, ".dockerignore must exist")
}

func TestDockerfileExists(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dockerfile := filepath.Join(root, "Dockerfile")
	_, err := os.Stat(dockerfile)
	require.NoError(t, err, "Dockerfile must exist")
}

func TestHelmTemplatePDBCreatedForMultiReplica(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// rulesGrant reports whether rules grant verb on group/resource. As in RBAC,
// "*" in resources does not match a subresource ("pipelines/pause").
func rulesGrant(d map[string]interface{}, group, resource, verb string) bool {
	rules, _ := d["rules"].([]interface{})
	for _, r := range rules {
		in := func(key, want string) bool {
			vals, _ := dig(r, key).([]interface{})
			for _, v := range vals {
				if v == want || (v == "*" && (key != "resources" || !strings.Contains(want, "/"))) {
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
	t.Parallel()
	roles := userRoles(renderChart(t, "kardinal-promoter"), "kardinal-promoter")
	require.Len(t, roles, 5)
	viewer, promoter, approver, admin := roles["viewer"], roles["promoter"], roles["approver"], roles["admin"]
	for _, kind := range []string{"pipelines", "bundles", "policygates", "promotionsteps", "auditevents"} {
		for _, r := range []map[string]interface{}{viewer, promoter, approver} {
			assert.True(t, rulesGrant(r, "kardinal.io", kind, "list"), "%v lists %s", dig(r, "metadata", "name"), kind)
		}
	}
	// The viewer reads every kardinal kind: one per CRD in config/crd/bases.
	crds, err := filepath.Glob(filepath.Join(repoRoot(t), "config", "crd", "bases", "kardinal.io_*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, crds)
	for _, f := range crds {
		kind := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "kardinal.io_"), ".yaml")
		for _, verb := range []string{"get", "list", "watch"} {
			assert.True(t, rulesGrant(viewer, "kardinal.io", kind, verb), "the viewer may %s %s", verb, kind)
		}
	}
	assert.True(t, rulesGrant(viewer, "", "events", "list"))
	assert.False(t, rulesGrant(viewer, "kardinal.io", "bundles", "create"), "the viewer cannot create")
	assert.True(t, rulesGrant(promoter, "kardinal.io", "bundles", "create"))
	assert.True(t, rulesGrant(promoter, "kardinal.io", "pipelines/pause", "update"))
	assert.True(t, rulesGrant(promoter, "kardinal.io", "pipelines/hold", "update"), "a promoter rolls back and holds")
	assert.False(t, rulesGrant(approver, "kardinal.io", "pipelines/hold", "update"), "an approver cannot hold")
	assert.False(t, rulesGrant(viewer, "kardinal.io", "pipelines/hold", "update"))
	assert.False(t, rulesGrant(promoter, "kardinal.io", "policygates/override", "update"), "a promoter cannot approve")
	assert.True(t, rulesGrant(approver, "kardinal.io", "policygates/override", "update"))
	assert.True(t, rulesGrant(approver, "kardinal.io", "approvals", "create"))
	assert.True(t, rulesGrant(approver, "kardinal.io", "approvals", "delete"), "an approver revokes their approval")
	assert.False(t, rulesGrant(promoter, "kardinal.io", "approvals", "create"), "a promoter cannot approve")
	assert.False(t, rulesGrant(approver, "kardinal.io", "bundles", "create"), "an approver cannot promote")
	// Least privilege: no update on the objects themselves by default.
	for _, r := range []map[string]interface{}{viewer, promoter, approver} {
		for _, kind := range []string{"pipelines", "policygates", "pipelines/edit", "policygates/edit"} {
			for _, verb := range []string{"update", "patch"} {
				assert.False(t, rulesGrant(r, "kardinal.io", kind, verb), "%v %s %s", dig(r, "metadata", "name"), verb, kind)
			}
		}
	}
	assert.True(t, rulesGrant(roles["admin-extra"], "kardinal.io", "pipelines", "delete"))
	for _, sub := range []string{"pipelines/pause", "pipelines/hold", "pipelines/edit", "policygates/override", "policygates/edit"} {
		assert.True(t, rulesGrant(roles["admin-extra"], "kardinal.io", sub, "update"), "admin %s", sub)
	}
	assert.Equal(t, map[string]interface{}{"kardinal.io/aggregate-to-admin": "true", "app.kubernetes.io/instance": "kardinal-promoter"},
		dig(admin, "aggregationRule", "clusterRoleSelectors").([]interface{})[0].(map[string]interface{})["matchLabels"])
	// Aggregation into view/edit/admin is opt-in: with it, everyone bound to
	// edit can both promote and approve.
	agg := userRoles(renderChart(t, "kardinal-promoter", "--set", "rbac.userRoles.aggregateToDefaultRoles=true"), "kardinal-promoter")
	assert.Equal(t, "true", dig(agg["viewer"], "metadata", "labels", "rbac.authorization.k8s.io/aggregate-to-view"))
	assert.Equal(t, "true", dig(agg["promoter"], "metadata", "labels", "rbac.authorization.k8s.io/aggregate-to-edit"))
	assert.Equal(t, "true", dig(agg["admin-extra"], "metadata", "labels", "rbac.authorization.k8s.io/aggregate-to-admin"))

	for name, r := range roles { // the default
		labels, _ := dig(r, "metadata", "labels").(map[string]interface{})
		for k := range labels {
			assert.NotContains(t, k, "rbac.authorization.k8s.io/aggregate-to-", "%s", name)
		}
	}
	assert.Empty(t, userRoles(renderChart(t, "kardinal-promoter", "--set", "rbac.userRoles.enabled=false"), "kardinal-promoter"))

	direct := userRoles(renderChart(t, "kardinal-promoter", "--set", "rbac.userRoles.directWrites=true"), "kardinal-promoter")
	assert.True(t, rulesGrant(direct["promoter"], "kardinal.io", "pipelines", "patch"), "directWrites: kardinal pause")
	assert.False(t, rulesGrant(direct["promoter"], "kardinal.io", "policygates", "patch"))
	assert.True(t, rulesGrant(direct["approver"], "kardinal.io", "policygates", "patch"), "directWrites: kardinal override")
	assert.False(t, rulesGrant(direct["approver"], "kardinal.io", "pipelines", "patch"))
}

// TestHelmTemplateTokenReviewOptions verifies the audience flags, the
// API-server-audience opt-in and the refusal of a static UI token together
// with TokenReview.
func TestHelmTemplateTokenReviewOptions(t *testing.T) {
	t.Parallel()
	helm := helmBin(t)
	chartDir := filepath.Join(repoRoot(t), "chart", "kardinal-promoter")
	render := func(args ...string) (string, error) {
		out, err := exec.Command(helm, append([]string{"template", "kardinal-promoter", chartDir}, args...)...).CombinedOutput()
		return string(out), err
	}
	out, err := render()
	require.NoError(t, err, out)
	assert.NotContains(t, out, "--tokenreview-audiences", "no TokenReview mode, no flag")
	tests := []struct {
		name    string
		args    []string
		want    []string
		absent  []string
		wantErr string
	}{
		{name: "default audience", args: []string{"--set", "ui.auth.tokenReview=true"},
			want: []string{"- --tokenreview-audiences=kardinal-promoter\n"}, absent: []string{"accept-apiserver-audience", "static-overrides"}},
		{name: "bundle API alone", args: []string{"--set", "bundleAPI.tokenReview=true", "--set", "tokenReview.audiences={ci,people}"},
			want: []string{"- --tokenreview-audiences=ci,people\n"}},
		{name: "API server audience opt-in", args: []string{"--set", "ui.auth.tokenReview=true", "--set", "tokenReview.acceptAPIServerAudience=true"},
			want: []string{"- --tokenreview-accept-apiserver-audience=true\n"}},
		{name: "no audience refused", args: []string{"--set", "ui.auth.tokenReview=true", "--set", "tokenReview.audiences=null"},
			wantErr: "tokenReview.audiences is empty"},
		// #1930: the UI's opt-in (SSO) is not the Bundle API's.
		{name: "UI opt-in stays on the UI", args: []string{"--set", "ui.auth.tokenReview=true", "--set", "bundleAPI.tokenReview=true",
			"--set", "tokenReview.acceptAPIServerAudience=true"},
			want: []string{"- --tokenreview-accept-apiserver-audience=true\n"}, absent: []string{"bundle-api-tokenreview-accept-apiserver-audience"}},
		{name: "Bundle API opt-in", args: []string{"--set", "bundleAPI.tokenReview=true", "--set", "bundleAPI.acceptAPIServerAudience=true"},
			want: []string{"- --bundle-api-tokenreview-accept-apiserver-audience=true\n"}, absent: []string{"- --tokenreview-accept-apiserver-audience"}},
		{name: "UI opt-in alone renders nothing for the Bundle API", args: []string{"--set", "bundleAPI.tokenReview=true",
			"--set", "tokenReview.acceptAPIServerAudience=true"}, absent: []string{"accept-apiserver-audience"}},
		{name: "Bundle API opt-in without the Bundle API renders nothing", args: []string{"--set", "ui.auth.tokenReview=true",
			"--set", "bundleAPI.acceptAPIServerAudience=true"}, absent: []string{"bundle-api-tokenreview-accept-apiserver-audience"}},
		{name: "no audience, Bundle API without its opt-in refused", args: []string{"--set", "ui.auth.tokenReview=true", "--set", "bundleAPI.tokenReview=true",
			"--set", "tokenReview.audiences=null", "--set", "tokenReview.acceptAPIServerAudience=true"},
			wantErr: "for the Bundle API, bundleAPI.acceptAPIServerAudience=true"},
		{name: "static token and TokenReview refused", args: []string{"--set", "ui.auth.tokenReview=true", "--set", "ui.auth.tokenSecretRef.name=ui"},
			wantErr: "ui.auth.allowStaticTokenWithTokenReview=true"},
		{name: "static token and TokenReview allowed", args: []string{"--set", "ui.auth.tokenReview=true", "--set", "ui.auth.tokenSecretRef.name=ui",
			"--set", "ui.auth.allowStaticTokenWithTokenReview=true"}, want: []string{"- --ui-auth-static-overrides-tokenreview=true\n"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := render(tt.args...)
			if tt.wantErr != "" {
				require.Error(t, err, out)
				assert.Contains(t, out, tt.wantErr)
				return
			}
			require.NoError(t, err, out)
			for _, w := range tt.want {
				assert.Contains(t, out, w)
			}
			for _, a := range tt.absent {
				assert.NotContains(t, out, a)
			}
		})
	}
}

// TestHelmTemplateBundleAPITokenReview verifies bundleAPI.tokenReview: the
// flag and the TokenReview/SubjectAccessReview grants.
func TestHelmTemplateBundleAPITokenReview(t *testing.T) {
	t.Parallel()
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

// kroWatchRules is the text of the kro aggregation ClusterRole in rendered.
func kroWatchRules(t *testing.T, rendered string) string {
	t.Helper()
	i := strings.Index(rendered, "-kro-watch\n")
	require.Positive(t, i, "the kro-watch ClusterRole is rendered")
	rest := rendered[i:]
	if j := strings.Index(rest, "\n---"); j > 0 {
		rest = rest[:j]
	}
	return rest
}

// TestHelmTemplateAuditorRole (spec 22 section 4.7, #2051, QA): the auditor
// reads are split so a namespace auditor cannot read other namespaces.
// <release>-auditor grants get and list on exactly the namespaced kinds of
// kardinal audit evidence-pack (compliance.PackFiles; each one Namespaced in
// its CRD) and <release>-auditor-cluster on exactly the cluster-scoped ones
// (Cluster in their CRDs) and the admission policies and bindings the pack
// lists by label; together they are the pack's reads. No write, no watch,
// no other kind, neither aggregated into admin.
//
// Covers EVIDENCE-PACK-AUDITOR-01.
func TestHelmTemplateAuditorRole(t *testing.T) {
	t.Parallel()
	roles := map[string]map[string]interface{}{}
	for _, d := range renderChart(t, "kardinal-promoter") {
		if name, _ := dig(d, "metadata", "name").(string); d["kind"] == "ClusterRole" &&
			(name == "kardinal-promoter-auditor" || name == "kardinal-promoter-auditor-cluster") {
			roles[name] = d
		}
	}
	require.Len(t, roles, 2, "the chart renders kardinal-promoter-auditor and -auditor-cluster")
	resources := map[string]string{
		"AuditEvent": "auditevents", "Approval": "approvals", "PolicyGate": "policygates", "Bundle": "bundles",
		"Pipeline": "pipelines", "ChangeWindow": "changewindows", "AgentIdentity": "agentidentities", "Verdict": "verdicts",
		"PromotionReceipt": "promotionreceipts",
	}
	// Each kind's scope, from its CRD.
	var namespaced, cluster []string
	for kind := range compliance.PackFiles {
		res, ok := resources[kind]
		require.True(t, ok, "a kind of the pack has a resource here: %s", kind)
		b, err := os.ReadFile(filepath.Join(repoRoot(t), "config", "crd", "bases", "kardinal.io_"+res+".yaml"))
		require.NoError(t, err)
		var crd map[string]interface{}
		require.NoError(t, sigyaml.Unmarshal(b, &crd))
		switch dig(crd, "spec", "scope") {
		case "Namespaced":
			namespaced = append(namespaced, res)
		case "Cluster":
			cluster = append(cluster, res)
		default:
			t.Fatalf("%s: unknown scope %v", res, dig(crd, "spec", "scope"))
		}
	}
	sort.Strings(namespaced)
	sort.Strings(cluster)
	type rule struct {
		Groups, Resources, Verbs []string
	}
	rulesOf := func(d map[string]interface{}) []rule {
		var got []rule
		for _, r := range d["rules"].([]interface{}) {
			strs := func(key string) []string {
				var out []string
				for _, v := range dig(r, key).([]interface{}) {
					out = append(out, v.(string))
				}
				sort.Strings(out)
				return out
			}
			got = append(got, rule{strs("apiGroups"), strs("resources"), strs("verbs")})
		}
		return got
	}
	read := []string{"get", "list"}
	assert.Equal(t, []rule{{[]string{"kardinal.io"}, namespaced, read}}, rulesOf(roles["kardinal-promoter-auditor"]),
		"the namespace role reads only namespaced kinds")
	assert.Equal(t, []rule{
		{[]string{"kardinal.io"}, cluster, read},
		{[]string{"admissionregistration.k8s.io"}, []string{"validatingadmissionpolicies", "validatingadmissionpolicybindings"}, read},
	}, rulesOf(roles["kardinal-promoter-auditor-cluster"]), "the cluster role reads only cluster-scoped kinds")
	for name, r := range roles {
		labels, _ := dig(r, "metadata", "labels").(map[string]interface{})
		assert.NotContains(t, labels, "kardinal.io/aggregate-to-admin", "%s is not part of admin", name)
	}
	for _, d := range renderChart(t, "kardinal-promoter", "--set", "rbac.userRoles.enabled=false") {
		name, _ := dig(d, "metadata", "name").(string)
		assert.NotContains(t, name, "-auditor", "rbac.userRoles.enabled=false renders no auditor role")
	}
}
