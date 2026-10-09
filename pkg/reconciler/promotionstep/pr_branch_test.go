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
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// TestClosedPRBranchIsDeleted covers B70: when kardinal closes the PR of a
// superseded step it also deletes the PR's head branch, since GitHub merges a
// closed PR through the API and does not once its branch is gone. Only a
// branch under kardinal/ is deleted, and a merged PR keeps its branch. A
// failed delete is retried, and the retry, which finds the PR closed, deletes
// the branch without closing or commenting again.
func TestClosedPRBranchIsDeleted(t *testing.T) {
	const branch = "kardinal/37a8eec1/b1/prod"
	mergedPRS := openPRStatus("prs", "org/repo", 42)
	mergedPRS.Status.Open, mergedPRS.Status.Merged = false, true
	tests := []struct {
		name         string
		prStatus     *v1alpha1.PRStatus
		outputs      map[string]string
		retryCount   int
		scm          mockSCM
		wantState    string
		wantMsg      string
		wantClosed   []string
		wantComments int
		wantDeleted  []string
		// again reconciles once more and checks the totals after it.
		again *struct {
			state    string
			closed   []string
			comments int
			deleted  []string
		}
	}{
		{name: "the closed PR's branch is deleted", prStatus: openPRStatus("prs", "org/repo", 42),
			outputs: map[string]string{"branch": branch}, scm: mockSCM{open: true},
			wantState: "Failed", wantMsg: "was superseded",
			wantClosed: []string{"org/repo#42"}, wantComments: 1, wantDeleted: []string{"org/repo:" + branch}},
		{name: "without a branch output the open-pr default is deleted", prStatus: openPRStatus("prs", "org/repo", 42),
			scm:       mockSCM{open: true},
			wantState: "Failed", wantClosed: []string{"org/repo#42"}, wantComments: 1,
			wantDeleted: []string{"org/repo:" + branch}},
		{name: "a PR found closed only loses its branch", prStatus: openPRStatus("prs", "org/repo", 42),
			outputs: map[string]string{"branch": branch}, scm: mockSCM{open: false},
			wantState: "Failed", wantDeleted: []string{"org/repo:" + branch}},
		{name: "a PR found merged keeps its branch", prStatus: openPRStatus("prs", "org/repo", 42),
			outputs: map[string]string{"branch": branch}, scm: mockSCM{open: false, merged: true},
			wantState: "Failed"},
		{name: "a PRStatus that says merged keeps the branch", prStatus: mergedPRS,
			outputs: map[string]string{"branch": branch}, scm: mockSCM{open: true},
			wantState: "Failed"},
		{name: "a branch kardinal does not own is not deleted", prStatus: openPRStatus("prs", "org/repo", 42),
			outputs: map[string]string{"branch": "main"}, scm: mockSCM{open: true},
			wantState: "Failed", wantClosed: []string{"org/repo#42"}, wantComments: 1},
		{name: "a failed delete is retried, and the retry only deletes", prStatus: openPRStatus("prs", "org/repo", 42),
			outputs:   map[string]string{"branch": branch},
			scm:       mockSCM{open: true, deleteErrs: []error{errors.New("HTTP 502")}},
			wantState: "WaitingForMerge", wantMsg: "PR #42 is closed, but deleting its branch kardinal/37a8eec1/b1/prod failed: HTTP 502",
			wantClosed: []string{"org/repo#42"}, wantComments: 1, wantDeleted: []string{"org/repo:" + branch},
			again: &struct {
				state    string
				closed   []string
				comments int
				deleted  []string
			}{"Failed", []string{"org/repo#42"}, 1, []string{"org/repo:" + branch, "org/repo:" + branch}}},
		{name: "a close whose response was lost is finished by the retry", prStatus: openPRStatus("prs", "org/repo", 42),
			outputs: map[string]string{"branch": branch}, scm: mockSCM{open: true, lostClose: 1},
			wantState: "WaitingForMerge", wantMsg: "closing its PR failed, retrying in 10s (1/5)",
			wantClosed: []string{"org/repo#42"},
			again: &struct {
				state    string
				closed   []string
				comments int
				deleted  []string
			}{"Failed", []string{"org/repo#42"}, 0, []string{"org/repo:" + branch}}},
		{name: "a delete that keeps failing says to delete the branch by hand", prStatus: openPRStatus("prs", "org/repo", 42),
			outputs: map[string]string{"branch": branch}, retryCount: 5,
			scm:       mockSCM{open: true, deleteErrs: []error{errors.New("HTTP 403")}},
			wantState: "Failed", wantMsg: "— delete branch kardinal/37a8eec1/b1/prod by hand",
			wantClosed: []string{"org/repo#42"}, wantComments: 1, wantDeleted: []string{"org/repo:" + branch}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := labelled(makeStep("step", "p", "b1", "prod"))
			ps.Status.State = "WaitingForMerge"
			ps.Status.RetryCount = tt.retryCount
			if tt.retryCount > 0 {
				retryingClose(ps)
			}
			ps.Status.Outputs = tt.outputs
			ps.Spec.PRStatusRef = tt.prStatus.Name
			bundle := makeBundle("b1", "p")
			bundle.Status.Phase = "Superseded"
			c := newClient(t, ps, makePipeline("p"), bundle, tt.prStatus)
			m := tt.scm
			r := &promotionstep.Reconciler{Client: c, SCM: &m, GitClient: &mockGit{}, NowFn: pastBackoff()}

			_, err := r.Reconcile(context.Background(), reqFor("step"))
			require.NoError(t, err)
			got := getStep(t, c, "step")
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Contains(t, got.Status.Message, tt.wantMsg)
			assert.Equal(t, tt.wantClosed, m.closed)
			assert.Len(t, m.comments, tt.wantComments)
			assert.Equal(t, tt.wantDeleted, m.deleted)
			if tt.again == nil {
				return
			}
			_, err = r.Reconcile(context.Background(), reqFor("step"))
			require.NoError(t, err)
			got = getStep(t, c, "step")
			assert.Equal(t, tt.again.state, got.Status.State, got.Status.Message)
			assert.Equal(t, tt.again.closed, m.closed, "the PR is closed once")
			assert.Len(t, m.comments, tt.again.comments, "the PR is commented on at most once")
			assert.Equal(t, tt.again.deleted, m.deleted)
		})
	}
}

// TestBranchWithoutPRIsDeleted covers the B79 review: a step that opens a PR
// but ends before it opened one has its head branch deleted. git-push may have
// pushed it, or a deleted step may have kept it for this one, and nothing else
// deletes it. Only a step whose sequence has open-pr (recorded, or, not
// started yet, its environment's) deletes it, and only a branch under
// kardinal/. With the Pipeline gone nothing names the repository, so the
// branch is left. A failed delete is retried, as for a closed PR.
func TestBranchWithoutPRIsDeleted(t *testing.T) {
	const branch = "test/repo:kardinal/37a8eec1/b1/prod"
	type againWant struct {
		state   string
		deleted []string
	}
	tests := []struct {
		name        string
		env         string // "prod" (pr-review) by default
		started     bool   // the step recorded its sequence (asPromoting)
		outputs     map[string]string
		prStatus    *v1alpha1.PRStatus // the step's PRStatus (nil: none)
		noPipeline  bool
		gitURL      string // the Pipeline's git.url ("" keeps makePipeline's)
		retryCount  int
		scm         mockSCM
		wantState   string
		wantMsg     string
		wantDeleted []string
		again       *againWant // reconciles once more and checks the totals after it
	}{
		{name: "a step superseded before its open-pr ran loses its branch", started: true,
			wantState: "Failed", wantMsg: "was superseded", wantDeleted: []string{branch}},
		{name: "a step superseded before it started loses its branch",
			wantState: "Failed", wantMsg: "before this step started", wantDeleted: []string{branch}},
		{name: "a PRStatus not filled in yet does not stop the delete", started: true,
			prStatus: openPRStatus("prs", "", 0), wantState: "Failed", wantDeleted: []string{branch}},
		{name: "the branch git-push reported is deleted", started: true,
			outputs:   map[string]string{"branch": "kardinal/37a8eec1/b1/prod"},
			wantState: "Failed", wantDeleted: []string{branch}},
		{name: "a branch kardinal does not own is not deleted", started: true,
			outputs: map[string]string{"branch": "main"}, wantState: "Failed"},
		{name: "an auto step has no branch to delete", env: "test", started: true, wantState: "Failed"},
		{name: "an auto step that did not start has no branch to delete", env: "test", wantState: "Failed"},
		{name: "with its Pipeline gone the branch is left", started: true, noPipeline: true, wantState: "Failed"},
		{name: "a failed delete is retried", started: true,
			scm:         mockSCM{deleteErrs: []error{errors.New("HTTP 502")}},
			wantState:   "Promoting",
			wantMsg:     "the step opened no PR, but deleting its branch kardinal/37a8eec1/b1/prod failed: HTTP 502",
			wantDeleted: []string{branch},
			again:       &againWant{"Failed", []string{branch, branch}}},
		{name: "a Pipeline git URL that names no repository is retried, not skipped", started: true,
			gitURL:    "https://git.example.com/",
			wantState: "Promoting",
			wantMsg:   "deleting its branch kardinal/37a8eec1/b1/prod failed: repository URL"},
		{name: "a delete that keeps failing says to delete the branch by hand", started: true, retryCount: 5,
			scm:       mockSCM{deleteErrs: []error{errors.New("HTTP 403")}},
			wantState: "Failed", wantMsg: "— delete branch kardinal/37a8eec1/b1/prod by hand", wantDeleted: []string{branch}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := tt.env
			if env == "" {
				env = "prod"
			}
			pipeline := makePipeline("p")
			if tt.gitURL != "" {
				pipeline.Spec.Git.URL = tt.gitURL
			}
			ps := labelled(makeStep("step", "p", "b1", env))
			if tt.started {
				ps = asPromoting(ps, pipeline)
			}
			ps.Status.RetryCount = tt.retryCount
			if tt.retryCount > 0 {
				retryingClose(ps)
			}
			ps.Status.Outputs = tt.outputs
			bundle := makeBundle("b1", "p")
			bundle.Status.Phase = "Superseded"
			objs := []client.Object{ps, bundle}
			if tt.prStatus != nil {
				ps.Spec.PRStatusRef = tt.prStatus.Name
				objs = append(objs, tt.prStatus)
			}
			if !tt.noPipeline {
				objs = append(objs, pipeline)
			}
			c := newClient(t, objs...)
			m := tt.scm
			r := &promotionstep.Reconciler{Client: c, SCM: &m, GitClient: &mockGit{}, NowFn: pastBackoff()}

			_, err := r.Reconcile(context.Background(), reqFor("step"))
			require.NoError(t, err)
			got := getStep(t, c, "step")
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Contains(t, got.Status.Message, tt.wantMsg)
			assert.Zero(t, m.getPRCalled, "there is no PR to ask about")
			assert.Empty(t, m.closed)
			assert.Empty(t, m.comments)
			assert.Equal(t, tt.wantDeleted, m.deleted)
			if tt.again == nil {
				return
			}
			_, err = r.Reconcile(context.Background(), reqFor("step"))
			require.NoError(t, err)
			got = getStep(t, c, "step")
			assert.Equal(t, tt.again.state, got.Status.State, got.Status.Message)
			assert.Equal(t, tt.again.deleted, m.deleted)
		})
	}

	// handleStepError: the step failed for good before open-pr ran.
	t.Run("a step that failed for good before its open-pr ran loses its branch", func(t *testing.T) {
		ps := asPromoting(labelled(makeStep("step", "p", "b1", "prod")), makePipeline("p"))
		ps.Status.RetryCount = 5
		c := newClient(t, ps, makePipeline("p"), makeBundle("b1", "p"))
		m := &mockSCM{}
		r := &promotionstep.Reconciler{Client: c, SCM: m, Recorder: events.NewFakeRecorder(20),
			GitClient: &mockGit{cloneErr: errors.New("connection reset by peer")},
			WorkDirFn: func(_, _ string) string { return filepath.Join(t.TempDir(), "w") }}

		_, err := r.Reconcile(context.Background(), reqFor("step"))
		require.NoError(t, err)
		got := getStep(t, c, "step")
		assert.Equal(t, "Failed", got.Status.State)
		assert.NotContains(t, got.Status.Message, "by hand")
		assert.Empty(t, m.closed)
		assert.Equal(t, []string{branch}, m.deleted)
	})
}

// TestClosedPRBranchIsDeleted_EveryClose covers B70 for the other ways
// kardinal closes its PR: waitForMergeTimeout, a step that failed for good,
// and a deleted step (FinalizerClosePR). A PR a human closed inside the grace
// window keeps its branch, so reopening it still works.
func TestClosedPRBranchIsDeleted_EveryClose(t *testing.T) {
	t.Run("waitForMergeTimeout", func(t *testing.T) {
		past := metav1.NewTime(time.Now().Add(-time.Minute))
		pipeline := makePipeline("p")
		pipeline.Spec.Environments[1].WaitForMergeTimeout = "1m"
		ps := makeStep("step", "p", "b1", "prod")
		ps.Spec.StepType = "pr-review"
		ps.Spec.PRStatusRef = "prs"
		ps.Status.State = "WaitingForMerge"
		ps.Status.WaitForMergeExpiry = &past
		ps.Status.Outputs = map[string]string{"branch": "kardinal/37a8eec1/b1/prod"}
		c := newClient(t, pipeline, makeBundle("b1", "p"), openPRStatus("prs", "org/repo", 42), ps)
		m := &mockSCM{open: true}
		r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{}}

		_, err := r.Reconcile(context.Background(), reqFor("step"))
		require.NoError(t, err)
		assert.Equal(t, "Failed", getStep(t, c, "step").Status.State)
		assert.Equal(t, []string{"org/repo#42"}, m.closed)
		assert.Equal(t, []string{"org/repo:kardinal/37a8eec1/b1/prod"}, m.deleted)
	})

	t.Run("a step that failed for good", func(t *testing.T) {
		ps := asPromoting(labelled(makeStep("step", "p", "b1", "prod")), makePipeline("p"))
		ps.Status.RetryCount = 5
		ps.Status.Outputs = map[string]string{"prURL": "https://github.com/org/repo/pull/7", "prNumber": "7"}
		c := newClient(t, ps, makePipeline("p"), makeBundle("b1", "p"))
		m := &mockSCM{open: true}
		r := &promotionstep.Reconciler{Client: c, SCM: m, Recorder: events.NewFakeRecorder(20),
			GitClient: &mockGit{cloneErr: errors.New("connection reset by peer")},
			WorkDirFn: func(_, _ string) string { return filepath.Join(t.TempDir(), "w") }}

		_, err := r.Reconcile(context.Background(), reqFor("step"))
		require.NoError(t, err)
		assert.Equal(t, "Failed", getStep(t, c, "step").Status.State)
		assert.Equal(t, []string{"org/repo#7"}, m.closed)
		assert.Equal(t, []string{"org/repo:kardinal/37a8eec1/b1/prod"}, m.deleted)
	})

	t.Run("a deleted step", func(t *testing.T) {
		ps := prStep("WaitingForMerge", 5)
		ps.Finalizers = []string{promotionstep.FinalizerClosePR}
		deleted := metav1.NewTime(time.Now().Add(-time.Second))
		ps.DeletionTimestamp = &deleted
		c := newClient(t, ps, makePipeline("nginx-demo"), openPRStatus("prs-step", "test/repo", 5))
		m := &mockSCM{open: true}
		r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{},
			Recorder: events.NewFakeRecorder(5), WorkDirFn: func(_, _ string) string { return t.TempDir() }}

		_, err := r.Reconcile(context.Background(), reqFor("step"))
		require.NoError(t, err)
		assert.Equal(t, []string{"test/repo#5"}, m.closed)
		assert.Equal(t, []string{"test/repo:kardinal/37a8eec1/bundle-1/prod"}, m.deleted)
	})

	t.Run("a PR closed by a human inside the grace window keeps its branch", func(t *testing.T) {
		closedAt := metav1.NewTime(time.Now().Add(-time.Minute))
		prs := openPRStatus("prs", "test/repo", 5)
		prs.Status.Open, prs.Status.ClosedAt = false, &closedAt
		ps := makeStep("step", "nginx-demo", "bundle-1", "prod")
		ps.Spec.PRStatusRef = prs.Name
		ps.Status.State = "WaitingForMerge"
		ps.Status.Outputs = map[string]string{"prURL": "https://github.com/test/repo/pull/5", "prNumber": "5",
			"branch": "kardinal/bundle-1/prod"}
		objs := []client.Object{ps, prs, makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo")}
		c := newClient(t, objs...)
		m := &mockSCM{}
		r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{},
			WorkDirFn: func(_, _ string) string { return t.TempDir() }}

		_, err := r.Reconcile(context.Background(), reqFor("step"))
		require.NoError(t, err)
		assert.Equal(t, "WaitingForMerge", getStep(t, c, "step").Status.State)
		assert.Empty(t, m.closed)
		assert.Empty(t, m.deleted)
	})
}
