// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// autoMergeSCM is a mockSCM that also implements scm.PRController and
// records EnableAutoMerge and DisableAutoMerge.
type autoMergeSCM struct {
	mockSCM
	mu         sync.Mutex
	enables    []scm.MergeOptions
	disables   int
	enableErrs []error // returned in order; nil entries succeed
}

func (m *autoMergeSCM) PRSupport() scm.PRSupport {
	return scm.PRSupport{Provider: "fake", AutoMerge: true}
}
func (m *autoMergeSCM) RequestReviewers(context.Context, string, int, []string, []string) error {
	return nil
}
func (m *autoMergeSCM) AddAssignees(context.Context, string, int, []string) error { return nil }
func (m *autoMergeSCM) EnableAutoMerge(_ context.Context, _ string, _ int, opts scm.MergeOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enables = append(m.enables, opts)
	if len(m.enableErrs) > 0 {
		err := m.enableErrs[0]
		m.enableErrs = m.enableErrs[1:]
		return err
	}
	return nil
}
func (m *autoMergeSCM) DisableAutoMerge(context.Context, string, int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disables++
	return nil
}

// waitingAutoMergeStep is a prod step waiting for PR #5 with auto-merge
// pending, behind gate "soak".
func waitingAutoMergeStep() (*v1alpha1.PromotionStep, *v1alpha1.PRStatus, *v1alpha1.PolicyGate) {
	prs := openPRStatus("prs", "test/repo", 5)
	ps := makeStep("step", "nginx-demo", "bundle-1", "prod")
	ps.Spec.PRStatusRef = prs.Name
	ps.Spec.RequiredGates = []string{"soak"}
	ps.Status.State = "WaitingForMerge"
	ps.Status.Message = "PR #5 is open, waiting for merge"
	ps.Status.Outputs = map[string]string{"prURL": "https://github.com/test/repo/pull/5", "prNumber": "5",
		"prAutoMerge": "pending", "prMergeOptions": `{"method":"squash","commitTitle":"deploy (#5)"}`}
	gate := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "soak", Namespace: "default"},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true"}, Status: v1alpha1.PolicyGateStatus{Ready: true}}
	return ps, prs, gate
}

// TestAutoMerge_FollowsPauseAndGates covers the QA finding on #1477: kardinal
// turns the SCM's auto-merge on while the step waits for its PR, off while
// the Pipeline is paused or a required gate is closed, and on again when
// they clear; each reconcile without a change calls the SCM no more.
// Covers SCM-PRCTL-ERR-01.
func TestAutoMerge_FollowsPauseAndGates(t *testing.T) {
	ctx := context.Background()
	ps, prs, gate := waitingAutoMergeStep()
	c := newClient(t, ps, prs, gate, makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo"))
	m := &autoMergeSCM{mockSCM: mockSCM{open: true}}
	r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{}, WorkDirFn: func(_, _ string) string { return t.TempDir() }}

	reconcileStep(t, r, "step")
	got := getStep(t, c, "step")
	assert.Equal(t, "enabled", got.Status.Outputs["prAutoMerge"])
	assert.Equal(t, "PR #5 is open, waiting for merge; auto-merge enabled", got.Status.Message)
	require.Len(t, m.enables, 1)
	assert.Equal(t, scm.MergeOptions{Method: "squash", CommitTitle: "deploy (#5)"}, m.enables[0])
	reconcileStep(t, r, "step")
	assert.Len(t, m.enables, 1, "idempotent")

	// Pause: off.
	require.NoError(t, lifecycle.Pause(ctx, c, "default", "nginx-demo"))
	reconcileStep(t, r, "step")
	got = getStep(t, c, "step")
	assert.Equal(t, "suspended", got.Status.Outputs["prAutoMerge"])
	assert.Equal(t, 1, m.disables)
	assert.Equal(t, "PR #5 is open, waiting for merge; auto-merge off: pipeline nginx-demo is paused", got.Status.Message)
	reconcileStep(t, r, "step")
	assert.Equal(t, 1, m.disables, "idempotent")

	// Resume: on again.
	require.NoError(t, lifecycle.Resume(ctx, c, "default", "nginx-demo"))
	reconcileStep(t, r, "step")
	assert.Equal(t, "enabled", getStep(t, c, "step").Status.Outputs["prAutoMerge"])
	assert.Len(t, m.enables, 2)

	// A required gate closes (a freeze, a soak): off; it reopens: on.
	var g v1alpha1.PolicyGate
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(gate), &g))
	g.Status.Ready = false
	require.NoError(t, c.Update(ctx, &g))
	reconcileStep(t, r, "step")
	got = getStep(t, c, "step")
	assert.Equal(t, "suspended", got.Status.Outputs["prAutoMerge"])
	assert.Equal(t, "PR #5 is open, waiting for merge; auto-merge off: gate soak is closed", got.Status.Message)
	assert.Equal(t, 2, m.disables)
	g.Status.Ready = true
	require.NoError(t, c.Update(ctx, &g))
	reconcileStep(t, r, "step")
	assert.Equal(t, "enabled", getStep(t, c, "step").Status.Outputs["prAutoMerge"])
	assert.Len(t, m.enables, 3)
}

// TestAutoMerge_RetriesAndFailures: a retryable SCM answer is retried with
// a backoff recorded in status.outputs (no sleep in the reconcile), up to a
// limit; ErrNothingPending is not retried, and the PR waits for a merge by
// hand with the reason in the message. Covers SCM-PRCTL-ERR-01.
func TestAutoMerge_RetriesAndFailures(t *testing.T) {
	t.Run("retry then on", func(t *testing.T) {
		ps, prs, gate := waitingAutoMergeStep()
		c := newClient(t, ps, prs, gate, makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo"))
		m := &autoMergeSCM{mockSCM: mockSCM{open: true}, enableErrs: []error{scm.ErrMergeabilityUnknown}}
		now := time.Now()
		r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{},
			WorkDirFn: func(_, _ string) string { return t.TempDir() }, NowFn: func() time.Time { return now }}

		res, err := r.Reconcile(context.Background(), reqFor("step"))
		require.NoError(t, err)
		assert.Equal(t, 5*time.Second, res.RequeueAfter, "requeued for the retry, not slept")
		got := getStep(t, c, "step")
		assert.Equal(t, "pending", got.Status.Outputs["prAutoMerge"])
		assert.Equal(t, "1", got.Status.Outputs["prAutoMergeAttempts"])
		assert.Contains(t, got.Status.Message, "auto-merge pending: ")
		reconcileStep(t, r, "step")
		assert.Len(t, m.enables, 1, "not before the retry time")
		now = now.Add(6 * time.Second)
		reconcileStep(t, r, "step")
		assert.Len(t, m.enables, 2)
		got = getStep(t, c, "step")
		assert.Equal(t, "enabled", got.Status.Outputs["prAutoMerge"])
		assert.NotContains(t, got.Status.Outputs, "prAutoMergeAttempts")
	})

	t.Run("nothing pending", func(t *testing.T) {
		ps, prs, gate := waitingAutoMergeStep()
		c := newClient(t, ps, prs, gate, makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo"))
		m := &autoMergeSCM{mockSCM: mockSCM{open: true}, enableErrs: []error{scm.ErrNothingPending}}
		r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{}, WorkDirFn: func(_, _ string) string { return t.TempDir() }}
		reconcileStep(t, r, "step")
		got := getStep(t, c, "step")
		assert.Equal(t, "failed", got.Status.Outputs["prAutoMerge"])
		assert.Contains(t, got.Status.Message, "; auto-merge failed: nothing is pending on the PR")
		assert.Equal(t, "WaitingForMerge", got.Status.State, "the PR waits for a merge by hand")
		reconcileStep(t, r, "step")
		assert.Len(t, m.enables, 1, "not retried")
	})

	t.Run("retries run out", func(t *testing.T) {
		ps, prs, gate := waitingAutoMergeStep()
		c := newClient(t, ps, prs, gate, makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo"))
		errs := make([]error, 20)
		for i := range errs {
			errs[i] = &scm.APIError{StatusCode: 502, Transient: true, Provider: "fake"}
		}
		m := &autoMergeSCM{mockSCM: mockSCM{open: true}, enableErrs: errs}
		now := time.Now()
		r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{},
			WorkDirFn: func(_, _ string) string { return t.TempDir() }, NowFn: func() time.Time { return now }}
		for i := 0; i < 12; i++ {
			reconcileStep(t, r, "step")
			now = now.Add(3 * time.Minute)
		}
		assert.Len(t, m.enables, 8)
		assert.Equal(t, "failed", getStep(t, c, "step").Status.Outputs["prAutoMerge"])
	})
}

// TestAutoMerge_StaleKeysCleared: a reconcile from a stale cache can leave
// the error of an earlier attempt next to prAutoMerge enabled; the next
// reconcile clears it and the message says auto-merge is enabled.
func TestAutoMerge_StaleKeysCleared(t *testing.T) {
	ps, prs, gate := waitingAutoMergeStep()
	ps.Status.Outputs["prAutoMerge"] = "enabled"
	ps.Status.Outputs["prAutoMergeError"] = "status 409: already scheduled"
	ps.Status.Outputs["prAutoMergeAttempts"] = "1"
	ps.Status.Message = "PR #5 is open, waiting for merge; auto-merge pending: status 409: already scheduled"
	c := newClient(t, ps, prs, gate, makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo"))
	m := &autoMergeSCM{mockSCM: mockSCM{open: true}}
	r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{}, WorkDirFn: func(_, _ string) string { return t.TempDir() }}
	reconcileStep(t, r, "step")
	got := getStep(t, c, "step")
	assert.Equal(t, "PR #5 is open, waiting for merge; auto-merge enabled", got.Status.Message)
	assert.NotContains(t, got.Status.Outputs, "prAutoMergeError")
	assert.NotContains(t, got.Status.Outputs, "prAutoMergeAttempts")
	assert.Empty(t, m.enables)
}
