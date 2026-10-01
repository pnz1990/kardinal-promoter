// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// prStep is a prod pr-review step in state with PRStatus prs-step and, when
// prNumber > 0, PR prNumber of test/repo in its status.
func prStep(state string, prNumber int) *v1alpha1.PromotionStep {
	ps := makeStep("step", "nginx-demo", "bundle-1", "prod")
	ps.Spec.StepType = "pr-review"
	ps.Spec.PRStatusRef = "prs-step"
	ps.Status.State = state
	if prNumber > 0 {
		ps.Status.PRURL = "https://github.com/test/repo/pull/" + strconv.Itoa(prNumber)
		ps.Status.Outputs = map[string]string{"prURL": ps.Status.PRURL, "prNumber": strconv.Itoa(prNumber)}
	}
	return ps
}

// TestPRFinalizer_FollowsState covers B40: a step holds kardinal.io/close-pr
// exactly while it is Promoting or WaitingForMerge and opens a PR. An auto step
// never holds it, and a step that is past its PR loses it.
func TestPRFinalizer_FollowsState(t *testing.T) {
	merged := openPRStatus("prs-step", "test/repo", 5)
	merged.Status.Open, merged.Status.Merged = false, true
	tests := []struct {
		name      string
		step      *v1alpha1.PromotionStep
		prs       *v1alpha1.PRStatus
		finalizer bool // the step holds the finalizer before the reconcile
		wantState string
		want      bool
	}{
		{
			name:      "a pr-review step gets it on entering Promoting, before its PR is opened",
			step:      prStep("", 0),
			prs:       openPRStatus("prs-step", "", 0),
			wantState: "Promoting",
			want:      true,
		},
		{
			name:      "a step waiting for its PR keeps it",
			step:      prStep("WaitingForMerge", 5),
			prs:       openPRStatus("prs-step", "test/repo", 5),
			wantState: "WaitingForMerge",
			want:      true,
		},
		{
			name:      "an auto step entering Promoting does not get it",
			step:      makeStep("step", "nginx-demo", "bundle-1", "test"),
			wantState: "Promoting",
			want:      false,
		},
		{
			name:      "a step whose PR merged loses it",
			step:      prStep("WaitingForMerge", 5),
			prs:       merged,
			finalizer: true,
			wantState: "HealthChecking",
			want:      false,
		},
		{
			name:      "a Failed step loses it",
			step:      prStep("Failed", 5),
			prs:       openPRStatus("prs-step", "test/repo", 5),
			finalizer: true,
			wantState: "Failed",
			want:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.finalizer {
				tt.step.Finalizers = []string{promotionstep.FinalizerClosePR}
			}
			objs := []client.Object{tt.step, makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo")}
			if tt.prs != nil {
				objs = append(objs, tt.prs)
			}
			c := newClient(t, objs...)
			m := &mockSCM{}
			r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{},
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}

			reconcileStep(t, r, "step")
			got := getStep(t, c, "step")
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Equal(t, tt.want, slices.Contains(got.Finalizers, promotionstep.FinalizerClosePR),
				"finalizers %v", got.Finalizers)
			assert.Empty(t, m.closed, "no PR is closed")
		})
	}
}

// TestPRFinalizer_DeleteClosesPR covers B40: deleting a step that holds an
// open PR closes the PR with a comment before the step goes. A merged PR, or a
// step past its PR, is left alone. A failed close is retried until five
// minutes after the delete; then the step goes anyway with a Warning Event.
func TestPRFinalizer_DeleteClosesPR(t *testing.T) {
	merged := openPRStatus("prs-step", "test/repo", 5)
	merged.Status.Open, merged.Status.Merged = false, true
	closeErr := errors.New("HTTP 502")
	tests := []struct {
		name        string
		step        *v1alpha1.PromotionStep
		prs         *v1alpha1.PRStatus
		bundle      bool // the Bundle still exists
		deletedAgo  time.Duration
		closeErrs   []error
		wantClosed  []string
		wantComment string
		wantGone    bool
		wantRequeue bool
		wantEvent   string
	}{
		{
			name:        "the Bundle was deleted: the open PR is closed",
			step:        prStep("WaitingForMerge", 5),
			prs:         openPRStatus("prs-step", "test/repo", 5),
			deletedAgo:  time.Second,
			wantClosed:  []string{"test/repo#5"},
			wantComment: "kardinal closed this PR: bundle bundle-1 was deleted.",
			wantGone:    true,
		},
		{
			name:        "only the step was deleted: the reason says so",
			step:        prStep("WaitingForMerge", 5),
			prs:         openPRStatus("prs-step", "test/repo", 5),
			bundle:      true,
			deletedAgo:  time.Second,
			wantClosed:  []string{"test/repo#5"},
			wantComment: "kardinal closed this PR: PromotionStep step was deleted.",
			wantGone:    true,
		},
		{
			name:        "the PRStatus is gone too: the PR is found in the step status",
			step:        prStep("WaitingForMerge", 5),
			deletedAgo:  time.Second,
			wantClosed:  []string{"test/repo#5"},
			wantComment: "bundle bundle-1 was deleted",
			wantGone:    true,
		},
		{
			name:       "a merged PR is not closed",
			step:       prStep("WaitingForMerge", 5),
			prs:        merged,
			deletedAgo: time.Second,
			wantGone:   true,
		},
		{
			name:       "a step past its PR goes at once",
			step:       prStep("HealthChecking", 5),
			prs:        openPRStatus("prs-step", "test/repo", 5),
			deletedAgo: time.Second,
			wantGone:   true,
		},
		{
			name:        "a failed close is retried",
			step:        prStep("WaitingForMerge", 5),
			prs:         openPRStatus("prs-step", "test/repo", 5),
			deletedAgo:  time.Minute,
			closeErrs:   []error{closeErr},
			wantClosed:  []string{"test/repo#5"},
			wantRequeue: true,
		},
		{
			name:       "after five minutes the step goes anyway",
			step:       prStep("WaitingForMerge", 5),
			prs:        openPRStatus("prs-step", "test/repo", 5),
			deletedAgo: 6 * time.Minute,
			closeErrs:  []error{closeErr},
			wantClosed: []string{"test/repo#5"},
			wantGone:   true,
			wantEvent:  "Warning ClosePRFailed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.step.Finalizers = []string{promotionstep.FinalizerClosePR}
			deleted := metav1.NewTime(time.Now().Add(-tt.deletedAgo))
			tt.step.DeletionTimestamp = &deleted
			objs := []client.Object{tt.step, makePipeline("nginx-demo")}
			if tt.bundle {
				objs = append(objs, makeBundle("bundle-1", "nginx-demo"))
			}
			if tt.prs != nil {
				objs = append(objs, tt.prs)
			}
			c := newClient(t, objs...)
			m := &mockSCM{closeErrs: tt.closeErrs}
			rec := events.NewFakeRecorder(5)
			r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{}, Recorder: rec,
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}

			res, err := r.Reconcile(context.Background(), reqFor("step"))
			require.NoError(t, err)
			assert.Equal(t, tt.wantClosed, m.closed)
			if tt.wantComment != "" {
				require.Len(t, m.comments, 1)
				assert.Contains(t, m.comments[0], tt.wantComment)
			} else {
				assert.Empty(t, m.comments)
			}
			var got v1alpha1.PromotionStep
			err = c.Get(context.Background(), client.ObjectKeyFromObject(tt.step), &got)
			if tt.wantGone {
				assert.True(t, apierrors.IsNotFound(err), "the step is gone: %v", err)
			} else {
				require.NoError(t, err)
				assert.Contains(t, got.Finalizers, promotionstep.FinalizerClosePR)
			}
			if tt.wantRequeue {
				assert.Greater(t, res.RequeueAfter, time.Duration(0))
				assert.LessOrEqual(t, res.RequeueAfter, time.Minute)
			} else {
				assert.Zero(t, res.RequeueAfter)
			}
			select {
			case ev := <-rec.Events:
				assert.Contains(t, ev, tt.wantEvent)
				assert.NotEmpty(t, tt.wantEvent, "unexpected event %q", ev)
			default:
				assert.Empty(t, tt.wantEvent, "no event emitted")
			}
		})
	}
}

// TestPRFinalizer_DeleteClosesPROnce covers B40: the PR of a deleted step is
// closed and commented on once, when the informer cache still shows the
// finalizer the first reconcile removed, and when removing the finalizer hits
// a conflict.
func TestPRFinalizer_DeleteClosesPROnce(t *testing.T) {
	tests := []struct {
		name      string
		conflicts int // finalizer patches that fail with a Conflict first
		stale     bool
	}{
		{name: "a second reconcile reads the step from a lagging cache", stale: true},
		{name: "removing the finalizer conflicts", conflicts: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			step := prStep("WaitingForMerge", 5)
			step.Finalizers = []string{promotionstep.FinalizerClosePR}
			deleted := metav1.NewTime(time.Now().Add(-time.Second))
			step.DeletionTimestamp = &deleted
			api := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.PRStatus{}).
				WithObjects(step, openPRStatus("prs-step", "test/repo", 5), makePipeline("nginx-demo")).Build()
			stale := step.DeepCopy()
			conflicts := tt.conflicts
			cache := interceptor.NewClient(api, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if ps, ok := obj.(*v1alpha1.PromotionStep); ok && tt.stale && key.Name == "step" {
						stale.DeepCopyInto(ps)
						return nil
					}
					return c.Get(ctx, key, obj, opts...)
				},
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
					if _, ok := obj.(*v1alpha1.PromotionStep); ok && conflicts > 0 {
						conflicts--
						return apierrors.NewConflict(v1alpha1.GroupVersion.WithResource("promotionsteps").GroupResource(),
							obj.GetName(), errors.New("the object has been modified"))
					}
					return c.Patch(ctx, obj, p, opts...)
				},
			})
			m := &mockSCM{}
			r := &promotionstep.Reconciler{Client: cache, APIReader: api, SCM: m, GitClient: &mockGit{},
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}

			for range 2 {
				_, err := r.Reconcile(context.Background(), reqFor("step"))
				require.NoError(t, err)
			}
			assert.Equal(t, []string{"test/repo#5"}, m.closed, "the PR is closed once")
			assert.Len(t, m.comments, 1, "the PR is commented on once")
			var gone v1alpha1.PromotionStep
			assert.True(t, apierrors.IsNotFound(api.Get(context.Background(), client.ObjectKeyFromObject(step), &gone)),
				"the step is gone")
		})
	}
}

// TestPRFinalizer_SyncConflict covers B55 for the close-pr finalizer: when the
// step changed since it was read, adding or removing kardinal.io/close-pr hits
// a conflict. The reconcile then stops and requeues without a reconcile error,
// and the next one syncs the finalizer. A step that needs the finalizer does
// not reach its state handler without it.
func TestPRFinalizer_SyncConflict(t *testing.T) {
	merged := openPRStatus("prs-step", "test/repo", 5)
	merged.Status.Open, merged.Status.Merged = false, true
	conflict := apierrors.NewConflict(v1alpha1.GroupVersion.WithResource("promotionsteps").GroupResource(),
		"step", errors.New("the object has been modified"))
	tests := []struct {
		name        string
		step        *v1alpha1.PromotionStep
		prs         *v1alpha1.PRStatus
		finalizer   bool // the step holds the finalizer before the reconcile
		patchErr    error
		wantErr     bool
		wantState   string
		wantWrites  int  // status writes in the reconcile that conflicts
		wantAfter   bool // the finalizer after the next reconcile
		wantStopped bool // the conflicting reconcile stopped before the state handler
	}{
		{
			name:        "adding it before the step's handler runs",
			step:        prStep("Promoting", 0),
			prs:         openPRStatus("prs-step", "", 0),
			patchErr:    conflict,
			wantState:   "Promoting",
			wantAfter:   true,
			wantStopped: true,
		},
		{
			name:       "adding it on entering Promoting",
			step:       prStep("", 0),
			prs:        openPRStatus("prs-step", "", 0),
			patchErr:   conflict,
			wantState:  "Promoting",
			wantWrites: 1,
			wantAfter:  true,
		},
		{
			name:       "removing it once the PR merged",
			step:       prStep("WaitingForMerge", 5),
			prs:        merged,
			finalizer:  true,
			patchErr:   conflict,
			wantState:  "HealthChecking",
			wantWrites: 1,
			wantAfter:  false,
		},
		{
			name:        "another error is still a reconcile error",
			step:        prStep("Promoting", 0),
			prs:         openPRStatus("prs-step", "", 0),
			patchErr:    apierrors.NewInternalError(errors.New("etcdserver: request timed out")),
			wantErr:     true,
			wantState:   "Promoting",
			wantAfter:   true,
			wantStopped: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.finalizer {
				tt.step.Finalizers = []string{promotionstep.FinalizerClosePR}
			}
			api := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.PRStatus{}, &v1alpha1.Bundle{}).
				WithObjects(tt.step, tt.prs, makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo")).Build()
			failing, writes := true, 0
			c := interceptor.NewClient(api, interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
					if _, ok := obj.(*v1alpha1.PromotionStep); ok && failing {
						return tt.patchErr
					}
					return c.Patch(ctx, obj, p, opts...)
				},
				SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
					if _, ok := obj.(*v1alpha1.PromotionStep); ok {
						writes++
					}
					return c.SubResource(sub).Update(ctx, obj, opts...)
				},
				SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
					if _, ok := obj.(*v1alpha1.PromotionStep); ok {
						writes++
					}
					return c.SubResource(sub).Patch(ctx, obj, p, opts...)
				},
			})
			m := &mockSCM{open: true, prURL: "https://github.com/test/repo/pull/5", prNumber: 5}
			r := &promotionstep.Reconciler{Client: c, APIReader: api, SCM: m, GitClient: &mockGit{},
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}

			res, err := r.Reconcile(context.Background(), reqFor("step"))
			if tt.wantErr {
				require.Error(t, err)
				assert.False(t, apierrors.IsConflict(err))
			} else {
				require.NoError(t, err, "a conflict is not a reconcile error")
				assert.Positive(t, res.RequeueAfter, "the step is reconciled again shortly")
			}
			got := getStep(t, api, "step")
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Equal(t, tt.finalizer, slices.Contains(got.Finalizers, promotionstep.FinalizerClosePR),
				"the finalizer is unchanged, finalizers %v", got.Finalizers)
			assert.Equal(t, tt.wantWrites, writes, "status writes")
			if tt.wantStopped {
				assert.Zero(t, m.openCalled, "no PR is opened")
			}

			failing = false
			_, err = r.Reconcile(context.Background(), reqFor("step"))
			require.NoError(t, err)
			got = getStep(t, api, "step")
			assert.Equal(t, tt.wantAfter, slices.Contains(got.Finalizers, promotionstep.FinalizerClosePR),
				"the next reconcile syncs the finalizer: state %s (%s), finalizers %v", got.Status.State, got.Status.Message, got.Finalizers)
			assert.Empty(t, m.closed, "no PR is closed")
		})
	}
}

// TestPRFinalizer_OrphanedStepClosesPR covers B40 with the orphan guard: a
// step whose Bundle is gone deletes itself, and the delete closes its PR.
func TestPRFinalizer_OrphanedStepClosesPR(t *testing.T) {
	c := newClient(t, prStep("WaitingForMerge", 5), openPRStatus("prs-step", "test/repo", 5), makePipeline("nginx-demo"))
	m := &mockSCM{}
	r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}

	reconcileStep(t, r, "step")
	got := getStep(t, c, "step")
	assert.False(t, got.DeletionTimestamp.IsZero(), "the orphan deleted itself")
	assert.Empty(t, m.closed, "the PR is closed by the delete, not before")

	reconcileStep(t, r, "step")
	assert.Equal(t, []string{"test/repo#5"}, m.closed)
	var gone v1alpha1.PromotionStep
	assert.True(t, apierrors.IsNotFound(c.Get(context.Background(), client.ObjectKeyFromObject(&got), &gone)))
}
