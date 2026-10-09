// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/validation"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

func analysisPipeline(refs ...kardinalv1alpha1.AnalysisTemplateRef) *kardinalv1alpha1.Pipeline {
	p := makeLinearPipeline("app", "test", "prod")
	p.Spec.Environments[1].Verification = &kardinalv1alpha1.VerificationSpec{
		AnalysisTemplates: refs,
		Args:              []kardinalv1alpha1.AnalysisArg{{Name: "service", Value: "app-prod"}, {Name: "tag", Value: "override"}},
	}
	return p
}

func smokeTemplate(kind, name, query string) graph.AnalysisTemplate {
	return graph.AnalysisTemplate{Kind: kind, Name: name, Spec: map[string]interface{}{
		"args": []interface{}{
			map[string]interface{}{"name": "service"},
			map[string]interface{}{"name": "tag"},
			map[string]interface{}{"name": "environment"},
			map[string]interface{}{"name": "image"},
			map[string]interface{}{"name": "threshold", "value": "0.99"},
			map[string]interface{}{"name": "api-key", "valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{"name": "dd", "key": "api-key"}}},
		},
		"dryRun": []interface{}{map[string]interface{}{"metricName": "noisy"}},
		"metrics": []interface{}{map[string]interface{}{
			"name":     "success-rate",
			"interval": "10s",
			"provider": map[string]interface{}{"prometheus": map[string]interface{}{"query": query}},
		}},
	}}
}

func analysisInput(tmpls ...graph.AnalysisTemplate) graph.AnalysisInput {
	in := graph.AnalysisInput{Templates: map[string]graph.AnalysisTemplate{}}
	for _, t := range tmpls {
		in.Templates[graph.AnalysisTemplateKey(t.Kind, t.Name)] = t
	}
	return in
}

func buildAnalysis(t *testing.T, p *kardinalv1alpha1.Pipeline, in graph.AnalysisInput) (*graph.BuildResult, error) {
	t.Helper()
	b := makeBundle("app-v1", "app")
	b.Spec.Images = []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "1.4.0"}}
	return graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: b, Analyses: in})
}

// TestBuilder_AnalysisNodes: each template of an environment's verification
// is an AnalysisRun node with the template's metrics and dryRun copied in,
// args resolved (Pipeline args over kardinal's over the template's), the
// step lists the run names in spec.analyses, and the mirror and the
// AnalysisRun read-back ref are added.
func TestBuilder_AnalysisNodes(t *testing.T) {
	p := analysisPipeline(
		kardinalv1alpha1.AnalysisTemplateRef{Name: "smoke"},
		kardinalv1alpha1.AnalysisTemplateRef{Name: "slo", Kind: "ClusterAnalysisTemplate"},
	)
	res, err := buildAnalysis(t, p, analysisInput(
		smokeTemplate("AnalysisTemplate", "smoke", `sum(rate(ok{service="{{args.service}}"}[1m]))`),
		smokeTemplate("ClusterAnalysisTemplate", "slo", "1"),
	))
	require.NoError(t, err)
	g := res.Graph
	for _, id := range []string{"refSteps", "refAnalysisRuns", "analysis0prod0smoke", "analysis0prod0slo", "live0prod"} {
		assert.True(t, hasNode(g, id), "node %s", id)
	}
	assert.False(t, hasNode(g, "refHookRuns"), "no hooks: no HookRun ref")
	assert.False(t, hasNode(g, "analysis0test0smoke"))

	run := hookNode(t, g, "analysis0prod0smoke").Template
	assert.Equal(t, "argoproj.io/v1alpha1", run["apiVersion"])
	assert.Equal(t, "AnalysisRun", run["kind"])
	labels := run["metadata"].(map[string]interface{})["labels"].(map[string]interface{})
	assert.Equal(t, "smoke", labels["kardinal.io/analysis-template"])
	assert.Equal(t, "prod", labels["kardinal.io/environment"])
	spec := run["spec"].(map[string]interface{})
	assert.Equal(t, []interface{}{map[string]interface{}{"metricName": "noisy"}}, spec["dryRun"])
	metrics := spec["metrics"].([]interface{})
	require.Len(t, metrics, 1)
	assert.Equal(t, "success-rate", metrics[0].(map[string]interface{})["name"])

	args := map[string]interface{}{}
	for _, a := range spec["args"].([]interface{}) {
		m := a.(map[string]interface{})
		if v, ok := m["value"]; ok {
			args[m["name"].(string)] = v
		} else {
			args[m["name"].(string)] = m["valueFrom"]
		}
	}
	assert.Equal(t, map[string]interface{}{
		"service":     "app-prod",              // Pipeline arg
		"tag":         "override",              // Pipeline arg over the built-in
		"environment": "prod",                  // built-in
		"image":       "ghcr.io/org/app:1.4.0", // built-in
		"threshold":   "0.99",                  // template default
		"api-key":     map[string]interface{}{"secretKeyRef": map[string]interface{}{"name": "dd", "key": "api-key"}},
	}, args)

	stepSpec := hookNode(t, g, "prod").Template["spec"].(map[string]interface{})
	names := stepSpec["analyses"].([]interface{})
	require.Len(t, names, 2)
	for _, n := range names {
		assert.Empty(t, validation.IsDNS1123Label(n.(string)), n)
		assert.True(t, strings.HasPrefix(n.(string), "app-app-v1-prod-"), n)
	}
	live := hookNode(t, g, "live0prod").Patch["spec"].(map[string]interface{})["live"].(map[string]interface{})
	assert.Contains(t, live, "analyses")
	assert.NotContains(t, live, "hooks")
	_, err = json.Marshal(g)
	require.NoError(t, err)
}

// TestBuilder_AnalysisFailsClosed: without the Argo Rollouts CRDs, or with a
// template that does not exist, a Graph whose environments verify is not
// built. Environments the Bundle does not promote do not count.
func TestBuilder_AnalysisFailsClosed(t *testing.T) {
	p := analysisPipeline(kardinalv1alpha1.AnalysisTemplateRef{Name: "smoke"})

	_, err := buildAnalysis(t, p, graph.AnalysisInput{Unavailable: "argoproj.io/v1alpha1 AnalysisRun is not served"})
	require.Error(t, err)
	assert.ErrorIs(t, err, graph.ErrInvalid)
	assert.Contains(t, err.Error(), `environment "prod" verification: argoproj.io/v1alpha1 AnalysisRun is not served`)

	_, err = buildAnalysis(t, p, analysisInput())
	require.Error(t, err)
	assert.Contains(t, err.Error(), `AnalysisTemplate "smoke" not found in namespace "default"`)

	cp := analysisPipeline(kardinalv1alpha1.AnalysisTemplateRef{Name: "slo", Kind: "ClusterAnalysisTemplate"})
	_, err = buildAnalysis(t, cp, analysisInput(smokeTemplate("AnalysisTemplate", "slo", "1")))
	require.Error(t, err, "a namespaced template of that name is not the cluster one")
	assert.Contains(t, err.Error(), `ClusterAnalysisTemplate "slo" not found`)

	_, err = buildAnalysis(t, p, analysisInput(graph.AnalysisTemplate{Kind: "AnalysisTemplate", Name: "smoke",
		Spec: map[string]interface{}{}}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no metrics")

	// A Bundle that stops at test does not need prod's analysis.
	b := makeBundle("app-v1", "app")
	b.Spec.Intent = &kardinalv1alpha1.BundleIntent{TargetEnvironment: "test"}
	_, err = graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: b,
		Analyses: graph.AnalysisInput{Unavailable: "not served"}})
	require.NoError(t, err)
}

// TestBuilder_AnalysisRunNameFollowsSpec: a template edit picked up by a
// later translation renames the run (kro creates a new run, not an edit of
// one in progress); the same template gives the same name.
func TestBuilder_AnalysisRunNameFollowsSpec(t *testing.T) {
	p := analysisPipeline(kardinalv1alpha1.AnalysisTemplateRef{Name: "smoke"})
	name := func(query string) string {
		res, err := buildAnalysis(t, p, analysisInput(smokeTemplate("AnalysisTemplate", "smoke", query)))
		require.NoError(t, err)
		return hookNode(t, res.Graph, "prod").Template["spec"].(map[string]interface{})["analyses"].([]interface{})[0].(string)
	}
	assert.Equal(t, name("a"), name("a"))
	assert.NotEqual(t, name("a"), name("b"))
}

// TestBuilder_AnalysisGating evaluates the AnalysisRun name: it resolves
// once the step entered Verifying, never for a Superseded Bundle.
func TestBuilder_AnalysisGating(t *testing.T) {
	p := analysisPipeline(kardinalv1alpha1.AnalysisTemplateRef{Name: "smoke"})
	res, err := buildAnalysis(t, p, analysisInput(smokeTemplate("AnalysisTemplate", "smoke", "1")))
	require.NoError(t, err)
	expr := nameExpr(t, res.Graph, "analysis0prod0smoke")
	steps := func(started bool) []interface{} {
		st := map[string]interface{}{"state": "HealthChecking"}
		if started {
			st["verificationStartedAt"] = "2026-10-09T00:00:00Z"
		}
		return []interface{}{map[string]interface{}{"metadata": map[string]interface{}{"name": "app-app-v1-prod"}, "status": st}}
	}
	cases := []struct {
		name    string
		phase   string
		started bool
		pending bool
	}{
		{"not verifying", "Promoting", false, true},
		{"verifying", "Promoting", true, false},
		{"superseded", "Superseded", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := celEval(t, expr, map[string]interface{}{
				"bundle":   map[string]interface{}{"status": map[string]interface{}{"phase": tc.phase}},
				"refSteps": steps(tc.started),
			})
			if tc.pending {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "index out of bounds")
				return
			}
			require.NoError(t, err)
			assert.Contains(t, out, "app-app-v1-prod-smoke-")
		})
	}
}

// TestBuilder_AnalysisMirror evaluates the mirror: the environment's runs
// with their template and phase, Pending before Rollouts writes a status.
func TestBuilder_AnalysisMirror(t *testing.T) {
	p := analysisPipeline(kardinalv1alpha1.AnalysisTemplateRef{Name: "smoke"})
	res, err := buildAnalysis(t, p, analysisInput(smokeTemplate("AnalysisTemplate", "smoke", "1")))
	require.NoError(t, err)
	expr := hookNode(t, res.Graph, "live0prod").Patch["spec"].(map[string]interface{})["live"].(map[string]interface{})["analyses"].(string)
	run := func(name, env string, status map[string]interface{}) map[string]interface{} {
		o := map[string]interface{}{"metadata": map[string]interface{}{"name": name, "labels": map[string]interface{}{
			"kardinal.io/environment": env, "kardinal.io/analysis-template": "smoke"}}}
		if status != nil {
			o["status"] = status
		}
		return o
	}
	out, err := celEval(t, expr, map[string]interface{}{"refAnalysisRuns": []interface{}{
		run("r1", "prod", map[string]interface{}{"phase": "Failed", "message": "Metric \"x\" assessed Failed"}),
		run("r2", "test", map[string]interface{}{"phase": "Successful"}),
		run("r3", "prod", nil),
	}})
	require.NoError(t, err)
	b, err := json.Marshal(out)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"name":"r1","template":"smoke","phase":"Failed","message":"Metric \"x\" assessed Failed"},
		{"name":"r3","template":"smoke","phase":"Pending","message":""}]`, string(b))
}

// TestBuilder_AnalysisWithHooks: an environment with both hooks and
// analyses has one mirror with both fields and all three read-back refs.
func TestBuilder_AnalysisWithHooks(t *testing.T) {
	p := analysisPipeline(kardinalv1alpha1.AnalysisTemplateRef{Name: "smoke"})
	p.Spec.Environments[1].Hooks = []kardinalv1alpha1.HookSpec{hook("e2e", "post", hookJob)}
	res, err := buildAnalysis(t, p, analysisInput(smokeTemplate("AnalysisTemplate", "smoke", "1")))
	require.NoError(t, err)
	for _, id := range []string{"refSteps", "refHookRuns", "refAnalysisRuns"} {
		assert.True(t, hasNode(res.Graph, id), id)
	}
	live := hookNode(t, res.Graph, "live0prod").Patch["spec"].(map[string]interface{})["live"].(map[string]interface{})
	assert.Contains(t, live, "hooks")
	assert.Contains(t, live, "analyses")
	spec := hookNode(t, res.Graph, "prod").Template["spec"].(map[string]interface{})
	assert.Len(t, spec["postHooks"], 1)
	assert.Len(t, spec["analyses"], 1)
}
