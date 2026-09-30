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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func buildGetPipelinesScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}

// TestGetPipelines_WatchFlagRegistered verifies that the --watch / -w flags
// are correctly registered on the `get pipelines` command.
func TestGetPipelines_WatchFlagRegistered(t *testing.T) {
	cmd := newGetPipelinesCmd()
	f := cmd.Flags().Lookup("watch")
	require.NotNil(t, f, "--watch flag must be registered on 'get pipelines'")
	assert.Equal(t, "false", f.DefValue, "--watch must default to false")

	// Shorthand
	sf := cmd.Flags().ShorthandLookup("w")
	require.NotNil(t, sf, "-w shorthand must be registered on 'get pipelines'")
}

// TestGetSteps_WatchFlagRegistered verifies that the --watch / -w flags
// are correctly registered on the `get steps` command.
func TestGetSteps_WatchFlagRegistered(t *testing.T) {
	cmd := newGetStepsCmd()
	f := cmd.Flags().Lookup("watch")
	require.NotNil(t, f, "--watch flag must be registered on 'get steps'")
	assert.Equal(t, "false", f.DefValue, "--watch must default to false")

	sf := cmd.Flags().ShorthandLookup("w")
	require.NotNil(t, sf, "-w shorthand must be registered on 'get steps'")
}

// TestGetPipelinesOnce_TableOutput verifies that getPipelinesOnce produces
// the expected pipeline table output (same as the non-watch path).
func TestGetPipelinesOnce_TableOutput(t *testing.T) {
	s := buildGetPipelinesScheme(t)

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-app",
			Namespace: "default",
		},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "prod"},
			},
		},
	}
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-app-test",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline":    "my-app",
				"kardinal.io/environment": "test",
				"kardinal.io/bundle":      "bundle-abc",
			},
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "my-app",
			Environment:  "test",
			BundleName:   "bundle-abc",
		},
		Status: v1alpha1.PromotionStepStatus{
			State: "Verified",
		},
	}

	fc := fake.NewClientBuilder().WithScheme(s).WithObjects(pipeline, step).Build()

	var buf bytes.Buffer
	err := getPipelinesOnce(&buf, fc, "default", nil, false)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "my-app", "pipeline name must appear in output")
	// The table must have at least a header row
	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Greater(t, len(lines), 1, "output must have at least a header and one data row")
}

// C09b-cli-25 / C09b-cli-27: get steps lists the steps of the pipeline's
// active Bundles only, and says so when there are none instead of listing
// every step.
func TestGetStepsOnce(t *testing.T) {
	bundle := func(name, phase string) *v1alpha1.Bundle {
		b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
		b.Spec.Pipeline = "demo"
		b.Status.Phase = phase
		return b
	}
	created := policyTestNow.Add(-time.Hour)
	cases := []struct {
		name     string
		output   string
		objs     []sigs_client.Object
		want     string
		contains []string
		excludes []string
	}{
		{
			name: "active bundle only",
			objs: []sigs_client.Object{
				bundle("old", "Superseded"), bundle("new", "Promoting"),
				explainStep("demo", "old", "test", "Failed", "old-msg", created),
				explainStep("demo", "new", "test", "Promoting", "new-msg", created),
			},
			contains: []string{"new-msg", "Promoting"},
			excludes: []string{"old-msg", "Failed"},
		},
		{
			name: "no active bundles",
			objs: []sigs_client.Object{
				bundle("old", "Superseded"),
				explainStep("demo", "old", "test", "Verified", "", created),
			},
			want: "No active bundles for pipeline \"demo\".\n",
		},
		{
			name:   "no active bundles as json",
			output: "json",
			objs: []sigs_client.Object{
				bundle("old", "Superseded"),
				explainStep("demo", "old", "test", "Verified", "", created),
			},
			want: "[]\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			globalOutput = tc.output
			t.Cleanup(func() { globalOutput = "" })
			var buf bytes.Buffer
			require.NoError(t, getStepsOnce(&buf, policyClient(t, tc.objs...), "default", "demo"))
			if tc.want != "" {
				assert.Equal(t, tc.want, buf.String())
			}
			for _, s := range tc.contains {
				assert.Contains(t, buf.String(), s)
			}
			for _, s := range tc.excludes {
				assert.NotContains(t, buf.String(), s)
			}
		})
	}
}

// C09b-cli-25: --watch clears the screen only for a table on a terminal and
// prints no footer into -o json output; it stops when the context is done.
func TestWatchLoop(t *testing.T) {
	for _, output := range []string{"", "json"} {
		t.Run("output="+output, func(t *testing.T) {
			globalOutput = output
			t.Cleanup(func() { globalOutput = "" })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var buf bytes.Buffer
			renders := 0
			err := watchLoop(ctx, &buf, time.Millisecond, func() error {
				renders++
				if renders == 2 {
					cancel()
				}
				_, _ = buf.WriteString("frame\n")
				return nil
			})
			require.NoError(t, err)
			assert.Equal(t, 2, renders)
			assert.NotContains(t, buf.String(), "\033[", "no clear-screen on a pipe")
			if output == "json" {
				assert.Equal(t, "frame\nframe\n", buf.String())
			} else {
				assert.Contains(t, buf.String(), "(watching")
			}
		})
	}
}

// TestGetPipelinesOnce_FailedBundle_ShowsError verifies that when a Bundle is in
// Failed phase with a TranslationError condition, getPipelinesOnce appends an
// ERROR: line after the pipeline table.
func TestGetPipelinesOnce_FailedBundle_ShowsError(t *testing.T) {
	s := buildGetPipelinesScheme(t)

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-app",
			Namespace: "default",
		},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "prod"},
			},
		},
	}

	failedBundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-app-v1",
			Namespace: "default",
		},
		Spec: v1alpha1.BundleSpec{
			Pipeline: "my-app",
			Type:     "image",
		},
		Status: v1alpha1.BundleStatus{
			Phase: "Failed",
			Conditions: []metav1.Condition{
				{
					Type:    "Failed",
					Status:  metav1.ConditionTrue,
					Reason:  "TranslationError",
					Message: `build: environment "prod" dependsOn unknown environment "staging"`,
				},
			},
		},
	}

	fc := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(pipeline, failedBundle).
		WithStatusSubresource(failedBundle).
		Build()

	var buf bytes.Buffer
	err := getPipelinesOnce(&buf, fc, "default", nil, false)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "my-app", "pipeline name must appear in table output")
	assert.Contains(t, out, "ERROR:", "ERROR: prefix must appear when a bundle is Failed")
	assert.Contains(t, out, `dependsOn unknown environment "staging"`,
		"condition message must appear in error output")
}

// TestGetPipelinesOnce_HealthyBundles_NoErrorSection verifies that when all bundles
// are healthy, no ERROR: section is printed.
func TestGetPipelinesOnce_HealthyBundles_NoErrorSection(t *testing.T) {
	s := buildGetPipelinesScheme(t)

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-app",
			Namespace: "default",
		},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}},
		},
	}

	goodBundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-app-v1",
			Namespace: "default",
		},
		Spec:   v1alpha1.BundleSpec{Pipeline: "my-app", Type: "image"},
		Status: v1alpha1.BundleStatus{Phase: "Verified"},
	}

	fc := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(pipeline, goodBundle).
		WithStatusSubresource(goodBundle).
		Build()

	var buf bytes.Buffer
	err := getPipelinesOnce(&buf, fc, "default", nil, false)
	require.NoError(t, err)

	out := buf.String()
	assert.NotContains(t, out, "ERROR:", "no ERROR: section expected when bundles are healthy")
}

// E2E-R17: every cell of a get pipelines row describes the row's BUNDLE, the
// pipeline's newest bundle: Waiting where it is still to come, a dash where it
// failed before getting there, never the older bundle's Verified.
func TestGetPipelinesOnce_CurrentBundleState(t *testing.T) {
	old, recent := time.Now().Add(-2*time.Hour), time.Now().Add(-10*time.Minute)
	b1Everywhere := func() []sigs_client.Object {
		return []sigs_client.Object{
			explainBundle("b1", "Verified", old),
			explainStep("demo", "b1", "test", "Verified", "", old),
			explainStep("demo", "b1", "uat", "Verified", "", old),
			explainStep("demo", "b1", "prod", "Verified", "", old),
		}
	}
	tests := []struct {
		name string
		objs []sigs_client.Object
		want map[string]string
	}{
		{
			// scenA.log: the row said b1 and Verified while status said Failed.
			name: "newer bundle failed at the first env",
			objs: append([]sigs_client.Object{
				explainBundle("b2", "Failed", recent),
				explainStep("demo", "b2", "test", "Failed", "", recent),
			}, b1Everywhere()...),
			want: map[string]string{"BUNDLE": "b2", "TEST": "Failed", "UAT": "-", "PROD": "-"},
		},
		{
			// j1-happy.log: b1's Verified was shown next to BUNDLE b2.
			name: "bundle held at prod by a gate",
			objs: append([]sigs_client.Object{
				explainBundle("b2", "Promoting", recent),
				explainStep("demo", "b2", "test", "Verified", "", recent),
				explainStep("demo", "b2", "uat", "Verified", "", recent),
				explainGateInstance("demo", "b2", "prod", "require-uat-soak", "false", false, true, "soak 3m < 30m"),
			}, b1Everywhere()...),
			want: map[string]string{"BUNDLE": "b2", "TEST": "Verified", "UAT": "Verified", "PROD": "Waiting"},
		},
		{
			name: "bundle health checking in test, ungated uat",
			objs: append([]sigs_client.Object{
				explainBundle("b2", "Promoting", recent),
				explainStep("demo", "b2", "test", "HealthChecking", "", recent),
			}, b1Everywhere()...),
			want: map[string]string{"BUNDLE": "b2", "TEST": "HealthChecking", "UAT": "Waiting", "PROD": "Waiting"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := policyClient(t, append(tt.objs, policyPipeline("demo", "test", "uat", "prod"))...)
			var buf bytes.Buffer
			require.NoError(t, getPipelinesOnce(&buf, fc, "default", []string{"demo"}, false))
			lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
			header := strings.Fields(lines[0])
			cells := strings.Fields(lines[1])
			require.Len(t, cells, len(header), buf.String())
			row := map[string]string{}
			for i, h := range header {
				row[h] = cells[i]
			}
			for col, want := range tt.want {
				assert.Equal(t, want, row[col], "column %s in:\n%s", col, buf.String())
			}
		})
	}
}
