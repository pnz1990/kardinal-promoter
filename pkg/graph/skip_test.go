// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// orgGate returns an ordinary gate in the org policy namespace.
func orgGate(name, env string) kardinalv1alpha1.PolicyGate {
	g := makePolicyGate(name, "platform-policies", env, "upstream.test.soakMinutes >= 30")
	g.Labels["kardinal.io/scope"] = "org"
	return g
}

// skipPermission returns a skip-permission gate for env in ns.
func skipPermission(name, ns, env, expression string, flag bool) kardinalv1alpha1.PolicyGate {
	g := makePolicyGate(name, ns, env, expression)
	g.Labels[graph.LabelGateType] = graph.GateTypeSkipPermission
	g.Spec.SkipPermission = flag
	return g
}

func skipBundle(skip ...string) *kardinalv1alpha1.Bundle {
	b := makeBundle("app-x7k2m", "app")
	b.Namespace = "team-a"
	b.Spec.Intent = &kardinalv1alpha1.BundleIntent{SkipEnvironments: skip}
	return b
}

// TestValidateSkipPermissions verifies who may grant a skip: only a
// skip-permission gate with spec.skipPermission set, in an org policy
// namespace, and only when an org gate applies to the skipped environment
// (C01-graph-03, C01-graph-06).
func TestValidateSkipPermissions(t *testing.T) {
	tests := []struct {
		name     string
		gates    []kardinalv1alpha1.PolicyGate
		policyNS []string
		wantErr  bool
	}{
		{name: "no org gate needs no permission",
			gates: []kardinalv1alpha1.PolicyGate{makePolicyGate("team-soak", "team-a", "staging", "true")}},
		{name: "org gate without permission is denied",
			gates: []kardinalv1alpha1.PolicyGate{orgGate("staging-soak", "staging")}, wantErr: true},
		{name: "team-namespace permission is denied",
			gates: []kardinalv1alpha1.PolicyGate{orgGate("staging-soak", "staging"),
				skipPermission("let-me-skip", "team-a", "staging", "true", true)}, wantErr: true},
		{name: "team-namespace permission labelled org is denied",
			gates: []kardinalv1alpha1.PolicyGate{orgGate("staging-soak", "staging"), func() kardinalv1alpha1.PolicyGate {
				g := skipPermission("let-me-skip", "team-a", "staging", "true", true)
				g.Labels["kardinal.io/scope"] = "org"
				return g
			}()}, wantErr: true},
		{name: "org permission without spec.skipPermission is denied",
			gates: []kardinalv1alpha1.PolicyGate{orgGate("staging-soak", "staging"),
				skipPermission("hotfix-skip", "platform-policies", "staging", "true", false)}, wantErr: true},
		{name: "org permission for another environment is denied",
			gates: []kardinalv1alpha1.PolicyGate{orgGate("staging-soak", "staging"),
				skipPermission("hotfix-skip", "platform-policies", "uat", "true", true)}, wantErr: true},
		{name: "org permission is allowed",
			gates: []kardinalv1alpha1.PolicyGate{orgGate("staging-soak", "staging"),
				skipPermission("hotfix-skip", "platform-policies", "staging", "true", true)}},
		{name: "scope=org gate in a team namespace needs permission",
			gates: []kardinalv1alpha1.PolicyGate{func() kardinalv1alpha1.PolicyGate {
				g := makePolicyGate("staging-soak", "team-a", "staging", "true")
				g.Labels["kardinal.io/scope"] = "org"
				return g
			}()}, wantErr: true},
		{name: "configured org namespace grants permission",
			policyNS: []string{"org-policies"},
			gates: []kardinalv1alpha1.PolicyGate{func() kardinalv1alpha1.PolicyGate {
				g := orgGate("staging-soak", "staging")
				g.Namespace = "org-policies"
				return g
			}(), skipPermission("hotfix-skip", "org-policies", "staging", "true", true)}},
		{name: "default org namespace is not org when others are configured",
			policyNS: []string{"org-policies"},
			gates: []kardinalv1alpha1.PolicyGate{orgGate("staging-soak", "staging"),
				skipPermission("hotfix-skip", "platform-policies", "staging", "true", true)}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := graph.ValidateSkipPermissions(skipBundle("staging"), tt.gates, tt.policyNS)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), `skip denied for environment "staging"`)
		})
	}
}

// gateInstances returns the PolicyGate instances a Graph creates, by
// environment label.
func gateInstances(t *testing.T, g *graph.Graph) map[string][]map[string]interface{} {
	t.Helper()
	out := map[string][]map[string]interface{}{}
	for _, o := range renderedOf(t, g, "PolicyGate") {
		env, _ := objLabels(o)["kardinal.io/environment"].(string)
		out[env] = append(out[env], o)
	}
	return out
}

// TestBuild_SkipPermissionHoldsNextEnvironment verifies that an allowed skip
// puts an instance of the permission gate in front of the next environment,
// so the PolicyGate reconciler evaluates the permission expression and the
// next environment waits until it is true (C01-graph-03, C01-graph-06). This
// is the documented hotfix example.
func TestBuild_SkipPermissionHoldsNextEnvironment(t *testing.T) {
	perm := skipPermission("allow-staging-skip-for-hotfix", "platform-policies", "staging",
		`bundle.version.startsWith("hotfix-")`, true)
	res, err := graph.NewBuilder().Build(graph.BuildInput{
		Pipeline:    makeLinearPipeline("app", "test", "staging", "prod"),
		Bundle:      skipBundle("staging"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{orgGate("staging-soak", "staging"), perm},
	})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)
	assert.Equal(t, []string{"test", "prod"}, res.Environments)

	instances := gateInstances(t, res.Graph)
	require.Len(t, instances["prod"], 1, "prod must be held by the permission instance")
	assert.Empty(t, instances["test"])
	inst := instances["prod"][0]
	meta := inst["metadata"].(map[string]interface{})
	labels := meta["labels"].(map[string]interface{})
	assert.Equal(t, graph.GateTypeSkipPermission, labels[graph.LabelGateType])
	assert.Equal(t, "allow-staging-skip-for-hotfix", labels["kardinal.io/gate-template"])
	assert.Equal(t, "platform-policies", labels[graph.LabelGateTemplateNamespace])
	assert.Equal(t, "app-x7k2m", labels["kardinal.io/bundle"], "an instance, not a template")
	assert.Equal(t, map[string]interface{}{graph.AnnotationSkippedEnvironments: "staging"}, meta["annotations"])
	assert.Equal(t, `bundle.version.startsWith("hotfix-")`, inst["spec"].(map[string]interface{})["expression"])

	prodSpec := nodeByID(res.Graph.Spec.Nodes)["prod"].Template["spec"].(map[string]interface{})
	required, _ := prodSpec["requiredGates"].([]interface{})
	require.Len(t, required, 1)
	assert.Contains(t, required[0], fmt.Sprintf("g.metadata.name == %q && g.?status.?ready.orValue(false) == true", objName(inst)))
}

// TestBuild_SkipPermissionPlacement verifies where permission instances go
// when several environments are skipped or the skipped one fans out.
func TestBuild_SkipPermissionPlacement(t *testing.T) {
	envs := func(specs ...kardinalv1alpha1.EnvironmentSpec) *kardinalv1alpha1.Pipeline {
		return pipelineOf("app", specs...)
	}
	tests := []struct {
		name     string
		pipeline *kardinalv1alpha1.Pipeline
		skip     []string
		gates    []kardinalv1alpha1.PolicyGate
		want     map[string]string // env → skipped-environments annotation of its one permission instance
	}{
		{
			name:     "two skipped environments, one permission each",
			pipeline: makeLinearPipeline("app", "test", "qa", "staging", "prod"),
			skip:     []string{"qa", "staging"},
			gates: []kardinalv1alpha1.PolicyGate{
				orgGate("qa-soak", "qa"), orgGate("staging-soak", "staging"),
				skipPermission("skip-qa", "platform-policies", "qa", "true", true),
				skipPermission("skip-staging", "platform-policies", "staging", "true", true),
			},
			want: map[string]string{"prod": "qa|staging"},
		},
		{
			name: "skipped environment with two dependents",
			pipeline: envs(
				kardinalv1alpha1.EnvironmentSpec{Name: "test"},
				kardinalv1alpha1.EnvironmentSpec{Name: "staging"},
				kardinalv1alpha1.EnvironmentSpec{Name: "prod-eu", DependsOn: []string{"staging"}},
				kardinalv1alpha1.EnvironmentSpec{Name: "prod-us", DependsOn: []string{"staging"}},
			),
			skip: []string{"staging"},
			gates: []kardinalv1alpha1.PolicyGate{orgGate("staging-soak", "staging"),
				skipPermission("skip-staging", "platform-policies", "staging", "true", true)},
			want: map[string]string{"prod-eu": "staging", "prod-us": "staging"},
		},
		{
			name:     "skipped leaf holds nothing",
			pipeline: makeLinearPipeline("app", "test", "prod"),
			skip:     []string{"prod"},
			gates: []kardinalv1alpha1.PolicyGate{orgGate("prod-soak", "prod"),
				skipPermission("skip-prod", "platform-policies", "prod", "true", true)},
			want: map[string]string{},
		},
		{
			name:     "skipped environment without org gate holds nothing",
			pipeline: makeLinearPipeline("app", "test", "staging", "prod"),
			skip:     []string{"staging"},
			gates:    []kardinalv1alpha1.PolicyGate{skipPermission("skip-staging", "platform-policies", "staging", "true", true)},
			want:     map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := graph.NewBuilder().Build(graph.BuildInput{
				Pipeline: tt.pipeline, Bundle: skipBundle(tt.skip...), PolicyGates: tt.gates,
			})
			require.NoError(t, err)
			assertKroValid(t, res.Graph)
			got := map[string]string{}
			for env, objs := range gateInstances(t, res.Graph) {
				var skipped []string
				for _, o := range objs {
					ann, _ := o["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})
					skipped = append(skipped, fmt.Sprint(ann[graph.AnnotationSkippedEnvironments]))
				}
				got[env] = joinSorted(skipped)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// joinSorted joins the entries with "|" in sorted order.
func joinSorted(s []string) string {
	c := append([]string(nil), s...)
	sort.Strings(c)
	return strings.Join(c, "|")
}

// TestBuild_SkipBridgesFanOutFanIn verifies that a skipped environment in a
// fan-out/fan-in is bridged to its surviving ancestors.
func TestBuild_SkipBridgesFanOutFanIn(t *testing.T) {
	p := pipelineOf("app",
		kardinalv1alpha1.EnvironmentSpec{Name: "test"},
		kardinalv1alpha1.EnvironmentSpec{Name: "eu", DependsOn: []string{"test"}},
		kardinalv1alpha1.EnvironmentSpec{Name: "us", DependsOn: []string{"test"}},
		kardinalv1alpha1.EnvironmentSpec{Name: "global", DependsOn: []string{"eu", "us"}},
	)
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: skipBundle("eu")})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)
	nodes := nodeByID(res.Graph.Spec.Nodes)
	assert.NotContains(t, nodes, "eu")
	assert.Equal(t, []string{"test", "us"}, upstreamIDs(t, nodes, "global"))
}

func TestIsPolicyNamespace(t *testing.T) {
	assert.True(t, graph.IsPolicyNamespace("platform-policies", nil), "the default")
	assert.False(t, graph.IsPolicyNamespace("team-a", nil))
	assert.True(t, graph.IsPolicyNamespace("org-b", []string{"org-a", "org-b"}))
	assert.False(t, graph.IsPolicyNamespace("platform-policies", []string{"org-a"}), "the default only when none are set")
	assert.False(t, graph.IsPolicyNamespace("", nil))
}
