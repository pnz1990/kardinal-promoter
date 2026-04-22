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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
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
		WithStatusSubresource(ps).
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
		WithObjects(ps).
		Build()

	var buf bytes.Buffer
	err := LogsFnForTest(&buf, client, "default", "static-pipeline", "", "")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "static-pipeline/prod")
	assert.Contains(t, out, "Verified")
	assert.Contains(t, out, "all done")
}
