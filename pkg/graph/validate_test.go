// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

func pipelineOf(name string, envs ...kardinalv1alpha1.EnvironmentSpec) *kardinalv1alpha1.Pipeline {
	return &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: envs},
	}
}

// TestBuild_RejectsCollidingEnvNodeIDs verifies that environment names that
// map to the same node ID are rejected by Build with both names in the error,
// instead of producing a Graph kro refuses (C01-graph-09).
func TestBuild_RejectsCollidingEnvNodeIDs(t *testing.T) {
	for _, pair := range [][2]string{
		{"prod-eu", "prod_eu"},
		{"prod-eu", "prodEu"},
		{"prod-eu", "prod.eu"},
		{"prod-eu", "Prod-eu"},
	} {
		t.Run(pair[0]+"_vs_"+pair[1], func(t *testing.T) {
			p := pipelineOf("app",
				kardinalv1alpha1.EnvironmentSpec{Name: "test"},
				kardinalv1alpha1.EnvironmentSpec{Name: pair[0], DependsOn: []string{"test"}},
				kardinalv1alpha1.EnvironmentSpec{Name: pair[1], DependsOn: []string{"test"}},
			)
			_, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-x7k2m", "app")})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "same node id")
			assert.Contains(t, err.Error(), `"`+pair[0]+`"`)
			assert.Contains(t, err.Error(), `"`+pair[1]+`"`)
		})
	}
}

// TestBuild_RejectsReservedNodeIDs verifies that environment names whose node
// ID kro reserves, or that collide with the Bundle ref node, are rejected by
// Build (C01-graph-28).
func TestBuild_RejectsReservedNodeIDs(t *testing.T) {
	tests := []struct {
		env     string
		wantErr string
	}{
		{env: "Status", wantErr: "reserves"},
		{env: "api-version", wantErr: "reserves"},
		{env: "Kind", wantErr: "reserves"},
		{env: "each", wantErr: "reserves"},
		{env: "self", wantErr: "reserves"},
		{env: "for", wantErr: "reserves"},
		{env: "bundle", wantErr: "same node id"},
	}
	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			envs := []kardinalv1alpha1.EnvironmentSpec{{Name: tt.env}}
			_, err := graph.NewBuilder().Build(graph.BuildInput{
				Pipeline: pipelineOf("app", envs...), Bundle: makeBundle("app-x7k2m", "app"),
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Contains(t, err.Error(), `environment "`+tt.env+`"`)
		})
	}
}

// TestBuild_RejectsCollidingGateNodeIDs verifies that gates whose node IDs
// collide are rejected by Build instead of producing a Graph kro refuses
// (C01-graph-14).
func TestBuild_RejectsCollidingGateNodeIDs(t *testing.T) {
	cases := [][2]kardinalv1alpha1.PolicyGate{
		{makePolicyGate("a", "b0c", "prod", "true"), makePolicyGate("a0b", "c", "prod", "true")},
		{makePolicyGate("no-weekend", "platform-policies", "prod", "true"),
			makePolicyGate("no.weekend", "platform-policies", "prod", "true")},
	}
	for _, gates := range cases {
		t.Run(gates[0].Name+"_vs_"+gates[1].Name, func(t *testing.T) {
			_, err := graph.NewBuilder().Build(graph.BuildInput{
				Pipeline:    makeLinearPipeline("app", "prod"),
				Bundle:      makeBundle("app-x7k2m", "app"),
				PolicyGates: gates[:],
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "same node id")
			assert.Contains(t, err.Error(), `PolicyGate "`+gates[0].Name+`"`)
			assert.Contains(t, err.Error(), `PolicyGate "`+gates[1].Name+`"`)
		})
	}
}

// TestBuild_TemplateNamesValidAndUnique verifies that every template renders
// a valid, unique metadata.name, including long names that must be cut and
// environment names that are not DNS-1123 (C01-graph-08, C01-graph-10).
func TestBuild_TemplateNamesValidAndUnique(t *testing.T) {
	cases := []struct {
		name     string
		pipeline string
		bundle   string
		envs     []string
	}{
		{"prstatus-truncation-collision", "payments-checkout-service-prod", "payments-checkout-service-prod-x7k2m",
			[]string{"production-eu-west-1", "production-eu-west-2"}},
		{"prstatus-truncation-trailing-dash", "payments-checkout-service-prod", "payments-checkout-service-prod-x7k2m",
			[]string{"prod-europe-west-1"}},
		{"env-name-underscore", "app", "app-x7k2m", []string{"prod_eu"}},
		{"env-name-uppercase", "app", "app-x7k2m", []string{"Prod"}},
		{"dotted-bundle", "app", "app-v1.2.3", []string{"test", "prod"}},
		{"long-bundle", "app", "app-" + strings.Repeat("b", 59), []string{"test", "prod"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envs := make([]kardinalv1alpha1.EnvironmentSpec, len(tc.envs))
			for i, e := range tc.envs {
				envs[i] = kardinalv1alpha1.EnvironmentSpec{Name: e}
			}
			res, err := graph.NewBuilder().Build(graph.BuildInput{
				Pipeline: pipelineOf(tc.pipeline, envs...),
				Bundle:   makeBundle(tc.bundle, tc.pipeline),
				PolicyGates: []kardinalv1alpha1.PolicyGate{
					makePolicyGate("no-weekend", "platform-policies", tc.envs[len(tc.envs)-1], "true"),
				},
			})
			require.NoError(t, err)
			assertKroValid(t, res.Graph)
		})
	}
}

// TestBuild_KeepsReadableNames verifies that names that are already valid
// slugs and fit keep their readable form, so existing Graphs and objects keep
// their names.
func TestBuild_KeepsReadableNames(t *testing.T) {
	res, err := graph.NewBuilder().Build(graph.BuildInput{
		Pipeline:    makeLinearPipeline("nginx-demo", "test", "prod"),
		Bundle:      makeBundle("nginx-demo-x7k2m", "nginx-demo"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{makePolicyGate("no-weekend", "platform-policies", "prod", "true")},
	})
	require.NoError(t, err)
	assert.Equal(t, "nginx-demo-nginx-demo-x7k2m", res.Graph.Name)
	want := map[string]bool{
		"nginx-demo-nginx-demo-x7k2m-prod":                    true,
		"prstatus-nginx-demo-x7k2m-prod":                      true,
		"no-weekend-platform-policies-prod--nginx-demo-x7k2m": true,
	}
	for _, n := range res.Graph.Spec.Nodes {
		if n.Template == nil {
			continue
		}
		name := n.Template["metadata"].(map[string]interface{})["name"].(string)
		delete(want, name)
	}
	assert.Empty(t, want, "readable names missing from the Graph")
}

// TestGraphNameFrom_Distinct verifies that different Bundles of one Pipeline
// never share a Graph name (C01-graph-11).
func TestGraphNameFrom_Distinct(t *testing.T) {
	long := "payments-checkout-service-prod" // 30 chars
	pairs := [][3]string{
		{long, long + "-bcxyz", long + "-dfxyz"}, // GenerateName Bundles differing in the first random chars
		{"app", "app-v1.2", "app-v1-2"},
		{"app", "App-v1", "app-v1"},
	}
	for _, p := range pairs {
		a := graph.GraphNameFrom(p[0], p[1])
		b := graph.GraphNameFrom(p[0], p[2])
		assert.NotEqual(t, a, b, "bundles %q and %q share Graph name %q", p[1], p[2], a)
		assert.LessOrEqual(t, len(a), 63)
		assert.LessOrEqual(t, len(b), 63)
	}
	assert.Equal(t, "app-app-v1", graph.GraphNameFrom("app", "app-v1"), "slug names stay readable")
}

// TestBuild_RejectsInvalidInput verifies that names the Graph cannot carry
// in a label value or object name are rejected with a message naming the
// field (C01-graph-16, C01-graph-25).
func TestBuild_RejectsInvalidInput(t *testing.T) {
	long := strings.Repeat("p", 64)
	tests := []struct {
		name    string
		mutate  func(p *kardinalv1alpha1.Pipeline, b *kardinalv1alpha1.Bundle)
		wantErr string
	}{
		{"long pipeline name", func(p *kardinalv1alpha1.Pipeline, b *kardinalv1alpha1.Bundle) {
			p.Name = long
			b.Spec.Pipeline = long
		}, "pipeline name"},
		{"long bundle name", func(_ *kardinalv1alpha1.Pipeline, b *kardinalv1alpha1.Bundle) {
			b.Name = long
		}, "bundle name"},
		{"environment name with a space", func(p *kardinalv1alpha1.Pipeline, _ *kardinalv1alpha1.Bundle) {
			p.Spec.Environments[1].Name = "prod eu"
		}, `environment name "prod eu"`},
		{"empty environment name", func(p *kardinalv1alpha1.Pipeline, _ *kardinalv1alpha1.Bundle) {
			p.Spec.Environments[1].Name = ""
		}, "has no name"},
		{"duplicate environment", func(p *kardinalv1alpha1.Pipeline, _ *kardinalv1alpha1.Bundle) {
			p.Spec.Environments[1].Name = "test"
		}, "declared twice"},
		// #1304: two or more regions fail at Graph build, before any step.
		{"two regions", func(p *kardinalv1alpha1.Pipeline, _ *kardinalv1alpha1.Bundle) {
			p.Spec.Environments[1].Regions = []string{"us-east-1", "eu-west-1"} //nolint:staticcheck // SA1019: tests the rejection
		}, `environment "prod": regions is not supported; declare one environment per region (prod-us, prod-eu) and use wave`},
		{"two invalid regions", func(p *kardinalv1alpha1.Pipeline, _ *kardinalv1alpha1.Bundle) {
			p.Spec.Environments[1].Regions = []string{"US_EAST", "US_EAST"} //nolint:staticcheck // SA1019: tests the rejection
		}, "regions is not supported"},
		{"unknown skipped environment", func(_ *kardinalv1alpha1.Pipeline, b *kardinalv1alpha1.Bundle) {
			b.Spec.Intent = &kardinalv1alpha1.BundleIntent{SkipEnvironments: []string{"stagign"}}
		}, "unknown environments [stagign]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := makeLinearPipeline("app", "test", "prod")
			b := makeBundle("app-x7k2m", "app")
			tt.mutate(p, b)
			_, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: b})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}

}

// TestBuild_RejectsBundleWithoutItsArtifacts is #1285: a Bundle whose type
// needs images and has none, or needs configRef.commitSHA and has none, fails
// Build with ErrInvalid (InvalidSpec on the Bundle) before any environment is
// promoted. Before, an empty image Bundle "succeeded" in every environment
// with no images to update.
func TestBuild_RejectsBundleWithoutItsArtifacts(t *testing.T) {
	images := []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v1"}}
	commit := &kardinalv1alpha1.ConfigRef{CommitSHA: "abc123"}
	tests := []struct {
		name    string
		spec    kardinalv1alpha1.BundleSpec
		wantErr string
	}{
		{name: "image without images", spec: kardinalv1alpha1.BundleSpec{Type: "image"},
			wantErr: `bundle "app-x7k2m": type "image" requires at least one entry in images`},
		{name: "no type counts as image", spec: kardinalv1alpha1.BundleSpec{},
			wantErr: `type "image" requires at least one entry in images`},
		{name: "image with only a config commit", spec: kardinalv1alpha1.BundleSpec{Type: "image", ConfigRef: commit},
			wantErr: `type "image" requires at least one entry in images`},
		{name: "config without configRef", spec: kardinalv1alpha1.BundleSpec{Type: "config", Images: images},
			wantErr: `type "config" requires configRef.commitSHA`},
		{name: "config with a repo and no commit",
			spec:    kardinalv1alpha1.BundleSpec{Type: "config", ConfigRef: &kardinalv1alpha1.ConfigRef{GitRepo: "https://g/cfg"}},
			wantErr: `type "config" requires configRef.commitSHA`},
		{name: "mixed without images", spec: kardinalv1alpha1.BundleSpec{Type: "mixed", ConfigRef: commit},
			wantErr: `type "mixed" requires at least one entry in images`},
		{name: "mixed without a commit", spec: kardinalv1alpha1.BundleSpec{Type: "mixed", Images: images},
			wantErr: `type "mixed" requires configRef.commitSHA`},
		{name: "image", spec: kardinalv1alpha1.BundleSpec{Type: "image", Images: images}},
		{name: "config", spec: kardinalv1alpha1.BundleSpec{Type: "config", ConfigRef: commit}},
		{name: "mixed", spec: kardinalv1alpha1.BundleSpec{Type: "mixed", Images: images, ConfigRef: commit}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := makeBundle("app-x7k2m", "app")
			tt.spec.Pipeline = "app"
			b.Spec = tt.spec
			_, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: makeLinearPipeline("app", "test", "prod"), Bundle: b})
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, graph.ErrInvalid)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestBuild_GateNameBackstop verifies that a gate whose name cannot go into
// the instance's kardinal.io/gate-template label fails only the Graphs that
// place it, and that the error names the gate, its namespace and the
// environment. The PolicyGate CRD refuses such names at creation
// (TestCRDSchemaPolicyGateName); this covers gates created before that rule.
func TestBuild_GateNameBackstop(t *testing.T) {
	long := strings.Repeat("g", 64)
	withoutAppliesTo := makePolicyGate("freeze-"+strings.Repeat("p", 60), "team-a", "", "false")
	delete(withoutAppliesTo.Labels, "kardinal.io/applies-to")
	tests := []struct {
		name    string
		gates   []kardinalv1alpha1.PolicyGate
		bundle  *kardinalv1alpha1.Bundle
		wantErr string
	}{
		{
			name:    "placed gate",
			gates:   []kardinalv1alpha1.PolicyGate{makePolicyGate(long, "platform-policies", "prod", "true")},
			wantErr: `PolicyGate "` + long + `" in namespace "platform-policies" applies to environment "prod"`,
		},
		{
			name: "placed skip-permission gate",
			gates: []kardinalv1alpha1.PolicyGate{
				orgGate("staging-soak", "staging"),
				skipPermission(long, "platform-policies", "staging", "true", true),
			},
			bundle:  skipBundle("staging"),
			wantErr: `PolicyGate "` + long + `" in namespace "platform-policies" applies to environment "prod"`,
		},
		{
			name:  "gate for an environment this Pipeline does not have",
			gates: []kardinalv1alpha1.PolicyGate{makePolicyGate(long, "platform-policies", "qa", "true")},
		},
		{
			name:  "gate without applies-to, such as a long freeze gate",
			gates: []kardinalv1alpha1.PolicyGate{withoutAppliesTo},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := tt.bundle
			if b == nil {
				b = makeBundle("app-x7k2m", "app")
			}
			_, err := graph.NewBuilder().Build(graph.BuildInput{
				Pipeline:    makeLinearPipeline("app", "test", "staging", "prod"),
				Bundle:      b,
				PolicyGates: tt.gates,
			})
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Contains(t, err.Error(), "at most 63 characters")
			assert.ErrorIs(t, err, graph.ErrInvalid)
		})
	}
}

// TestBuild_LabelValuesValid verifies that the longest accepted Pipeline and
// Bundle names give valid label values everywhere (C01-graph-16).
func TestBuild_LabelValuesValid(t *testing.T) {
	pipeline := strings.Repeat("p", 48)
	res, err := graph.NewBuilder().Build(graph.BuildInput{
		Pipeline:    pipelineOf(pipeline, kardinalv1alpha1.EnvironmentSpec{Name: "test"}, kardinalv1alpha1.EnvironmentSpec{Name: "prod"}),
		Bundle:      makeBundle(pipeline+"-x7k2m", pipeline),
		PolicyGates: []kardinalv1alpha1.PolicyGate{makePolicyGate("no-weekend", "platform-policies", "prod", "true")},
	})
	require.NoError(t, err)
	assertKroValid(t, res.Graph)
}

// TestBuild_GateInstanceSpec verifies what a gate instance copies from its
// template: expression, message, recheckInterval, and when only if set.
// spec.overrides is not copied, because kro would own the field and revert
// overrides the CLI records on the instance (C01-graph-05, C01-graph-29).
// spec.generated is set, so the instance may have a name over 63 characters
// and is never collected as a template (GATE-REJECT-02).
func TestBuild_GateInstanceSpec(t *testing.T) {
	tests := []struct {
		name     string
		when     string
		wantWhen bool
	}{
		{name: "when set", when: "pre-deploy", wantWhen: true},
		{name: "when unset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tpl := makePolicyGate("no-weekend-deploy", "platform-policies", "prod", "!schedule.isWeekend")
			tpl.Spec.When = tt.when //nolint:staticcheck // SA1019: the deprecated field is still copied
			tpl.Spec.Overrides = []kardinalv1alpha1.PolicyGateOverride{{Reason: "P0 hotfix", Stage: "prod"}}
			res, err := graph.NewBuilder().Build(graph.BuildInput{
				Pipeline:    makeLinearPipeline("app", "prod"),
				Bundle:      makeBundle("app-x7k2m", "app"),
				PolicyGates: []kardinalv1alpha1.PolicyGate{tpl},
			})
			require.NoError(t, err)
			var spec map[string]interface{}
			for _, n := range res.Graph.Spec.Nodes {
				if n.Template != nil && n.Template["kind"] == "PolicyGate" {
					spec = n.Template["spec"].(map[string]interface{})
				}
			}
			require.NotNil(t, spec, "gate instance must be emitted")
			assert.Equal(t, "!schedule.isWeekend", spec["expression"])
			assert.Equal(t, "5m", spec["recheckInterval"])
			assert.Equal(t, true, spec["generated"])
			assert.NotContains(t, spec, "overrides")
			if tt.wantWhen {
				assert.Equal(t, tt.when, spec["when"])
			} else {
				assert.NotContains(t, spec, "when")
			}
		})
	}
}

// TestValidateNodeIDs verifies the node ID checks directly.
func TestValidateNodeIDs(t *testing.T) {
	tests := []struct {
		name    string
		nodes   []graph.GraphNode
		wantErr string
	}{
		{name: "valid", nodes: []graph.GraphNode{{ID: "bundle"}, {ID: "prodEu"}}},
		{name: "bad grammar", nodes: []graph.GraphNode{{ID: "prod-eu"}}, wantErr: "does not match"},
		{name: "leading digit", nodes: []graph.GraphNode{{ID: "1prod"}}, wantErr: "does not match"},
		{name: "reserved", nodes: []graph.GraphNode{{ID: "metadata"}}, wantErr: "reserves"},
		{name: "duplicate", nodes: []graph.GraphNode{{ID: "prod"}, {ID: "prod"}}, wantErr: "same node id"},
		{name: "iterator", nodes: []graph.GraphNode{
			{ID: "prod", ForEach: []map[string]string{{"region": "${[\"eu\"]}"}}}, {ID: "region"},
		}, wantErr: "forEach iterator"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := graph.ValidateNodeIDs(tt.nodes)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestCELSafeSlug verifies the camelCase node ID form.
func TestCELSafeSlug(t *testing.T) {
	tests := map[string]string{
		"kardinal-test-app-uat": "kardinalTestAppUat",
		"no_weekend_deploys":    "noWeekendDeploys",
		"prod-eu":               "prodEu",
		"0bad":                  "x0bad",
		"MyApp":                 "myApp",
		"-prod":                 "prod",
		"-Prod":                 "prod",
		"prod-1":                "prod1",
		"v1.2.3":                "v123",
		"":                      "x",
	}
	for in, want := range tests {
		assert.Equal(t, want, graph.CELSafeSlug(in), "CELSafeSlug(%q)", in)
	}
}

// TestBuild_ErrorsAreErrInvalid verifies that every Build error wraps
// ErrInvalid and keeps its message, so the Bundle reconciler can fail the
// Bundle instead of retrying an error only a spec change can fix.
func TestBuild_ErrorsAreErrInvalid(t *testing.T) {
	tests := []struct {
		name  string
		input graph.BuildInput
	}{
		{"nil pipeline", graph.BuildInput{Bundle: makeBundle("app-x7k2m", "app")}},
		{"invalid name", graph.BuildInput{Pipeline: pipelineOf("app", kardinalv1alpha1.EnvironmentSpec{Name: "prod eu"}),
			Bundle: makeBundle("app-x7k2m", "app")}},
		{"steps", graph.BuildInput{Pipeline: pipelineOf("app", kardinalv1alpha1.EnvironmentSpec{Name: "prod",
			Steps: []kardinalv1alpha1.StepSpec{{Uses: "git-clone"}}}), Bundle: makeBundle("app-x7k2m", "app")}},
		{"cycle", graph.BuildInput{Pipeline: pipelineOf("app",
			kardinalv1alpha1.EnvironmentSpec{Name: "a", DependsOn: []string{"b"}},
			kardinalv1alpha1.EnvironmentSpec{Name: "b", DependsOn: []string{"a"}}), Bundle: makeBundle("app-x7k2m", "app")}},
		{"skip denied", graph.BuildInput{Pipeline: makeLinearPipeline("app", "test", "staging", "prod"),
			Bundle: skipBundle("staging"), PolicyGates: []kardinalv1alpha1.PolicyGate{orgGate("staging-soak", "staging")}}},
		{"node id collision", graph.BuildInput{Pipeline: pipelineOf("app",
			kardinalv1alpha1.EnvironmentSpec{Name: "prod-eu"}, kardinalv1alpha1.EnvironmentSpec{Name: "prod_eu"}),
			Bundle: makeBundle("app-x7k2m", "app")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := graph.NewBuilder().Build(tt.input)
			require.Error(t, err)
			assert.ErrorIs(t, err, graph.ErrInvalid)
			assert.True(t, strings.HasPrefix(err.Error(), "build: "), "message kept: %v", err)
		})
	}
	assert.ErrorIs(t, graph.ValidateNodeIDs([]graph.GraphNode{{ID: "prod-eu"}}), graph.ErrInvalid)
}

// TestValidateSecretRef: a Pipeline may name only a git Secret in its own
// namespace (C03-promotionstep-18). This is a refusal, not a missing feature,
// so it is a validation error and never reads "not implemented".
func TestValidateSecretRef(t *testing.T) {
	tests := []struct {
		name      string
		namespace string // the Pipeline's namespace
		ref       *kardinalv1alpha1.SecretRef
		wantErr   string
	}{
		{name: "no secretRef", namespace: "team-a"},
		{name: "empty namespace means the Pipeline's", namespace: "team-a",
			ref: &kardinalv1alpha1.SecretRef{Name: "github-token"}},
		{name: "the Pipeline's namespace", namespace: "team-a",
			ref: &kardinalv1alpha1.SecretRef{Name: "github-token", Namespace: "team-a"}},
		{name: "another namespace", namespace: "team-a",
			ref:     &kardinalv1alpha1.SecretRef{Name: "github-token", Namespace: "kardinal-system"},
			wantErr: `git.secretRef.namespace "kardinal-system" is not allowed: the Secret must be in the Pipeline's namespace "team-a"`},
		{name: "an unknown Pipeline namespace fails closed", namespace: "",
			ref:     &kardinalv1alpha1.SecretRef{Name: "github-token", Namespace: "team-b"},
			wantErr: `git.secretRef.namespace "team-b" is not allowed`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := pipelineOf("app", kardinalv1alpha1.EnvironmentSpec{Name: "test"})
			p.Namespace = tc.namespace
			p.Spec.Git.SecretRef = tc.ref
			err := graph.ValidateSecretRef(p)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.NotContains(t, err.Error(), "not implemented")
			assert.Empty(t, graph.UnimplementedFields(p), "a refusal is not an unimplemented field")
		})
	}
}

// TestValidateCIRunURL: a new Bundle's ciRunURL is empty or an absolute
// http(s) URL without user info, whitespace or control characters. Errors do
// not echo the URL or any part of it (url.Parse errors quote the "port" of
// https://user:token/x).
func TestValidateCIRunURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		{name: "empty", url: ""},
		{name: "GitHub Actions run", url: "https://github.com/o/r/actions/runs/123?check=1#step:2:1"},
		{name: "http with port", url: "http://jenkins.local:8080/job/app/7/"},
		{name: "upper-case scheme", url: "HTTPS://gitlab.com/g/p/-/pipelines/1"},
		{name: "parentheses", url: "https://ci.example.com/run(1)"},
		{name: "javascript", url: "javascript:alert(1)", wantErr: "absolute http or https URL"},
		{name: "data", url: "data:text/html,<b>x</b>", wantErr: "absolute http or https URL"},
		{name: "ftp", url: "ftp://ci.example.com/1", wantErr: "absolute http or https URL"},
		{name: "relative path", url: "/o/r/actions/runs/1", wantErr: "absolute http or https URL"},
		{name: "no scheme", url: "ci.example.com/runs/1", wantErr: "absolute http or https URL"},
		{name: "no host", url: "https:///runs/1", wantErr: "absolute http or https URL"},
		{name: "scheme-relative", url: "//ci.example.com/runs/1", wantErr: "absolute http or https URL"},
		{name: "user info", url: "https://user:s3cret@ci.example.com/1", wantErr: "user info"},
		{name: "host disguised as user info", url: "https://github.com@evil.example/1", wantErr: "user info"},
		{name: "space", url: "https://ci.example.com/a b", wantErr: "whitespace or control"},
		{name: "leading space", url: " https://ci.example.com/1", wantErr: "whitespace or control"},
		{name: "newline", url: "https://ci.example.com/1\n| x |", wantErr: "whitespace or control"},
		{name: "tab", url: "https://ci.example.com/\t1", wantErr: "whitespace or control"},
		{name: "NUL", url: "https://ci.example.com/\x001", wantErr: "whitespace or control"},
		{name: "no-break space", url: "https://ci.example.com/ 1", wantErr: "whitespace or control"},
		{name: "bad escape", url: "https://ci.example.com/%zz", wantErr: "not a valid URL"},
		{name: "bad host", url: "https://[::1/runs", wantErr: "not a valid URL"},
		{name: "credential parsed as a port", url: "https://user:s3cret/x", wantErr: "not a valid URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := graph.ValidateCIRunURL(tt.url)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Contains(t, err.Error(), "provenance.ciRunURL")
			for _, secret := range []string{"s3cret", "evil.example", "ci.example.com", "runs", "zz", "::1"} {
				assert.NotContains(t, err.Error(), secret, "the error must not echo the URL")
			}
		})
	}
}

// TestValidateUpdateStrategy: update.strategy argocd with approval: pr-review
// is a validation error (#1281). It feeds the Pipeline Ready condition and
// "kardinal validate" for Pipelines stored before the CRD rule.
func TestValidateUpdateStrategy(t *testing.T) {
	argocd := func(approval string) kardinalv1alpha1.EnvironmentSpec {
		return kardinalv1alpha1.EnvironmentSpec{Name: "prod", Approval: approval,
			Update: kardinalv1alpha1.UpdateConfig{Strategy: "argocd",
				ArgoCD: &kardinalv1alpha1.ArgoCDUpdateConfig{Application: "app-prod"}}}
	}
	tests := []struct {
		name    string
		env     kardinalv1alpha1.EnvironmentSpec
		wantErr string
	}{
		{name: "argocd with pr-review", env: argocd("pr-review"),
			wantErr: `environment "prod": update.strategy argocd patches the Application directly and cannot honour approval: pr-review`},
		{name: "argocd with auto", env: argocd("auto")},
		{name: "argocd with default approval", env: argocd("")},
		{name: "kustomize with pr-review", env: kardinalv1alpha1.EnvironmentSpec{Name: "prod", Approval: "pr-review",
			Update: kardinalv1alpha1.UpdateConfig{Strategy: "kustomize"}}},
		{name: "default strategy with pr-review", env: kardinalv1alpha1.EnvironmentSpec{Name: "prod", Approval: "pr-review"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := graph.ValidateUpdateStrategy(pipelineOf("app", kardinalv1alpha1.EnvironmentSpec{Name: "test"}, tc.env))
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestBuild_RejectsArgoCDForConfigAndMixed: argocd sets only the image in the
// Argo CD Application, so a config or mixed Bundle fails at build when any
// environment it promotes uses argocd, even a later one (#1281). An image
// Bundle, or an argocd environment the Bundle does not reach, still builds.
func TestBuild_RejectsArgoCDForConfigAndMixed(t *testing.T) {
	pipeline := func() *kardinalv1alpha1.Pipeline {
		p := makeLinearPipeline("app", "test", "staging", "prod")
		p.Spec.Environments[2].Update = kardinalv1alpha1.UpdateConfig{Strategy: "argocd",
			ArgoCD: &kardinalv1alpha1.ArgoCDUpdateConfig{Application: "app-prod"}}
		return p
	}
	bundle := func(typ, target string) *kardinalv1alpha1.Bundle {
		b := makeBundle("app-x7k2m", "app")
		b.Spec.Type = typ
		if typ != "image" {
			b.Spec.ConfigRef = &kardinalv1alpha1.ConfigRef{CommitSHA: "abc123"}
		}
		if target != "" {
			b.Spec.Intent = &kardinalv1alpha1.BundleIntent{TargetEnvironment: target}
		}
		return b
	}
	tests := []struct {
		name    string
		bundle  *kardinalv1alpha1.Bundle
		wantErr string
	}{
		{name: "config", bundle: bundle("config", ""),
			wantErr: `build: environment "prod" uses update.strategy argocd, which does not support config Bundles`},
		{name: "mixed", bundle: bundle("mixed", ""),
			wantErr: `build: environment "prod" uses update.strategy argocd, which does not support mixed Bundles`},
		{name: "image", bundle: bundle("image", "")},
		{name: "config stopping before the argocd environment", bundle: bundle("config", "staging")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: pipeline(), Bundle: tc.bundle})
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, graph.ErrInvalid)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
