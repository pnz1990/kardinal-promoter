// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package prstatus_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/prstatus"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

func apiErr(status int, body string, transient bool) error {
	return fmt.Errorf("get PR status: %w", &scm.APIError{Provider: "GitHub", Method: "GET",
		Path: "/repos/owner/repo/pulls/7", StatusCode: status, Body: body, Transient: transient})
}

// TestPollError proves that a GetPRStatus error a retry cannot fix (401, 403
// that is not a rate limit, 404, 410; scm.IsPermanentError) is recorded in
// status.pollError, so the PromotionStep waiting for the PR can fail with it,
// and is polled again only every 5 minutes instead of every 30 seconds.
// Transient errors keep the 30s retry and record nothing, and the next
// successful poll clears the error.
func TestPollError(t *testing.T) {
	tests := []struct {
		name        string
		pr          *v1alpha1.PRStatus
		err         error
		open        bool
		wantErr     string
		wantRequeue time.Duration
		wantPatched bool
		wantOpen    bool
	}{
		{name: "401 is recorded", pr: prAt(nil, v1alpha1.PRStatusStatus{}),
			err: apiErr(401, `{"message":"Bad credentials"}`, false), wantErr: "status 401: {\"message\":\"Bad credentials\"}",
			wantRequeue: 5 * time.Minute, wantPatched: true},
		{name: "404 after earlier polls keeps the last known state", pr: prAt(ago(time.Minute), v1alpha1.PRStatusStatus{Open: true}),
			err: apiErr(404, `{"message":"Not Found"}`, false), wantErr: "status 404",
			wantRequeue: 5 * time.Minute, wantPatched: true, wantOpen: true},
		{name: "the same error is not patched again", pr: prAt(nil, v1alpha1.PRStatusStatus{
			PollError: `get PR status: GitHub API GET /repos/owner/repo/pulls/7: status 410: gone`}),
			err: apiErr(410, "gone", false), wantErr: "status 410: gone", wantRequeue: 5 * time.Minute},
		{name: "403 rate limit is retried", pr: prAt(nil, v1alpha1.PRStatusStatus{}),
			err: apiErr(403, "API rate limit exceeded", true), wantRequeue: 30 * time.Second},
		{name: "5xx is retried", pr: prAt(nil, v1alpha1.PRStatusStatus{}),
			err: apiErr(502, "bad gateway", true), wantRequeue: 30 * time.Second},
		{name: "network error is retried", pr: prAt(nil, v1alpha1.PRStatusStatus{}),
			err: errors.New("connection reset by peer"), wantRequeue: 30 * time.Second},
		{name: "a successful poll clears the error", pr: prAt(nil, v1alpha1.PRStatusStatus{PollError: "status 401"}),
			open: true, wantRequeue: 30 * time.Second, wantPatched: true, wantOpen: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(tt.pr).
				WithStatusSubresource(&v1alpha1.PRStatus{}).Build()
			key := types.NamespacedName{Name: "pr", Namespace: "default"}
			var before v1alpha1.PRStatus
			require.NoError(t, c.Get(context.Background(), key, &before))
			s := &fakeSCM{open: tt.open, err: tt.err}
			r := &prstatus.Reconciler{Client: c, SCM: s}

			res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
			require.NoError(t, err)

			var after v1alpha1.PRStatus
			require.NoError(t, c.Get(context.Background(), key, &after))
			if tt.wantErr == "" {
				assert.Empty(t, after.Status.PollError)
			} else {
				assert.Contains(t, after.Status.PollError, tt.wantErr)
			}
			assert.Equal(t, tt.wantRequeue, res.RequeueAfter)
			assert.Equal(t, tt.wantPatched, after.ResourceVersion != before.ResourceVersion, "status patched")
			assert.Equal(t, tt.wantOpen, after.Status.Open)
			if tt.err != nil {
				// A set lastCheckedAt with open=false reads as "closed without merging".
				assert.Equal(t, before.Status.LastCheckedAt, after.Status.LastCheckedAt, "a failed poll does not touch lastCheckedAt")
			}
		})
	}

	t.Run("a long error body is truncated", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(prAt(nil, v1alpha1.PRStatusStatus{})).
			WithStatusSubresource(&v1alpha1.PRStatus{}).Build()
		r := &prstatus.Reconciler{Client: c, SCM: &fakeSCM{err: apiErr(404, strings.Repeat("x", 5000), false)}}
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "pr", Namespace: "default"}})
		require.NoError(t, err)
		var after v1alpha1.PRStatus
		require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "pr", Namespace: "default"}, &after))
		assert.Contains(t, after.Status.PollError, "status 404")
		assert.LessOrEqual(t, len(after.Status.PollError), 520)
	})
}
