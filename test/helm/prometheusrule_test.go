// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// kardinalAnchor keeps a series every controller-runtime operator exports
// to kardinal's own scrape targets: kardinal_pr_duration_seconds_count is a
// histogram without labels, so each kardinal target exports it from start-up
// and no other operator does. The Grafana dashboard uses the same join
// (#1941).
const kardinalAnchor = "and on (cluster, job, instance) kardinal_pr_duration_seconds_count"

// sharedMetric matches the metrics every controller-runtime operator exports.
var sharedMetric = regexp.MustCompile(`\b(controller_runtime_|workqueue_|go_|process_)\w+`)

// renderedRuleSpec renders the chart's PrometheusRule and returns its spec
// (the groups).
func renderedRuleSpec(t *testing.T) map[string]any {
	t.Helper()
	for _, d := range render(t, "kardinal-promoter", "--set", "prometheusRule.enabled=true") {
		if d.Kind != "PrometheusRule" {
			continue
		}
		var obj struct {
			Spec map[string]any `json:"spec"`
		}
		require.NoError(t, json.Unmarshal(d.raw, &obj))
		return obj.Spec
	}
	t.Fatal("no PrometheusRule rendered with prometheusRule.enabled=true")
	return nil
}

// TestChartPrometheusRuleScopedToKardinal (#2037): in a Prometheus that also
// scrapes other operators, an alert on a metric every controller-runtime
// operator exports (reconcile errors, work queues) judges only kardinal's
// targets: each such selector is followed by the join on kardinal's own
// series, and each aggregation keeps cluster and namespace, so two kardinal
// installations alert apart. A metric without the join (the chart before
// #2037) fails here.
func TestChartPrometheusRuleScopedToKardinal(t *testing.T) {
	spec := renderedRuleSpec(t)
	groups, _ := spec["groups"].([]any)
	require.NotEmpty(t, groups)
	aggregation := regexp.MustCompile(`\b(sum|max|min|avg|count)\s+by\s*\(([^)]*)\)`)
	checked := 0
	for _, g := range groups {
		rules, _ := g.(map[string]any)["rules"].([]any)
		for _, r := range rules {
			rule := r.(map[string]any)
			name, _ := rule["alert"].(string)
			expr, _ := rule["expr"].(string)
			shared := sharedMetric.FindAllString(expr, -1)
			if len(shared) == 0 {
				continue
			}
			checked++
			assert.Equal(t, len(shared), strings.Count(expr, kardinalAnchor),
				"%s: each of %v, which every operator exports, needs its own %q:\n%s", name, shared, kardinalAnchor, expr)
			for _, m := range aggregation.FindAllStringSubmatch(expr, -1) {
				for _, l := range []string{"cluster", "namespace"} {
					assert.Contains(t, strings.Split(strings.ReplaceAll(m[2], " ", ""), ","), l,
						"%s: %q drops %s, so two installations would alert as one", name, m[0], l)
				}
			}
		}
	}
	assert.Equal(t, 4, checked, "the four alerts on controller-runtime and work queue metrics")
}

// promtoolBin is bin/e2e/promtool when hack/ci/install-promtool.sh installed
// it (the version CI runs, hack/tool-versions.env), and promtool on PATH
// otherwise. Without either it skips the test locally and fails it in CI
// (CI=true, as GitHub Actions sets it, or KARDINAL_REQUIRE_PROMTOOL), so the
// PromQL tests never stop running there unnoticed.
func promtoolBin(t *testing.T) string {
	t.Helper()
	pinned := filepath.Join(repoRoot(t), "bin", "e2e", "promtool")
	if fi, err := os.Stat(pinned); err == nil && fi.Mode()&0o111 != 0 {
		return pinned
	}
	bin, err := exec.LookPath("promtool")
	if err == nil {
		return bin
	}
	if os.Getenv("CI") == "true" || os.Getenv("KARDINAL_REQUIRE_PROMTOOL") != "" {
		t.Fatalf("promtool is required in CI (run hack/ci/install-promtool.sh): %v", err)
	}
	t.Skip("promtool not installed (hack/ci/install-promtool.sh): skipping the PromQL unit tests")
	return ""
}

// TestChartPrometheusRuleAlertsOnlyOnKardinal (#2037) runs promtool's rule
// unit tests (testdata/prometheusrule_test.yaml) on the rendered rules:
// Crossplane's reconcile errors, another target of kardinal's job name, and
// Rancher Fleet's stalled "bundle" controller with a backlog alert nothing;
// kardinal's own errors, stall and backlog do, and its slow PolicyGate
// reconciles alert once per installation (cluster, namespace, controller). It needs promtool in CI
// (promtoolBin); the structure test above runs without it.
func TestChartPrometheusRuleAlertsOnlyOnKardinal(t *testing.T) {
	promtool := promtoolBin(t)
	raw, err := yaml.Marshal(renderedRuleSpec(t))
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rules.yaml"), raw, 0o600))
	tests, err := os.ReadFile(filepath.Join("testdata", "prometheusrule_test.yaml"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rules_test.yaml"), tests, 0o600))

	out, err := exec.Command(promtool, "check", "rules", filepath.Join(dir, "rules.yaml")).CombinedOutput()
	require.NoError(t, err, "promtool check rules:\n%s", out)
	cmd := exec.Command(promtool, "test", "rules", "rules_test.yaml")
	cmd.Dir = dir
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "promtool test rules:\n%s", out)
}
