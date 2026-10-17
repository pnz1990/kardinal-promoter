// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// targetOnlyBundle is a rollback-style Bundle for prod that carries the
// verification of test and uat by app-v1.
func targetOnlyBundle(evidence ...string) *kardinalv1alpha1.Bundle {
	b := makeBundle("app-rb", "app")
	b.Spec.Intent = &kardinalv1alpha1.BundleIntent{TargetEnvironment: "prod", TargetOnly: true}
	at := metav1.NewTime(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	for i, env := range evidence {
		b.Status.UpstreamEvidence = append(b.Status.UpstreamEvidence,
			kardinalv1alpha1.UpstreamEvidence{Environment: env, Bundle: "app-v1", VerifiedAt: at, SoakMinutes: int64(60 * (i + 1))})
	}
	return b
}

// #1311: a targetOnly Bundle that carries the verification of every
// upstream environment builds a Graph of the target only. No skip
// permission is needed for an org gate upstream: those environments are not
// skipped, they verified the artifacts. The target is a root of the Graph,
// but a gate on it reads the soak of the carried upstream.
func TestBuild_TargetOnlyCarriesUpstream(t *testing.T) {
	pipeline := makeLinearPipeline("app", "test", "uat", "prod")
	orgUat := orgGate("uat-soak", "uat")
	b := targetOnlyBundle("test", "uat")
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: pipeline, Bundle: b,
		PolicyGates: []kardinalv1alpha1.PolicyGate{orgUat, makePolicyGate("prod-gate", "default", "prod", "true")}})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)
	nodes := nodeByID(res.Graph.Spec.Nodes)
	assert.Contains(t, nodes, "prod")
	assert.NotContains(t, nodes, "test")
	assert.NotContains(t, nodes, "uat")
	assert.Equal(t, []string{"prod"}, res.Environments)
	instances := gateInstances(t, res.Graph)
	assert.Len(t, instances["prod"], 1, "prod's own gate applies, and no skip permission")
	assert.Empty(t, instances["uat"])

	promoted, err := graph.PromotedEnvironments(pipeline, b)
	require.NoError(t, err)
	assert.Equal(t, []string{"prod"}, promoted, "the Bundle is Verified when prod is")
	ups, err := graph.DirectUpstreams(pipeline, b, "prod")
	require.NoError(t, err)
	assert.Empty(t, ups, "prod is released at once")
	soakUps, err := graph.SoakUpstreams(pipeline, b, "prod")
	require.NoError(t, err)
	assert.Equal(t, []string{"uat"}, soakUps, "its soak gate reads uat")
	soak, ok := graph.UpstreamSoak(b, "uat")
	assert.True(t, ok)
	assert.Equal(t, int64(120), soak)
	_, ok = graph.UpstreamSoak(b, "staging")
	assert.False(t, ok)
}

// #1311: without the evidence a targetOnly Bundle promotes the whole path,
// as a targetEnvironment Bundle does; evidence on a Bundle without
// targetOnly is ignored.
func TestBuild_TargetOnlyWithoutEvidence(t *testing.T) {
	pipeline := makeLinearPipeline("app", "test", "uat", "prod")
	for name, b := range map[string]*kardinalv1alpha1.Bundle{
		"no evidence": targetOnlyBundle(),
		"not targetOnly": func() *kardinalv1alpha1.Bundle {
			b := targetOnlyBundle("test", "uat")
			b.Spec.Intent.TargetOnly = false
			return b
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			assert.Nil(t, graph.CarriedEnvironments(b))
			promoted, err := graph.PromotedEnvironments(pipeline, b)
			require.NoError(t, err)
			assert.Equal(t, []string{"test", "uat", "prod"}, promoted)
			soakUps, err := graph.SoakUpstreams(pipeline, b, "prod")
			require.NoError(t, err)
			assert.Equal(t, []string{"uat"}, soakUps)
			_, ok := graph.UpstreamSoak(b, "uat")
			assert.False(t, ok, "only a verification the Bundle carries counts")
		})
	}
}
