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

package prstatus_test

import (
	"context"
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

// TestNewPRClearsOldStatus covers B72: a PromotionStep recreated after its PR
// was closed opens a new PR and points the PRStatus spec at it, which bumps
// metadata.generation. The status of the old PR (closed for good, a poll
// error, approvals) is cleared, and the next reconcile polls the new PR. A
// status written by an older release (observedGeneration 0) is kept.
func TestNewPRClearsOldStatus(t *testing.T) {
	old := v1alpha1.PRStatusStatus{ClosedAt: metaAgo(10 * time.Minute), ClosedFinal: true,
		PollError: "status 404: Not Found", Approved: true, ApprovalCount: 2, ObservedGeneration: 1}

	t.Run("the old PR's status is cleared, then the new PR is polled", func(t *testing.T) {
		prs := prAt(ago(time.Minute), old)
		prs.Generation = 2
		c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
			WithObjects(prs).WithStatusSubresource(prs).Build()
		f := &fakeSCM{open: true}
		r := &prstatus.Reconciler{Client: c, SCM: f}
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: prs.Name, Namespace: prs.Namespace}}

		res, err := r.Reconcile(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{Requeue: true}, res, "the cleared status is polled at once")
		assert.Zero(t, f.calls, "the clear does not poll")
		var got v1alpha1.PRStatus
		require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
		assert.Equal(t, v1alpha1.PRStatusStatus{ObservedGeneration: 2}, got.Status)
		assert.True(t, prstatus.DescribesSpec(&got))

		_, err = r.Reconcile(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, 1, f.calls, "the new PR is polled")
		require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
		assert.True(t, got.Status.Open)
		assert.False(t, got.Status.ClosedFinal)
		assert.NotNil(t, got.Status.LastCheckedAt)
		assert.Equal(t, int64(2), got.Status.ObservedGeneration)
	})

	t.Run("a status written by an older release is kept", func(t *testing.T) {
		legacy := old
		legacy.ObservedGeneration = 0
		prs := prAt(ago(time.Minute), legacy)
		prs.Generation = 2
		c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
			WithObjects(prs).WithStatusSubresource(prs).Build()
		f := &fakeSCM{open: true}
		r := &prstatus.Reconciler{Client: c, SCM: f}
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: prs.Name, Namespace: prs.Namespace}}

		_, err := r.Reconcile(context.Background(), req)
		require.NoError(t, err)
		assert.Zero(t, f.calls, "a final-closed PR is not polled")
		var got v1alpha1.PRStatus
		require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
		assert.True(t, got.Status.ClosedFinal)
		assert.Equal(t, 2, got.Status.ApprovalCount)
	})

	t.Run("a poll records the generation it polled", func(t *testing.T) {
		prs := prAt(nil, v1alpha1.PRStatusStatus{})
		prs.Generation = 3
		c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
			WithObjects(prs).WithStatusSubresource(prs).Build()
		r := &prstatus.Reconciler{Client: c, SCM: &fakeSCM{open: true}}
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: prs.Name, Namespace: prs.Namespace}}

		_, err := r.Reconcile(context.Background(), req)
		require.NoError(t, err)
		var got v1alpha1.PRStatus
		require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
		assert.Equal(t, int64(3), got.Status.ObservedGeneration)
	})
}

// TestLegacyStatusGetsGeneration covers the B72 gap for a status a release
// before observedGeneration wrote (observedGeneration 0) that nothing polls
// or patches any more: a merged PR with its merge commit, or a PR closed for
// good. It kept observedGeneration 0 for ever, so DescribesSpec held for any
// later spec: a PromotionStep recreated after the upgrade that pointed the
// spec at its new PR read the old PR's merged (a false merge, HealthChecking)
// or closedFinal (a false failure). The first reconcile now records the
// generation, with no poll and one patch; the next reconcile patches nothing;
// and a spec that then names a new PR gets its status cleared.
func TestLegacyStatusGetsGeneration(t *testing.T) {
	// Whole seconds: the status is compared after a round trip through JSON.
	hourAgo := metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second))
	tests := []struct {
		name   string
		status v1alpha1.PRStatusStatus
	}{
		{name: "merged with its merge commit", status: v1alpha1.PRStatusStatus{
			Merged: true, MergeCommitSHA: "d7d4d8a", LastCheckedAt: &hourAgo}},
		{name: "closed for good", status: v1alpha1.PRStatusStatus{
			ClosedAt: &hourAgo, ClosedFinal: true, LastCheckedAt: &hourAgo}},
		{name: "closed by a release before the grace window", status: v1alpha1.PRStatusStatus{
			LastCheckedAt: &hourAgo}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prs := prAt(nil, tc.status)
			prs.Generation = 1
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithObjects(prs).WithStatusSubresource(prs).Build()
			f := &fakeSCM{open: true}
			r := &prstatus.Reconciler{Client: c, SCM: f}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: prs.Name, Namespace: prs.Namespace}}

			res, err := r.Reconcile(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{}, res, "nothing left to poll")
			assert.Zero(t, f.calls, "a merged or final-closed PR is not polled")
			var got v1alpha1.PRStatus
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			want := tc.status
			want.ObservedGeneration = 1
			assert.Equal(t, want, got.Status, "the generation is recorded and the rest is kept")

			rv := got.ResourceVersion
			res, err = r.Reconcile(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{}, res)
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Equal(t, rv, got.ResourceVersion, "the second reconcile patches nothing")
			assert.Zero(t, f.calls)

			// A recreated PromotionStep points the spec at its new PR.
			got.Spec.PRNumber, got.Spec.PRURL = 8, "https://github.com/owner/repo/pull/8"
			got.Generation = 2
			require.NoError(t, c.Update(context.Background(), &got))
			res, err = r.Reconcile(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{Requeue: true}, res, "the cleared status is polled at once")
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Equal(t, v1alpha1.PRStatusStatus{ObservedGeneration: 2}, got.Status,
				"the old PR's status says nothing about the new PR")
		})
	}
}
