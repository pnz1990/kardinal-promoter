// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestCircuitOpen_DoesNotSpendRetries covers #1476: a step whose open-pr
// meets an open SCM circuit waits until the circuit lets a call through and
// runs again, without counting a retry, however long the outage: 30 waits
// leave all five retries for real failures.
func TestCircuitOpen_DoesNotSpendRetries(t *testing.T) {
	pipeline := makePipeline("nginx-demo")
	ps := asPromoting(makeStep("step-cw", "nginx-demo", "b1", "prod"), pipeline)
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
		WithObjects(ps, pipeline, makeBundle("b1", "nginx-demo")).Build()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mock := &mockSCM{prURL: "https://github.com/org/repo/pull/7", prNumber: 7}
	workDir := filepath.Join(t.TempDir(), "w")
	r := &promotionstep.Reconciler{Client: c, SCM: mock, GitClient: &mockGit{},
		Recorder: events.NewFakeRecorder(50), WorkDirFn: func(_, _ string) string { return workDir },
		NowFn: func() time.Time { return now }}
	reconcile := func() time.Duration {
		t.Helper()
		res, err := r.Reconcile(context.Background(), reqFor("step-cw"))
		require.NoError(t, err)
		return res.RequeueAfter
	}
	circuit := func(until time.Time) error {
		return fmt.Errorf("open PR: forgejo scm: %w", &scm.ErrCircuitOpen{RetryAfter: until})
	}

	for i := 0; i < 30; i++ {
		mock.openPRErr = circuit(now.Add(7 * time.Second))
		require.Equal(t, 7*time.Second, reconcile(), "wait %d: requeue when the circuit lets a call through", i)
		got := getStep(t, c, "step-cw")
		require.Equal(t, "Promoting", got.Status.State, got.Status.Message)
		require.Zero(t, got.Status.RetryCount, "an open circuit is not a retry")
		require.Contains(t, got.Status.Message, "not counted as a retry")
		now = now.Add(7 * time.Second)
	}
	require.Equal(t, 30, mock.openCalled)

	// A circuit open for an hour (a rate-limit reset) is looked at again at
	// least every retryMaxDelay.
	mock.openPRErr = circuit(now.Add(time.Hour))
	assert.Equal(t, 2*time.Minute, reconcile())
	now = now.Add(2 * time.Minute)

	// The outage turns into a real failure: that counts.
	mock.openPRErr = fmt.Errorf("open PR: %w", errors.New("503 Service Unavailable"))
	assert.Equal(t, 10*time.Second, reconcile())
	assert.Equal(t, 1, getStep(t, c, "step-cw").Status.RetryCount)
	now = now.Add(10 * time.Second)

	mock.openPRErr, mock.open = nil, true
	reconcile()
	got := getStep(t, c, "step-cw")
	assert.Equal(t, "WaitingForMerge", got.Status.State, got.Status.Message)
}

// TestCircuitOpen_SupersededCloseWaits covers the branches #1476 left
// behind: a superseded step whose PR close meets an open circuit waits for
// it without spending its close retries, then closes the PR.
func TestCircuitOpen_SupersededCloseWaits(t *testing.T) {
	bundle := makeBundle("old", "my-pipeline")
	bundle.Status.Phase = "Superseded"
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "old-prod", Namespace: "default"},
		Spec:       v1alpha1.PromotionStepSpec{PipelineName: "my-pipeline", BundleName: "old", Environment: "prod"},
		Status: v1alpha1.PromotionStepStatus{State: "WaitingForMerge",
			Outputs: map[string]string{"prURL": "https://github.com/org/repo/pull/42"}},
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var errs []error
	for i := 0; i < 12; i++ {
		errs = append(errs, fmt.Errorf("close PR: %w", &scm.ErrCircuitOpen{RetryAfter: now.Add(time.Duration(i+1) * 20 * time.Second)}))
	}
	mock := &mockSCM{open: true, closeErrs: errs}
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(step, bundle, makePipeline("my-pipeline")).
		WithStatusSubresource(step).Build()
	r := promotionstep.Reconciler{Client: c, SCM: mock, GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() }, NowFn: func() time.Time { return now }}

	for i := 0; i < 12; i++ {
		res, err := r.Reconcile(context.Background(), reqFor("old-prod"))
		require.NoError(t, err)
		require.Equal(t, 20*time.Second, res.RequeueAfter, "wait %d", i)
		got := getStep(t, c, "old-prod")
		require.Equal(t, "WaitingForMerge", got.Status.State, got.Status.Message)
		require.Zero(t, got.Status.RetryCount)
		now = now.Add(20 * time.Second)
	}
	_, err := r.Reconcile(context.Background(), reqFor("old-prod"))
	require.NoError(t, err)
	got := getStep(t, c, "old-prod")
	assert.Equal(t, "Failed", got.Status.State)
	assert.NotContains(t, got.Status.Message, "by hand", "the PR was closed after the outage")
	assert.Len(t, mock.closed, 13)
	assert.False(t, mock.open)
}
