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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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

// asPromoting puts ps in Promoting with the step list handlePending records
// for an image Bundle in ps's environment of pl, every step Pending.
func asPromoting(ps *v1alpha1.PromotionStep, pl *v1alpha1.Pipeline) *v1alpha1.PromotionStep {
	var env v1alpha1.EnvironmentSpec
	for _, e := range pl.Spec.Environments {
		if e.Name == ps.Spec.Environment {
			env = e
		}
	}
	ps.Status.State = "Promoting"
	ps.Status.Steps = nil
	for _, name := range steps.DefaultSequenceForBundle(env.Approval, "image", env.Update.Strategy, env.Layout) {
		ps.Status.Steps = append(ps.Status.Steps, v1alpha1.StepStatus{Name: name, State: v1alpha1.StepExecutionPending})
	}
	return ps
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
			assert.Equal(t, tt.wantSteps, recordedNames(got), "status.steps keeps the recorded list")
			assert.Equal(t, v1alpha1.StepExecutionCompleted, stepStates(got)[tt.retrying])
		})
	}
}

// TestPendingRetriesFailedBundleRead proves that a step whose Bundle read
// fails while it leaves Pending records no step list and returns the error,
// so it is requeued, and records its Bundle type's list on the retry. The
// recorded list is run to the end: recording the image list instead verified a
// config or mixed Bundle without its config-merge step.
func TestPendingRetriesFailedBundleRead(t *testing.T) {
	for _, bundleType := range []string{"config", "mixed"} {
		t.Run(bundleType, func(t *testing.T) {
			pl, b := makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo")
			b.Spec.Type = bundleType
			step := makeStep("step-1", pl.Name, b.Name, "test")
			api := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.PRStatus{}, &v1alpha1.Bundle{}).
				WithObjects(step, pl, b).Build()
			bundleGets := 0
			c := interceptor.NewClient(api, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*v1alpha1.Bundle); ok {
						bundleGets++
						// The first Bundle read is the orphan guard's; the
						// second is the one that picks the step list.
						if bundleGets == 2 {
							return errors.New("connection reset by peer")
						}
					}
					return c.Get(ctx, key, obj, opts...)
				},
			})
			r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}

			_, err := r.Reconcile(context.Background(), reqFor(step.Name))
			require.Error(t, err, "a failed Bundle read is returned, so the step is requeued")
			assert.ErrorContains(t, err, "load bundle")
			got := getStep(t, api, step.Name)
			assert.Equal(t, promotionstep.StatePending, got.Status.State, got.Status.Message)
			assert.Empty(t, got.Status.Steps, "no step list is recorded without the Bundle type")

			reconcileStep(t, r, step.Name)
			got = getStep(t, api, step.Name)
			require.Equal(t, "Promoting", got.Status.State, got.Status.Message)
			names := recordedNames(got)
			assert.Equal(t, steps.DefaultSequenceForBundle("auto", bundleType, "", ""), names)
			assert.Contains(t, names, "config-merge")
		})
	}
}

// finalizerAtOpenSCM is a mockSCM that records, at every OpenPR call, whether
// the step holds the close-pr finalizer.
type finalizerAtOpenSCM struct {
	*mockSCM
	c        client.Client
	step     string
	heldAtPR []bool
}

func (s *finalizerAtOpenSCM) OpenPR(ctx context.Context, repo, title, body, head, base string) (string, int, error) {
	var ps v1alpha1.PromotionStep
	if err := s.c.Get(ctx, client.ObjectKey{Name: s.step, Namespace: "default"}, &ps); err != nil {
		return "", 0, err
	}
	s.heldAtPR = append(s.heldAtPR, slices.Contains(ps.Finalizers, promotionstep.FinalizerClosePR))
	return s.mockSCM.OpenPR(ctx, repo, title, body, head, base)
}

// TestPromotingWithoutStepList covers a step that is Promoting with no
// status.steps (a status edited by hand, or a step started before
// status.steps existed). Its first reconcile only records the step list, so
// the close-pr finalizer is added before a pr-review step opens its PR; the
// next reconcile runs the list. Running the list in the first reconcile
// opened the PR before the finalizer was added.
func TestPromotingWithoutStepList(t *testing.T) {
	tests := []struct {
		env           string
		approval      string
		wantFinalizer bool
		wantState     string // after the second reconcile
		wantPushes    []string
		wantHeldAtPR  []bool
	}{
		{env: "prod", approval: "pr-review", wantFinalizer: true, wantState: "WaitingForMerge",
			wantPushes: []string{"kardinal/bundle-1/prod force=true"}, wantHeldAtPR: []bool{true}},
		{env: "test", approval: "auto", wantFinalizer: false, wantState: "HealthChecking",
			wantPushes: []string{"main force=false"}},
	}
	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			pl, b := makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo")
			step := builtStep(t, pl, b, tt.env)
			step.Status.State = "Promoting"
			c := newClient(t, step, pl, b, openPRStatus(step.Spec.PRStatusRef, "", 0))
			m := &finalizerAtOpenSCM{c: c, step: step.Name,
				mockSCM: &mockSCM{open: true, prURL: "https://github.com/test/repo/pull/5", prNumber: 5}}
			git := &pushRecorder{headGit: headGit{sha: newSHA}}
			r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: git,
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}
			wantSteps := steps.DefaultSequenceForBundle(tt.approval, "image", "", "")

			reconcileStep(t, r, step.Name) // records the step list only
			got := getStep(t, c, step.Name)
			require.Equal(t, "Promoting", got.Status.State, got.Status.Message)
			assert.Equal(t, wantSteps, recordedNames(got))
			assert.Equal(t, v1alpha1.StepExecutionPending, stepStates(got)["git-clone"])
			assert.Equal(t, tt.wantFinalizer, slices.Contains(got.Finalizers, promotionstep.FinalizerClosePR),
				"finalizers %v", got.Finalizers)
			assert.Empty(t, git.pushes, "nothing is pushed before the list is recorded")
			assert.Zero(t, m.openCalled, "no PR is opened before the list is recorded")

			reconcileStep(t, r, step.Name) // runs the recorded list
			got = getStep(t, c, step.Name)
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Equal(t, wantSteps, recordedNames(got))
			assert.Equal(t, tt.wantPushes, git.pushes)
			assert.Equal(t, tt.wantHeldAtPR, m.heldAtPR, "the finalizer is held when the PR is opened")
		})
	}
}

// recordedNames returns the step names in ps's status.steps.
func recordedNames(ps v1alpha1.PromotionStep) []string {
	names := make([]string, 0, len(ps.Status.Steps))
	for _, s := range ps.Status.Steps {
		names = append(names, s.Name)
	}
	return names
}
