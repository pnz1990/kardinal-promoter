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

// explainBundle is a Bundle of pipeline demo in phase, created at created.
func explainBundle(name, phase string, created time.Time) *v1alpha1.Bundle {
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: metav1.NewTime(created)},
		Spec:       v1alpha1.BundleSpec{Pipeline: "demo", Type: "image"},
	}
	b.Status.Phase = phase
	return b
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
		explainBundle("b1", "Promoting", created),
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
				explainBundle("b1", "Promoting", recent),
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
				explainBundle("b1", "Promoting", recent),
				explainStep("demo", "b1", "uat", "Verified", "", recent),
				explainGateInstance("demo", "b1", "prod", "uat-soak", "upstream.uat.soakMinutes >= 30", false, false, ""),
			},
			contains: []string{"uat-soak", "upstream.uat.soakMinutes >= 30", "Pending"},
		},
		{
			name: "only the current bundle",
			objs: []sigs_client.Object{
				explainBundle("old", "Verified", old),
				explainBundle("new", "Promoting", recent),
				explainStep("demo", "old", "prod", "Verified", "old-step", old),
				explainGateInstance("demo", "old", "prod", "old-gate", "true", true, true, "old"),
				explainStep("demo", "new", "prod", "Promoting", "new-step", recent),
				explainGateInstance("demo", "new", "prod", "new-gate", "true", false, false, ""),
			},
			contains: []string{"new-step", "new-gate", "Promoting"},
			excludes: []string{"old-step", "old-gate"},
		},
		{
			name: "the newest bundle beats an older rolling back one",
			objs: []sigs_client.Object{
				explainBundle("b1", "Failed", old),
				explainBundle("b2", "Verified", recent),
				explainStep("demo", "b2", "prod", "Verified", "b2 verified", recent),
				explainStep("demo", "b1", "prod", "RollingBack", "rolling back", old),
			},
			contains: []string{"b2 verified"},
			excludes: []string{"RollingBack", "rolling back"},
		},
		{
			name: "a failed bundle is current until a newer one exists",
			objs: []sigs_client.Object{
				explainBundle("b1", "Verified", old),
				explainBundle("b2", "Failed", recent),
				explainStep("demo", "b1", "prod", "Verified", "b1 verified", old),
				explainStep("demo", "b2", "prod", "Failed", "PR #8 closed", recent),
			},
			contains: []string{"Failed", "PR #8 closed"},
			excludes: []string{"b1 verified"},
		},
		{
			name: "gates before the step is created",
			objs: []sigs_client.Object{
				explainBundle("b1", "Promoting", recent),
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
				explainBundle("b1", "Verified", old),
				explainBundle("b2", "Promoting", recent),
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
			name: "a newer bundle shows its gates before it reaches the environment",
			objs: []sigs_client.Object{
				explainBundle("b1", "Verified", old),
				explainBundle("b2", "Promoting", recent),
				explainStep("demo", "b1", "prod", "Verified", "b1 verified", old),
				explainStep("demo", "b2", "uat", "Promoting", "", recent),
				explainGateInstance("demo", "b2", "prod", "no-weekend-deploys", "!schedule.isWeekend", false, true,
					"!schedule.isWeekend = false"),
			},
			env:      "prod",
			contains: []string{"no-weekend-deploys", "Block"},
			excludes: []string{"b1 verified"},
		},
		{
			name: "an environment without the newest bundle keeps the older one",
			objs: []sigs_client.Object{
				explainBundle("b1", "Verified", old),
				explainBundle("b2", "Promoting", recent),
				explainStep("demo", "b1", "test", "Verified", "b1 verified", old),
				explainStep("demo", "b2", "uat", "Promoting", "b2 promoting", recent),
			},
			contains: []string{"b1 verified", "b2 promoting"},
		},
		{
			name: "steps and gates of a deleted bundle are ignored",
			objs: []sigs_client.Object{
				explainBundle("b1", "Verified", old),
				explainStep("demo", "b1", "prod", "Verified", "b1 verified", old),
				explainStep("demo", "gone", "prod", "Promoting", "gone step", recent),
				explainGateInstance("demo", "gone", "prod", "gone-gate", "true", false, true, "x"),
			},
			contains: []string{"b1 verified"},
			excludes: []string{"gone step", "gone-gate"},
		},
		{
			name: "gate of another pipeline is ignored",
			objs: []sigs_client.Object{
				explainBundle("b1", "Promoting", recent),
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

// E2E-R02: once the newest Bundle is Verified everywhere, explain shows its
// steps, not the gate instances of an older Superseded Bundle that never
// reached prod.
func TestExplain_SupersededBundleIsNotCurrent(t *testing.T) {
	old := policyTestNow.Add(-18 * time.Hour)
	recent := policyTestNow.Add(-17 * time.Minute)
	objs := []sigs_client.Object{
		policyPipeline("demo", "test", "uat", "prod"),
		explainBundle("kardinal-test-app-7qvsr", "Superseded", old),
		explainBundle("kardinal-test-app-9tptr", "Verified", recent),
		explainStep("demo", "kardinal-test-app-7qvsr", "test", "Verified", "7qvsr test", old),
		explainStep("demo", "kardinal-test-app-7qvsr", "uat", "Verified", "7qvsr uat", old),
		explainStep("demo", "kardinal-test-app-9tptr", "test", "Verified", "9tptr test", recent),
		explainStep("demo", "kardinal-test-app-9tptr", "uat", "Verified", "9tptr uat", recent),
		explainStep("demo", "kardinal-test-app-9tptr", "prod", "Verified", "9tptr prod", recent),
	}
	for _, b := range []string{"kardinal-test-app-7qvsr", "kardinal-test-app-9tptr"} {
		version := map[string]string{"kardinal-test-app-7qvsr": "main", "kardinal-test-app-9tptr": "sha-9349a3f"}[b]
		objs = append(objs,
			explainGateInstance("demo", b, "prod", "no-weekend-deploys", "!schedule.isWeekend", true, true,
				"bundle.version="+version+": !schedule.isWeekend = true"),
			explainGateInstance("demo", b, "prod", "require-uat-soak", "bundle.upstreamSoakMinutes >= 30", true, true,
				"bundle.version="+version+": bundle.upstreamSoakMinutes >= 30 = true"))
	}
	c := policyClient(t, objs...)

	out, err := runExplain(t, c, "demo", "prod", false)
	require.NoError(t, err)
	assert.Contains(t, out, "9tptr prod")
	assert.Contains(t, out, "bundle.version=sha-9349a3f")
	assert.NotContains(t, out, "bundle.version=main", "the Superseded Bundle's gates are not shown:\n%s", out)

	out, err = runExplain(t, c, "demo", "", false)
	require.NoError(t, err)
	for _, want := range []string{"9tptr test", "9tptr uat", "9tptr prod"} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, "7qvsr")
}

// E2E-R11: a Bundle that skips an environment waits at a skip-permission gate
// instance. explain shows that instance, not the Step of the older Bundle
// already in the environment, and lists the gate that blocks first.
func TestExplain_WaitingBundleSkipGate(t *testing.T) {
	old := policyTestNow.Add(-5 * time.Minute)
	recent := policyTestNow.Add(-time.Minute)
	skip := explainGateInstance("demo", "gapa-e2e2-wbptb", "prod", "gapa-allow-stage-skip",
		`bundle.version == "sha-9349a3f"`, false, true, `bundle.version=main: bundle.version == "sha-9349a3f" = false`)
	skip.Labels["kardinal.io/type"] = "skip-permission"
	skip.Annotations = map[string]string{"kardinal.io/skipped-environments": "uat"}
	c := policyClient(t,
		policyPipeline("demo", "test", "uat", "prod"),
		explainBundle("gapa-e2e2-gkvb2", "Verified", old),
		explainBundle("gapa-e2e2-wbptb", "Promoting", recent),
		explainStep("demo", "gapa-e2e2-gkvb2", "prod", "Verified", "gkvb2 health check passed", old),
		explainGateInstance("demo", "gapa-e2e2-gkvb2", "prod", "gapa-predeploy-freeze",
			`changewindow.isAllowed("gapa-freeze")`, true, true, "gkvb2 freeze"),
		explainStep("demo", "gapa-e2e2-wbptb", "test", "Verified", "", recent),
		skip,
		explainGateInstance("demo", "gapa-e2e2-wbptb", "prod", "a-predeploy-freeze",
			`changewindow.isAllowed("gapa-freeze")`, true, true, "wbptb freeze"),
	)

	out, err := runExplain(t, c, "demo", "prod", false)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 3, out)
	assert.Contains(t, lines[1], "gapa-allow-stage-skip", "the not-ready gate comes first:\n%s", out)
	assert.Contains(t, lines[1], "Block")
	assert.Contains(t, lines[1], `bundle.version == "sha-9349a3f" = false`)
	assert.Contains(t, lines[2], "a-predeploy-freeze")
	assert.Contains(t, lines[2], "Pass")
	assert.NotContains(t, out, "gkvb2")
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
		explainBundle("b1", "Promoting", created),
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
