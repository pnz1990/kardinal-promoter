// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// prDurationSamples returns the sample count and sum of kardinal_pr_duration_seconds.
func prDurationSamples(t *testing.T) (uint64, float64) {
	t.Helper()
	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(observability.PRDurationSeconds))
	families, err := reg.Gather()
	require.NoError(t, err)
	require.Len(t, families, 1)
	h := families[0].GetMetric()[0].GetHistogram()
	return h.GetSampleCount(), h.GetSampleSum()
}

// TestWaitingForMerge_PRDurationFromPROpening: kardinal_pr_duration_seconds
// measures from the PR opening (the open-pr step's completedAt) to the merge,
// not from the PRStatus creation. The Graph creates the PRStatus with the
// Bundle, so its age also counts the upstream environments and the gates.
func TestWaitingForMerge_PRDurationFromPROpening(t *testing.T) {
	ago := func(d time.Duration) *metav1.Time { mt := metav1.NewTime(time.Now().Add(-d)); return &mt }
	tests := []struct {
		name      string
		steps     []v1alpha1.StepStatus
		wantCount uint64
		wantSum   float64
	}{
		{name: "from the open-pr completion",
			steps: []v1alpha1.StepStatus{
				{Name: "open-pr", State: v1alpha1.StepExecutionCompleted, StartedAt: ago(91 * time.Second), CompletedAt: ago(90 * time.Second)},
				{Name: "wait-for-merge", State: v1alpha1.StepExecutionInProgress, StartedAt: ago(89 * time.Second)},
			},
			wantCount: 1, wantSum: 90},
		{name: "from the wait-for-merge start",
			steps: []v1alpha1.StepStatus{
				{Name: "open-pr", State: v1alpha1.StepExecutionPending},
				{Name: "wait-for-merge", State: v1alpha1.StepExecutionInProgress, StartedAt: ago(30 * time.Second)},
			},
			wantCount: 1, wantSum: 30},
		{name: "no step times: not observed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			step := makeStep("step-prd", "nginx-demo", "bundle-1", "prod")
			step.Spec.PRStatusRef = "prstatus-step-prd"
			step.Status.State = "WaitingForMerge"
			step.Status.PRURL = "https://github.com/test/repo/pull/5"
			step.Status.Outputs = map[string]string{"prNumber": "5", "prURL": step.Status.PRURL}
			step.Status.Steps = tt.steps
			prStatus := &v1alpha1.PRStatus{
				// Created with the Graph, an hour before the merge.
				ObjectMeta: metav1.ObjectMeta{Name: "prstatus-step-prd", Namespace: "default", CreationTimestamp: *ago(time.Hour)},
				Spec:       v1alpha1.PRStatusSpec{PRURL: step.Status.PRURL, PRNumber: 5, Repo: "test/repo"},
				Status:     v1alpha1.PRStatusStatus{Merged: true},
			}
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithStatusSubresource(
				&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}, &v1alpha1.PRStatus{},
			).WithObjects(step, makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo"), prStatus).Build()
			r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}

			count, sum := prDurationSamples(t)
			_, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "step-prd", Namespace: "default"}})
			require.NoError(t, err)
			var got v1alpha1.PromotionStep
			require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-prd", Namespace: "default"}, &got))
			require.Equal(t, "HealthChecking", got.Status.State)

			afterCount, afterSum := prDurationSamples(t)
			assert.Equal(t, tt.wantCount, afterCount-count, "observations")
			assert.InDelta(t, tt.wantSum, afterSum-sum, 5, "observed seconds")
		})
	}
}

// TestWaitingForMerge_PRDurationObservedOncePatched: kardinal_pr_duration_seconds
// is observed once the move to HealthChecking is patched. A failed patch is
// retried by the next reconcile, which observes it then, once; a step
// deleted while reconciling does not transition and is not observed.
func TestWaitingForMerge_PRDurationObservedOncePatched(t *testing.T) {
	ago := func(d time.Duration) *metav1.Time { mt := metav1.NewTime(time.Now().Add(-d)); return &mt }
	for _, tt := range []struct {
		name      string
		notFound  bool
		wantCount uint64
	}{
		{name: "the first patch fails, the retry works", wantCount: 1},
		{name: "the step is deleted while reconciling", notFound: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			step := makeStep("step-prd", "nginx-demo", "bundle-1", "prod")
			step.Spec.PRStatusRef = "prstatus-step-prd"
			step.Status.State = "WaitingForMerge"
			step.Status.PRURL = "https://github.com/test/repo/pull/5"
			step.Status.Outputs = map[string]string{"prNumber": "5", "prURL": step.Status.PRURL}
			step.Status.Steps = []v1alpha1.StepStatus{
				{Name: "open-pr", State: v1alpha1.StepExecutionCompleted, StartedAt: ago(91 * time.Second), CompletedAt: ago(90 * time.Second)},
				{Name: "wait-for-merge", State: v1alpha1.StepExecutionInProgress, StartedAt: ago(89 * time.Second)},
			}
			prStatus := &v1alpha1.PRStatus{
				ObjectMeta: metav1.ObjectMeta{Name: "prstatus-step-prd", Namespace: "default"},
				Spec:       v1alpha1.PRStatusSpec{PRURL: step.Status.PRURL, PRNumber: 5, Repo: "test/repo"},
				Status:     v1alpha1.PRStatusStatus{Merged: true},
			}
			patches := 0
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithStatusSubresource(
				&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}, &v1alpha1.PRStatus{},
			).WithObjects(step, makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo"), prStatus).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object,
						patch client.Patch, opts ...client.SubResourcePatchOption) error {
						if _, ok := obj.(*v1alpha1.PromotionStep); ok {
							patches++
							if tt.notFound {
								return apierrors.NewNotFound(schema.GroupResource{Group: "kardinal.io", Resource: "promotionsteps"}, obj.GetName())
							}
							if patches == 1 {
								return errors.New("etcdserver: request timed out")
							}
						}
						return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
					},
				}).Build()
			r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-prd", Namespace: "default"}}

			count, _ := prDurationSamples(t)
			_, err := r.Reconcile(context.Background(), req)
			if tt.notFound {
				require.NoError(t, err)
			} else {
				require.Error(t, err, "the first patch fails")
				_, err = r.Reconcile(context.Background(), req)
				require.NoError(t, err)
				var got v1alpha1.PromotionStep
				require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
				require.Equal(t, "HealthChecking", got.Status.State)
			}
			afterCount, _ := prDurationSamples(t)
			assert.Equal(t, tt.wantCount, afterCount-count, "observations")
		})
	}
}
