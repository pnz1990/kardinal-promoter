// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/validation"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/labelvalue"
)

// longTemplate is a MetricCheck or AnalysisTemplate name longer than a label
// value (Kubernetes names can be 253 characters).
var longTemplate = "error-rate-of-the-checkout-service-in-every-region-and-every-zone-v2"

// labelValues collects every literal label value of the Graph's node
// templates, and every collection item field "template" (which a forEach
// copies into a label), with where it was found.
func labelValues(t *testing.T, res *graph.BuildResult) map[string]string {
	t.Helper()
	out := map[string]string{}
	var walk func(path string, v interface{})
	walk = func(path string, v interface{}) {
		switch v := v.(type) {
		case map[string]interface{}:
			for k, x := range v {
				if k == "labels" {
					if labels, ok := x.(map[string]interface{}); ok {
						for lk, lv := range labels {
							if s, ok := lv.(string); ok && !strings.Contains(s, "${") {
								out[path+".labels."+lk] = s
							}
						}
					}
				}
				if k == "template" {
					if s, ok := x.(string); ok && v["environment"] != nil {
						out[path+".template"] = s
					}
				}
				walk(path+"."+k, x)
			}
		case []interface{}:
			for _, x := range v {
				walk(path+"[]", x)
			}
		}
	}
	for _, n := range res.Graph.Spec.Nodes {
		walk(n.ID, n.Template)
		walk(n.ID+".def", n.Def)
	}
	return out
}

// TestBuilder_LongTemplateNamesFitLabels (#2034 class): a MetricCheck or
// AnalysisTemplate named longer than 63 characters still yields valid
// labels on the MetricCheck and AnalysisRun instances, in both Graph shapes,
// as labelvalue.Fit of the name: an invalid label failed the whole Graph.
func TestBuilder_LongTemplateNamesFitLabels(t *testing.T) {
	require.Greater(t, len(longTemplate), validation.LabelValueMaxLength)
	for _, shape := range []string{graph.GraphShapeNodes, graph.GraphShapeCompact} {
		t.Run(shape, func(t *testing.T) {
			p := analysisPipeline(kardinalv1alpha1.AnalysisTemplateRef{Name: longTemplate})
			p.Annotations = map[string]string{graph.AnnotationGraphShape: shape}
			b := makeBundle("app-v1", "app")
			b.Spec.Images = []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "1.4.0"}}
			res, err := graph.NewBuilder().Build(graph.BuildInput{
				Pipeline: p, Bundle: b,
				Analyses:     analysisInput(smokeTemplate("AnalysisTemplate", longTemplate, "up")),
				PolicyGates:  []kardinalv1alpha1.PolicyGate{makePolicyGate("err-gate", "default", "prod", `metrics["`+longTemplate+`"].result == "Pass"`)},
				MetricChecks: []kardinalv1alpha1.MetricCheck{metricTemplate(longTemplate, "up")},
			})
			require.NoError(t, err)
			values := labelValues(t, res)
			fitted := 0
			for where, v := range values {
				assert.Empty(t, validation.IsValidLabelValue(v), "%s = %q", where, v)
				if v == labelvalue.Fit(longTemplate) {
					fitted++
				}
			}
			assert.Positive(t, fitted, "the template names are labels, fitted: %v", values)
		})
	}
}
