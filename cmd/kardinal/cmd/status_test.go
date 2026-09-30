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
	"errors"
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

func TestStatusPipelineWriter_NoSteps(t *testing.T) {
	out := runStatusPipeline(t, policyPipeline("demo", "test", "prod"))
	assert.Contains(t, out, "No active promotions.")
}

func TestStatusPipelineWriter_NotFound(t *testing.T) {
	var buf bytes.Buffer
	err := statusPipelineWriter(&buf, policyClient(t), "default", "missing-pipeline")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `pipeline "missing-pipeline" not found`)
}

// An in-flight PromotionStep is shown with a ▶ marker and the active step name.
func TestStatusPipelineWriter_ActiveStep(t *testing.T) {
	step := explainStep("demo", "bundle-abc", "prod", "WaitingForMerge", "waiting for PR merge",
		time.Now().Add(-5*time.Minute))
	step.Status.PRURL = "https://github.com/org/repo/pull/42"
	step.Status.Steps = []v1alpha1.StepStatus{
		{Name: "git-clone", State: "Completed"},
		{Name: "open-pr", State: "Running"},
	}
	out := runStatusPipeline(t, policyPipeline("demo", "test", "prod"),
		explainBundle("bundle-abc", "Promoting", time.Now().Add(-5*time.Minute)), step)

	assert.Contains(t, out, "Pipeline: demo")
	assert.Contains(t, out, "Active bundle(s): bundle-abc")
	assert.Contains(t, out, "▶ prod")
	assert.Contains(t, out, "WaitingForMerge")
	assert.Contains(t, out, "open-pr")
	assert.Contains(t, out, "pull/42")
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

	_, gateSection, found := strings.Cut(out, "Blocking Policy Gates")
	require.True(t, found, out)
	assert.Contains(t, gateSection, "no-weekend-deploys")
	assert.NotContains(t, gateSection, "bundle-abc-prod-no-weekend-deploys", "rows use the gate name")
	assert.Contains(t, gateSection, "prod")
	assert.Contains(t, gateSection, "!schedule.isWeekend = false")
	assert.NotContains(t, out, "terminal state", "a Bundle held at a gate is not idle")
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
	preDeploy := func() *v1alpha1.PolicyGate {
		g := soak()
		g.Spec.When = "pre-deploy"
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
			// checkPreDeployGates keeps the step Pending, before git.
			name:         "pre-deploy gate holds the Pending prod step",
			objs:         append(upstream(), prodStep("Pending"), preDeploy()),
			wantBlocking: true,
		},
		{
			name:         "pre-deploy gate holds the new prod step",
			objs:         append(upstream(), prodStep(""), preDeploy()),
			wantBlocking: true,
		},
		{
			name: "pre-deploy gate after the prod step started",
			objs: append(upstream(), prodStep("Promoting"), preDeploy()),
		},
		{
			name: "post-deploy gate does not hold the Pending prod step",
			objs: append(upstream(), prodStep("Pending"), soak()),
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
			assert.Contains(t, out, "Active bundle(s):", out)
			assert.NotContains(t, out, "terminal state", "a bundle still to reach prod is not finished:\n%s", out)
			if !tt.wantBlocking {
				assert.NotContains(t, out, "Blocking Policy Gates", out)
				assert.NotContains(t, out, "require-uat-soak", out)
				return
			}
			_, gateSection, found := strings.Cut(out, "Blocking Policy Gates")
			require.True(t, found, out)
			assert.Contains(t, gateSection, "require-uat-soak")
			assert.Contains(t, gateSection, "prod")
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
			assert.NotContains(t, out, "Blocking Policy Gates", out)
		})
	}
}

// C09b-cli-18: only the active Bundle's steps are listed, one row per region.
func TestStatusPipelineWriter_ActiveBundleAndRegions(t *testing.T) {
	old := time.Now().Add(-3 * time.Hour)
	recent := time.Now().Add(-time.Hour)
	east := explainStep("demo", "b2", "prod", "Promoting", "", recent)
	east.Name += "-us-east-1"
	east.Spec.Region = "us-east-1"
	west := explainStep("demo", "b2", "prod", "Verified", "", recent)
	west.Name += "-eu-west-1"
	west.Spec.Region = "eu-west-1"
	out := runStatusPipeline(t,
		policyPipeline("demo", "test", "prod"),
		explainBundle("b1", "Verified", old),
		explainBundle("b2", "Promoting", recent),
		explainStep("demo", "b1", "prod", "Verified", "", old),
		explainStep("demo", "b1", "test", "Verified", "", old),
		explainStep("demo", "b2", "test", "Verified", "", recent),
		east, west,
	)

	assert.Contains(t, out, "Active bundle(s): b2\n")
	assert.Contains(t, out, "REGION")
	assert.Regexp(t, `\n  prod +eu-west-1 +b2 +Verified`, out)
	assert.Regexp(t, `\n▶ prod +us-east-1 +b2 +Promoting`, out)
	assert.Regexp(t, `\n  test +- +b2 +Verified`, out)
	assert.Equal(t, 3, strings.Count(out, "\n  prod")+strings.Count(out, "\n▶ prod")+strings.Count(out, "\n  test"),
		"b1's steps are not listed:\n%s", out)
}

// E2E-R02, E2E-R11: status describes the same current Bundle as explain. A
// Superseded Bundle's gate instances never hide the newest Bundle's steps,
// and a Bundle held at a skip-permission gate shows that gate as blocking.
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
		assert.Contains(t, out, "Active bundle(s): kardinal-test-app-9tptr\n")
		assert.Regexp(t, `\n  prod +- +kardinal-test-app-9tptr +Verified`, out)
		assert.Regexp(t, `\n  uat +- +kardinal-test-app-9tptr +Verified`, out)
		assert.Regexp(t, `\n  test +- +kardinal-test-app-9tptr +Verified`, out)
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
		assert.Contains(t, out, "Active bundle(s): b1, b2\n")
		assert.Regexp(t, `\n  test +- +b2 +Failed`, out)
		assert.Regexp(t, `\n  uat +- +b1 +Verified`, out)
		assert.Regexp(t, `\n  prod +- +b1 +Verified`, out)
		assert.NotContains(t, out, "Blocking Policy Gates")
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
		assert.Contains(t, out, "Active bundle(s): gapa-e2e2-wbptb\n")
		_, gateSection, found := strings.Cut(out, "Blocking Policy Gates")
		require.True(t, found, out)
		assert.Contains(t, gateSection, "gapa-allow-stage-skip")
		assert.NotContains(t, gateSection, "gapa-predeploy-freeze", "a passing gate does not block")
		assert.NotContains(t, out, "terminal state")
	})
}

func TestStatusPipelineWriter_TerminalSteps(t *testing.T) {
	for _, state := range []string{"Verified", "Failed", "AbortedByAlarm", "RollingBack"} {
		t.Run(state, func(t *testing.T) {
			out := runStatusPipeline(t,
				policyPipeline("demo", "test", "prod"),
				explainBundle("b1", "Promoting", time.Now().Add(-time.Hour)),
				explainStep("demo", "b1", "prod", state, "", time.Now().Add(-time.Hour)),
			)
			assert.Contains(t, out, state)
			assert.Contains(t, out, "terminal state")
		})
	}
	out := runStatusPipeline(t,
		policyPipeline("demo", "test", "prod"),
		explainBundle("b1", "Promoting", time.Now().Add(-time.Hour)),
		explainStep("demo", "b1", "prod", "HealthChecking", "", time.Now().Add(-time.Hour)),
	)
	assert.NotContains(t, out, "terminal state")
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
