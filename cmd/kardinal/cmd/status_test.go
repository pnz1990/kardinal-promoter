// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func runStatusPipeline(t *testing.T, objs ...sigs_client.Object) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, statusPipelineWriter(&buf, policyClient(t, objs...), "default", "demo"))
	return buf.String()
}

// statusRows parses the environment table of kardinal status <pipeline>.
func statusRows(t *testing.T, out string) []map[string]string {
	t.Helper()
	_, table, ok := strings.Cut(out, "\nENVIRONMENT ")
	require.True(t, ok, out)
	table, _, _ = strings.Cut("ENVIRONMENT "+table, "\n\n")
	return parseTable(table)
}

// parseTable parses a table whose cells are separated by two or more
// spaces.
func parseTable(table string) []map[string]string {
	lines := strings.Split(strings.TrimRight(table, "\n"), "\n")
	split := func(l string) []string { return regexp.MustCompile(`  +`).Split(strings.TrimSpace(l), -1) }
	header := split(lines[0])
	var rows []map[string]string
	for _, l := range lines[1:] {
		if !strings.Contains(l, "  ") {
			break
		}
		row := map[string]string{}
		for i, c := range split(l) {
			if i < len(header) {
				row[header[i]] = c
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// blockingGates is the gates section of kardinal status <pipeline>, "" when
// it has none.
func blockingGates(out string) string {
	_, section, _ := strings.Cut(out, "\nBlocking gates\n")
	return section
}

func TestStatusPipelineWriter_NoSteps(t *testing.T) {
	out := runStatusPipeline(t, policyPipeline("demo", "test", "prod"))
	assert.Equal(t, "Pipeline: demo   Namespace: default\n\nNo versions yet. Ship one: kardinal create bundle demo --image <ref>\n", out)
}

func TestStatusPipelineWriter_NotFound(t *testing.T) {
	var buf bytes.Buffer
	err := statusPipelineWriter(&buf, policyClient(t), "default", "missing-pipeline")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `pipeline "missing-pipeline" not found`)
}

// A step waiting for its PR: the environment's newest version is incoming,
// in words, with its PR; the summary sentence leads.
func TestStatusPipelineWriter_ActiveStep(t *testing.T) {
	step := explainStep("demo", "bundle-abc", "prod", "WaitingForMerge", "waiting for PR merge",
		time.Now().Add(-5*time.Minute))
	step.Status.PRURL = "https://github.com/org/repo/pull/42"
	b := explainBundle("bundle-abc", "Promoting", time.Now().Add(-5*time.Minute))
	b.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/o/app", Tag: "1.5.0"}}
	out := runStatusPipeline(t, policyPipeline("demo", "test", "prod"), b, step)

	assert.True(t, strings.HasPrefix(out, "Pipeline: demo   Namespace: default\n1.5.0 waiting for PR merge in prod (#42)\n\n"), out)
	assert.Equal(t, []map[string]string{
		{"ENVIRONMENT": "test", "RUNNING": "-", "INCOMING": "-", "STATE": "-", "SINCE": "-", "PR": "-"},
		{"ENVIRONMENT": "prod", "RUNNING": "-", "INCOMING": "1.5.0 (bundle-abc)", "STATE": "Waiting for PR merge", "SINCE": "-",
			"PR": "https://github.com/org/repo/pull/42"},
	}, statusRows(t, out), out)
	assertNoRawStates(t, out)
}

// C09b-cli-18: a gate blocks when it is an instance of the Bundle waiting at
// that environment. Gate instances carry the bundle label; the step is created
// only once every gate passes (pkg/graph/builder.go requiredGates).
func TestStatusPipelineWriter_BlockingGate(t *testing.T) {
	recent := time.Now().Add(-time.Hour)
	gate := explainGateInstance("demo", "bundle-abc", "prod", "no-weekend-deploys", "!schedule.isWeekend",
		false, true, "!schedule.isWeekend = false")
	out := runStatusPipeline(t,
		policyPipeline("demo", "uat", "prod"),
		explainBundle("bundle-abc", "Promoting", recent),
		explainStep("demo", "bundle-abc", "uat", "Verified", "", recent),
		gate,
	)

	rows := statusRows(t, out)
	require.Len(t, rows, 2, out)
	assert.Equal(t, "Held by gate no-weekend-deploys", rows[1]["STATE"], out)
	assert.Equal(t, "bundle-abc", rows[1]["INCOMING"], out)
	section := blockingGates(out)
	require.NotEmpty(t, section, out)
	assert.Contains(t, section, "no-weekend-deploys")
	assert.NotContains(t, section, "bundle-abc-prod-no-weekend-deploys", "rows use the gate name")
	assert.Contains(t, section, "prod")
	assert.Contains(t, section, "!schedule.isWeekend = false")
}

// GATE-MESSAGE-01: the REASON of a gate its expression blocks is the gate's
// spec.message, whole, although it is longer than the 35 characters a reason
// is cut to. A gate whose message does not explain the block (an evaluation
// error) shows its reason as before.
func TestStatusPipelineWriter_BlockingGateMessage(t *testing.T) {
	recent := time.Now().Add(-time.Hour)
	const msg = "Production deployments are blocked on weekends"
	weekend := explainGateInstance("demo", "bundle-abc", "prod", "no-weekend-deploys", "!schedule.isWeekend",
		false, true, msg+" (!schedule.isWeekend = false; version 1.2.0)")
	weekend.Spec.Message = msg
	broken := explainGateInstance("demo", "bundle-abc", "prod", "broken", "metrics.x.value > 1",
		false, true, "CEL evaluation error: no such key: x")
	broken.Spec.Message = "x must be above 1"
	out := runStatusPipeline(t,
		policyPipeline("demo", "uat", "prod"),
		explainBundle("bundle-abc", "Promoting", recent),
		explainStep("demo", "bundle-abc", "uat", "Verified", "", recent),
		weekend, broken,
	)

	gateSection := blockingGates(out)
	require.NotEmpty(t, gateSection, out)
	assert.Contains(t, gateSection, msg+"  ", "the whole message is the REASON")
	assert.NotContains(t, gateSection, "version 1.2.0", "the CEL detail is left to kardinal explain")
	assert.Contains(t, gateSection, "CEL evaluation error: no such ke...", "cut to 35 characters")
	assert.NotContains(t, gateSection, "x must be above 1")
}

// E2E-R18: a not-ready gate blocks only once the Bundle has reached its
// environment, every upstream environment Verified for that Bundle. A soak
// gate on prod does not block a Bundle still health checking in test.
func TestStatusPipelineWriter_BlockingOnlyWhenReached(t *testing.T) {
	old, recent := time.Now().Add(-2*time.Hour), time.Now().Add(-10*time.Minute)
	soak := func() *v1alpha1.PolicyGate {
		return explainGateInstance("demo", "b2", "prod", "require-uat-soak", "upstream.uat.soakMinutes >= 30",
			false, true, "upstream.uat.soakMinutes >= 30 = false")
	}
	postDeploy := func() *v1alpha1.PolicyGate {
		g := soak()
		g.Spec.When = "post-deploy" //nolint:staticcheck // SA1019: when has no effect (#1323)
		return g
	}
	// prodStep is the prod step waiting on the soak gate, the way the Graph
	// builder lists gate instances in spec.requiredGates.
	prodStep := func(state string) *v1alpha1.PromotionStep {
		s := explainStep("demo", "b2", "prod", state, "", recent)
		s.Spec.RequiredGates = []string{soak().Name}
		return s
	}
	upstream := func() []sigs_client.Object {
		return []sigs_client.Object{explainBundle("b2", "Promoting", recent),
			explainStep("demo", "b2", "test", "Verified", "", recent),
			explainStep("demo", "b2", "uat", "Verified", "", recent)}
	}
	tests := []struct {
		name         string
		objs         []sigs_client.Object
		wantBlocking bool
	}{
		{
			// checkRequiredGates keeps the step Pending, before git.
			name:         "gate holds the Pending prod step",
			objs:         append(upstream(), prodStep("Pending"), soak()),
			wantBlocking: true,
		},
		{
			name:         "gate holds the new prod step",
			objs:         append(upstream(), prodStep(""), soak()),
			wantBlocking: true,
		},
		{
			name: "gate after the prod step started",
			objs: append(upstream(), prodStep("Promoting"), soak()),
		},
		{
			// #1323: spec.when has no effect.
			name:         "post-deploy gate holds the Pending prod step",
			objs:         append(upstream(), prodStep("Pending"), postDeploy()),
			wantBlocking: true,
		},
		{
			// j6-supersede.log: 7k9rk HealthChecking in test, the prod soak
			// gate listed as blocking.
			name: "bundle health checking in test",
			objs: []sigs_client.Object{
				explainBundle("b1", "Superseded", old), explainBundle("b2", "Promoting", recent),
				explainStep("demo", "b1", "test", "Verified", "", old),
				explainStep("demo", "b1", "uat", "Verified", "", old),
				explainStep("demo", "b1", "prod", "Verified", "", old),
				explainStep("demo", "b2", "test", "HealthChecking", "", recent),
				soak(),
			},
		},
		{
			name: "bundle Verified in test, uat not started",
			objs: []sigs_client.Object{
				explainBundle("b2", "Promoting", recent),
				explainStep("demo", "b2", "test", "Verified", "", recent),
				soak(),
			},
		},
		{
			name: "bundle promoting in uat",
			objs: []sigs_client.Object{
				explainBundle("b2", "Promoting", recent),
				explainStep("demo", "b2", "test", "Verified", "", recent),
				explainStep("demo", "b2", "uat", "Promoting", "", recent),
				soak(),
			},
		},
		{
			name: "bundle Verified in test and uat",
			objs: []sigs_client.Object{
				explainBundle("b1", "Superseded", old), explainBundle("b2", "Promoting", recent),
				explainStep("demo", "b1", "prod", "Verified", "", old),
				explainStep("demo", "b2", "test", "Verified", "", recent),
				explainStep("demo", "b2", "uat", "Verified", "", recent),
				soak(),
			},
			wantBlocking: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := runStatusPipeline(t, append(tt.objs, policyPipeline("demo", "test", "uat", "prod"))...)
			if !tt.wantBlocking {
				assert.Empty(t, blockingGates(out), out)
				assert.NotContains(t, out, "require-uat-soak", out)
				return
			}
			section := blockingGates(out)
			require.NotEmpty(t, section, out)
			assert.Contains(t, section, "require-uat-soak")
			assert.Contains(t, section, "prod")
			assert.Equal(t, "Held by gate require-uat-soak", statusRows(t, out)[2]["STATE"], out)
		})
	}
}

// C09b-cli-18: gates of another Bundle, templates and passing gates are not
// reported as blocking.
func TestStatusPipelineWriter_NotBlocking(t *testing.T) {
	old := time.Now().Add(-3 * time.Hour)
	recent := time.Now().Add(-time.Hour)
	template := policyGate("no-weekend-deploys", "default", "prod", "!schedule.isWeekend")
	template.Labels["kardinal.io/pipeline"] = "demo"
	cases := []struct {
		name string
		objs []sigs_client.Object
	}{
		{
			name: "stale gate of an older bundle",
			objs: []sigs_client.Object{
				explainBundle("demo-old", "Failed", old),
				explainBundle("demo-new", "Verified", recent),
				explainStep("demo", "demo-old", "prod", "Failed", "", old),
				explainStep("demo", "demo-new", "prod", "Verified", "", recent),
				explainGateInstance("demo", "demo-old", "prod", "no-weekend-deploys", "!schedule.isWeekend",
					false, true, "weekend"),
			},
		},
		{
			name: "template without a bundle label",
			objs: []sigs_client.Object{
				explainBundle("b1", "Promoting", recent),
				explainStep("demo", "b1", "prod", "Promoting", "", recent),
				template,
			},
		},
		{
			name: "step already created",
			objs: []sigs_client.Object{
				explainBundle("b1", "Promoting", recent),
				explainStep("demo", "b1", "prod", "Promoting", "", recent),
				explainGateInstance("demo", "b1", "prod", "g", "true", false, false, ""),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := append([]sigs_client.Object{policyPipeline("demo", "test", "prod")}, tc.objs...)
			out := runStatusPipeline(t, objs...)
			assert.Empty(t, blockingGates(out), out)
		})
	}
}

// P1: status reads the Pipeline's status.environments and summary as the
// controller wrote them, and computes nothing itself when they are there:
// the steps, which say something else here, are not read. -o json prints the
// same data with the API's state values.
func TestStatusPipelineWriter_FromStatusEnvironments(t *testing.T) {
	since := metav1.NewTime(time.Now().Add(-90 * time.Minute).Truncate(time.Second))
	p := policyPipeline("demo", "test", "prod")
	p.Status.Summary = "1.5.0 live in test; held by gate no-weekend in prod"
	p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{
		{Name: "test", Bundle: "b2", Version: "1.5.0", State: "Verified", Since: &since},
		{Name: "prod", Bundle: "b1", Version: "1.4.0", IncomingBundle: "b2", IncomingVersion: "1.5.0",
			State: "Pending", BlockedBy: []string{"no-weekend"}, Since: &since},
	}
	gate := explainGateInstance("demo", "b2", "prod", "no-weekend", "!schedule.isWeekend", false, true,
		"No deploys on weekends (!schedule.isWeekend = false)")
	gate.Spec.Message = "No deploys on weekends"
	other := explainGateInstance("demo", "b1", "prod", "no-weekend", "!schedule.isWeekend", false, true, "old")
	objs := []sigs_client.Object{p, gate, other,
		explainBundle("b1", "Verified", since.Time), explainBundle("b2", "Promoting", since.Time),
		explainStep("demo", "b2", "test", "Failed", "", since.Time)}

	out := runStatusPipeline(t, objs...)
	assert.True(t, strings.HasPrefix(out, "Pipeline: demo   Namespace: default\n"+p.Status.Summary+"\n\n"), out)
	assert.Equal(t, []map[string]string{
		{"ENVIRONMENT": "test", "RUNNING": "1.5.0 (b2)", "INCOMING": "-", "STATE": "Live", "SINCE": "1h", "PR": "-"},
		{"ENVIRONMENT": "prod", "RUNNING": "1.4.0 (b1)", "INCOMING": "1.5.0 (b2)", "STATE": "Held by gate no-weekend", "SINCE": "1h", "PR": "-"},
	}, statusRows(t, out), out)
	gates := parseTable(strings.SplitN(blockingGates(out), "\n", 2)[1])
	require.Len(t, gates, 1, "only the incoming Bundle's gate:\n%s", out)
	assert.Equal(t, "No deploys on weekends", gates[0]["REASON"])

	globalOutput = "json"
	t.Cleanup(func() { globalOutput = "" })
	var view pipelineStatus
	require.NoError(t, json.Unmarshal([]byte(runStatusPipeline(t, objs...)), &view))
	assert.Equal(t, "controller", view.Source)
	assert.Equal(t, p.Status.Environments, view.Environments)
	assert.Equal(t, "Pending", view.Environments[1].State, "API values")
	require.Len(t, view.BlockingGates, 1)
	view.BlockingGates[0].LastEvaluatedAt = nil
	assert.Equal(t, statusGate{Name: "no-weekend", Instance: gate.Name, Environment: "prod", Bundle: "b2",
		Expression: "!schedule.isWeekend", Message: "No deploys on weekends",
		Reason: "No deploys on weekends (!schedule.isWeekend = false)"}, view.BlockingGates[0])
}

// Without status.environments (a controller that predates them), status
// computes them with the controller's projection; SINCE is known only for a
// live environment, and -o json says the CLI computed them.
func TestStatusPipelineWriter_OlderController(t *testing.T) {
	recent := time.Now().Add(-time.Hour)
	verified := explainStep("demo", "b1", "test", "Verified", "", recent)
	verified.Status.Conditions = []metav1.Condition{{Type: "Verified", Status: metav1.ConditionTrue, LastTransitionTime: metav1.NewTime(recent)}}
	objs := []sigs_client.Object{policyPipeline("demo", "test", "prod"), explainBundle("b1", "Promoting", recent),
		verified, explainStep("demo", "b1", "prod", "HealthChecking", "", recent)}
	out := runStatusPipeline(t, objs...)
	assert.Equal(t, []map[string]string{
		{"ENVIRONMENT": "test", "RUNNING": "b1", "INCOMING": "-", "STATE": "Live", "SINCE": "1h", "PR": "-"},
		// Its change landed in prod: it runs there while its health is checked.
		{"ENVIRONMENT": "prod", "RUNNING": "b1", "INCOMING": "-", "STATE": "Checking health", "SINCE": "-", "PR": "-"},
	}, statusRows(t, out), out)
	assert.Contains(t, out, "Live in test; checking health in prod\n")

	globalOutput = "json"
	t.Cleanup(func() { globalOutput = "" })
	var view pipelineStatus
	require.NoError(t, json.Unmarshal([]byte(runStatusPipeline(t, objs...)), &view))
	assert.Equal(t, "cli", view.Source)
	require.Len(t, view.Environments, 2)
	assert.Nil(t, view.Environments[1].Since)
	assert.Empty(t, view.BlockingGates)
}

// E2E-R02, E2E-R11: status describes the environments as the UI does. A
// Superseded Bundle's gate instances never hide the newest Bundle, and a
// Bundle held at a skip-permission gate shows that gate as blocking.
func TestStatusPipelineWriter_CurrentBundle(t *testing.T) {
	old := time.Now().Add(-18 * time.Hour)
	recent := time.Now().Add(-17 * time.Minute)

	t.Run("superseded bundle", func(t *testing.T) {
		out := runStatusPipeline(t,
			policyPipeline("demo", "test", "uat", "prod"),
			explainBundle("kardinal-test-app-7qvsr", "Superseded", old),
			explainBundle("kardinal-test-app-9tptr", "Verified", recent),
			explainStep("demo", "kardinal-test-app-7qvsr", "test", "Verified", "", old),
			explainStep("demo", "kardinal-test-app-7qvsr", "uat", "Verified", "", old),
			explainGateInstance("demo", "kardinal-test-app-7qvsr", "prod", "require-uat-soak", "true", true, true, "x"),
			explainStep("demo", "kardinal-test-app-9tptr", "test", "Verified", "", recent),
			explainStep("demo", "kardinal-test-app-9tptr", "uat", "Verified", "", recent),
			explainStep("demo", "kardinal-test-app-9tptr", "prod", "Verified", "", recent),
			explainGateInstance("demo", "kardinal-test-app-9tptr", "prod", "require-uat-soak", "true", true, true, "x"),
		)
		for _, r := range statusRows(t, out) {
			assert.Equal(t, "kardinal-test-app-9tptr", r["RUNNING"], out)
			assert.Equal(t, "-", r["INCOMING"], out)
			assert.Equal(t, "Live", r["STATE"], out)
		}
		assert.Empty(t, blockingGates(out))
	})

	// The Graph creates every gate instance when the Bundle starts: b2 failed
	// at test but has a gate instance in prod that will never pass.
	t.Run("newer bundle failed upstream", func(t *testing.T) {
		out := runStatusPipeline(t,
			policyPipeline("demo", "test", "uat", "prod"),
			explainBundle("b1", "Verified", old),
			explainBundle("b2", "Failed", recent),
			explainStep("demo", "b1", "test", "Verified", "", old),
			explainStep("demo", "b1", "uat", "Verified", "", old),
			explainStep("demo", "b1", "prod", "Verified", "", old),
			explainStep("demo", "b2", "test", "Failed", "", recent),
			explainGateInstance("demo", "b2", "prod", "no-weekend-deploys", "!schedule.isWeekend", false, true,
				"!schedule.isWeekend = false"),
		)
		rows := statusRows(t, out)
		assert.Equal(t, map[string]string{"ENVIRONMENT": "test", "RUNNING": "b1", "INCOMING": "b2", "STATE": "Failed", "SINCE": "-", "PR": "-"}, rows[0], out)
		assert.Equal(t, "Live", rows[1]["STATE"], out)
		assert.Equal(t, "Live", rows[2]["STATE"], out)
		assert.Empty(t, blockingGates(out))
		assert.NotContains(t, out, "no-weekend-deploys")
	})

	t.Run("skip-permission gate", func(t *testing.T) {
		skip := explainGateInstance("demo", "gapa-e2e2-wbptb", "prod", "gapa-allow-stage-skip",
			`bundle.version == "sha-9349a3f"`, false, true, `bundle.version == "sha-9349a3f" = false`)
		skip.Labels["kardinal.io/type"] = "skip-permission"
		// The bundle skips uat, so prod's upstream is test (Verified).
		skipping := explainBundle("gapa-e2e2-wbptb", "Promoting", recent)
		skipping.Spec.Intent = &v1alpha1.BundleIntent{SkipEnvironments: []string{"uat"}}
		out := runStatusPipeline(t,
			policyPipeline("demo", "test", "uat", "prod"),
			explainBundle("gapa-e2e2-gkvb2", "Verified", old),
			skipping,
			explainStep("demo", "gapa-e2e2-gkvb2", "prod", "Verified", "", old),
			explainStep("demo", "gapa-e2e2-wbptb", "test", "Verified", "", recent),
			skip,
			explainGateInstance("demo", "gapa-e2e2-wbptb", "prod", "gapa-predeploy-freeze", "true", true, true, "ok"),
		)
		gateSection := blockingGates(out)
		require.NotEmpty(t, gateSection, out)
		assert.Contains(t, gateSection, "gapa-allow-stage-skip")
		assert.NotContains(t, gateSection, "gapa-predeploy-freeze", "a passing gate does not block")
	})
}

// Every step state reads as words in STATE.
func TestStatusPipelineWriter_StepStates(t *testing.T) {
	for _, state := range []string{"Verified", "Failed", "AbortedByAlarm", "RollingBack", "HealthChecking", "Promoting"} {
		t.Run(state, func(t *testing.T) {
			out := runStatusPipeline(t,
				policyPipeline("demo", "test", "prod"),
				explainBundle("b1", "Promoting", time.Now().Add(-time.Hour)),
				explainStep("demo", "b1", "prod", state, "", time.Now().Add(-time.Hour)),
			)
			assert.Equal(t, stepLabel(state), statusRows(t, out)[1]["STATE"], out)
			assertNoRawStates(t, out)
		})
	}
}

// C09b-cli-19: the summary reads the version from --controller-namespace,
// counts Available and Promoting Bundles as active, and names Degraded
// Pipelines.
func TestStatusSummary(t *testing.T) {
	pipe := func(ns, name, phase string) *v1alpha1.Pipeline {
		p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
		p.Status.Phase = phase
		return p
	}
	bundle := func(name, phase string) *v1alpha1.Bundle {
		b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
		b.Status.Phase = phase
		return b
	}
	c := doctorClient(
		versionConfigMap("kardinal", "v0.6.0"),
		pipe("team-a", "web", "Ready"),
		pipe("team-b", "api", "Degraded"),
		bundle("b1", "Available"), bundle("b2", "Promoting"),
		bundle("b3", "Verified"), bundle("b4", "Superseded"),
	)

	var buf bytes.Buffer
	require.NoError(t, statusSummaryFn(&buf, c, "kardinal"))
	out := buf.String()
	assert.Contains(t, out, "Controller:  v0.6.0\n")
	assert.Contains(t, out, "Pipelines:   2 (1 degraded: team-b/api)\n")
	assert.Contains(t, out, "Bundles:     4 (2 active)\n")
	assert.Contains(t, out, "Warning: 1 pipeline(s) Degraded")

	buf.Reset()
	require.NoError(t, statusSummaryFn(&buf, c, defaultControllerNamespace))
	assert.Contains(t, buf.String(), "Controller:  unknown\n")
}

func TestStatusSummary_ListError(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(rootScheme).WithInterceptorFuncs(interceptor.Funcs{
		List: func(_ context.Context, _ sigs_client.WithWatch, list sigs_client.ObjectList, _ ...sigs_client.ListOption) error {
			if _, ok := list.(*v1alpha1.BundleList); ok {
				return errors.New("bundles.kardinal.io is forbidden")
			}
			return nil
		},
	}).Build()

	var buf bytes.Buffer
	err := statusSummaryFn(&buf, c, "kardinal")
	require.Error(t, err)
	assert.Equal(t, "list bundles: bundles.kardinal.io is forbidden", err.Error())
}

// TestStatusPipelineWriter_PipelineNotices (P4): kardinal status shows the
// Pipeline's own problems first, before any promotion: an ERROR for
// Ready=False (GitCredentialMissing with the fix command) and a WARNING for
// an unlabelled git Secret that still works.
//
// Covers PIPE-GITCRED-02.
func TestStatusPipelineWriter_PipelineNotices(t *testing.T) {
	p := policyPipeline("demo", "test", "prod")
	p.Status.Conditions = []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionFalse, Reason: "GitCredentialMissing",
			Message: "git Secret t not found in namespace default, so git has no token for https://x; create it: kubectl create secret generic t"},
		{Type: "SecretReferenceable", Status: metav1.ConditionFalse, Reason: "SecretNotReferenceable",
			Message: "git Secret t is not labeled kardinal.io/referenceable=true; label it (kubectl label secret t -n default kardinal.io/referenceable=true)"},
	}
	out := runStatusPipeline(t, p)
	assert.Contains(t, out, "ERROR: pipeline demo is not Ready (GitCredentialMissing): git Secret t not found in namespace default")
	assert.Contains(t, out, "WARNING: pipeline demo: git Secret t is not labeled kardinal.io/referenceable=true")
	assert.Less(t, strings.Index(out, "ERROR:"), strings.Index(out, "No versions yet."), out)

	ok := policyPipeline("demo", "test", "prod")
	ok.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Valid"}}
	assert.NotContains(t, runStatusPipeline(t, ok), "ERROR:")
}

// TestStatusPipelineWriter_RegionAndWide (C4 QA): REGION shows where each
// environment runs when one of them says (here the kubeconfig of a remote
// cluster), and -o wide adds the step each newest Bundle is on.
//
// Covers CLI-STYLE-01.
func TestStatusPipelineWriter_RegionAndWide(t *testing.T) {
	recent := time.Now().Add(-time.Hour)
	p := policyPipeline("demo", "test", "prod")
	p.Spec.Environments[1].Health.KubeconfigSecretRef = &v1alpha1.KubeconfigSecretRef{Name: "prod-eu-kubeconfig"}
	step := explainStep("demo", "b1", "prod", "WaitingForMerge", "", recent)
	step.Status.Steps = []v1alpha1.StepStatus{{Name: "open-pr", State: "Completed"}, {Name: "wait-for-merge", State: "InProgress"}}
	objs := []sigs_client.Object{p, explainBundle("b1", "Promoting", recent), explainStep("demo", "b1", "test", "Verified", "", recent), step}

	rows := statusRows(t, runStatusPipeline(t, objs...))
	require.Len(t, rows, 2)
	assert.Equal(t, "-", rows[0]["REGION"])
	assert.Equal(t, "prod-eu-kubeconfig", rows[1]["REGION"])
	assert.NotContains(t, rows[1], "ACTIVE STEP", "only with -o wide")

	globalOutput = "wide"
	t.Cleanup(func() { globalOutput = "" })
	rows = statusRows(t, runStatusPipeline(t, objs...))
	assert.Equal(t, "wait-for-merge", rows[1]["ACTIVE STEP"])

	globalOutput = ""
	plain := runStatusPipeline(t, policyPipeline("demo", "test", "prod"), explainBundle("b1", "Promoting", recent),
		explainStep("demo", "b1", "test", "Verified", "", recent))
	assert.NotContains(t, plain, "REGION", "no environment says where it runs")
}
