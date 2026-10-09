// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// TestRejectionGuard cancels the steps of a rejected Bundle that have not
// delivered the change: a WaitingForMerge step closes its PR with a comment
// naming who rejected the Bundle and fails with a PromotionRejected
// AuditEvent; a Pending step fails without starting and without an
// AuditEvent. A HealthChecking step is not cancelled: its change is live.
// spec.rejected alone is enough, before the Bundle reconciler writes the
// Rejected phase. A second reconcile changes nothing (idempotent).
func TestRejectionGuard(t *testing.T) {
	rejected := &v1alpha1.BundleRejection{By: "alice", Reason: "bad build"}
	cases := []struct {
		name      string
		phase     string
		state     string
		wantState string
		wantMsg   string
		wantClose bool
		wantAudit []string
	}{
		{name: "waiting for merge, phase written", phase: "Rejected", state: "WaitingForMerge", wantState: "Failed",
			wantMsg: "bundle app-v1 was rejected — promotion cancelled", wantClose: true, wantAudit: []string{"PromotionRejected"}},
		{name: "waiting for merge, spec only", phase: "Promoting", state: "WaitingForMerge", wantState: "Failed",
			wantMsg: "bundle app-v1 was rejected — promotion cancelled", wantClose: true, wantAudit: []string{"PromotionRejected"}},
		{name: "pending", phase: "Rejected", state: "", wantState: "Failed",
			wantMsg: "bundle app-v1 was rejected before this step started"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bundle := makeBundle("app-v1", "app")
			bundle.Spec.Rejected = rejected
			bundle.Status.Phase = tc.phase
			step := &v1alpha1.PromotionStep{
				ObjectMeta: metav1.ObjectMeta{Name: "app-app-v1-prod", Namespace: "default",
					Labels: map[string]string{"kardinal.io/bundle": "app-v1", "kardinal.io/pipeline": "app", "kardinal.io/environment": "prod"}},
				Spec:   v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "app-v1", Environment: "prod"},
				Status: v1alpha1.PromotionStepStatus{State: tc.state},
			}
			if tc.state == "WaitingForMerge" {
				step.Status.Outputs = map[string]string{"prURL": "https://github.com/org/repo/pull/7"}
			}
			mock := &mockSCM{open: true}
			git := &mockGit{}
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithObjects(step, bundle, makePipeline("app")).WithStatusSubresource(step).Build()
			r := promotionstep.Reconciler{Client: c, SCM: mock, GitClient: git,
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: step.Name, Namespace: "default"}}

			for range 2 {
				_, err := r.Reconcile(context.Background(), req)
				require.NoError(t, err)
			}
			var got v1alpha1.PromotionStep
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Equal(t, tc.wantState, got.Status.State)
			assert.Equal(t, tc.wantMsg, got.Status.Message)
			if tc.wantClose {
				assert.Equal(t, []string{"org/repo#7"}, mock.closed, "the rejected step's PR is closed once")
				require.Len(t, mock.comments, 1)
				assert.Contains(t, mock.comments[0], "bundle app-v1 was rejected by alice")
			} else {
				assert.Empty(t, mock.closed)
			}
			var audits v1alpha1.AuditEventList
			require.NoError(t, c.List(context.Background(), &audits))
			var actions []string
			for _, a := range audits.Items {
				actions = append(actions, a.Spec.Action)
			}
			assert.Equal(t, tc.wantAudit, actions)
		})
	}
}

// TestRejectionGuard_HealthCheckingContinues: a rejected Bundle's step that
// already delivered the change keeps health-checking; reject does not cancel
// or revert it.
func TestRejectionGuard_HealthCheckingContinues(t *testing.T) {
	bundle := makeBundle("app-v1", "app")
	bundle.Spec.Rejected = &v1alpha1.BundleRejection{By: "alice", Reason: "bad build"}
	bundle.Status.Phase = "Rejected"
	step := makeStep("app-app-v1-prod", "app", "app-v1", "prod")
	step.Status.State = "HealthChecking"
	mock := &mockSCM{open: true}
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithObjects(step, bundle, makePipeline("app")).WithStatusSubresource(step).Build()
	r := promotionstep.Reconciler{Client: c, SCM: mock, GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}
	_, _ = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: step.Name, Namespace: "default"}})
	var got v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: step.Name, Namespace: "default"}, &got))
	assert.NotContains(t, got.Status.Message, "rejected")
	assert.NotEqual(t, "Failed", got.Status.State)
	assert.Empty(t, mock.closed)
}
