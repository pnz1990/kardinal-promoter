// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// explainStep is a PromotionStep shaped like the ones the Graph creates.
func explainStep(pipeline, bundle, env, state, msg string, created time.Time) *v1alpha1.PromotionStep {
	return &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name: pipeline + "-" + bundle + "-" + env, Namespace: "default",
			CreationTimestamp: metav1.NewTime(created),
			Labels: map[string]string{
				"kardinal.io/pipeline": pipeline, "kardinal.io/bundle": bundle, "kardinal.io/environment": env,
			},
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: pipeline, BundleName: bundle, Environment: env, StepType: "pr-review",
		},
		Status: v1alpha1.PromotionStepStatus{State: state, Message: msg},
	}
}

// explainGateInstance is a PolicyGate instance labelled the way the Graph
// builder labels them (pkg/graph/builder.go buildPolicyGateNode). evaluated
// sets lastEvaluatedAt.
func explainGateInstance(pipeline, bundle, env, template, expr string, ready, evaluated bool, reason string) *v1alpha1.PolicyGate {
	g := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name: bundle + "-" + env + "-" + template, Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline": pipeline, "kardinal.io/bundle": bundle, "kardinal.io/environment": env,
				"kardinal.io/gate-template": template, "kardinal.io/gate-name": template,
				"kardinal.io/scope": "org", "kardinal.io/applies-to": env,
			},
		},
		Spec:   v1alpha1.PolicyGateSpec{Expression: expr},
		Status: v1alpha1.PolicyGateStatus{Ready: ready, Reason: reason},
	}
	if evaluated {
		now := metav1.NewTime(policyTestNow)
		g.Status.LastEvaluatedAt = &now
	}
	return g
}

func runExplain(t *testing.T, c sigs_client.Client, pipeline, env string, color bool) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	err := explainOnce(&buf, c, "default", pipeline, env, color)
	return buf.String(), err
}

// C09a-cli-01 / E2E-03: explain shows the gate instances the Graph created for
// the active Bundle, including org gates whose template lives in
// platform-policies, with the controller's evaluation.
func TestExplain_ShowsAppliedGateInstances(t *testing.T) {
	created := policyTestNow.Add(-time.Hour)
	template := policyGate("no-weekend-deploys", "platform-policies", "prod", "!schedule.isWeekend",
		"kardinal.io/scope", "org")
	template.Status = v1alpha1.PolicyGateStatus{
		Reason: "valid CEL syntax (not yet evaluated — awaiting bundle promotion)",
	}
	c := policyClient(t,
		policyPipeline("demo", "test", "uat", "prod"),
		template,
		explainStep("demo", "b1", "prod", "Pending", "", created),
		explainGateInstance("demo", "b1", "prod", "no-weekend-deploys", "!schedule.isWeekend", false, true,
			"bundle.version=sha-abc1234: !schedule.isWeekend = false"),
		explainGateInstance("demo", "b1", "prod", "change-freeze", "!changewindow['q4'].isBlocked()", true, true,
			"bundle.version=sha-abc1234: !changewindow['q4'].isBlocked() = true"),
	)

	out, err := runExplain(t, c, "demo", "prod", false)
	require.NoError(t, err)
	assert.Contains(t, out, "ENVIRONMENT")
	assert.Contains(t, out, "EXPRESSION")
	lines := strings.Split(out, "\n")
	var weekend, freeze string
	for _, l := range lines {
		switch {
		case strings.Contains(l, "no-weekend-deploys"):
			weekend = l
		case strings.Contains(l, "change-freeze"):
			freeze = l
		}
	}
	require.NotEmpty(t, weekend, "the org gate blocking prod must be shown:\n%s", out)
	assert.Contains(t, weekend, "PolicyGate")
	assert.Contains(t, weekend, "Block")
	assert.Contains(t, weekend, "!schedule.isWeekend = false", "the instance's evaluation must be shown")
	assert.NotContains(t, out, "b1-prod-no-weekend-deploys", "rows use the gate name, not the instance name")
	require.NotEmpty(t, freeze)
	assert.Contains(t, freeze, "Pass")
	assert.NotContains(t, out, "awaiting bundle promotion", "templates are not rows")
}

func TestExplain_Rows(t *testing.T) {
	old := policyTestNow.Add(-2 * time.Hour)
	recent := policyTestNow.Add(-time.Hour)
	cases := []struct {
		name     string
		objs     []sigs_client.Object
		env      string
		contains []string
		excludes []string
	}{
		{
			name: "env filter",
			objs: []sigs_client.Object{
				explainStep("demo", "b1", "test", "Verified", "", recent),
				explainStep("demo", "b1", "prod", "WaitingForMerge", "PR #7 open", recent),
			},
			env:      "prod",
			contains: []string{"prod", "WaitingForMerge", "PR #7 open"},
			excludes: []string{"Verified", "test "},
		},
		{
			name: "unevaluated gate is Pending",
			objs: []sigs_client.Object{
				explainStep("demo", "b1", "uat", "Verified", "", recent),
				explainGateInstance("demo", "b1", "prod", "uat-soak", "upstream.uat.soakMinutes >= 30", false, false, ""),
			},
			contains: []string{"uat-soak", "upstream.uat.soakMinutes >= 30", "Pending"},
		},
		{
			name: "only the active bundle",
			objs: []sigs_client.Object{
				explainStep("demo", "old", "prod", "Verified", "old-step", old),
				explainGateInstance("demo", "old", "prod", "old-gate", "true", true, true, "old"),
				explainStep("demo", "new", "prod", "Promoting", "new-step", recent),
				explainGateInstance("demo", "new", "prod", "new-gate", "true", false, false, ""),
			},
			contains: []string{"new-step", "new-gate", "Promoting"},
			excludes: []string{"old-step", "old-gate"},
		},
		{
			name: "rolling back beats an older verified bundle",
			objs: []sigs_client.Object{
				explainStep("demo", "b2", "prod", "Verified", "b2 verified", recent),
				explainStep("demo", "b1", "prod", "RollingBack", "rolling back", old),
			},
			contains: []string{"RollingBack", "rolling back"},
			excludes: []string{"b2 verified"},
		},
		{
			name: "gates before the step is created",
			objs: []sigs_client.Object{
				explainStep("demo", "b1", "uat", "Promoting", "", recent),
				explainGateInstance("demo", "b1", "prod", "no-weekend-deploys", "!schedule.isWeekend", false, true,
					"!schedule.isWeekend = false"),
			},
			env:      "prod",
			contains: []string{"no-weekend-deploys", "Block"},
		},
		{
			name: "a newer bundle held at a gate beats an older verified one",
			objs: []sigs_client.Object{
				explainStep("demo", "b1", "prod", "Verified", "b1 verified", old),
				explainStep("demo", "b2", "uat", "Verified", "", recent),
				explainGateInstance("demo", "b2", "prod", "no-weekend-deploys", "!schedule.isWeekend", false, true,
					"!schedule.isWeekend = false"),
			},
			env:      "prod",
			contains: []string{"no-weekend-deploys", "Block", "!schedule.isWeekend = false"},
			excludes: []string{"b1 verified"},
		},
		{
			name: "a newer bundle that has not reached the environment",
			objs: []sigs_client.Object{
				explainStep("demo", "b1", "prod", "Verified", "b1 verified", old),
				explainStep("demo", "b2", "uat", "Promoting", "", recent),
				explainGateInstance("demo", "b2", "prod", "no-weekend-deploys", "!schedule.isWeekend", false, true,
					"!schedule.isWeekend = false"),
			},
			env:      "prod",
			contains: []string{"b1 verified"},
			excludes: []string{"no-weekend-deploys"},
		},
		{
			name: "gate of another pipeline is ignored",
			objs: []sigs_client.Object{
				explainStep("demo", "b1", "prod", "Promoting", "", recent),
				explainGateInstance("other", "b1", "prod", "other-gate", "true", false, true, "x"),
			},
			contains: []string{"Promoting"},
			excludes: []string{"other-gate"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := append([]sigs_client.Object{policyPipeline("demo", "test", "uat", "prod")}, tc.objs...)
			out, err := runExplain(t, policyClient(t, objs...), "demo", tc.env, false)
			require.NoError(t, err)
			for _, s := range tc.contains {
				assert.Contains(t, out, s)
			}
			for _, s := range tc.excludes {
				assert.NotContains(t, out, s)
			}
		})
	}
}

// C09a-cli-14: an unknown env or pipeline is an error; a valid idle env is not.
func TestExplain_Errors(t *testing.T) {
	c := policyClient(t, policyPipeline("demo", "test", "uat", "prod"))

	_, err := runExplain(t, c, "demo", "prdo", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `environment "prdo" not found in pipeline "demo" (environments: test, uat, prod)`)

	_, err = runExplain(t, c, "missing", "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `pipeline "missing" not found`)

	out, err := runExplain(t, c, "demo", "prod", false)
	require.NoError(t, err)
	assert.Equal(t, "No promotion for \"prod\" in pipeline \"demo\" yet\n", out)

	out, err = runExplain(t, c, "demo", "", false)
	require.NoError(t, err)
	assert.Equal(t, "No promotion for pipeline \"demo\" yet\n", out)
}

// C09a-cli-13: only the STATE cell is colored, and every step state has a color.
func TestExplain_ColorOnlyStateColumn(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	created := policyTestNow.Add(-time.Hour)
	c := policyClient(t,
		policyPipeline("demo", "test", "uat", "prod"),
		explainStep("demo", "b1", "prod", "Promoting", "push Failed once, retrying", created),
		explainStep("demo", "b1", "uat", "Verified", "", created),
		explainGateInstance("demo", "b1", "prod", "gate", "bundle.type != 'Block'", true, true, "Pass"),
	)

	out, err := runExplain(t, c, "demo", "", true)
	require.NoError(t, err)
	assert.Contains(t, out, ansiYellow+"Promoting"+ansiReset)
	assert.Contains(t, out, ansiGreen+"Verified"+ansiReset)
	assert.Contains(t, out, ansiGreen+"Pass"+ansiReset)
	assert.Equal(t, 3, strings.Count(out, ansiReset), "exactly the three STATE cells are colored:\n%q", out)
	assert.Contains(t, out, "push Failed once, retrying")
	assert.Contains(t, out, "bundle.type != 'Block'")

	plain, err := runExplain(t, c, "demo", "", false)
	require.NoError(t, err)
	assert.NotContains(t, plain, "\033[")
	// Colouring only inserts escape codes; the columns stay aligned.
	stripped := strings.NewReplacer(ansiYellow, "", ansiGreen, "", ansiReset, "").Replace(out)
	assert.Equal(t, plain, stripped)
}

// watchBuffer cancels the watch after it has rendered n times.
type watchBuffer struct {
	buf    bytes.Buffer
	n      int
	cancel context.CancelFunc
}

func (b *watchBuffer) Write(p []byte) (int, error) {
	n, err := b.buf.Write(p)
	if strings.Count(b.buf.String(), "(watching") >= b.n {
		b.cancel()
	}
	return n, err
}

// C09a-cli-15: --watch keeps polling after an error and does not write
// clear-screen escapes when the output is not a terminal.
func TestExplainWatch_ContinuesAfterErrorAndNoClearOnPipe(t *testing.T) {
	c := policyClient(t) // no pipeline: every render is an error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &watchBuffer{n: 3, cancel: cancel}

	require.NoError(t, explainWatch(ctx, out, c, "default", "demo", "", false, time.Millisecond))

	got := out.buf.String()
	assert.Equal(t, 3, strings.Count(got, `error: pipeline "demo" not found`), got)
	assert.NotContains(t, got, "\033[H\033[2J", "no clear-screen on a pipe")
}
