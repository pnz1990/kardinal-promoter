// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// metricTemplate is a per-promotion MetricCheck in the default namespace.
func metricTemplate(name, query string) kardinalv1alpha1.MetricCheck {
	return kardinalv1alpha1.MetricCheck{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: kardinalv1alpha1.MetricCheckSpec{
			Provider:      "prometheus",
			PrometheusURL: "http://prometheus:9090",
			Query:         query,
			Threshold:     kardinalv1alpha1.MetricThreshold{Operator: "lt", Value: 0.5},
			Interval:      "30s",
			PerPromotion:  true,
		},
	}
}

// metricNodes returns the MetricCheck nodes of a Graph by metadata.name.
func metricNodes(t *testing.T, g *graph.Graph) map[string]graph.GraphNode {
	t.Helper()
	out := map[string]graph.GraphNode{}
	for _, n := range g.Spec.Nodes {
		if n.Template["kind"] == "MetricCheck" {
			out[n.Template["metadata"].(map[string]interface{})["name"].(string)] = n
		}
	}
	return out
}

func TestRenderMetricText(t *testing.T) {
	vars := map[string]string{"bundle.version": "1.2.3", "environment.name": "prod", "bundle.imageTag": `x"} or vector(1)`}
	tests := []struct {
		name, in, want string
	}{
		{"no placeholder", `up{job="a"}`, `up{job="a"}`},
		{"replaced", `err{version="{{ bundle.version }}",env="{{environment.name}}"}`, `err{version="1.2.3",env="prod"}`},
		{"unknown stays", `x{{ bundle.nope }}`, `x{{ bundle.nope }}`},
		{"unsafe value stays", `t="{{ bundle.imageTag }}"`, `t="{{ bundle.imageTag }}"`},
		{"single braces untouched", `sum(rate(x{a="b"}[5m]))`, `sum(rate(x{a="b"}[5m]))`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, graph.RenderMetricText(tt.in, vars))
		})
	}
	assert.Equal(t, []string{"bundle.version", "x"}, graph.MetricPlaceholders("{{ bundle.version }} {{x}}"))
	assert.True(t, graph.KnownMetricPlaceholder("pipeline.name"))
	assert.False(t, graph.KnownMetricPlaceholder("bundle.nope"))
}

func TestMetricTemplateVars(t *testing.T) {
	p := makeLinearPipeline("app", "test", "prod")
	b := makeBundle("app-v1", "app")
	b.Spec.Images[0].Digest = "sha256:abc"
	b.Spec.Provenance = &kardinalv1alpha1.BundleProvenance{CommitSHA: "deadbeef"}
	vars := graph.MetricTemplateVars(p, b, "prod")
	assert.Equal(t, map[string]string{
		"bundle.name": "app-v1", "bundle.version": "v1", "bundle.imageTag": "v1",
		"bundle.imageDigest": "sha256:abc", "bundle.commitSHA": "deadbeef",
		"pipeline.name": "app", "environment.name": "prod", "namespace": "default",
	}, vars)

	cfg := &kardinalv1alpha1.Bundle{Spec: kardinalv1alpha1.BundleSpec{Type: "config",
		ConfigRef: &kardinalv1alpha1.ConfigRef{CommitSHA: "0123456789abcdef"}}}
	assert.Equal(t, "01234567", graph.BundleVersion(cfg))
}

func TestExprReadsMetric(t *testing.T) {
	tests := []struct {
		expr string
		want bool
	}{
		{`metrics["error-rate"].result == "Pass"`, true},
		{`metrics['error-rate'].result == "Pass"`, true},
		{`metrics.errors.result == "Pass"`, true},
		{`metrics.errorsTotal.result == "Pass"`, false},
		{`schedule.isWeekend`, false},
	}
	for _, tt := range tests {
		name := "error-rate"
		if strings.Contains(tt.expr, "metrics.") {
			name = "errors"
		}
		assert.Equal(t, tt.want, graph.ExprReadsMetric(tt.expr, name), tt.expr)
	}
}

// TestBuilder_PerPromotionMetricCheck: a per-promotion MetricCheck that a
// prod gate reads gets one instance node for prod, with the Bundle's values in
// its query, held until uat is Verified and suspended once the Bundle stops
// promoting. Templates no gate reads, shared MetricChecks, and templates read
// only by org gates get no node.
func TestBuilder_PerPromotionMetricCheck(t *testing.T) {
	p := makeLinearPipeline("app", "test", "uat", "prod")
	b := makeBundle("app-v1", "app")
	gates := []kardinalv1alpha1.PolicyGate{
		makePolicyGate("err-gate", "default", "prod", `metrics["error-rate"].result == "Pass"`),
		makePolicyGate("err-gate-2", "default", "prod", `double(metrics["error-rate"].value) < 1.0`),
		makePolicyGate("org-gate", "platform-policies", "uat", `metrics["org-only"].result == "Pass"`),
		makePolicyGate("shared-gate", "default", "test", `metrics["shared"].result == "Pass"`),
	}
	shared := metricTemplate("shared", "up")
	shared.Spec.PerPromotion = false
	checks := []kardinalv1alpha1.MetricCheck{
		metricTemplate("error-rate", `rate(errors{version="{{ bundle.version }}",env="{{ environment.name }}"}[5m])`),
		metricTemplate("unused", "up"),
		metricTemplate("org-only", "up"),
		shared,
	}
	res, err := graph.NewBuilder().Build(graph.BuildInput{
		Pipeline: p, Bundle: b, PolicyGates: gates, MetricChecks: checks,
		PolicyNamespaces: []string{"platform-policies"},
	})
	require.NoError(t, err)

	nodes := metricNodes(t, res.Graph)
	require.Len(t, nodes, 1, "one instance: error-rate for prod")
	n, ok := nodes["error-rate-prod--app-v1"]
	require.True(t, ok, "instance name <template>-<env>--<bundle>: %v", nodes)
	assert.Equal(t, "metric0errorRate0prod00appV1", n.ID)
	assert.Empty(t, n.ReadyWhen)

	meta := n.Template["metadata"].(map[string]interface{})
	assert.Equal(t, map[string]interface{}{
		"kardinal.io/pipeline": "app", "kardinal.io/bundle": "app-v1",
		"kardinal.io/environment": "prod", graph.LabelMetricTemplate: "error-rate",
	}, meta["labels"])

	spec := n.Template["spec"].(map[string]interface{})
	assert.NotContains(t, spec, "perPromotion")
	assert.Equal(t, `${!(bundle.status.phase in ["Available", "Promoting"])}`, spec["suspend"])
	assert.Equal(t, "http://prometheus:9090", spec["prometheusURL"])
	assert.Equal(t, "30s", spec["interval"])
	assert.Equal(t,
		`${["rate(errors{version=\"v1\",env=\"prod\"}[5m])"].filter(x_, uat.status.state == "Verified")[0]}`,
		spec["query"], "the query only resolves once uat is Verified")
	require.NoError(t, graph.ValidateNodeIDs(res.Graph.Spec.Nodes))
}

// TestBuilder_PerPromotionMetricCheck_RootAndWeb: a root environment's
// instance is not held; a web instance holds web.url; a "${" in any copied
// string reaches kro as a CEL string literal.
func TestBuilder_PerPromotionMetricCheck_RootAndWeb(t *testing.T) {
	p := makeLinearPipeline("app", "test", "prod")
	b := makeBundle("app-v1", "app")
	hdr := "v={{ bundle.version }}"
	web := kardinalv1alpha1.MetricCheck{
		ObjectMeta: metav1.ObjectMeta{Name: "smoke", Namespace: "default"},
		Spec: kardinalv1alpha1.MetricCheckSpec{
			Provider: "web", PerPromotion: true,
			Web: &kardinalv1alpha1.WebProviderSpec{
				URL: "http://svc/check?v={{ bundle.version }}", JSONPath: "{.ok}",
				Body:    `{"tmpl":"${not.cel}"}`,
				Headers: []kardinalv1alpha1.WebHeader{{Name: "X-Version", Value: &hdr}},
			},
			Threshold: kardinalv1alpha1.MetricThreshold{Operator: "eq", Text: strPtr("true")},
		},
	}
	prom := metricTemplate("lat", "latency{v=\"{{ bundle.version }}\"}")
	gates := []kardinalv1alpha1.PolicyGate{
		makePolicyGate("smoke-gate", "default", "prod", `metrics.smoke.result == "Pass"`),
		makePolicyGate("lat-gate", "default", "test", `metrics.lat.result == "Pass"`),
	}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: b, PolicyGates: gates,
		MetricChecks: []kardinalv1alpha1.MetricCheck{web, prom}})
	require.NoError(t, err)
	nodes := metricNodes(t, res.Graph)
	require.Len(t, nodes, 2)

	root := nodes["lat-test--app-v1"].Template["spec"].(map[string]interface{})
	assert.Equal(t, `latency{v="v1"}`, root["query"], "a root environment's instance is not held")

	w := nodes["smoke-prod--app-v1"].Template["spec"].(map[string]interface{})["web"].(map[string]interface{})
	assert.Equal(t, `${["http://svc/check?v=v1"].filter(x_, test.status.state == "Verified")[0]}`, w["url"])
	assert.Equal(t, `${"{\"tmpl\":\"${not.cel}\"}"}`, w["body"], "a ${ in text is a CEL literal for kro")
	assert.Equal(t, "v=v1", w["headers"].([]interface{})[0].(map[string]interface{})["value"])
}

// TestBuilder_MetricExpressionsEvaluate: the held query and the suspend
// expression compile and evaluate in CEL as kro would: the query is
// data-pending (index out of bounds) until the upstream is Verified, a
// literal string is itself, and suspend follows the Bundle phase.
func TestBuilder_MetricExpressionsEvaluate(t *testing.T) {
	env, err := cel.NewEnv(cel.Variable("bundle", cel.DynType), cel.Variable("uat", cel.DynType))
	require.NoError(t, err)
	eval := func(expr string, vars map[string]interface{}) (interface{}, error) {
		ast, iss := env.Compile(strings.TrimSuffix(strings.TrimPrefix(expr, "${"), "}"))
		require.NoError(t, iss.Err(), expr)
		prg, err := env.Program(ast)
		require.NoError(t, err)
		out, _, err := prg.Eval(vars)
		if err != nil {
			return nil, err
		}
		return out.Value(), nil
	}
	p := makeLinearPipeline("app", "uat", "prod")
	res, err := graph.NewBuilder().Build(graph.BuildInput{
		Pipeline: p, Bundle: makeBundle("app-v1", "app"),
		PolicyGates:  []kardinalv1alpha1.PolicyGate{makePolicyGate("g", "default", "prod", `metrics.m.result == "Pass"`)},
		MetricChecks: []kardinalv1alpha1.MetricCheck{metricTemplate("m", `x{v="{{ bundle.version }}"} > ${y}`)},
	})
	require.NoError(t, err)
	spec := metricNodes(t, res.Graph)["m-prod--app-v1"].Template["spec"].(map[string]interface{})

	query := spec["query"].(string)
	_, err = eval(query, map[string]interface{}{"uat": map[string]interface{}{"status": map[string]interface{}{"state": "Promoting"}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "index out of bounds", "held: kro treats it as data pending")
	out, err := eval(query, map[string]interface{}{"uat": map[string]interface{}{"status": map[string]interface{}{"state": "Verified"}}})
	require.NoError(t, err)
	assert.Equal(t, `x{v="v1"} > ${y}`, out)

	for phase, suspended := range map[string]bool{"Available": false, "Promoting": false, "Verified": true, "Failed": true, "Superseded": true} {
		out, err := eval(spec["suspend"].(string), map[string]interface{}{"bundle": map[string]interface{}{"status": map[string]interface{}{"phase": phase}}})
		require.NoError(t, err)
		assert.Equal(t, suspended, out, phase)
	}
}

func strPtr(s string) *string { return &s }
