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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// staleSpec is the PRStatus spec of PR #5, the PR of the step before it was
// recreated; spec8 names PR #8, the PR the recreated step opened.
var (
	staleSpec = v1alpha1.PRStatusSpec{PRURL: "https://github.com/test/repo/pull/5", PRNumber: 5, Repo: "test/repo"}
	spec8     = v1alpha1.PRStatusSpec{PRURL: "https://github.com/test/repo/pull/8", PRNumber: 8, Repo: "test/repo"}
)

// stepPRStatus is PRStatus prs-step with spec, metadata.generation gen and a
// status written for observedGeneration obs. The fake client does not bump
// the generation on a spec patch, so each case sets it as the API server
// would.
func stepPRStatus(spec v1alpha1.PRStatusSpec, gen, obs int64, st v1alpha1.PRStatusStatus) *v1alpha1.PRStatus {
	st.ObservedGeneration = obs
	return &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: "prs-step", Namespace: "default", Generation: gen},
		Spec:       spec,
		Status:     st,
	}
}

// TestRecreatedStepTracksItsNewPR covers B72: a PromotionStep deleted while it
// waits for its PR closes that PR, and kro recreates the step, which opens a
// new PR. The PRStatus spec still names the old PR. The step points it at the
// new one, and does not act on the status of the old PR (closed for good, a
// poll error, or merged) until the PRStatus reconciler has polled the new PR.
func TestRecreatedStepTracksItsNewPR(t *testing.T) {
	checked := metav1.NewTime(time.Now().Add(-time.Minute))
	closedAt := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	closedFinal := v1alpha1.PRStatusStatus{LastCheckedAt: &checked, ClosedAt: &closedAt, ClosedFinal: true}
	tests := []struct {
		name        string
		prs         *v1alpha1.PRStatus
		wantState   string
		wantMsg     string
		wantSpec    v1alpha1.PRStatusSpec
		wantRequeue time.Duration
		wantSHA     string
	}{
		{name: "a spec on the old PR, closed for good, is pointed at the new PR",
			prs:       stepPRStatus(staleSpec, 1, 1, closedFinal),
			wantState: "WaitingForMerge", wantSpec: spec8, wantRequeue: 30 * time.Second},
		{name: "a poll error of the old PR does not fail the step",
			prs:       stepPRStatus(staleSpec, 1, 1, v1alpha1.PRStatusStatus{LastCheckedAt: &checked, PollError: "status 404: Not Found"}),
			wantState: "WaitingForMerge", wantSpec: spec8, wantRequeue: 30 * time.Second},
		{name: "a status written for the old spec is not read",
			prs: stepPRStatus(spec8, 2, 1, v1alpha1.PRStatusStatus{LastCheckedAt: &checked, Merged: true,
				MergeCommitSHA: "0123456"}),
			wantState: "WaitingForMerge", wantSpec: spec8, wantRequeue: 30 * time.Second},
		{name: "a status written for the new spec is read",
			prs: stepPRStatus(spec8, 2, 2, v1alpha1.PRStatusStatus{LastCheckedAt: &checked, Merged: true,
				MergeCommitSHA: "89abcde"}),
			wantState: "HealthChecking", wantMsg: "PR #8 merged", wantSpec: spec8, wantSHA: "89abcde"},
		{name: "a status written by an older release is read",
			prs:       stepPRStatus(spec8, 2, 0, v1alpha1.PRStatusStatus{LastCheckedAt: &checked, Merged: true}),
			wantState: "HealthChecking", wantMsg: "PR #8 merged", wantSpec: spec8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := prStep("WaitingForMerge", 8)
			ps.Finalizers = []string{promotionstep.FinalizerClosePR}
			c := newClient(t, ps, tt.prs, makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo"))
			m := &mockSCM{open: true}
			r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{},
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}

			res, err := r.Reconcile(context.Background(), reqFor("step"))
			require.NoError(t, err)
			got := getStep(t, c, "step")
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Contains(t, got.Status.Message, tt.wantMsg)
			assert.Equal(t, tt.wantSHA, got.Status.Outputs["mergeCommitSHA"])
			if tt.wantRequeue != 0 {
				assert.Equal(t, tt.wantRequeue, res.RequeueAfter)
			}
			var prs v1alpha1.PRStatus
			require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "prs-step", Namespace: "default"}, &prs))
			assert.Equal(t, tt.wantSpec, prs.Spec)
			assert.Empty(t, m.closed, "no PR is closed")
		})
	}
}

// TestRecreatedStepTracksItsNewPR_OpenPR covers B72 where the new PR is
// opened: the open-pr step points the PRStatus spec, which still names the
// old PR, at the PR it opened.
func TestRecreatedStepTracksItsNewPR_OpenPR(t *testing.T) {
	checked := metav1.NewTime(time.Now().Add(-time.Minute))
	old := v1alpha1.PRStatusSpec{PRURL: "https://github.com/org/repo/pull/5", PRNumber: 5, Repo: "org/repo"}
	prs := stepPRStatus(old, 1, 1, v1alpha1.PRStatusStatus{LastCheckedAt: &checked, ClosedAt: &checked, ClosedFinal: true})
	prs.Name = "prs"
	ps := asPromoting(labelled(makeStep("step", "p", "b1", "prod")), makePipeline("p"))
	ps.Status.CurrentStepIndex = 4 // open-pr, then wait-for-merge
	ps.Spec.PRStatusRef = "prs"
	c := newClient(t, ps, prs, makePipeline("p"), makeBundle("b1", "p"))
	m := &mockSCM{open: true, prURL: "https://github.com/org/repo/pull/7", prNumber: 7}
	r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}

	reconcileStep(t, r, "step")
	assert.Equal(t, "WaitingForMerge", getStep(t, c, "step").Status.State)
	var got v1alpha1.PRStatus
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(prs), &got))
	assert.Equal(t, v1alpha1.PRStatusSpec{PRURL: "https://github.com/org/repo/pull/7", PRNumber: 7, Repo: "org/repo"},
		got.Spec)
}

// TestRecreatedStepClosesItsOwnPR covers B72 where the recreated step is
// cancelled (its Bundle superseded) before the PRStatus names its PR, or
// before the PRStatus reconciler cleared the old PR's status: the step closes
// the PR it opened, not the old one, and a merge of the old PR does not keep
// the new PR open.
func TestRecreatedStepClosesItsOwnPR(t *testing.T) {
	checked := metav1.NewTime(time.Now().Add(-time.Minute))
	tests := []struct {
		name string
		prs  *v1alpha1.PRStatus
	}{
		{name: "the spec names the old, closed PR",
			prs: stepPRStatus(staleSpec, 1, 1, v1alpha1.PRStatusStatus{LastCheckedAt: &checked, ClosedAt: &checked, ClosedFinal: true})},
		{name: "the spec names the old, merged PR",
			prs: stepPRStatus(staleSpec, 1, 1, v1alpha1.PRStatusStatus{LastCheckedAt: &checked, Merged: true})},
		{name: "the status is the old PR's",
			prs: stepPRStatus(spec8, 2, 1, v1alpha1.PRStatusStatus{LastCheckedAt: &checked, Merged: true})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := prStep("WaitingForMerge", 8)
			ps.Finalizers = []string{promotionstep.FinalizerClosePR}
			bundle := makeBundle("bundle-1", "nginx-demo")
			bundle.Status.Phase = "Superseded"
			c := newClient(t, ps, tt.prs, makePipeline("nginx-demo"), bundle)
			m := &mockSCM{open: true}
			r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{},
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}

			reconcileStep(t, r, "step")
			got := getStep(t, c, "step")
			assert.Equal(t, "Failed", got.Status.State, got.Status.Message)
			assert.Equal(t, []string{"test/repo#8"}, m.closed)
		})
	}
}
