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
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func logsTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}

func TestLogsFollowFlag(t *testing.T) {
	// Verify that the --follow flag is registered on the logs command.
	cmd := newLogsCmd()
	followFlag := cmd.Flags().Lookup("follow")
	require.NotNil(t, followFlag, "--follow flag should be registered")
	assert.Equal(t, "f", followFlag.Shorthand, "--follow shorthand should be -f")
}

func TestAllTerminal(t *testing.T) {
	tests := []struct {
		name   string
		states []string
		want   bool
	}{
		{
			name:   "all verified",
			states: []string{"Verified", "Verified"},
			want:   true,
		},
		{
			name:   "all failed",
			states: []string{"Failed", "Failed"},
			want:   true,
		},
		{
			name:   "mixed terminal",
			states: []string{"Verified", "Failed"},
			want:   true,
		},
		{
			name:   "one promoting",
			states: []string{"Verified", "Promoting"},
			want:   false,
		},
		{
			name:   "waiting for merge",
			states: []string{"WaitingForMerge"},
			want:   false,
		},
		{
			name:   "empty",
			states: []string{},
			want:   true, // vacuously terminal
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var steps []v1alpha1.PromotionStep
			for _, state := range tt.states {
				s := v1alpha1.PromotionStep{}
				s.Status.State = state
				steps = append(steps, s)
			}
			got := allTerminal(steps)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLogsFollowExitsOnTerminal(t *testing.T) {
	// Build a fake client with a single PromotionStep in Verified state.
	scheme := logsTestScheme(t)
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ps",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "my-pipeline"},
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "my-pipeline",
			BundleName:   "my-bundle",
			Environment:  "test",
		},
		Status: v1alpha1.PromotionStepStatus{
			State: "Verified",
			Steps: []v1alpha1.StepStatus{
				{Name: "git-clone", State: "Success", Message: "cloned ok", DurationMs: 1200},
			},
		},
	}

	client := sigs_client.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ps).
		WithStatusSubresource(ps).WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()

	var buf bytes.Buffer
	ctx := context.Background()

	err := logsFollowFn(ctx, &buf, client, "default", "my-pipeline", "", "")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "All steps reached terminal state.", "should exit when all steps are terminal")
	assert.True(t, strings.Contains(out, "git-clone") || strings.Contains(out, "Following"), "should output some content")
}

func TestLogsStaticOutput(t *testing.T) {
	// Verify that static (non-follow) output still works correctly.
	scheme := logsTestScheme(t)
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ps-static",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "static-pipeline"},
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "static-pipeline",
			BundleName:   "static-bundle",
			Environment:  "prod",
		},
		Status: v1alpha1.PromotionStepStatus{
			State:   "Verified",
			Message: "all done",
		},
	}

	client := sigs_client.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ps).WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		Build()

	var buf bytes.Buffer
	err := logsFn(&buf, client, "default", "static-pipeline", "", "")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "static-pipeline/prod")
	assert.Contains(t, out, "Verified")
	assert.Contains(t, out, "all done")
}

// TestLogsStepTable checks the per-step table static output prints from
// status.steps: aligned STEP/STATE/DURATION/MESSAGE columns, durations in
// seconds with one decimal, "-" for a step with no duration, and no table at
// all when status.steps is empty (#974, #1318).
func TestLogsStepTable(t *testing.T) {
	tests := []struct {
		name  string
		steps []v1alpha1.StepStatus
		want  [][]string // fields of each table row after the header and rule; nil means no table
	}{
		{
			name: "durations and an unfinished step",
			steps: []v1alpha1.StepStatus{
				{Name: "git-clone", State: "Completed", Message: "cloned", DurationMs: 1200},
				{Name: "open-pr", State: "Pending"},
			},
			want: [][]string{
				{"git-clone", "Completed", "1.2s", "cloned"},
				{"open-pr", "Pending", "-"},
			},
		},
		{
			name:  "nil steps prints no steps block",
			steps: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			step := explainStep("demo", "b1", "prod", "Verified", "", policyTestNow)
			step.Status.Steps = tt.steps
			var buf bytes.Buffer
			require.NoError(t, logsFn(&buf, policyClient(t, step), "default", "demo", "", ""))
			out := buf.String()
			require.Contains(t, out, "=== demo/prod (b1) [Verified] ===")

			if tt.want == nil {
				assert.NotContains(t, out, "steps:")
				assert.NotContains(t, out, "DURATION")
				return
			}

			_, table, found := strings.Cut(out, "  steps:\n")
			require.True(t, found, out)
			lines := strings.Split(strings.TrimRight(table, "\n"), "\n")
			require.Len(t, lines, 2+len(tt.want), out)
			assert.Equal(t, []string{"STEP", "STATE", "DURATION", "MESSAGE"}, strings.Fields(lines[0]))
			assert.Equal(t, []string{"----", "-----", "--------", "-------"}, strings.Fields(lines[1]))
			durCol := strings.Index(lines[0], "DURATION")
			for i, row := range tt.want {
				line := lines[2+i]
				assert.Equal(t, row, strings.Fields(line))
				assert.Equal(t, durCol, strings.Index(line, " "+row[2])+1,
					"DURATION column is aligned in %q", line)
			}
		})
	}
}

// C09b-cli-17: --follow prints each state change once: a step with sub-steps
// prints its transition, and a terminal step next to a running one does not
// repeat on every poll.
func TestLogsFollow_StateChangesPrintedOnce(t *testing.T) {
	created := policyTestNow.Add(-time.Hour)
	verified := explainStep("demo", "b1", "test", "Verified", "", created)
	verified.Status.Steps = []v1alpha1.StepStatus{
		{Name: "git-clone", State: "Completed", DurationMs: 1200},
		{Name: "health-check", State: "Completed", DurationMs: 3000},
	}
	var out bytes.Buffer
	require.NoError(t, logsFollowFn(context.Background(), &out, policyClient(t, verified), "default", "demo", "", ""))
	assert.Contains(t, out.String(), "[demo/test] git-clone")
	assert.Contains(t, out.String(), "[demo/test] → Verified\n")

	bare := explainStep("demo", "b1", "uat", "Verified", "", created)
	running := explainStep("demo", "b1", "prod", "Promoting", "", created)
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	out.Reset()
	require.NoError(t, logsFollowFn(ctx, &out, policyClient(t, bare, running), "default", "demo", "", ""))
	assert.Equal(t, 1, strings.Count(out.String(), "[demo/uat] → Verified"), out.String())
	assert.Equal(t, 1, strings.Count(out.String(), "[demo/prod] → Promoting"), out.String())
}

// RollingBack is terminal: the reconciler stops there, so --follow exits.
func TestLogsFollow_RollingBackIsTerminal(t *testing.T) {
	step := explainStep("demo", "b1", "prod", "RollingBack", "health check failed", policyTestNow.Add(-time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out bytes.Buffer
	require.NoError(t, logsFollowFn(ctx, &out, policyClient(t, step), "default", "demo", "", ""))
	assert.True(t, strings.HasSuffix(out.String(), "[demo/prod] → RollingBack\nAll steps reached terminal state.\n"), out.String())
}

// --follow waits for the Bundle, not only for the steps that exist: the next
// environment's PromotionStep is created only once its upstream is Verified
// and its gates pass, so a Verified test step of a Promoting Bundle is not the
// end of the promotion.
func TestLogsFollow_WaitsForInFlightBundle(t *testing.T) {
	step := explainStep("demo", "b1", "test", "Verified", "", policyTestNow.Add(-time.Hour))
	bundle := func(name, phase string) *v1alpha1.Bundle {
		return &v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       v1alpha1.BundleSpec{Pipeline: "demo", Type: "image"},
			Status:     v1alpha1.BundleStatus{Phase: phase},
		}
	}
	follow := func(t *testing.T, timeout time.Duration, env, bundleName string, objs ...ctrlclient.Object) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		var out bytes.Buffer
		require.NoError(t, logsFollowFn(ctx, &out, policyClient(t, objs...), "default", "demo", env, bundleName))
		return out.String()
	}
	const done = "All steps reached terminal state.\n"

	out := follow(t, 2500*time.Millisecond, "", "", step, bundle("b1", "Promoting"))
	assert.True(t, strings.HasSuffix(out, "\nStopped.\n"), "a Promoting Bundle keeps --follow running:\n%s", out)
	assert.NotContains(t, out, done)
	assert.Equal(t, 1, strings.Count(out, "[demo/test] → Verified\n"), out)

	for _, tc := range []struct {
		name, env, bundle string
		objs              []ctrlclient.Object
	}{
		{"the Bundle is Verified", "", "", []ctrlclient.Object{step, bundle("b1", "Verified")}},
		{"the Bundle is Failed", "", "", []ctrlclient.Object{step, bundle("b1", "Failed")}},
		{"--env follows one environment", "test", "", []ctrlclient.Object{step, bundle("b1", "Promoting")}},
		{"--bundle ignores other Bundles", "", "b1", []ctrlclient.Object{step, bundle("b1", "Verified"), bundle("b2", "Promoting")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := follow(t, 10*time.Second, tc.env, tc.bundle, tc.objs...)
			assert.True(t, strings.HasSuffix(out, done), out)
		})
	}
}

// C09b-cli-25: truncation counts runes, so it never splits a UTF-8 character.
func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"abcdefghijk", 10, "abcdefg..."},
		{"déploiement échoué", 10, "déploie..."},
		{"日本語のメッセージです", 6, "日本語..."},
	}
	for _, tc := range cases {
		got := truncateRunes(tc.in, tc.n)
		assert.Equal(t, tc.want, got)
		assert.True(t, utf8.ValidString(got))
	}
}
