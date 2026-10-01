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

// policy_test.go — tests for policy list, simulate and test, and for validate's
// CEL check. Gate selection and evaluation must match the controller's.
package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// policyTestNow is a Wednesday. Every simulate test resolves --time from it.
var policyTestNow = time.Date(2026, time.September, 30, 13, 37, 0, 0, time.UTC)

func buildPolicyScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

// policyPipeline is a Pipeline named name in "default" with the given envs,
// in order.
func policyPipeline(name string, envs ...string) *v1alpha1.Pipeline {
	p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
	p.Spec.Git.URL = "https://github.com/pnz1990/kardinal-demo"
	for _, e := range envs {
		p.Spec.Environments = append(p.Spec.Environments, v1alpha1.EnvironmentSpec{Name: e})
	}
	return p
}

// policyGate is a PolicyGate template. labels are key, value pairs.
func policyGate(name, ns, appliesTo, expr string, labels ...string) *v1alpha1.PolicyGate {
	g := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{}},
		Spec:       v1alpha1.PolicyGateSpec{Expression: expr, Message: "Blocked by " + name},
	}
	if appliesTo != "" {
		g.Labels["kardinal.io/applies-to"] = appliesTo
	}
	for i := 0; i+1 < len(labels); i += 2 {
		g.Labels[labels[i]] = labels[i+1]
	}
	return g
}

func policyClient(t *testing.T, objs ...sigs_client.Object) sigs_client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(buildPolicyScheme(t)).WithObjects(objs...).Build()
}

// runSimulate runs policy simulate for pipeline "demo" in "default" with
// defaults filled in (env prod, now = policyTestNow).
func runSimulate(t *testing.T, c sigs_client.Client, opts simulateOptions) (string, error) {
	t.Helper()
	if opts.Pipeline == "" {
		opts.Pipeline = "demo"
	}
	if opts.Env == "" {
		opts.Env = "prod"
	}
	if opts.Now.IsZero() {
		opts.Now = policyTestNow
	}
	var out bytes.Buffer
	err := policySimulateFn(&out, c, "default", opts)
	return out.String(), err
}

// ─── --time parsing (C09b-cli-09) ───────────────────────────────────────────

func TestParseSimulatedTime(t *testing.T) {
	at := func(day, hour, minute int) time.Time {
		return time.Date(2026, time.October, day, hour, minute, 0, 0, time.UTC)
	}
	tests := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{in: "", want: policyTestNow},
		{in: "Tuesday 10am", want: at(6, 10, 0)},
		{in: "Saturday 3pm", want: at(3, 15, 0)},
		{in: "3pm Saturday", want: at(3, 15, 0)},
		{in: "sat 15:30 UTC", want: at(3, 15, 30)},
		{in: "Friday 3:30pm", want: at(2, 15, 30)},
		{in: "  SATURDAY   15  ", want: at(3, 15, 0)},
		{in: "Monday 12am", want: at(5, 0, 0)},
		{in: "mon 12pm", want: at(5, 12, 0)},
		{in: "Thursday 0", want: at(1, 0, 0)},
		// Today counts, even when the hour has passed.
		{in: "Wednesday 9", want: time.Date(2026, time.September, 30, 9, 0, 0, 0, time.UTC)},
		{in: "2026-10-03T15:00:00Z", want: at(3, 15, 0)},
		{in: "2026-10-03T17:00:00+02:00", want: at(3, 15, 0)},
		{in: "not a time", wantErr: true},
		{in: "Saturday", wantErr: true},
		{in: "3pm", wantErr: true},
		{in: "Saturday 3pm Sunday", wantErr: true},
		{in: "Saturday 3pm 4pm", wantErr: true},
		{in: "Saturday 24", wantErr: true},
		{in: "Saturday 25pm", wantErr: true},
		{in: "Saturday 13pm", wantErr: true},
		{in: "Saturday 0pm", wantErr: true},
		{in: "Saturday 10:5", wantErr: true},
		{in: "Saturday 10:60", wantErr: true},
		{in: "Saturday -3", wantErr: true},
		{in: "Saturday 3pm PST", wantErr: true},
		{in: "Satur 3pm", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseSimulatedTime(tt.in, policyTestNow)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "invalid --time")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// The result depends only on the input and the reference day, never on the
// hour the command runs at.
func TestParseSimulatedTime_IndependentOfCurrentHour(t *testing.T) {
	want := time.Date(2026, time.October, 6, 10, 0, 0, 0, time.UTC)
	for h := 0; h < 24; h++ {
		now := time.Date(2026, time.September, 30, h, 59, 59, 0, time.UTC)
		got, err := parseSimulatedTime("Tuesday 10am", now)
		require.NoError(t, err)
		assert.Equal(t, want, got, "now hour %d", h)
	}
}

func TestPolicySimulate_InvalidInputIsAnError(t *testing.T) {
	c := policyClient(t, policyPipeline("demo", "test", "uat", "prod"))
	tests := []struct {
		name    string
		opts    simulateOptions
		wantErr string
	}{
		{name: "bad time", opts: simulateOptions{Time: "someday"}, wantErr: "invalid --time"},
		{name: "negative soak", opts: simulateOptions{SoakMinutes: -1}, wantErr: "--soak-minutes"},
		{name: "unknown pipeline", opts: simulateOptions{Pipeline: "nope"}, wantErr: `pipeline "nope" not found`},
		{name: "unknown env", opts: simulateOptions{Env: "staging"}, wantErr: `environment "staging" not found`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := runSimulate(t, c, tt.opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.NotContains(t, out, "RESULT:")
		})
	}
}

// ─── gate selection (C09b-cli-07, C12-examples-demo-05) ─────────────────────

// Simulate reports exactly the gates the controller's Graph attaches to the
// environment: the translator's namespaces and the builder's applies-to match.
func TestPolicySimulate_SelectsControllerGates(t *testing.T) {
	const blocks = "false"
	objs := []sigs_client.Object{
		policyPipeline("demo", "test", "uat", "prod"),
		// #483: org gate outside the caller's namespace.
		policyGate("org-prod", "platform-policies", "prod", blocks, "kardinal.io/scope", "org"),
		policyGate("team-prod", "default", "prod", blocks),
		policyGate("no-applies-to", "platform-policies", "", blocks),
		policyGate("uat-only", "platform-policies", "uat", blocks),
		policyGate("skip-perm", "platform-policies", "prod", blocks, "kardinal.io/type", "skip-permission"),
		policyGate("other-namespace", "team-b", "prod", blocks),
		// A Graph-stamped instance is not a template.
		policyGate("demo-old-org-prod-prod", "default", "prod", blocks,
			"kardinal.io/gate-template", "org-prod", "kardinal.io/bundle", "demo-old"),
	}
	out, err := runSimulate(t, policyClient(t, objs...), simulateOptions{Time: "Tuesday 10am"})
	require.NoError(t, err)

	assert.Contains(t, out, "RESULT: BLOCKED")
	// A comma list in applies-to is not a valid label value (C13a-docs-03),
	// so it has no case here.
	for _, want := range []string{"org-prod", "team-prod"} {
		assert.Contains(t, out, "Blocked by: "+want+"\n")
		assert.Contains(t, out, want+":")
	}
	for _, unwanted := range []string{"no-applies-to", "uat-only", "skip-perm", "other-namespace", "demo-old"} {
		assert.NotContains(t, out, unwanted)
	}
}

func TestPolicySimulate_PolicyNamespaces(t *testing.T) {
	custom := policyPipeline("demo", "test", "prod")
	custom.Spec.PolicyNamespaces = []string{"custom-policies"}
	gates := []sigs_client.Object{
		policyGate("platform-gate", "platform-policies", "prod", "false"),
		policyGate("custom-gate", "custom-policies", "prod", "false"),
		policyGate("flag-gate", "flag-policies", "prod", "false"),
	}
	tests := []struct {
		name     string
		pipeline *v1alpha1.Pipeline
		flag     []string
		want     []string
	}{
		{name: "controller default", pipeline: policyPipeline("demo", "test", "prod"), want: []string{"platform-gate"}},
		{name: "controller flag", pipeline: policyPipeline("demo", "test", "prod"), flag: []string{"flag-policies"}, want: []string{"flag-gate"}},
		// spec.policyNamespaces adds to the controller's namespaces; it does
		// not replace them, so a Pipeline cannot opt out of org policy.
		{name: "pipeline spec adds to default", pipeline: custom, want: []string{"platform-gate", "custom-gate"}},
		{name: "pipeline spec adds to flag", pipeline: custom, flag: []string{"flag-policies"}, want: []string{"flag-gate", "custom-gate"}},
	}
	all := []string{"platform-gate", "custom-gate", "flag-gate"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := policyClient(t, append([]sigs_client.Object{tt.pipeline}, gates...)...)
			out, err := runSimulate(t, c, simulateOptions{Time: "Tuesday 10am", PolicyNamespaces: tt.flag})
			require.NoError(t, err)
			for _, name := range all {
				if containsString(tt.want, name) {
					assert.Contains(t, out, name+":")
				} else {
					assert.NotContains(t, out, name)
				}
			}
		})
	}
}

// The github-demo example gates must be the ones simulate reports for prod.
func TestPolicySimulate_GithubDemoExample(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "github-demo", "pipeline.yaml"))
	require.NoError(t, err)
	gates, err := parsePolicyGateYAML(data)
	require.NoError(t, err)
	require.NotEmpty(t, gates)

	objs := []sigs_client.Object{policyPipeline("demo", "test", "uat", "prod")}
	for i := range gates {
		gates[i].Namespace = "default"
		objs = append(objs, &gates[i])
	}
	c := policyClient(t, objs...)
	out, err := runSimulate(t, c, simulateOptions{Time: "Saturday 2pm", SoakMinutes: 45})
	require.NoError(t, err)
	t.Log(out)
	for _, g := range gates {
		assert.Contains(t, out, g.Name+":", "example gate %s is not attached to prod", g.Name)
	}
	// The output documented in examples/github-demo/README.md.
	assert.Contains(t, out, "RESULT: BLOCKED\nBlocked by: no-weekend-deploys\n"+
		"Message: \"Block deployments on Saturday and Sunday UTC\"\nNext window: Monday 00:00 UTC\n")
	assert.Regexp(t, `uat-soak-gate: +PASS +\(upstream\.uat\.soakMinutes >= 30 = true\)`, out)

	out, err = runSimulate(t, c, simulateOptions{Time: "Tuesday 10am", SoakMinutes: 45})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(out, "RESULT: PASS\n"), out)
}

// Without --soak-minutes every upstream environment has soaked 0 minutes: a
// soak gate blocks on its expression, it does not fail to evaluate. The
// pre-#1246 simulate had no upstream map, so upstream.uat.soakMinutes was
// "no such attribute(s): upstream".
func TestPolicySimulate_NoSoakMinutesIsZeroSoak(t *testing.T) {
	tests := []struct {
		name, expr string
	}{
		{name: "upstream map", expr: "upstream.uat.soakMinutes >= 30"},
		{name: "bundle field", expr: "bundle.upstreamSoakMinutes >= 30"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := policyClient(t, policyPipeline("demo", "test", "uat", "prod"),
				policyGate("require-uat-soak", "default", "prod", tt.expr))
			out, err := runSimulate(t, c, simulateOptions{Time: "Tuesday 10am"})
			require.NoError(t, err)
			assert.Contains(t, out, "RESULT: BLOCKED")
			assert.Regexp(t, `require-uat-soak: +BLOCK +\(`+regexp.QuoteMeta(tt.expr)+` = false\)`, out)
			assert.NotContains(t, out, "no such attribute")
			assert.NotContains(t, out, celEvalErrorPrefix)
		})
	}
}

func TestPolicySimulate_NoGates(t *testing.T) {
	out, err := runSimulate(t, policyClient(t, policyPipeline("demo", "test", "prod")), simulateOptions{Time: "Saturday 3pm"})
	require.NoError(t, err)
	assert.Contains(t, out, "RESULT: PASS")
	assert.Contains(t, out, `No PolicyGates found for pipeline "demo" environment "prod"`)
}

// ─── evaluation (C09b-cli-08, E2E-06) ───────────────────────────────────────

// Simulate evaluates with the controller's reconciler: its CEL environment
// and its context, read from the cluster.
func TestPolicySimulate_ControllerEnvironmentAndContext(t *testing.T) {
	freeze := &v1alpha1.ChangeWindow{
		ObjectMeta: metav1.ObjectMeta{Name: "holiday-freeze"},
		Spec: v1alpha1.ChangeWindowSpec{
			Type:  "blackout",
			Start: metav1.NewTime(time.Date(2026, time.October, 3, 0, 0, 0, 0, time.UTC)),
			End:   metav1.NewTime(time.Date(2026, time.October, 4, 0, 0, 0, 0, time.UTC)),
		},
	}
	// Fresh at policyTestNow (the real time of the run), not at the simulated
	// --time days later: simulate judges staleness at the real time (#1302).
	// The gate is an org gate, so it reads the MetricChecks of
	// platform-policies, not the Pipeline namespace's.
	fresh := metav1.NewTime(policyTestNow.Add(5 * time.Minute))
	errorRate := &v1alpha1.MetricCheck{
		ObjectMeta: metav1.ObjectMeta{Name: "error-rate", Namespace: "platform-policies"},
		Status:     v1alpha1.MetricCheckStatus{Result: "Pass", LastValue: "0.01", ValidUntil: &fresh},
	}
	teamErrorRate := &v1alpha1.MetricCheck{
		ObjectMeta: metav1.ObjectMeta{Name: "error-rate", Namespace: "default"},
		Status:     v1alpha1.MetricCheckStatus{Result: "Fail", LastValue: "0.9", ValidUntil: &fresh},
	}
	expired := metav1.NewTime(policyTestNow.Add(-time.Minute))
	staleRate := &v1alpha1.MetricCheck{
		ObjectMeta: metav1.ObjectMeta{Name: "stale-rate", Namespace: "platform-policies"},
		Status:     v1alpha1.MetricCheckStatus{Result: "Pass", LastValue: "0.01", ValidUntil: &expired},
	}
	tests := []struct {
		expr string
		time string
		soak int64
		pass bool
	}{
		{expr: `changewindow.isAllowed("holiday-freeze")`, time: "Tuesday 10am", pass: true},
		{expr: `changewindow.isAllowed("holiday-freeze")`, time: "Saturday 3pm", pass: false},
		{expr: `!changewindow.isBlocked("holiday-freeze")`, time: "Saturday 3pm", pass: false},
		{expr: `!changewindow["holiday-freeze"]`, time: "Tuesday 10am", pass: true},
		{expr: `"PROD".lowerAscii() == environment.name`, time: "Tuesday 10am", pass: true},
		{expr: `json.unmarshal("{\"ready\": true}").ready == true`, time: "Tuesday 10am", pass: true},
		{expr: `metrics["error-rate"].result == "Pass"`, time: "Tuesday 10am", pass: true},
		{expr: `double(metrics["error-rate"].value) < 0.05`, time: "Saturday 3pm", pass: true},
		{expr: `metrics["stale-rate"].result == "Pass"`, time: "Tuesday 10am", pass: false},
		{expr: `metrics["stale-rate"].stale`, time: "Tuesday 10am", pass: true},
		{expr: `upstream.uat.soakMinutes >= 30`, time: "Tuesday 10am", soak: 60, pass: true},
		{expr: `upstream.uat.soakMinutes >= 30`, time: "Tuesday 10am", soak: 10, pass: false},
		{expr: `bundle.upstreamSoakMinutes >= 30`, time: "Tuesday 10am", soak: 30, pass: true},
		{expr: `upstream.test.recentSuccessCount >= 1`, time: "Tuesday 10am", pass: true},
		{expr: `bundle.type == "image"`, time: "Tuesday 10am", pass: true},
		{expr: `schedule.dayOfWeek == "Tuesday" && schedule.hour == 10`, time: "Tuesday 10am", pass: true},
		{expr: `!schedule.isWeekend`, time: "Saturday 3pm", pass: false},
	}
	for _, tt := range tests {
		t.Run(tt.expr+"@"+tt.time, func(t *testing.T) {
			c := policyClient(t, policyPipeline("demo", "test", "uat", "prod"), freeze, errorRate, teamErrorRate,
				staleRate, policyGate("gate", "platform-policies", "prod", tt.expr))
			out, err := runSimulate(t, c, simulateOptions{Time: tt.time, SoakMinutes: tt.soak})
			require.NoError(t, err)
			assert.NotContains(t, out, "compile error")
			assert.NotContains(t, out, "evaluation error")
			if tt.pass {
				assert.Contains(t, out, "RESULT: PASS", out)
			} else {
				assert.Contains(t, out, "RESULT: BLOCKED", out)
			}
		})
	}
}

// An org gate reads metrics.* from its org policy namespace and a team gate
// from the Pipeline namespace, as in the controller, also when both
// namespaces have a MetricCheck of the same name.
func TestPolicySimulate_GateMetricsNamespace(t *testing.T) {
	fresh := metav1.NewTime(policyTestNow.Add(5 * time.Minute))
	metric := func(ns, result string) *v1alpha1.MetricCheck {
		return &v1alpha1.MetricCheck{
			ObjectMeta: metav1.ObjectMeta{Name: "error-rate", Namespace: ns},
			Status:     v1alpha1.MetricCheckStatus{Result: result, LastValue: "0.01", ValidUntil: &fresh},
		}
	}
	const expr = `metrics["error-rate"].result == "Pass"`
	tests := []struct {
		name     string
		gateNS   string
		policyNS []string
		pass     bool
	}{
		{name: "org gate", gateNS: "org-policies", policyNS: []string{"org-policies"}, pass: true},
		{name: "team gate", gateNS: "default", policyNS: []string{"org-policies"}, pass: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := policyClient(t, policyPipeline("demo", "test", "prod"),
				metric("org-policies", "Pass"), metric("default", "Fail"),
				policyGate("error-budget", tt.gateNS, "prod", expr))
			out, err := runSimulate(t, c, simulateOptions{Time: "Tuesday 10am", PolicyNamespaces: tt.policyNS})
			require.NoError(t, err)
			if tt.pass {
				assert.Contains(t, out, "RESULT: PASS", out)
			} else {
				assert.Contains(t, out, "RESULT: BLOCKED", out)
			}
		})
	}
}

// A blocked gate shows the next hour it would pass with the same inputs, and
// no window when time alone cannot unblock it.
func TestPolicySimulate_NextWindow(t *testing.T) {
	tests := []struct {
		name string
		expr string
		time string
		want string // "" means no Next window line
	}{
		{name: "weekend", expr: "!schedule.isWeekend", time: "Saturday 3pm", want: "Next window: Monday 00:00 UTC"},
		{name: "business hours", expr: "schedule.hour >= 9 && schedule.hour < 17", time: "Tuesday 8pm", want: "Next window: Wednesday 09:00 UTC"},
		{name: "soak (E2E-06)", expr: "bundle.upstreamSoakMinutes >= 30", time: "Saturday 3pm"},
		{name: "never", expr: "false", time: "Tuesday 10am"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := policyClient(t, policyPipeline("demo", "test", "prod"), policyGate("gate", "platform-policies", "prod", tt.expr))
			out, err := runSimulate(t, c, simulateOptions{Time: tt.time})
			require.NoError(t, err)
			assert.Contains(t, out, "RESULT: BLOCKED")
			assert.Contains(t, out, `Message: "Blocked by gate"`)
			if tt.want == "" {
				assert.NotContains(t, out, "Next window")
			} else {
				assert.Contains(t, out, tt.want)
			}
		})
	}
}

// Simulate runs the controller's reconciler, which patches status and writes
// AuditEvents; none of it may reach the cluster.
func TestPolicySimulate_NeverWritesToCluster(t *testing.T) {
	writeErr := errors.New("simulate wrote to the cluster")
	base := policyClient(t, policyPipeline("demo", "test", "prod"),
		policyGate("no-weekend-deploys", "platform-policies", "prod", "!schedule.isWeekend"))
	c := interceptor.NewClient(base.(sigs_client.WithWatch), interceptor.Funcs{
		Create: func(context.Context, sigs_client.WithWatch, sigs_client.Object, ...sigs_client.CreateOption) error {
			return writeErr
		},
		Update: func(context.Context, sigs_client.WithWatch, sigs_client.Object, ...sigs_client.UpdateOption) error {
			return writeErr
		},
		Patch: func(context.Context, sigs_client.WithWatch, sigs_client.Object, sigs_client.Patch, ...sigs_client.PatchOption) error {
			return writeErr
		},
		Delete: func(context.Context, sigs_client.WithWatch, sigs_client.Object, ...sigs_client.DeleteOption) error {
			return writeErr
		},
		SubResourceCreate: func(context.Context, sigs_client.Client, string, sigs_client.Object, sigs_client.Object, ...sigs_client.SubResourceCreateOption) error {
			return writeErr
		},
		SubResourceUpdate: func(context.Context, sigs_client.Client, string, sigs_client.Object, ...sigs_client.SubResourceUpdateOption) error {
			return writeErr
		},
		SubResourcePatch: func(context.Context, sigs_client.Client, string, sigs_client.Object, sigs_client.Patch, ...sigs_client.SubResourcePatchOption) error {
			return writeErr
		},
	})
	out, err := runSimulate(t, c, simulateOptions{Time: "Saturday 3pm"})
	require.NoError(t, err)
	assert.Contains(t, out, "RESULT: BLOCKED")
	assert.Contains(t, out, "Next window: Monday 00:00 UTC")

	var gates v1alpha1.PolicyGateList
	require.NoError(t, base.List(context.Background(), &gates))
	assert.Len(t, gates.Items, 1, "no gate instance was created in the cluster")
	var audit v1alpha1.AuditEventList
	require.NoError(t, base.List(context.Background(), &audit))
	assert.Empty(t, audit.Items)
}

// The CLI reads the reconciler's status.reason; these are the prefixes it
// depends on (policy_eval.go).
func TestGateEvaluator_ReasonPrefixes(t *testing.T) {
	ctx := context.Background()

	msg, invalid, err := celSyntaxCheck(ctx, "schedule.hour >>> 9")
	require.NoError(t, err)
	assert.True(t, invalid, "reason prefix %q not found", celSyntaxErrorPrefix)
	assert.NotEmpty(t, msg)

	tmpl := policyGate("tmpl", "default", "prod", "true")
	ev, err := newGateEvaluator(newSimulationClient(buildPolicyScheme(t), nil, tmpl))
	require.NoError(t, err)
	_, reason, err := ev.evaluate(ctx, sigs_client.ObjectKeyFromObject(tmpl), policyTestNow)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(reason, celSyntaxValidPrefix), reason)

	_, reason, err = localGateCheck(ctx, *policyGate("g", "default", "prod", `metrics["x"].result == "Pass"`), "prod", policyTestNow)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(reason, celEvalErrorPrefix), reason)
}

// ─── policy test (C09b-cli-10, C09b-cli-11) ─────────────────────────────────

func writePolicyFile(t *testing.T, content string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "gates.yaml")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o600))
	return f
}

func gateYAML(name, expr string) string {
	return "apiVersion: kardinal.io/v1alpha1\nkind: PolicyGate\nmetadata:\n  name: " + name +
		"\n  labels:\n    kardinal.io/applies-to: prod\nspec:\n  expression: '" + expr + "'\n"
}

func TestPolicyTest(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr bool
		want    []string
		notWant []string
	}{
		{
			name:    "passes",
			content: gateYAML("allow", "true"),
			want:    []string{"Syntax: valid", "Result: PASS", "All gates valid and pass current context (1 gate(s))"},
		},
		{
			name:    "blocks",
			content: gateYAML("deny", "false"),
			want:    []string{"Result: FAIL", "Some gates would BLOCK"},
		},
		{
			name:    "environment from applies-to",
			content: gateYAML("env", `environment.name == "prod"`),
			want:    []string{"Result: PASS"},
		},
		{
			name:    "needs cluster data is not a syntax error",
			content: gateYAML("error-rate", `metrics["error-rate"].result == "Pass"`),
			want:    []string{"Syntax: valid", "Result: UNKNOWN", "need cluster context"},
			notWant: []string{"CEL syntax errors found", "INVALID"},
		},
		{
			name:    "controller functions",
			content: gateYAML("lower", `"A".lowerAscii() == "a"`),
			want:    []string{"Result: PASS"},
		},
		{
			// An unknown ChangeWindow blocks in the controller (docs/policy-gates.md),
			// and offline there are no ChangeWindows, so the result needs the cluster.
			name:    "changewindow needs cluster data",
			content: gateYAML("cw", `changewindow.isAllowed("holiday-freeze")`),
			want:    []string{"Syntax: valid", "Result: UNKNOWN", `unknown ChangeWindow "holiday-freeze"`, "need cluster context"},
			notWant: []string{"Result: PASS", "INVALID"},
		},
		{
			name:    "syntax error",
			content: gateYAML("bad", "schedule.hour >>> 9"),
			wantErr: true,
			want:    []string{"Syntax: INVALID", "CEL syntax errors found"},
		},
		{
			name:    "undeclared function",
			content: gateYAML("bad", "nope()"),
			wantErr: true,
			want:    []string{"Syntax: INVALID"},
		},
		{
			name:    "multiple documents",
			content: "---\n" + gateYAML("one", "true") + "---\n" + gateYAML("two", "false") + "---\n",
			want:    []string{`PolicyGate "one"`, `PolicyGate "two"`, "(2 gate(s))"},
		},
		{
			name:    "no expression",
			content: "kind: PolicyGate\nmetadata:\n  name: empty\n",
			want:    []string{"Syntax: SKIP"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := policyTestFn(&out, writePolicyFile(t, tt.content), policyTestNow)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err, out.String())
			}
			for _, w := range tt.want {
				assert.Contains(t, out.String(), w)
			}
			for _, w := range tt.notWant {
				assert.NotContains(t, out.String(), w)
			}
		})
	}
}

// C09a-cli-22: the weekend gate's result depends only on the injected time.
func TestPolicyTest_WeekendGateAtFixedTimes(t *testing.T) {
	file := writePolicyFile(t, gateYAML("no-weekend-deploys", "!schedule.isWeekend"))
	for _, tc := range []struct {
		now  time.Time
		want string
	}{
		{time.Date(2026, 3, 7, 15, 0, 0, 0, time.UTC), "Result: FAIL"},  // Saturday
		{time.Date(2026, 3, 10, 10, 0, 0, 0, time.UTC), "Result: PASS"}, // Tuesday
	} {
		t.Run(tc.now.Weekday().String(), func(t *testing.T) {
			var out bytes.Buffer
			require.NoError(t, policyTestFn(&out, file, tc.now))
			assert.Contains(t, out.String(), tc.want)
		})
	}
}

func TestParsePolicyGateYAML(t *testing.T) {
	quickstart, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "quickstart", "policy-gates.yaml"))
	require.NoError(t, err)
	tests := []struct {
		name    string
		data    string
		want    []string
		wantErr bool
	}{
		{name: "quickstart example", data: string(quickstart), want: []string{"no-weekend-deploys", "require-uat-soak"}},
		{name: "list", data: "apiVersion: v1\nkind: List\nitems:\n- kind: PolicyGate\n  metadata: {name: a}\n- kind: PolicyGate\n  metadata: {name: b}\n", want: []string{"a", "b"}},
		{name: "json", data: `{"kind":"PolicyGate","metadata":{"name":"j"},"spec":{"expression":"true"}}`, want: []string{"j"}},
		{name: "other kinds ignored", data: "kind: Pipeline\nmetadata: {name: p}\n---\n" + gateYAML("g", "true"), want: []string{"g"}},
		{name: "no gates", data: "kind: Pipeline\nmetadata: {name: p}\n", wantErr: true},
		{name: "not yaml", data: "a: [", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gates, err := parsePolicyGateYAML([]byte(tt.data))
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			var names []string
			for _, g := range gates {
				names = append(names, g.Name)
			}
			assert.Equal(t, tt.want, names)
		})
	}
}

// ─── policy list (C09b-cli-12) ──────────────────────────────────────────────

func TestPolicyList(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "quickstart", "policy-gates.yaml"))
	require.NoError(t, err)
	quickstart, err := parsePolicyGateYAML(data)
	require.NoError(t, err)
	require.Len(t, quickstart, 2)
	quickstart[0].Status.Reason = "valid CEL syntax (not yet evaluated — awaiting bundle promotion)"
	quickstart[1].Status.Reason = "CEL syntax error: boom"

	objs := []sigs_client.Object{
		policyPipeline("nginx-demo", "test", "uat", "prod"),
		&quickstart[0], &quickstart[1],
		policyGate("uat-team-gate", "default", "uat", "true"),
		policyGate("unattached", "default", "", "true"),
		policyGate("other-team", "team-b", "prod", "true"),
		policyGate("nginx-demo-abc-no-weekend-deploys-prod", "default", "prod", "true",
			"kardinal.io/gate-template", "no-weekend-deploys", "kardinal.io/bundle", "nginx-demo-abc"),
	}
	c := policyClient(t, objs...)

	tests := []struct {
		name     string
		pipeline string
		want     []string
		notWant  []string
	}{
		{
			name:    "all templates",
			want:    []string{"no-weekend-deploys", "require-uat-soak", "uat-team-gate", "unattached", "other-team"},
			notWant: []string{"nginx-demo-abc"},
		},
		{
			name:     "attached to pipeline",
			pipeline: "nginx-demo",
			want:     []string{"no-weekend-deploys", "require-uat-soak", "uat-team-gate"},
			notWant:  []string{"unattached", "other-team", "nginx-demo-abc"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			require.NoError(t, policyListFn(&out, c, "default", tt.pipeline, nil))
			s := out.String()
			assert.Contains(t, s, "NAME")
			assert.Contains(t, s, "CEL")
			assert.NotContains(t, s, "READY")
			for _, w := range tt.want {
				assert.Contains(t, s, w)
			}
			for _, w := range tt.notWant {
				assert.NotContains(t, s, w)
			}
		})
	}

	var out bytes.Buffer
	require.NoError(t, policyListFn(&out, c, "default", "", nil))
	rows := map[string]string{}
	for _, line := range strings.Split(out.String(), "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			rows[f[0]] = line
		}
	}
	assert.Regexp(t, `platform-policies\s+org\s+prod\s+5m\s+valid\s`, rows["no-weekend-deploys"])
	assert.Regexp(t, `platform-policies\s+org\s+prod\s+1m\s+invalid\s`, rows["require-uat-soak"])
	assert.Regexp(t, `default\s+team\s+-\s+5m\s+-\s`, rows["unattached"])

	var missing bytes.Buffer
	assert.ErrorContains(t, policyListFn(&missing, c, "default", "nope", nil), `pipeline "nope" not found`)
}

// TestPolicyList_LastEvaluated: LAST-EVALUATED is the newest evaluation of a
// template's instances, matched by the labels the Graph builder sets (name,
// scope and template namespace; the instance lives in the Pipeline's
// namespace), so an org template and a team template of the same name never
// share instances. An instance without the namespace label (from an older
// controller) is matched by scope: org in every namespace, team in its own.
func TestPolicyList_LastEvaluated(t *testing.T) {
	now := time.Now()
	// instance is labelled as an older controller labelled it, without
	// kardinal.io/gate-template-namespace.
	instance := func(name, ns, pipeline, template, scope string, ago time.Duration) *v1alpha1.PolicyGate {
		g := policyGate(name, ns, "test", "true", "kardinal.io/gate-template", template,
			"kardinal.io/scope", scope, "kardinal.io/pipeline", pipeline, "kardinal.io/bundle", pipeline+"-b")
		g.Spec.Generated = true
		at := metav1.NewTime(now.Add(-ago))
		g.Status.LastEvaluatedAt = &at
		return g
	}
	// labelled is an instance as the Graph builder labels it now.
	labelled := func(name, ns, pipeline, template, scope, templateNS string, ago time.Duration) *v1alpha1.PolicyGate {
		g := instance(name, ns, pipeline, template, scope, ago)
		g.Labels["kardinal.io/gate-template-namespace"] = templateNS
		return g
	}
	c := policyClient(t,
		policyPipeline("demo", "test"),
		policyGate("entry", "default", "test", "true", "kardinal.io/scope", "team"),
		policyGate("entry", "platform-policies", "test", "true", "kardinal.io/scope", "org"),
		policyGate("entry", "team-b", "test", "true"),
		policyGate("audit", "platform-policies", "test", "true", "kardinal.io/scope", "org"),
		policyGate("plain", "platform-policies", "test", "true"),
		policyGate("idle", "default", "test", "true"),
		// demo's instances in default: two of team entry, the org audit and
		// the unlabelled plain (scope team, as the builder sets it).
		instance("entry-default-test--demo-b", "default", "demo", "entry", "team", 150*time.Second),
		instance("entry-default-test--demo-a", "default", "demo", "entry", "team", 650*time.Second),
		instance("audit-platform-policies-test--demo-b", "default", "demo", "audit", "org", 330*time.Second),
		instance("plain-platform-policies-test--demo-b", "default", "demo", "plain", "team", 150*time.Second),
		// Another pipeline's instance of the org audit, evaluated last.
		instance("audit-platform-policies-test--other-b", "team-c", "other", "audit", "org", 90*time.Second),
		// A team template another namespace's Pipeline reads through
		// spec.policyNamespaces: only the namespace label ties its instance
		// to it. The label also keeps idle's namesake from another
		// namespace off idle.
		policyGate("shared", "team-policies", "test", "true"),
		labelled("shared-team-policies-test--demo-b", "default", "demo", "shared", "team", "team-policies", 200*time.Second),
		labelled("idle-elsewhere-test--demo-b", "default", "demo", "idle", "team", "elsewhere", 60*time.Second),
	)

	// lastEval maps name/namespace to LAST-EVALUATED, the last column (its
	// value has a space).
	lastEval := func(t *testing.T, pipeline string) map[string]string {
		t.Helper()
		var out bytes.Buffer
		require.NoError(t, policyListFn(&out, c, "default", pipeline, nil))
		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		col := strings.Index(lines[0], "LAST-EVALUATED")
		require.Positive(t, col, out.String())
		got := map[string]string{}
		for _, l := range lines[1:] {
			f := strings.Fields(l)
			got[f[0]+"/"+f[1]] = strings.TrimSpace(l[col:])
		}
		return got
	}

	assert.Equal(t, map[string]string{
		"entry/default":           "2m ago", // the newer of its two instances
		"entry/platform-policies": "-",      // org: default's entry instances are team
		"entry/team-b":            "-",      // team: no instance in its namespace
		"audit/platform-policies": "1m ago", // org: every namespace
		"plain/platform-policies": "-",      // team, and its instances are in default
		"idle/default":            "-",      // its namesake's instance is labelled elsewhere
		"shared/team-policies":    "3m ago", // labelled with its namespace
	}, lastEval(t, ""))
	assert.Equal(t, map[string]string{
		"entry/default":           "2m ago",
		"entry/platform-policies": "-",
		"audit/platform-policies": "5m ago", // demo's instance only
		"plain/platform-policies": "-",
		"idle/default":            "-",
	}, lastEval(t, "demo"))
}

// ─── validate CEL (C09b-cli-16) ─────────────────────────────────────────────

func TestValidateCELExpression(t *testing.T) {
	tests := []struct {
		expr    string
		wantErr bool
	}{
		{expr: "!schedule.isWeekend"},
		{expr: `metrics["error-rate"].result == "Pass"`},
		{expr: `changewindow.isAllowed("x")`},
		{expr: "schedule.hour >>> 9", wantErr: true},
		{expr: "(schedule.hour", wantErr: true},
		{expr: "nope()", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			err := validateCELExpression(tt.expr)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
