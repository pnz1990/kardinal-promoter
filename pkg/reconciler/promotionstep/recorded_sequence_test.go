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
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// pushRecorder is a GitClient that records every push and fails the first
// len(pushErrs) of them with those errors. Its head commit is newSHA.
type pushRecorder struct {
	headGit
	pushErrs []error
	pushes   []string // "<branch> force=<force>" of every Push call
}

func (g *pushRecorder) Push(_ context.Context, _, _, branch, _ string, force bool) error {
	g.pushes = append(g.pushes, fmt.Sprintf("%s force=%t", branch, force))
	if len(g.pushErrs) == 0 {
		return nil
	}
	err := g.pushErrs[0]
	g.pushErrs = g.pushErrs[1:]
	return err
}

// recordedSteps is the status.steps handlePending records for an image Bundle
// in an environment with approval.
func recordedSteps(approval string) []v1alpha1.StepStatus {
	var out []v1alpha1.StepStatus
	for _, name := range steps.DefaultSequenceForBundle(approval, "image", "", "") {
		out = append(out, v1alpha1.StepStatus{Name: name, State: v1alpha1.StepExecutionCompleted})
	}
	return out
}

// setApproval edits the approval of env in the Pipeline, as a user would.
func setApproval(t *testing.T, c client.Client, pipeline, env, approval string) {
	t.Helper()
	var pl v1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: pipeline, Namespace: "default"}, &pl))
	for i := range pl.Spec.Environments {
		if pl.Spec.Environments[i].Name == env {
			pl.Spec.Environments[i].Approval = approval
		}
	}
	require.NoError(t, c.Update(context.Background(), &pl))
}

// TestApprovalEditMidPromotion proves that a step runs the step list it
// recorded on leaving Pending, never one rebuilt from the live Pipeline, so an
// approval edit made while the step runs applies from the next Bundle.
//
//   - pr-review edited to auto while open-pr retries: the PR still opens and
//     the step waits for its merge. A rebuilt list put the step on the auto
//     list's health-check placeholder, so the change, pushed only to the PR
//     branch, never reached the base branch and no PR was opened.
//   - auto edited to pr-review while git-push retries: the change is pushed to
//     the base branch, its commit is recorded for the health check, and no PR
//     is opened. A rebuilt list pushed to a PR branch and opened a PR the
//     close-pr finalizer had not been added for.
func TestApprovalEditMidPromotion(t *testing.T) {
	transient := errors.New("connection reset by peer")
	tests := []struct {
		name          string
		env           string
		editTo        string
		failOpenPR    bool    // the first OpenPR call fails with transient
		pushErrs      []error // the first pushes fail with these
		retrying      string  // the step that retries when the approval is edited
		wantState     string
		wantOpenCalls int
		wantPushes    []string
		wantFinalizer bool
		wantCommit    string // status.outputs.commitSHA
		wantSteps     []string
	}{
		{name: "pr-review edited to auto while open-pr retries", env: "prod", editTo: "auto", failOpenPR: true,
			retrying: "open-pr", wantState: "WaitingForMerge", wantOpenCalls: 2,
			wantPushes: []string{"kardinal/bundle-1/prod force=true"}, wantFinalizer: true,
			wantSteps: steps.DefaultSequenceForBundle("pr-review", "image", "", "")},
		{name: "auto edited to pr-review while git-push retries", env: "test", editTo: "pr-review",
			pushErrs: []error{transient}, retrying: "git-push", wantState: "HealthChecking", wantOpenCalls: 0,
			wantPushes: []string{"main force=false", "main force=false"}, wantFinalizer: false, wantCommit: newSHA,
			wantSteps: steps.DefaultSequenceForBundle("auto", "image", "", "")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pl, b := makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo")
			step := builtStep(t, pl, b, tt.env)
			c := newClient(t, step, pl, b, openPRStatus(step.Spec.PRStatusRef, "", 0))
			m := &mockSCM{open: true, prURL: "https://github.com/test/repo/pull/5", prNumber: 5}
			if tt.failOpenPR {
				m.openPRErr = transient
			}
			git := &pushRecorder{headGit: headGit{sha: newSHA}, pushErrs: tt.pushErrs}
			r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: git,
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}

			reconcileStep(t, r, step.Name) // Pending → Promoting records the step list
			reconcileStep(t, r, step.Name) // runs up to the failing step
			got := getStep(t, c, step.Name)
			require.Equal(t, "Promoting", got.Status.State, got.Status.Message)
			require.Equal(t, 1, got.Status.RetryCount, got.Status.Message)
			require.Equal(t, tt.retrying, got.Status.Steps[got.Status.CurrentStepIndex].Name)

			setApproval(t, c, pl.Name, tt.env, tt.editTo)
			m.openPRErr = nil
			reconcileStep(t, r, step.Name) // the retry, after the edit
			got = getStep(t, c, step.Name)

			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Equal(t, tt.wantOpenCalls, m.openCalled)
			assert.Equal(t, tt.wantPushes, git.pushes)
			assert.Equal(t, tt.wantFinalizer, slices.Contains(got.Finalizers, promotionstep.FinalizerClosePR),
				"finalizers %v", got.Finalizers)
			assert.Equal(t, tt.wantCommit, got.Status.Outputs["commitSHA"])
			names := make([]string, 0, len(got.Status.Steps))
			for _, s := range got.Status.Steps {
				names = append(names, s.Name)
			}
			assert.Equal(t, tt.wantSteps, names, "status.steps keeps the recorded list")
			assert.Equal(t, v1alpha1.StepExecutionCompleted, stepStates(got)[tt.retrying])
		})
	}
}
