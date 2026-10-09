// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package prstatus_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/prstatus"
)

// commentSCM is a fakeSCM that records PR comments.
type commentSCM struct {
	fakeSCM
	comments   []string
	commentErr error
}

func (c *commentSCM) CommentOnPR(_ context.Context, _ string, _ int, body string) error {
	c.comments = append(c.comments, body)
	return c.commentErr
}

func metaAgo(d time.Duration) *metav1.Time {
	t := metav1.NewTime(time.Now().Add(-d))
	return &t
}

// TestClosedGraceWindow covers #1306. A PR closed without merging is polled
// for 5 minutes from the status.closedAt the first closed poll writes. A
// reopen or a merge clears closedAt. The first poll at or after closedAt+5m
// comments on the PR once, sets status.closedFinal and stops polling. A
// closed PRStatus without closedAt (an older release) stays final.
func TestClosedGraceWindow(t *testing.T) {
	tests := []struct {
		name       string
		status     v1alpha1.PRStatusStatus
		scmMerged  bool
		scmOpen    bool
		commentErr error

		wantCalls    int
		wantComments int
		wantClosedAt string // "nil", "set" (== lastCheckedAt), "kept"
		wantFinal    bool
		wantOpen     bool
		wantRequeue  func(t *testing.T, d time.Duration)
	}{
		{
			name:         "first closed poll records closedAt and keeps polling",
			status:       v1alpha1.PRStatusStatus{Open: true, LastCheckedAt: metaAgo(time.Minute)},
			wantCalls:    1,
			wantClosedAt: "set",
			wantRequeue:  func(t *testing.T, d time.Duration) { assert.Equal(t, 30*time.Second, d) },
		},
		{
			name:         "still closed inside the window",
			status:       v1alpha1.PRStatusStatus{LastCheckedAt: metaAgo(time.Minute), ClosedAt: metaAgo(2 * time.Minute)},
			wantCalls:    1,
			wantClosedAt: "kept",
			wantRequeue:  func(t *testing.T, d time.Duration) { assert.Equal(t, 30*time.Second, d) },
		},
		{
			name:         "the last poll of the window lands on its end",
			status:       v1alpha1.PRStatusStatus{LastCheckedAt: metaAgo(time.Minute), ClosedAt: metaAgo(4*time.Minute + 50*time.Second)},
			wantCalls:    1,
			wantClosedAt: "kept",
			wantRequeue: func(t *testing.T, d time.Duration) {
				assert.Greater(t, d, time.Duration(0))
				assert.LessOrEqual(t, d, 10*time.Second)
			},
		},
		{
			name:         "reopened inside the window clears closedAt",
			status:       v1alpha1.PRStatusStatus{LastCheckedAt: metaAgo(time.Minute), ClosedAt: metaAgo(2 * time.Minute)},
			scmOpen:      true,
			wantCalls:    1,
			wantClosedAt: "nil",
			wantOpen:     true,
			wantRequeue:  func(t *testing.T, d time.Duration) { assert.Equal(t, 30*time.Second, d) },
		},
		{
			name:         "merged inside the window clears closedAt",
			status:       v1alpha1.PRStatusStatus{LastCheckedAt: metaAgo(time.Minute), ClosedAt: metaAgo(2 * time.Minute)},
			scmMerged:    true,
			wantCalls:    1,
			wantClosedAt: "nil",
			wantRequeue:  func(t *testing.T, d time.Duration) { assert.Zero(t, d) },
		},
		{
			name:         "closed past the window comments once and is final",
			status:       v1alpha1.PRStatusStatus{LastCheckedAt: metaAgo(time.Minute), ClosedAt: metaAgo(5*time.Minute + time.Second)},
			wantCalls:    1,
			wantComments: 1,
			wantClosedAt: "kept",
			wantFinal:    true,
			wantRequeue:  func(t *testing.T, d time.Duration) { assert.Zero(t, d) },
		},
		{
			name:         "a failed comment still makes the PR final",
			status:       v1alpha1.PRStatusStatus{LastCheckedAt: metaAgo(time.Minute), ClosedAt: metaAgo(6 * time.Minute)},
			commentErr:   errors.New("HTTP 403"),
			wantCalls:    1,
			wantComments: 1,
			wantClosedAt: "kept",
			wantFinal:    true,
			wantRequeue:  func(t *testing.T, d time.Duration) { assert.Zero(t, d) },
		},
		{
			name: "final-closed is not polled again",
			status: v1alpha1.PRStatusStatus{LastCheckedAt: metaAgo(time.Minute), ClosedAt: metaAgo(7 * time.Minute),
				ClosedFinal: true},
			wantClosedAt: "kept",
			wantFinal:    true,
			wantRequeue:  func(t *testing.T, d time.Duration) { assert.Zero(t, d) },
		},
		{
			name:         "closed without closedAt from an older release is not polled",
			status:       v1alpha1.PRStatusStatus{LastCheckedAt: metaAgo(time.Hour)},
			wantClosedAt: "nil",
			wantRequeue:  func(t *testing.T, d time.Duration) { assert.Zero(t, d) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := prAt(nil, tt.status)
			pr.Labels = map[string]string{"kardinal.io/environment": "prod"}
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(pr).
				WithStatusSubresource(&v1alpha1.PRStatus{}).Build()
			s := &commentSCM{fakeSCM: fakeSCM{merged: tt.scmMerged, open: tt.scmOpen}, commentErr: tt.commentErr}
			r := &prstatus.Reconciler{Client: c, SCM: s}
			key := types.NamespacedName{Name: "pr", Namespace: "default"}

			res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
			require.NoError(t, err)

			var got v1alpha1.PRStatus
			require.NoError(t, c.Get(context.Background(), key, &got))
			assert.Equal(t, tt.wantCalls, s.calls, "GetPRStatus calls")
			assert.Equal(t, tt.wantFinal, got.Status.ClosedFinal, "status.closedFinal")
			assert.Equal(t, tt.wantOpen, got.Status.Open, "status.open")
			switch tt.wantClosedAt {
			case "nil":
				assert.Nil(t, got.Status.ClosedAt, "status.closedAt")
			case "set":
				require.NotNil(t, got.Status.ClosedAt, "status.closedAt")
				assert.True(t, got.Status.ClosedAt.Equal(got.Status.LastCheckedAt),
					"closedAt is the time of the poll that first saw the PR closed")
			case "kept":
				require.NotNil(t, got.Status.ClosedAt, "status.closedAt")
				assert.WithinDuration(t, tt.status.ClosedAt.Time, got.Status.ClosedAt.Time, time.Second)
			}
			if tt.wantFinal && tt.wantCalls > 0 {
				assert.WithinDuration(t, time.Now(), got.Status.LastCheckedAt.Time, 5*time.Second,
					"the poll that decides the PR is final records its time")
			}
			tt.wantRequeue(t, res.RequeueAfter)
			require.Len(t, s.comments, tt.wantComments)
			if tt.wantComments > 0 {
				assert.Contains(t, s.comments[0], "environment prod")
				assert.Contains(t, s.comments[0], "create a new Bundle")
			}

			// Reconciling a final-closed PR again neither polls nor comments.
			if tt.wantFinal {
				calls := s.calls
				_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
				require.NoError(t, err)
				assert.Equal(t, calls, s.calls, "a final-closed PR is not polled")
				assert.Len(t, s.comments, tt.wantComments, "the comment is posted once")
			}
		})
	}
}

// TestClosedGraceWindow_CommentOnceWhenSaveFails: the "stopped tracking"
// comment is posted only after status.closedFinal is saved. A failed save
// returns an error and the next reconcile polls again (lastCheckedAt was not
// saved either), so commenting before the save posted one comment per failed
// attempt.
func TestClosedGraceWindow_CommentOnceWhenSaveFails(t *testing.T) {
	for _, failures := range []int{0, 1, 3} {
		t.Run(fmt.Sprintf("%d failed saves", failures), func(t *testing.T) {
			pr := prAt(nil, v1alpha1.PRStatusStatus{LastCheckedAt: metaAgo(time.Minute), ClosedAt: metaAgo(6 * time.Minute)})
			pr.Labels = map[string]string{"kardinal.io/environment": "prod"}
			left := failures
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(pr).
				WithStatusSubresource(&v1alpha1.PRStatus{}).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourcePatch: func(ctx context.Context, cl client.Client, sub string, obj client.Object,
						patch client.Patch, opts ...client.SubResourcePatchOption) error {
						if left > 0 {
							left--
							return errors.New("etcdserver: request timed out")
						}
						return cl.SubResource(sub).Patch(ctx, obj, patch, opts...)
					},
				}).Build()
			s := &commentSCM{}
			r := &prstatus.Reconciler{Client: c, SCM: s}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "pr", Namespace: "default"}}

			for i := 0; i < failures; i++ {
				_, err := r.Reconcile(context.Background(), req)
				require.Error(t, err, "attempt %d: the failed save is returned", i+1)
				assert.Empty(t, s.comments, "attempt %d: no comment before closedFinal is saved", i+1)
			}
			_, err := r.Reconcile(context.Background(), req)
			require.NoError(t, err)
			_, err = r.Reconcile(context.Background(), req)
			require.NoError(t, err)

			var got v1alpha1.PRStatus
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.True(t, got.Status.ClosedFinal)
			assert.Len(t, s.comments, 1, "exactly one comment")
			assert.Equal(t, failures+1, s.calls, "polled once per attempt, not after closedFinal is saved")
		})
	}
}

// TestClosedGraceWindow_NoCommentOnPRKardinalClosed covers #1351: a PR that
// kardinal closed itself (the PromotionStep cancel path) already has the
// "kardinal closed this PR" comment, and the step reconciler records that on
// the PRStatus in the closed-by annotation, with the PR number. The end of
// the grace window then posts no second "stopped tracking" comment. An
// annotation that names another PR (the PRStatus now tracks a new PR, B72)
// does not count, and neither does a PR a person closed.
func TestClosedGraceWindow_NoCommentOnPRKardinalClosed(t *testing.T) {
	tests := []struct {
		name         string
		annotation   string
		wantComments int
	}{
		{name: "closed by a person: one stopped-tracking comment", wantComments: 1},
		{name: "closed by kardinal: no second comment", annotation: "7"},
		{name: "annotation for an earlier PR: comment", annotation: "6", wantComments: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := prAt(nil, v1alpha1.PRStatusStatus{LastCheckedAt: metaAgo(time.Minute), ClosedAt: metaAgo(6 * time.Minute)})
			pr.Labels = map[string]string{"kardinal.io/environment": "prod"}
			if tt.annotation != "" {
				pr.Annotations = map[string]string{prstatus.AnnotationClosedByKardinal: tt.annotation}
			}
			require.Equal(t, 7, pr.Spec.PRNumber, "fixture PR number")
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(pr).
				WithStatusSubresource(&v1alpha1.PRStatus{}).Build()
			s := &commentSCM{}
			r := &prstatus.Reconciler{Client: c, SCM: s}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "pr", Namespace: "default"}}
			for i := 0; i < 2; i++ {
				_, err := r.Reconcile(context.Background(), req)
				require.NoError(t, err)
			}
			var got v1alpha1.PRStatus
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.True(t, got.Status.ClosedFinal, "the PR is final-closed either way")
			assert.Len(t, s.comments, tt.wantComments)
		})
	}
}
