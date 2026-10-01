// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package prstatus_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/prstatus"
)

// mergeSCM is a fakeSCM that also reports merge commits.
type mergeSCM struct {
	fakeSCM
	sha        string
	shaErr     error
	shaCalls   int
	lastPRRepo string
}

func (m *mergeSCM) GetPRMergeCommit(_ context.Context, repo string, _ int) (string, error) {
	m.shaCalls++
	m.lastPRRepo = repo
	return m.sha, m.shaErr
}

func prAt(lastChecked *time.Time, st v1alpha1.PRStatusStatus) *v1alpha1.PRStatus {
	if lastChecked != nil {
		t := metav1.NewTime(*lastChecked)
		st.LastCheckedAt = &t
	}
	return &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: "pr", Namespace: "default"},
		Spec:       v1alpha1.PRStatusSpec{PRURL: "https://github.com/owner/repo/pull/7", PRNumber: 7, Repo: "owner/repo"},
		Status:     st,
	}
}

func ago(d time.Duration) *time.Time {
	t := time.Now().Add(-d)
	return &t
}

// TestPollThrottle proves C03-promotionstep-20: a poll must not re-trigger
// the next one. Reconciles within the poll interval of the last recorded
// check do not call the SCM, and a poll that changes nothing does not patch
// the status (every patch is a watch event that re-enqueues the object).
func TestPollThrottle(t *testing.T) {
	tests := []struct {
		name        string
		pr          *v1alpha1.PRStatus
		scmOpen     bool
		wantCalls   int
		wantPatched bool
		wantRequeue func(t *testing.T, d time.Duration)
	}{
		{name: "first poll patches", pr: prAt(nil, v1alpha1.PRStatusStatus{}), scmOpen: true,
			wantCalls: 1, wantPatched: true,
			wantRequeue: func(t *testing.T, d time.Duration) { assert.Equal(t, 30*time.Second, d) }},
		{name: "reconcile right after a poll is throttled", pr: prAt(ago(2*time.Second), v1alpha1.PRStatusStatus{Open: true}),
			scmOpen: true, wantCalls: 0,
			wantRequeue: func(t *testing.T, d time.Duration) {
				assert.Greater(t, d, 20*time.Second)
				assert.LessOrEqual(t, d, 30*time.Second)
			}},
		{name: "unchanged poll does not patch", pr: prAt(ago(time.Minute), v1alpha1.PRStatusStatus{Open: true}),
			scmOpen: true, wantCalls: 1,
			wantRequeue: func(t *testing.T, d time.Duration) { assert.Equal(t, 30*time.Second, d) }},
		{name: "unchanged poll refreshes a stale lastCheckedAt", pr: prAt(ago(6*time.Minute), v1alpha1.PRStatusStatus{Open: true}),
			scmOpen: true, wantCalls: 1, wantPatched: true,
			wantRequeue: func(t *testing.T, d time.Duration) { assert.Equal(t, 30*time.Second, d) }},
		{name: "changed poll patches", pr: prAt(ago(time.Minute), v1alpha1.PRStatusStatus{Open: true}),
			scmOpen: false, wantCalls: 1, wantPatched: true,
			// A closed PR is polled through its grace window (#1306).
			wantRequeue: func(t *testing.T, d time.Duration) { assert.Equal(t, 30*time.Second, d) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(tt.pr).
				WithStatusSubresource(&v1alpha1.PRStatus{}).Build()
			var before v1alpha1.PRStatus
			require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "pr", Namespace: "default"}, &before))
			s := &fakeSCM{open: tt.scmOpen}
			r := &prstatus.Reconciler{Client: c, SCM: s}

			res, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "pr", Namespace: "default"}})
			require.NoError(t, err)

			var after v1alpha1.PRStatus
			require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "pr", Namespace: "default"}, &after))
			assert.Equal(t, tt.wantCalls, s.calls, "GetPRStatus calls")
			assert.Equal(t, tt.wantPatched, after.ResourceVersion != before.ResourceVersion, "status patched")
			tt.wantRequeue(t, res.RequeueAfter)
		})
	}
}

// TestMergeCommitRecorded proves the PRStatus half of E2E-01: the merge
// commit is recorded, whether this reconciler or the webhook saw the merge.
// When the reconciler stops asking for it, it says so in
// status.mergeCommitUnavailable, which the argocd health check waits for
// (B80): the provider answered without a commit, or still failed after the
// window. A failed lookup within the window is retried and sets nothing.
func TestMergeCommitRecorded(t *testing.T) {
	tests := []struct {
		name            string
		pr              *v1alpha1.PRStatus
		sha             string
		shaErr          error
		wantSHA         string
		wantUnavailable bool
		wantCalls       int // GetPRStatus
		wantSHACall     int
		wantRequeue     bool
	}{
		{name: "poll sees the merge", pr: prAt(nil, v1alpha1.PRStatusStatus{Open: true}),
			sha: "abc123", wantSHA: "abc123", wantCalls: 1, wantSHACall: 1},
		{name: "poll sees the merge, provider reports no merge commit", pr: prAt(nil, v1alpha1.PRStatusStatus{Open: true}),
			wantUnavailable: true, wantCalls: 1, wantSHACall: 1},
		{name: "poll sees the merge, lookup fails: retried", pr: prAt(nil, v1alpha1.PRStatusStatus{Open: true}),
			shaErr: errors.New("502"), wantCalls: 1, wantSHACall: 1, wantRequeue: true},
		{name: "webhook recorded the merge", pr: prAt(ago(time.Second), v1alpha1.PRStatusStatus{Merged: true}),
			sha: "abc123", wantSHA: "abc123", wantSHACall: 1},
		{name: "webhook recorded the merge, provider reports no merge commit", pr: prAt(ago(time.Second), v1alpha1.PRStatusStatus{Merged: true}),
			wantUnavailable: true, wantSHACall: 1},
		{name: "SCM error is retried", pr: prAt(ago(time.Second), v1alpha1.PRStatusStatus{Merged: true}),
			shaErr: errors.New("502"), wantSHACall: 1, wantRequeue: true},
		{name: "gives up after the window", pr: prAt(ago(11*time.Minute), v1alpha1.PRStatusStatus{Merged: true}),
			sha: "abc123", wantUnavailable: true},
		{name: "known merge commit is a no-op", pr: prAt(ago(time.Second), v1alpha1.PRStatusStatus{Merged: true, MergeCommitSHA: "def456"}),
			sha: "abc123", wantSHA: "def456"},
		{name: "recorded unavailable is a no-op", pr: prAt(ago(time.Second), v1alpha1.PRStatusStatus{Merged: true, MergeCommitUnavailable: true}),
			sha: "abc123", wantUnavailable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(tt.pr).
				WithStatusSubresource(&v1alpha1.PRStatus{}).Build()
			s := &mergeSCM{fakeSCM: fakeSCM{merged: true}, sha: tt.sha, shaErr: tt.shaErr}
			r := &prstatus.Reconciler{Client: c, SCM: s}

			res, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "pr", Namespace: "default"}})
			require.NoError(t, err)

			var after v1alpha1.PRStatus
			require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "pr", Namespace: "default"}, &after))
			assert.True(t, after.Status.Merged)
			assert.Equal(t, tt.wantSHA, after.Status.MergeCommitSHA)
			assert.Equal(t, tt.wantUnavailable, after.Status.MergeCommitUnavailable, "status.mergeCommitUnavailable")
			assert.Equal(t, tt.wantCalls, s.calls, "GetPRStatus calls")
			assert.Equal(t, tt.wantSHACall, s.shaCalls, "GetPRMergeCommit calls")
			assert.Equal(t, tt.wantRequeue, res.RequeueAfter > 0)
			if tt.wantSHACall > 0 {
				assert.Equal(t, "owner/repo", s.lastPRRepo)
			}

			// Idempotent: once the outcome is recorded, a second reconcile
			// neither asks the SCM again nor patches the status.
			if tt.wantRequeue {
				return
			}
			s.calls, s.shaCalls = 0, 0
			res, err = r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "pr", Namespace: "default"}})
			require.NoError(t, err)
			var again v1alpha1.PRStatus
			require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "pr", Namespace: "default"}, &again))
			assert.Equal(t, after.ResourceVersion, again.ResourceVersion, "second reconcile patched the status")
			assert.Zero(t, s.calls+s.shaCalls, "second reconcile called the SCM")
			assert.Zero(t, res.RequeueAfter)
		})
	}
}

// TestMergeCommitUnavailableWithoutLookup: when the SCM provider cannot
// report merge commits (or none is configured), the merge commit will never
// be known, so status.mergeCommitUnavailable is set with the merge, or at the
// first reconcile of a PRStatus a webhook (or an older release) marked
// merged. The argocd health check then does not wait for it (B80).
func TestMergeCommitUnavailableWithoutLookup(t *testing.T) {
	tests := []struct {
		name string
		pr   *v1alpha1.PRStatus
		scm  bool
	}{
		{name: "poll sees the merge", pr: prAt(nil, v1alpha1.PRStatusStatus{Open: true}), scm: true},
		{name: "webhook recorded the merge", pr: prAt(ago(time.Second), v1alpha1.PRStatusStatus{Merged: true}), scm: true},
		{name: "merged before the field existed", pr: prAt(ago(time.Hour), v1alpha1.PRStatusStatus{Merged: true}), scm: true},
		{name: "no SCM configured", pr: prAt(ago(time.Second), v1alpha1.PRStatusStatus{Merged: true})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(tt.pr).
				WithStatusSubresource(&v1alpha1.PRStatus{}).Build()
			r := &prstatus.Reconciler{Client: c}
			if tt.scm {
				r.SCM = &fakeSCM{merged: true} // no GetPRMergeCommit
			}
			res, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "pr", Namespace: "default"}})
			require.NoError(t, err)
			assert.Zero(t, res.RequeueAfter)

			var after v1alpha1.PRStatus
			require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "pr", Namespace: "default"}, &after))
			assert.True(t, after.Status.Merged)
			assert.Empty(t, after.Status.MergeCommitSHA)
			assert.True(t, after.Status.MergeCommitUnavailable, "status.mergeCommitUnavailable")
		})
	}
}
