//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

const (
	// metricInterval is the shortest MetricCheck interval; values below it
	// are raised to it.
	metricInterval = "10s"
	// metricTimeout bounds one MetricCheck re-evaluation: an interval plus
	// a Prometheus scrape and reconcile latency.
	metricTimeout = time.Minute
)

// passFail is the MetricCheck result for pass.
func passFail(pass bool) string {
	if pass {
		return "Pass"
	}
	return "Fail"
}

// TestMetric_ThresholdOperators checks every threshold operator both ways,
// on a one-element vector and on a scalar(): the result, lastValue and reason
// the MetricCheck shows. When the value changes, the next evaluation follows
// it.
//
// Covers METRIC-01.
func TestMetric_ThresholdOperators(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	e.SetMetric(t, ns, 5)
	vector := framework.PushedQuery(ns)
	scalar := "scalar(" + vector + ")"
	checks := []struct {
		name, query, op string
		value           float64
		at5, at45       bool // the result at 5 and at 4.5
	}{
		{"lt-pass", vector, "lt", 6, true, true},
		{"lt-fail", scalar, "lt", 5, false, true},
		{"gt-pass", scalar, "gt", 4, true, true},
		{"gt-fail", vector, "gt", 5, false, false},
		{"lte-pass", vector, "lte", 5, true, true},
		{"lte-fail", scalar, "lte", 4.5, false, true},
		{"gte-pass", scalar, "gte", 5, true, false},
		{"gte-fail", vector, "gte", 5.5, false, false},
		{"eq-pass", vector, "eq", 5, true, false},
		{"eq-fail", scalar, "eq", 4, false, false},
	}
	for _, c := range checks {
		e.CreateMetricCheck(t, framework.MetricCheck(t, ns, c.name, c.query, c.op, c.value, metricInterval))
	}
	for _, at := range []struct {
		value string
		pass  func(int) bool
	}{
		{"5", func(i int) bool { return checks[i].at5 }},
		{"4.5", func(i int) bool { return checks[i].at45 }},
	} {
		if at.value == "4.5" {
			e.SetMetric(t, ns, 4.5)
		}
		for i, c := range checks {
			want := fmt.Sprintf("%s %s %g = %t", at.value, c.op, c.value, at.pass(i))
			e.WaitMetricCheck(t, ns, c.name, metricTimeout, "at "+at.value, func(mc *v1alpha1.MetricCheck) bool {
				return mc.Status.Result == passFail(at.pass(i)) && mc.Status.LastValue == at.value && mc.Status.Reason == want
			})
		}
	}
}

// TestMetric_GateBlocksUntilMetricPasses checks a gate on metrics.<name>,
// one on .result and one on .value: before the MetricCheck exists the
// expression cannot be evaluated and blocks; while the MetricCheck fails the
// gates are false and prod does not start; once it passes, the gates
// re-evaluate at once (their recheck is 5m) and prod promotes. Every reason
// names the Bundle version, the evaluation error's too.
//
// Covers METRIC-02.
func TestMetric_GateBlocksUntilMetricPasses(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newFluxApp(t, e, "prod")
	const metric = "error-rate"
	gates := map[string]string{
		"metric-result": fmt.Sprintf(`metrics[%q].result == "Pass"`, metric),
		"metric-value":  fmt.Sprintf(`double(metrics[%q].value) < 0.5`, metric),
	}
	for name, expr := range gates {
		e.CreateGate(t, framework.Gate(a.ns, name, "prod", expr, "5m"))
	}
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	version := "bundle.version=" + fixtures.V2 + ": "

	for name := range gates {
		g := e.WaitGateReady(t, a.ns, bundle, "prod", name, false, "CEL evaluation error: no such key: "+metric, gateTimeout)
		assert.True(t, strings.HasPrefix(g.Status.Reason, version),
			"%s: an evaluation error names the Bundle version too: %q", name, g.Status.Reason)
	}

	e.SetMetric(t, a.ns, 1)
	e.CreateMetricCheck(t, framework.MetricCheck(t, a.ns, metric, framework.PushedQuery(a.ns), "lt", 0.5, metricInterval))
	e.WaitMetricCheck(t, a.ns, metric, metricTimeout, "failing", framework.MetricResult("Fail", "1 lt 0.5 = false"))
	for name, expr := range gates {
		e.WaitGateReady(t, a.ns, bundle, "prod", name, false, version+expr+" = false", gateTimeout)
	}
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)
	row := e.WaitExplainGate(t, a.ns, pipelineName, "prod", "metric-result", "Block", 10*time.Second)
	assert.Contains(t, row, "= false", "explain shows the gate's reason")
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))

	e.SetMetric(t, a.ns, 0)
	mc := e.WaitMetricCheck(t, a.ns, metric, metricTimeout, "passing", framework.MetricResult("Pass", "0 lt 0.5 = true"))
	for name, expr := range gates {
		g := e.WaitGateReady(t, a.ns, bundle, "prod", name, true, version+expr+" = true", gateTimeout)
		cond := meta.FindStatusCondition(g.Status.Conditions, "Ready")
		require.NotNil(t, cond, "%s has a Ready condition", name)
		lag := cond.LastTransitionTime.Sub(mc.Status.LastEvaluatedAt.Time)
		assert.True(t, lag >= -time.Second && lag <= 5*time.Second,
			"%s re-evaluated when the MetricCheck passed, not at its recheck: %s after it", name, lag)
	}
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assert.Equal(t, fixtures.Image+":"+fixtures.V2, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
}

// TestMetric_StaleResultFailsClosed makes the API server deny the
// controller's status writes for a passing MetricCheck. The controller
// retries once after 5s, then at the MetricCheck's interval, not in a tight
// backoff loop. The last result still reads Pass, but once its validUntil
// passes gates read it as stale and block, with a note naming the metric.
// When writes are allowed again the MetricCheck recovers within one
// interval, and the gates pass.
//
// Covers METRIC-03, METRIC-09.
func TestMetric_StaleResultFailsClosed(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newFluxApp(t, e, "prod")
	const metric = "error-rate"
	e.SetMetric(t, a.ns, 0)
	e.CreateMetricCheck(t, framework.MetricCheck(t, a.ns, metric, framework.PushedQuery(a.ns), "lt", 0.5, metricInterval))
	mc := e.WaitMetricCheck(t, a.ns, metric, metricTimeout, "passing", framework.MetricResult("Pass", "0 lt 0.5 = true"))
	require.NotNil(t, mc.Status.ValidUntil)
	assert.Equal(t, 30*time.Second, mc.Status.ValidUntil.Sub(mc.Status.LastEvaluatedAt.Time),
		"a result of a 10s MetricCheck is valid for three intervals")

	resultExpr := fmt.Sprintf(`metrics[%q].result == "Pass"`, metric)
	valueExpr := fmt.Sprintf(`double(metrics[%q].value) < 0.5`, metric)
	e.CreateGate(t, framework.Gate(a.ns, "metric-result", "prod", resultExpr, recheck))
	e.CreateGate(t, framework.Gate(a.ns, "metric-value", "prod", valueExpr, recheck))
	a.apply(t, a.pipeline(nil))

	policy, lift := e.DenyStatusWrites(t, a.ns, metric)
	base, err := e.AdmissionDenials(ctx, policy)
	require.NoError(t, err)
	var since time.Time
	framework.Eventually(t, time.Minute, "the controller's status write to be denied", func(ctx context.Context) (bool, string) {
		n, err := e.AdmissionDenials(ctx, policy)
		if err != nil {
			return false, err.Error()
		}
		since = time.Now()
		return n > base, fmt.Sprintf("%g denied writes", n-base)
	})
	framework.Consistently(t, 12*time.Second, "the controller to retry the denied write after 5s, then at its 10s interval", func(ctx context.Context) (bool, string) {
		n, err := e.AdmissionDenials(ctx, policy)
		if err != nil {
			return false, err.Error()
		}
		return n-base <= 3, fmt.Sprintf("%g denied writes in %s", n-base, time.Since(since).Round(time.Second))
	})

	// Nothing refreshes the result: it still reads Pass, then goes stale.
	e.WaitMetricCheck(t, a.ns, metric, time.Minute, "still Pass with validUntil in the past", func(mc *v1alpha1.MetricCheck) bool {
		return mc.Status.Result == "Pass" && mc.Status.ValidUntil != nil && time.Since(mc.Status.ValidUntil.Time) > time.Second
	})
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	note := fmt.Sprintf("metric %q result is stale", metric)
	e.WaitGateReady(t, a.ns, bundle, "prod", "metric-result", false, resultExpr+" = false; "+note, gateTimeout)
	g := e.WaitGateReady(t, a.ns, bundle, "prod", "metric-value", false, note, gateTimeout)
	assert.Contains(t, g.Status.Reason, "CEL evaluation error", "a stale value is empty, so double() fails")
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
	n, err := e.AdmissionDenials(ctx, policy)
	require.NoError(t, err)
	elapsed := time.Since(since)
	assert.LessOrEqual(t, n-base, elapsed.Seconds()/10+2, "at most one denied write per interval over %s", elapsed.Round(time.Second))

	liftAt := time.Now()
	lift()
	e.WaitMetricCheck(t, a.ns, metric, 15*time.Second, "to write a fresh result within one interval", func(mc *v1alpha1.MetricCheck) bool {
		return mc.Status.Result == "Pass" && mc.Status.LastEvaluatedAt != nil &&
			!mc.Status.LastEvaluatedAt.Time.Before(liftAt.Add(-time.Second))
	})
	e.WaitGateReady(t, a.ns, bundle, "prod", "metric-result", true, resultExpr+" = true", gateTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "metric-value", true, valueExpr+" = true", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assert.Equal(t, fixtures.Image+":"+fixtures.V2, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
}

// TestMetric_WriteRetryAfterEdit makes the API server deny the status writes
// of a passing MetricCheck with a 2m interval, then edits the MetricCheck (an
// annotation) seconds after its last write. The edit evaluates at once, and
// its denied write is retried at the interval, not every 5s until the 2m are
// up. Once writes are allowed again, the next edit writes a fresh result.
//
// Covers METRIC-10.
func TestMetric_WriteRetryAfterEdit(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	ns := e.Namespace(t)
	const name = "edited"
	e.CreateMetricCheck(t, framework.MetricCheck(t, ns, name, "vector(0)", "lt", 1, "2m"))
	mc := e.WaitMetricCheck(t, ns, name, metricTimeout, "passing", framework.MetricResult("Pass", "0 lt 1 = true"))
	written := mc.Status.LastEvaluatedAt.Time
	edit := func(n int) {
		t.Helper()
		obj := &v1alpha1.MetricCheck{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
		require.NoError(t, e.Client.Patch(ctx, obj, client.RawPatch(types.MergePatchType,
			fmt.Appendf(nil, `{"metadata":{"annotations":{"e2e.kardinal.io/edit":"%d"}}}`, n))))
	}

	policy, lift := e.DenyStatusWrites(t, ns, name)
	base, err := e.AdmissionDenials(ctx, policy)
	require.NoError(t, err)
	edit(1)
	framework.Eventually(t, 30*time.Second, "the edit's status write to be denied", func(ctx context.Context) (bool, string) {
		n, err := e.AdmissionDenials(ctx, policy)
		if err != nil {
			return false, err.Error()
		}
		return n > base, fmt.Sprintf("%g denied writes", n-base)
	})
	edited := time.Now()
	require.Less(t, edited.Sub(written), 80*time.Second, "the edit came well before the 2m evaluation")
	framework.Consistently(t, 30*time.Second, "the denied write to wait for the interval, not retry every 5s", func(ctx context.Context) (bool, string) {
		n, err := e.AdmissionDenials(ctx, policy)
		if err != nil {
			return false, err.Error()
		}
		return n-base <= 1, fmt.Sprintf("%g denied writes in %s", n-base, time.Since(edited).Round(time.Second))
	})
	mc, err = e.GetMetricCheck(ctx, ns, name)
	require.NoError(t, err)
	assert.True(t, mc.Status.LastEvaluatedAt.Time.Equal(written), "no write worked while denied")

	lift()
	liftAt := time.Now()
	edit(2)
	e.WaitMetricCheck(t, ns, name, 15*time.Second, "the next edit to write a fresh result", func(mc *v1alpha1.MetricCheck) bool {
		return mc.Status.Result == "Pass" && mc.Status.LastEvaluatedAt != nil &&
			!mc.Status.LastEvaluatedAt.Time.Before(liftAt.Add(-time.Second))
	})
}

// TestMetric_QueryErrorsFail checks that every way a query can fail gives
// result Fail with no value and the cause in the reason: bad PromQL, an empty
// vector, two series, a range vector, a host that does not resolve, a server
// that is not Prometheus, and a passing MetricCheck whose query is edited to
// bad PromQL.
//
// Covers METRIC-04.
func TestMetric_QueryErrorsFail(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	ns := e.Namespace(t)
	prom := framework.PrometheusURL(t)
	const prefix = "prometheus query error: "
	cases := []struct {
		name, url, query string
		reason           []string
	}{
		{"bad-syntax", prom, "sum(", []string{prefix + "prometheus returned HTTP 400: bad_data: "}},
		{"empty", prom, "vector(1) > 2", []string{prefix + "prometheus query returned empty vector"}},
		{"two-series", prom, `label_replace(vector(1), "x", "a", "", "") or label_replace(vector(2), "x", "b", "", "")`,
			[]string{prefix + "prometheus query returned 2 series, expected 1"}},
		{"matrix", prom, "vector(1)[1m:10s]", []string{prefix + `unsupported prometheus resultType "matrix"`}},
		{"no-such-host", "http://no-such-prometheus." + ns + ".svc:9090", "vector(0)",
			[]string{prefix + "prometheus GET: ", "no such host"}},
		{"not-prometheus", "http://pushgateway." + framework.MonitoringNamespace + ".svc:9091", "vector(0)",
			[]string{prefix + "prometheus returned HTTP 404"}},
	}
	for _, c := range cases {
		mc := framework.MetricCheck(t, ns, c.name, c.query, "lt", 1, metricInterval)
		mc.Spec.PrometheusURL = c.url
		e.CreateMetricCheck(t, mc)
	}
	failed := func(reason ...string) func(*v1alpha1.MetricCheck) bool {
		return func(mc *v1alpha1.MetricCheck) bool {
			for _, r := range reason {
				if !strings.Contains(mc.Status.Reason, r) {
					return false
				}
			}
			return mc.Status.LastEvaluatedAt != nil && mc.Status.Result == "Fail" && mc.Status.LastValue == "" &&
				strings.HasPrefix(mc.Status.Reason, prefix)
		}
	}
	for _, c := range cases {
		e.WaitMetricCheck(t, ns, c.name, metricTimeout, "failing", failed(c.reason...))
	}

	e.CreateMetricCheck(t, framework.MetricCheck(t, ns, "was-passing", "vector(0)", "lt", 1, metricInterval))
	e.WaitMetricCheck(t, ns, "was-passing", metricTimeout, "passing", func(mc *v1alpha1.MetricCheck) bool {
		return mc.Status.Result == "Pass" && mc.Status.LastValue == "0" && mc.Status.Reason == "0 lt 1 = true"
	})
	obj := &v1alpha1.MetricCheck{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "was-passing"}}
	require.NoError(t, e.Client.Patch(ctx, obj, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"query":"sum("}}`))))
	e.WaitMetricCheck(t, ns, "was-passing", metricTimeout, "failing on the edited query",
		failed(prefix+"prometheus returned HTTP 400: bad_data: "))
}

// TestMetric_IntervalDefaults checks how often a MetricCheck re-queries and
// how long a result stays valid (three intervals): unset and "0" mean 1m, a
// value below 10s is raised to 10s, and 20s is kept. A value that is not a
// duration is rejected by the API server.
//
// Covers METRIC-05.
func TestMetric_IntervalDefaults(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	err := e.Client.Create(context.Background(), framework.MetricCheck(t, ns, "not-a-duration", "vector(1)", "lt", 2, "fast"))
	require.Error(t, err, "interval \"fast\" is rejected")
	assert.True(t, apierrors.IsInvalid(err), "rejected by validation: %v", err)

	for _, c := range []struct {
		name, interval string
		every          time.Duration
		gaps           int
	}{
		{"unset", "", time.Minute, 2},
		{"zero", "0", time.Minute, 2},
		{"below-minimum", "1s", 10 * time.Second, 3},
		{"twenty-seconds", "20s", 20 * time.Second, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e.CreateMetricCheck(t, framework.MetricCheck(t, ns, c.name, "vector(1)", "lt", 2, c.interval))
			var seen []time.Time
			framework.Eventually(t, time.Duration(c.gaps+1)*c.every+time.Minute,
				fmt.Sprintf("%d evaluations of %s", c.gaps+1, c.name), func(ctx context.Context) (bool, string) {
					mc, err := e.GetMetricCheck(ctx, ns, c.name)
					if err != nil {
						return false, err.Error()
					}
					if at := mc.Status.LastEvaluatedAt; at != nil && (len(seen) == 0 || !at.Time.Equal(seen[len(seen)-1])) {
						seen = append(seen, at.Time)
						require.NotNil(t, mc.Status.ValidUntil)
						assert.Equal(t, 3*c.every, mc.Status.ValidUntil.Sub(at.Time), "valid for three intervals")
						assert.Equal(t, "Pass", mc.Status.Result)
					}
					return len(seen) > c.gaps, fmt.Sprintf("evaluated at %v", seen)
				})
			for i := 1; i < len(seen); i++ {
				gap := seen[i].Sub(seen[i-1])
				assert.True(t, gap >= c.every-time.Second && gap <= c.every+3*time.Second,
					"evaluation %d came %s after the previous one, want %s", i+1, gap, c.every)
			}
		})
	}
}

// TestMetric_EgressGuard checks that a prometheusURL on loopback, an
// unspecified address, or a cloud metadata address is refused before any
// request is sent: the MetricCheck fails with the guard's reason. The
// in-cluster Prometheus is allowed.
//
// Covers METRIC-06.
func TestMetric_EgressGuard(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	cases := []struct{ name, url, reason string }{
		{"loopback", "http://127.0.0.1:9090", "127.0.0.1 is loopback"},
		{"localhost", "http://localhost:9090", " is loopback"},
		{"loopback-ipv6", "http://[::1]:9090", "::1 is loopback"},
		{"unspecified", "http://0.0.0.0:9090", "0.0.0.0 is unspecified"},
		{"link-local", "http://169.254.169.254", "169.254.169.254 is link-local (cloud metadata)"},
		{"aws-ipv6", "http://[fd00:ec2::254]", "fd00:ec2::254 is AWS instance metadata (IPv6)"},
		{"alibaba", "http://100.100.100.200", "100.100.100.200 is Alibaba Cloud metadata"},
		{"azure", "http://168.63.129.16", "168.63.129.16 is Azure WireServer"},
	}
	for _, c := range cases {
		mc := framework.MetricCheck(t, ns, c.name, "vector(0)", "lt", 1, metricInterval)
		mc.Spec.PrometheusURL = c.url
		e.CreateMetricCheck(t, mc)
	}
	e.CreateMetricCheck(t, framework.MetricCheck(t, ns, "in-cluster", "vector(0)", "lt", 1, metricInterval))
	for _, c := range cases {
		e.WaitMetricCheck(t, ns, c.name, metricTimeout, "refused by the egress guard", func(mc *v1alpha1.MetricCheck) bool {
			return mc.Status.Result == "Fail" && mc.Status.LastValue == "" &&
				strings.HasPrefix(mc.Status.Reason, "prometheus query error: prometheus GET: ") &&
				strings.Contains(mc.Status.Reason, "destination address is not allowed: ") &&
				strings.Contains(mc.Status.Reason, c.reason)
		})
	}
	e.WaitMetricCheck(t, ns, "in-cluster", metricTimeout, "passing", framework.MetricResult("Pass", "0 lt 1 = true"))
}

// TestMetric_ProviderValidation checks the provider field: the API server
// rejects a provider kardinal does not have and a provider without its
// settings block, and a MetricCheck that sets none gets prometheus and
// evaluates.
//
// Covers METRIC-07.
func TestMetric_ProviderValidation(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	ns := e.Namespace(t)
	other := framework.MetricCheck(t, ns, "graphite", "vector(0)", "lt", 1, metricInterval)
	other.Spec.Provider = "graphite"
	err := e.Client.Create(ctx, other)
	require.Error(t, err, "provider graphite is rejected")
	assert.True(t, apierrors.IsInvalid(err), "rejected by validation: %v", err)
	assert.Contains(t, err.Error(), `Unsupported value: "graphite"`)

	noBlock := framework.MetricCheck(t, ns, "datadog", "avg:x{*}", "lt", 1, metricInterval)
	noBlock.Spec.Provider = "datadog"
	err = e.Client.Create(ctx, noBlock)
	require.Error(t, err, "provider datadog without spec.datadog is rejected")
	assert.True(t, apierrors.IsInvalid(err), "rejected by validation: %v", err)
	assert.Contains(t, err.Error(), "provider datadog requires datadog")

	unset := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": v1alpha1.GroupVersion.String(),
		"kind":       "MetricCheck",
		"metadata":   map[string]interface{}{"name": "no-provider", "namespace": ns},
		"spec": map[string]interface{}{
			"prometheusURL": framework.PrometheusURL(t),
			"query":         "vector(0)",
			"threshold":     map[string]interface{}{"operator": "lt", "value": int64(1)},
			"interval":      metricInterval,
		},
	}}
	require.NoError(t, e.Client.Create(ctx, unset))
	mc := e.WaitMetricCheck(t, ns, "no-provider", metricTimeout, "passing", framework.MetricResult("Pass", "0 lt 1 = true"))
	assert.Equal(t, "prometheus", mc.Spec.Provider, "an unset provider defaults to prometheus")
}

// TestMetric_OrgGateUsesOrgMetrics checks that an org gate in
// platform-policies reads the MetricChecks in platform-policies: a team
// MetricCheck of the same name in the Pipeline's namespace passes, but the
// org gate blocks on the failing org MetricCheck, and promotes once that
// passes.
//
// Covers METRIC-08.
func TestMetric_OrgGateUsesOrgMetrics(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	env := orgEnv(t)
	metric := env + "-errors"
	job := "e2e-" + env
	e.EnsureNamespace(t, framework.PolicyNamespace)
	e.SetMetric(t, job, 1)
	e.CreateMetricCheck(t, framework.MetricCheck(t, framework.PolicyNamespace, metric, framework.PushedQuery(job), "lt", 0.5, metricInterval))
	e.WaitMetricCheck(t, framework.PolicyNamespace, metric, metricTimeout, "failing", framework.MetricResult("Fail", "1 lt 0.5 = false"))
	expr := fmt.Sprintf(`metrics[%q].result == "Pass"`, metric)
	org := framework.Gate(framework.PolicyNamespace, env+"-metric", env, expr, recheck)
	org.Labels["kardinal.io/scope"] = "org"
	e.CreateGate(t, org)

	a := newFluxApp(t, e, env)
	e.CreateMetricCheck(t, framework.MetricCheck(t, a.ns, metric, "vector(0)", "lt", 0.5, metricInterval))
	e.WaitMetricCheck(t, a.ns, metric, metricTimeout, "passing", framework.MetricResult("Pass", "0 lt 0.5 = true"))
	a.apply(t, a.pipeline(nil))

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	name := framework.GateInstanceName(framework.PolicyNamespace, org.Name, env, bundle)
	e.WaitGateNamed(t, a.ns, name, gateTimeout, "blocked on the org MetricCheck", framework.Evaluated(false, expr+" = false"))
	e.NoStep(t, a.ns, pipelineName, bundle, env, holdFor)
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload(env)))

	e.SetMetric(t, job, 0)
	e.WaitMetricCheck(t, framework.PolicyNamespace, metric, metricTimeout, "passing", framework.MetricResult("Pass", "0 lt 0.5 = true"))
	e.WaitGateNamed(t, a.ns, name, gateTimeout, "open", framework.Evaluated(true, expr+" = true"))
	e.WaitStepState(t, a.ns, pipelineName, bundle, env, "Verified", promoteTimeout)
	assert.Equal(t, fixtures.Image+":"+fixtures.V2, e.DeploymentImage(t, a.ns, fixtures.Workload(env)))
}

// builtinSteps are the built-in promotion step names (pkg/steps/defaults.go).
var builtinSteps = []string{
	"argocd-set-image", "config-merge", "git-clone", "git-commit", "git-push", "health-check",
	"helm-set-image", "kustomize-set-image", "open-pr", "render-manifests", "wait-for-merge",
}

// controllers are the controllers docs/guides/monitoring.md lists as
// controller label values.
var controllers = []string{
	"bundle", "changewindow", "metriccheck", "notificationhook", "pipeline", "policygate",
	"promotionstep", "prstatus", "rollbackpolicy", "scheduleclock", "subscription",
}

// TestObs_ControllerMetrics drives each kardinal metric with one promotion
// story and reads the controller's /metrics: a Bundle superseded while its
// prod gate blocks, a Bundle that waits on the gate and then on its prod PR
// before it is Verified, and a Bundle that fails its health check. It also
// checks the controller-runtime reconcile and workqueue metrics the
// monitoring guide lists.
//
// Covers OBS-METRICS-01, OBS-CRMETRICS-01.
func TestObs_ControllerMetrics(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newFluxRepo(t, e, "test", "prod", "gone")
	for _, env := range []string{"test", "prod"} {
		a.kustomize(t, env, fluxSource, 10*time.Minute)
	}
	a.waitSynced(t, "test")
	a.waitSynced(t, "prod")
	e.CreateGate(t, framework.Gate(a.ns, "needs-open-label", "prod", openExpr, recheck))
	e.CreateMetricCheck(t, framework.MetricCheck(t, a.ns, "always-passes", "vector(0)", "lt", 1, metricInterval))
	before, err := e.ControllerMetrics(ctx)
	require.NoError(t, err)

	a.apply(t, a.pipelineOf(pipelineName, map[string]string{"prod": "pr-review"}, "test", "prod"))
	fails := a.pipelineOf("fails", nil, "gone")
	fails.Spec.Environments[0].Health.Timeout = "1m"
	a.apply(t, fails)
	failed := e.CreateBundle(t, a.ns, "fails", "--image", fixtures.Image+":"+fixtures.V2)

	older := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, older, "test", "Verified", promoteTimeout)
	e.WaitGateReady(t, a.ns, older, "prod", "needs-open-label", false, "= false", gateTimeout)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	e.WaitBundlePhase(t, a.ns, older, "Superseded", time.Minute)

	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "needs-open-label", false, "= false", gateTimeout)
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", 12*time.Second)
	e.SetBundleLabel(t, a.ns, bundle, openLabel, "true")
	e.WaitGateReady(t, a.ns, bundle, "prod", "needs-open-label", true, "= true", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, a.repo, time.Minute, "prod promotion PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	require.NoError(t, e.Git.MergePR(ctx, a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
	e.WaitBundlePhase(t, a.ns, failed, "Failed", 3*time.Minute)

	after, err := e.ControllerMetrics(ctx)
	require.NoError(t, err)
	// Other tests share the controller, so only increases are checked.
	delta := func(name string, labels map[string]string) float64 {
		return after.Sum(name, labels) - before.Sum(name, labels)
	}

	// kardinal metrics.
	for _, phase := range []string{"Promoting", "Verified", "Failed", "Superseded"} {
		assert.GreaterOrEqual(t, delta("kardinal_bundles_total", map[string]string{"phase": phase}), 1.0, "bundles entering %s", phase)
	}
	assert.GreaterOrEqual(t, delta("kardinal_steps_total", map[string]string{"type": "PromotionStep", "result": "succeeded"}), 3.0,
		"test twice and prod succeeded")
	assert.GreaterOrEqual(t, delta("kardinal_steps_total", map[string]string{"type": "PromotionStep", "result": "failed"}), 1.0)
	assert.Equal(t, []string{"PromotionStep"}, after.LabelValues("kardinal_steps_total", "type"))
	for _, result := range []string{"blocked", "allowed"} {
		assert.GreaterOrEqual(t, delta("kardinal_gate_evaluations_total", map[string]string{"result": result}), 1.0, "%s evaluations", result)
	}
	for _, h := range []string{"kardinal_pr_duration_seconds", "kardinal_step_duration_seconds",
		"kardinal_gate_blocking_duration_seconds", "kardinal_promotionstep_age_seconds"} {
		assert.GreaterOrEqual(t, delta(h+"_count", nil), 1.0, "%s observations", h)
	}
	assert.GreaterOrEqual(t, delta("kardinal_gate_blocking_duration_seconds_sum", nil), 10.0, "prod's gate blocked for more than 10s")
	assert.Subset(t, builtinSteps, after.LabelValues("kardinal_step_duration_seconds_count", "step"), "step labels are step names")
	// Four promotions above: older and bundle in test, bundle in prod, fails
	// in gone. git-clone starts and finishes within one reconcile; the state
	// machine closes wait-for-merge (prod) and the health check (all four).
	for step, want := range map[string]float64{"git-clone": 4, "wait-for-merge": 1, "health-check": 4} {
		assert.GreaterOrEqual(t, delta("kardinal_step_duration_seconds_count", map[string]string{"step": step}), want,
			"%s observations", step)
	}
	ps, ok, err := e.Step(ctx, a.ns, pipelineName, bundle, "test")
	require.NoError(t, err)
	require.True(t, ok, "test PromotionStep of %s", bundle)
	require.NotEmpty(t, ps.Status.Steps)
	assert.Equal(t, "git-clone", ps.Status.Steps[0].Name)
	assert.Positive(t, ps.Status.Steps[0].DurationMs, "git-clone status.steps[0].durationMs")

	// controller-runtime metrics.
	assert.Equal(t, []string{"error", "requeue", "requeue_after", "success"},
		after.LabelValues("controller_runtime_reconcile_total", "result"))
	assert.Subset(t, after.LabelValues("controller_runtime_reconcile_total", "controller"), controllers)
	for _, c := range []string{"bundle", "promotionstep", "policygate", "metriccheck"} {
		l := map[string]string{"controller": c}
		assert.GreaterOrEqual(t, delta("controller_runtime_reconcile_total", l), 1.0, "%s reconciles", c)
		assert.GreaterOrEqual(t, delta("controller_runtime_reconcile_time_seconds_count", l), 1.0, "%s reconcile time", c)
		assert.True(t, after.Has("controller_runtime_reconcile_errors_total", l), "%s reconcile errors", c)
		assert.True(t, after.Has("controller_runtime_active_workers", l), "%s active workers", c)
		// The default worker counts (cmd/kardinal-controller/workers.go,
		// #1554; chart controller.workers unset): reconciles run in parallel.
		// MetricCheck runs its global query slots plus 4, at least 16 (#1563;
		// the query slots, --metriccheck-*-slots, are a separate limit).
		want := map[string]float64{"bundle": 4, "promotionstep": 16, "policygate": 8, "metriccheck": 16}[c]
		assert.Equal(t, want, after.Sum("controller_runtime_max_concurrent_reconciles", l), "%s max concurrent reconciles", c)
		q := map[string]string{"name": c}
		assert.GreaterOrEqual(t, delta("workqueue_adds_total", q), 1.0, "%s queue adds", c)
		for _, m := range []string{"workqueue_depth", "workqueue_queue_duration_seconds_count",
			"workqueue_work_duration_seconds_count", "workqueue_retries_total"} {
			assert.True(t, after.Has(m, q), "%s{name=%q}", m, c)
		}
	}
	assert.GreaterOrEqual(t, delta("controller_runtime_reconcile_total",
		map[string]string{"controller": "metriccheck", "result": "requeue_after"}), 1.0, "a MetricCheck requeues after its interval")
}

// TestObs_ChartMonitoring checks the chart's monitoring integration with
// Prometheus Operator (hack/e2e/up.sh flux enables serviceMonitor and
// prometheusRule): Prometheus scrapes the controller through the
// ServiceMonitor, loads the alerts and evaluates them without error, and
// KardinalControllerDown stays inactive. TestObs_GrafanaDashboard covers the
// grafanaDashboard.
//
// Covers CHART-MON-01.
func TestObs_ChartMonitoring(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	const job = "kardinal-promoter"
	framework.Eventually(t, 2*time.Minute, "Prometheus to scrape the controller", func(ctx context.Context) (bool, string) {
		s, err := e.PromQuery(ctx, fmt.Sprintf("up{job=%q}", job))
		if err != nil {
			return false, err.Error()
		}
		return len(s) == 1 && s[0].Value == "1" && s[0].Metric["namespace"] == framework.ControllerNamespace, fmt.Sprintf("%v", s)
	})

	// The rule group's last evaluation can predate the first scrape the
	// query above saw, when absent(up) still held and KardinalControllerDown
	// went pending; it is inactive from the first evaluation after that. A
	// job label the alert does not match keeps it pending, then firing.
	var group framework.PromRuleGroup
	framework.Eventually(t, 2*time.Minute, "Prometheus to load and evaluate the chart's alerts, KardinalControllerDown inactive", func(ctx context.Context) (bool, string) {
		groups, err := e.PromRules(ctx)
		if err != nil {
			return false, err.Error()
		}
		for _, g := range groups {
			if g.Name == "kardinal-promoter" {
				group = g
			}
		}
		for _, r := range group.Rules {
			if r.Health != "ok" {
				return false, fmt.Sprintf("%s health=%q lastError=%q", r.Name, r.Health, r.LastError)
			}
			if r.Name == "KardinalControllerDown" && r.State != "inactive" {
				return false, fmt.Sprintf("%s state=%q", r.Name, r.State)
			}
		}
		return len(group.Rules) > 0, fmt.Sprintf("group %q with %d rules", group.Name, len(group.Rules))
	})
	var names []string
	for _, r := range group.Rules {
		names = append(names, r.Name)
		assert.Equal(t, "alerting", r.Type, r.Name)
		assert.Empty(t, r.LastError, r.Name)
		if r.Name == "KardinalControllerDown" {
			assert.Contains(t, r.Query, fmt.Sprintf("up{job=%q}", job), "the alert watches the ServiceMonitor's job")
		}
	}
	assert.ElementsMatch(t, []string{"KardinalControllerDown", "KardinalHighReconcileErrors", "KardinalBundleReconcilerStalled",
		"KardinalWorkQueueBacklog", "KardinalPolicyGateReconcileSlow"}, names)
}
