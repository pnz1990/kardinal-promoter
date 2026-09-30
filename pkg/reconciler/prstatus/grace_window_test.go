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
