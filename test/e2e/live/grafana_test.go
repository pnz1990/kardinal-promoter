//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// allVariables replaces the dashboard's query variables with the value
// Grafana sends for All.
var allVariables = strings.NewReplacer(`"$namespace"`, `".*"`, `"$cluster"`, `".*"`)

// pushIntruder puts another controller-runtime operator's metrics into the
// suite's Prometheus, through the Pushgateway, as a shared Prometheus holds
// them: a reconcile counter for controller "intruder" and a work queue named
// bundle. It deletes them when the test ends.
func pushIntruder(t *testing.T, e *framework.Env) {
	t.Helper()
	path := "/api/v1/namespaces/" + framework.MonitoringNamespace + "/services/http:pushgateway:9091/proxy/metrics/job/e2e-intruder"
	body := "# TYPE controller_runtime_reconcile_total counter\n" +
		"controller_runtime_reconcile_total{controller=\"intruder\",result=\"success\"} 5\n" +
		"# TYPE controller_runtime_reconcile_errors_total counter\n" +
		"controller_runtime_reconcile_errors_total{controller=\"intruder\"} 3\n" +
		"# TYPE workqueue_depth gauge\n" +
		"workqueue_depth{name=\"bundle\",controller=\"intruder\"} 7\n"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, e.Kube.CoreV1().RESTClient().Put().AbsPath(path).
		SetHeader("Content-Type", "text/plain; version=0.0.4").Body([]byte(body)).Do(ctx).Error())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = e.Kube.CoreV1().RESTClient().Delete().AbsPath(path).Do(ctx).Error()
	})
	framework.Eventually(t, time.Minute, "Prometheus to scrape the intruder's metrics", func(ctx context.Context) (bool, string) {
		s, err := e.PromQuery(ctx, `workqueue_depth{job="e2e-intruder"}`)
		if err != nil {
			return false, err.Error()
		}
		return len(s) == 1, fmt.Sprintf("%v", s)
	})
}

// alwaysHasData reports whether a dashboard query has data on any running
// controller: the controller's own job, controller-runtime's metrics, and
// the stats that fall back to vector(0). The kardinal_* series appear once
// a Bundle, step or gate records them, which the suite's other tests do at
// their own pace.
func alwaysHasData(expr string) bool {
	return strings.Contains(expr, `job="kardinal-promoter"`) || strings.Contains(expr, "controller_runtime_") ||
		strings.HasSuffix(expr, "or vector(0)")
}

// TestObs_GrafanaDashboard checks the chart's dashboard in Grafana
// (hack/e2e/up.sh flux enables grafanaDashboard; hack/e2e/components/grafana.sh
// runs Grafana, whose sidecar imports the ConfigMaps labelled
// grafana_dashboard=1 in kardinal-system). What a user who opens the
// dashboard sees: the ConfigMap carries the sidecar label and the chart's
// dashboard; Grafana lists it by title and serves it by uid as provisioned;
// every panel's query names a datasource the dashboard resolves to the
// Prometheus datasource, and runs there through Grafana without error; the
// controller's panels show data; the Work Queue Depth panel shows the
// policygate queue once a gate is queued; and with another operator's
// controller-runtime metrics in the same Prometheus, no panel shows them.
//
// Covers CHART-MON-02.
func TestObs_GrafanaDashboard(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()

	cm, err := e.Kube.CoreV1().ConfigMaps(framework.ControllerNamespace).Get(ctx, "kardinal-promoter-grafana-dashboard", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "1", cm.Labels["grafana_dashboard"], "the Grafana sidecar label")
	shipped, err := os.ReadFile("../../../chart/kardinal-promoter/dashboards/kardinal-promoter-dashboard.json")
	require.NoError(t, err)
	assert.JSONEq(t, string(shipped), cm.Data["kardinal-promoter-dashboard.json"], "the ConfigMap carries the chart's dashboard")
	var want struct {
		UID   string `json:"uid"`
		Title string `json:"title"`
	}
	require.NoError(t, json.Unmarshal(shipped, &want))
	require.Equal(t, "kardinal-promoter-v1", want.UID)

	var dash framework.GrafanaDashboard
	framework.Eventually(t, 2*time.Minute, "Grafana to import the dashboard", func(ctx context.Context) (bool, string) {
		hits, err := e.GrafanaSearch(ctx, want.Title)
		if err != nil {
			return false, err.Error()
		}
		found := false
		for _, h := range hits {
			found = found || (h.UID == want.UID && h.Title == want.Title && h.Type == "dash-db")
		}
		if !found {
			return false, fmt.Sprintf("search %q: %+v", want.Title, hits)
		}
		if err := e.GrafanaGet(ctx, "api/dashboards/uid/"+want.UID, nil, &dash); err != nil {
			return false, err.Error()
		}
		return true, ""
	})
	assert.True(t, dash.Meta.Provisioned, "the sidecar provisioned the dashboard")
	assert.Equal(t, want.Title, dash.Dashboard.Title)

	datasources, err := e.GrafanaDatasources(ctx)
	require.NoError(t, err)
	type query struct {
		framework.GrafanaPanelQuery
		ds framework.GrafanaDatasource
	}
	var queries []query
	resolved := true
	for _, q := range dash.Queries() {
		// The dashboard's cluster and namespace variables default to All:
		// Grafana sends their allValue, .*, which this test sends too.
		q.Expr = allVariables.Replace(q.Expr)
		ds, err := dash.Resolve(q.Datasource, datasources)
		resolved = assert.NoError(t, err, "panel %q", q.Panel) &&
			assert.Equal(t, framework.GrafanaPrometheusUID, ds.UID, "panel %q queries the Prometheus datasource", q.Panel) && resolved
		queries = append(queries, query{q, ds})
	}
	require.True(t, resolved, "every panel's datasource resolves to Prometheus")
	require.NotEmpty(t, queries)

	framework.Eventually(t, 2*time.Minute, "every panel's query to run in Grafana", func(ctx context.Context) (bool, string) {
		for _, q := range queries {
			r, err := e.GrafanaQuery(ctx, q.ds, q.Expr)
			if err != nil {
				return false, fmt.Sprintf("panel %q: %v", q.Panel, err)
			}
			if r.Error != "" || r.Status != 200 {
				return false, fmt.Sprintf("panel %q, %s: status %d: %s", q.Panel, q.Expr, r.Status, r.Error)
			}
			if alwaysHasData(q.Expr) && r.Rows() == 0 {
				return false, fmt.Sprintf("panel %q, %s: no data", q.Panel, q.Expr)
			}
		}
		return true, ""
	})
	t.Logf("%d panel queries ran on datasource %s", len(queries), framework.GrafanaPrometheusUID)

	// A queue's depth series appears once something is queued on it: queue a
	// PolicyGate.
	var depth *query
	for i := range queries {
		if queries[i].Panel == "Work Queue Depth" {
			depth = &queries[i]
		}
	}
	require.NotNil(t, depth, "the dashboard has a Work Queue Depth panel")
	e.CreateGate(t, framework.Gate(e.Namespace(t), "queued", "test", "true", recheck))
	var queues []string
	framework.Eventually(t, 2*time.Minute, "the Work Queue Depth panel to show the policygate queue", func(ctx context.Context) (bool, string) {
		r, err := e.GrafanaQuery(ctx, depth.ds, depth.Expr)
		if err != nil {
			return false, err.Error()
		}
		queues = queues[:0]
		for _, l := range r.SeriesLabels() {
			queues = append(queues, l["name"])
		}
		return slices.Contains(queues, "policygate"), fmt.Sprintf("queues %v", queues)
	})
	for _, q := range queues {
		assert.Contains(t, []string{"bundle", "promotionstep", "policygate"}, q, "the panel shows only the controller's queues")
	}

	// In a Prometheus that also scrapes other controller-runtime operators,
	// the controller-runtime and work queue panels show kardinal's series
	// only (#1941): each is scoped to kardinal's targets.
	pushIntruder(t, e)
	for _, q := range queries {
		if !strings.Contains(q.Expr, "controller_runtime_") && !strings.Contains(q.Expr, "workqueue_") {
			continue
		}
		r, err := e.GrafanaQuery(ctx, q.ds, q.Expr)
		require.NoError(t, err, "panel %q", q.Panel)
		for _, l := range r.SeriesLabels() {
			assert.NotEqual(t, "e2e-intruder", l["job"], "panel %q shows another operator's series: %v", q.Panel, l)
			assert.NotEqual(t, "intruder", l["controller"], "panel %q shows another operator's controller: %v", q.Panel, l)
		}
	}
}
