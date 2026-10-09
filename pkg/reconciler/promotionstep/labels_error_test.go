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

package promotionstep_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// TestLabelsErrorInWaitingMessage covers B71: when open-pr cannot label the
// PR it opened, the step waits for the merge, and its message still says the
// labels failed (docs/pr-evidence.md), as does status.outputs.prLabelsError.
func TestLabelsErrorInWaitingMessage(t *testing.T) {
	t.Run("the step that opened the PR", func(t *testing.T) {
		ps := asPromoting(labelled(makeStep("step", "p", "b1", "prod")), makePipeline("p"))
		ps.Status.CurrentStepIndex = 4 // open-pr, then wait-for-merge
		ps.Spec.PRStatusRef = "prs"
		c := newClient(t, ps, openPRStatus("prs", "", 0), makePipeline("p"), makeBundle("b1", "p"))
		m := &mockSCM{open: true, prURL: "https://github.com/org/repo/pull/7", prNumber: 7,
			labelsErr: errors.New("status 403: no label permission")}
		r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{},
			WorkDirFn: func(_, _ string) string { return t.TempDir() }}

		reconcileStep(t, r, "step")
		got := getStep(t, c, "step")
		assert.Equal(t, "WaitingForMerge", got.Status.State)
		assert.Equal(t, "PR #7 is open, waiting for merge; adding labels failed: status 403: no label permission",
			got.Status.Message)
		assert.Equal(t, "status 403: no label permission", got.Status.Outputs["prLabelsError"])
	})

	t.Run("a reopen inside the grace window", func(t *testing.T) {
		prs := openPRStatus("prs", "test/repo", 5)
		step := makeStep("step", "nginx-demo", "bundle-1", "prod")
		step.Spec.PRStatusRef = prs.Name
		step.Status.State = "WaitingForMerge"
		step.Status.Message = "PR #5 is closed; the step fails 5m0s after closing unless it is reopened"
		step.Status.Outputs = map[string]string{"prURL": "https://github.com/test/repo/pull/5", "prNumber": "5",
			"prLabelsError": "status 403"}
		c := newClient(t, step, prs, makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo"))
		r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
			WorkDirFn: func(_, _ string) string { return t.TempDir() }}

		reconcileStep(t, r, "step")
		assert.Equal(t, "PR #5 is open, waiting for merge; adding labels failed: status 403",
			getStep(t, c, "step").Status.Message)
	})

	t.Run("labels that worked add nothing", func(t *testing.T) {
		ps := asPromoting(labelled(makeStep("step", "p", "b1", "prod")), makePipeline("p"))
		ps.Status.CurrentStepIndex = 4
		ps.Spec.PRStatusRef = "prs"
		c := newClient(t, ps, openPRStatus("prs", "", 0), makePipeline("p"), makeBundle("b1", "p"))
		m := &mockSCM{open: true, prURL: "https://github.com/org/repo/pull/7", prNumber: 7}
		r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{},
			WorkDirFn: func(_, _ string) string { return t.TempDir() }}

		_, err := r.Reconcile(context.Background(), reqFor("step"))
		require.NoError(t, err)
		got := getStep(t, c, "step")
		assert.Equal(t, "PR #7 is open, waiting for merge", got.Status.Message)
		assert.NotContains(t, got.Status.Outputs, "prLabelsError")
	})
}

// TestControlsErrorInWaitingMessage: the pr controls open-pr could not apply
// (status.outputs.prControlsError) and an enabled auto-merge
// (status.outputs.prAutoMerge) stay in the WaitingForMerge message, which
// replaces open-pr's, and the message rebuilt on a reopen (#1453).
//
// Covers SCM-PRCTL-ERR-01.
func TestControlsErrorInWaitingMessage(t *testing.T) {
	tests := []struct {
		name    string
		outputs map[string]string
		want    string
	}{
		{name: "controls failed", outputs: map[string]string{"prControlsError": "reviewers: status 422"},
			want: "PR #5 is open, waiting for merge; PR controls failed: reviewers: status 422"},
		{name: "auto-merge on", outputs: map[string]string{"prAutoMerge": "enabled"},
			want: "PR #5 is open, waiting for merge; auto-merge enabled"},
		{name: "labels and controls", outputs: map[string]string{"prLabelsError": "status 403", "prControlsError": "assignees: x", "prAutoMerge": "enabled"},
			want: "PR #5 is open, waiting for merge; adding labels failed: status 403; PR controls failed: assignees: x; auto-merge enabled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prs := openPRStatus("prs", "test/repo", 5)
			step := makeStep("step", "nginx-demo", "bundle-1", "prod")
			step.Spec.PRStatusRef = prs.Name
			step.Status.State = "WaitingForMerge"
			// A reopened PR: the message is rebuilt from the outputs.
			step.Status.Message = "PR #5 is closed; the step fails 5m0s after closing unless it is reopened"
			step.Status.Outputs = map[string]string{"prURL": "https://github.com/test/repo/pull/5", "prNumber": "5"}
			for k, v := range tt.outputs {
				step.Status.Outputs[k] = v
			}
			c := newClient(t, step, prs, makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo"))
			r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}

			reconcileStep(t, r, "step")
			assert.Equal(t, tt.want, getStep(t, c, "step").Status.Message)
			// A second reconcile changes nothing.
			reconcileStep(t, r, "step")
			assert.Equal(t, tt.want, getStep(t, c, "step").Status.Message)
		})
	}
}
