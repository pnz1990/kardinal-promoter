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
	out := runStatusPipeline(t, policyPipeline("demo", "test", "prod"), step)

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
		explainStep("demo", "bundle-abc", "uat", "Verified", "", recent),
		gate,
	)

	require.Contains(t, out, "Blocking Policy Gates")
	gateSection := out[strings.Index(out, "Blocking Policy Gates"):]
	assert.Contains(t, gateSection, "no-weekend-deploys")
	assert.NotContains(t, gateSection, "bundle-abc-prod-no-weekend-deploys", "rows use the gate name")
	assert.Contains(t, gateSection, "prod")
	assert.Contains(t, gateSection, "!schedule.isWeekend = false")
	assert.NotContains(t, out, "terminal state", "a Bundle held at a gate is not idle")
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
			name: "stale gate of a superseded bundle",
			objs: []sigs_client.Object{
				explainStep("demo", "demo-old", "prod", "Failed", "", old),
				explainStep("demo", "demo-new", "prod", "Verified", "", recent),
				explainGateInstance("demo", "demo-old", "prod", "no-weekend-deploys", "!schedule.isWeekend",
					false, true, "weekend"),
			},
		},
		{
			name: "template without a bundle label",
			objs: []sigs_client.Object{
				explainStep("demo", "b1", "prod", "Promoting", "", recent),
				template,
			},
		},
		{
			name: "step already created",
			objs: []sigs_client.Object{
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
		explainStep("demo", "b1", "prod", "Verified", "", old),
		explainStep("demo", "b1", "test", "Verified", "", old),
		explainStep("demo", "b2", "test", "Verified", "", recent),
		east, west,
	)

	assert.Contains(t, out, "Active bundle(s): b2\n")
	assert.Contains(t, out, "REGION")
	assert.Regexp(t, `\n  prod +eu-west-1 +Verified`, out)
	assert.Regexp(t, `\n▶ prod +us-east-1 +Promoting`, out)
	assert.Regexp(t, `\n  test +- +Verified`, out)
	assert.Equal(t, 3, strings.Count(out, "\n  prod")+strings.Count(out, "\n▶ prod")+strings.Count(out, "\n  test"),
		"b1's steps are not listed:\n%s", out)
}

func TestStatusPipelineWriter_TerminalSteps(t *testing.T) {
	out := runStatusPipeline(t,
		policyPipeline("demo", "test", "prod"),
		explainStep("demo", "b1", "prod", "Verified", "", time.Now().Add(-time.Hour)),
	)
	assert.Contains(t, out, "Verified")
	assert.Contains(t, out, "terminal state")
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
