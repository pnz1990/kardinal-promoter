// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// rebaseGit is a pushRecorder that can rebase, and runs onRebase first.
type rebaseGit struct {
	pushRecorder
	onRebase func()
}

var _ scm.Rebaser = (*rebaseGit)(nil)

func (g *rebaseGit) RebaseOnRemote(context.Context, string, string, string, scm.GitAuth) ([]string, error) {
	if g.onRebase != nil {
		g.onRebase()
	}
	return []string{"environments/test/kustomization.yaml"}, nil
}

// TestPushGuard covers #1603: a promotion that became stale must never push
// over a newer one. The checks read the API server (APIReader), so a cache
// that has not seen the supersession yet does not let the push through. A
// push is refused, and the step ends Superseded, only for a Bundle that
// supersedes it (lifecycle.SupersedingSiblings).
//
// Covers PIPE-SHARED-04.
func TestPushGuard(t *testing.T) {
	t0 := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	bundleAt := func(name string, at time.Time) *v1alpha1.Bundle {
		b := makeBundle(name, "nginx-demo")
		b.CreationTimestamp = metav1.NewTime(at)
		return b
	}
	newerPushing := func() *v1alpha1.PromotionStep {
		s := labelled(asPromoting(makeStep("step-b2", "nginx-demo", "b2", "test"), makePipeline("nginx-demo")))
		s.Status.Outputs = map[string]string{"pushIntent": t0.Format(time.RFC3339Nano)}
		return s
	}
	b2 := func(mut func(*v1alpha1.Bundle)) *v1alpha1.Bundle {
		b := bundleAt("b2", t0.Add(time.Second))
		b.Status.Phase = "Promoting"
		if mut != nil {
			mut(b)
		}
		return b
	}
	tests := []struct {
		name       string
		both       []client.Object // in the cache and on the API server
		api        []client.Object // what the API server says, over the cache's objects
		pushErrs   []error
		onRebase   func(cache, api client.Client)
		wantState  string
		wantMsg    string
		wantPushes int
	}{
		{name: "current: pushes and records its intent", wantState: "HealthChecking", wantPushes: 1},
		{name: "superseded per the API server, not yet in the cache",
			api: []client.Object{func() client.Object {
				b := bundleAt("b1", t0)
				b.Status.Phase = "Superseded"
				return b
			}()},
			wantState: "Failed", wantMsg: "bundle b1 was superseded — promotion cancelled"},
		{name: "rejected",
			api: []client.Object{func() client.Object {
				b := bundleAt("b1", t0)
				b.Spec.Rejected = &v1alpha1.BundleRejection{Reason: "bad build"}
				return b
			}()},
			wantState: "Failed", wantMsg: "bundle b1 was rejected"},
		{name: "a newer Bundle already pushed to the environment",
			both:      []client.Object{b2(nil), newerPushing()},
			wantState: "Superseded", wantMsg: "newer bundle b2 already pushed to test"},
		{name: "a newer Bundle and its step on the API server only: the cache never allows",
			api:       []client.Object{b2(nil), newerPushing()},
			wantState: "Superseded", wantMsg: "newer bundle b2 already pushed to test"},
		{name: "a newer Verified Bundle pushed",
			both:      []client.Object{b2(func(b *v1alpha1.Bundle) { b.Status.Phase = "Verified" }), newerPushing()},
			wantState: "Superseded", wantMsg: "newer bundle b2 already pushed to test"},
		{name: "a newer Bundle that was rejected supersedes nothing: this one pushes",
			both: []client.Object{b2(func(b *v1alpha1.Bundle) {
				b.Spec.Rejected = &v1alpha1.BundleRejection{Reason: "bad build"}
			}), newerPushing()},
			wantState: "HealthChecking", wantPushes: 1},
		{name: "a newer Bundle rejected on the API server, not yet in the cache",
			both: []client.Object{b2(nil), newerPushing()},
			api: []client.Object{b2(func(b *v1alpha1.Bundle) {
				b.Spec.Rejected = &v1alpha1.BundleRejection{Reason: "bad build"}
			})},
			wantState: "HealthChecking", wantPushes: 1},
		{name: "a newer Bundle that Failed supersedes nothing",
			both:      []client.Object{b2(func(b *v1alpha1.Bundle) { b.Status.Phase = "Failed" }), newerPushing()},
			wantState: "HealthChecking", wantPushes: 1},
		{name: "a stale intent: the newer step failed before it pushed",
			both: []client.Object{b2(nil), func() client.Object {
				s := newerPushing()
				s.Status.State = "Failed"
				return s
			}()},
			wantState: "HealthChecking", wantPushes: 1},
		{name: "a newer Bundle of another type supersedes nothing",
			both:      []client.Object{b2(func(b *v1alpha1.Bundle) { b.Spec.Type = "config" }), newerPushing()},
			wantState: "HealthChecking", wantPushes: 1},
		{name: "an older Bundle's push does not stop the newer one",
			both: []client.Object{func() client.Object {
				b := bundleAt("b0", t0.Add(-time.Second))
				b.Status.Phase = "Promoting"
				return b
			}(), func() client.Object {
				s := newerPushing()
				s.Name, s.Spec.BundleName = "step-b0", "b0"
				return labelled(s)
			}()},
			wantState: "HealthChecking", wantPushes: 1},
		{name: "a newer Bundle pushes while this one rebases",
			pushErrs: []error{scm.ErrNonFastForward},
			onRebase: func(cache, api client.Client) {
				for _, c := range []client.Client{cache, api} {
					_ = c.Create(context.Background(), b2(nil))
					_ = c.Create(context.Background(), newerPushing())
					s := &v1alpha1.PromotionStep{}
					_ = c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "step-b2"}, s)
					s.Status.Outputs = map[string]string{"pushIntent": t0.Format(time.RFC3339Nano)}
					_ = c.Status().Update(context.Background(), s)
				}
			},
			wantState: "Superseded", wantMsg: "newer bundle b2 already pushed to test", wantPushes: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pl := makePipeline("nginx-demo")
			step := labelled(asPromoting(makeStep("step-b1", "nginx-demo", "b1", "test"), pl))
			inCache := []client.Object{step, pl, bundleAt("b1", t0)}
			for _, o := range tt.both {
				inCache = append(inCache, o.DeepCopyObject().(client.Object))
			}
			cache := newClient(t, inCache...)
			// The API server: the same objects, with tt.api in place of the
			// ones of the same kind and name.
			objs := []client.Object{step.DeepCopy(), pl.DeepCopy(), bundleAt("b1", t0)}
			for _, o := range tt.both {
				objs = append(objs, o.DeepCopyObject().(client.Object))
			}
			for _, o := range tt.api {
				replaced := false
				for i, have := range objs {
					if have.GetName() == o.GetName() && fmt.Sprintf("%T", have) == fmt.Sprintf("%T", o) {
						objs[i], replaced = o, true
					}
				}
				if !replaced {
					objs = append(objs, o)
				}
			}
			api := newClient(t, objs...)
			git := &rebaseGit{pushRecorder: pushRecorder{headGit: headGit{sha: newSHA}, pushErrs: tt.pushErrs}}
			if tt.onRebase != nil {
				git.onRebase = func() { tt.onRebase(cache, api) }
			}
			r := &promotionstep.Reconciler{Client: cache, APIReader: api, SCM: &mockSCM{}, GitClient: git,
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}
			reconcileStep(t, r, step.Name)
			got := getStep(t, cache, step.Name)
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			if tt.wantMsg != "" {
				assert.Contains(t, got.Status.Message, tt.wantMsg)
			}
			assert.Len(t, git.pushes, tt.wantPushes, "pushes: %v", git.pushes)
			if tt.wantState == "HealthChecking" {
				assert.NotEmpty(t, got.Status.Outputs["pushIntent"], "the push intent is recorded before the push")
			}
		})
	}
}

// TestPushGuard_PRBranchOnlyChecksSupersession checks that a push to
// kardinal's own PR branch (pr-review) is refused for a superseded Bundle but
// not for a newer Bundle's push: an old PR is closed, not merged.
func TestPushGuard_PRBranchOnlyChecksSupersession(t *testing.T) {
	pl := makePipeline("nginx-demo")
	step := labelled(asPromoting(makeStep("step-b1", "nginx-demo", "b1", "prod"), pl))
	b := makeBundle("b1", "nginx-demo")
	c := newClient(t, step, pl, b, openPRStatus(step.Spec.PRStatusRef, "", 0))
	git := &pushRecorder{headGit: headGit{sha: newSHA}}
	r := &promotionstep.Reconciler{Client: c, APIReader: c, SCM: &mockSCM{open: true, prURL: "https://github.com/test/repo/pull/5", prNumber: 5},
		GitClient: git, WorkDirFn: func(_, _ string) string { return t.TempDir() }}
	reconcileStep(t, r, step.Name)
	got := getStep(t, c, step.Name)
	assert.Equal(t, "WaitingForMerge", got.Status.State, got.Status.Message)
	assert.Len(t, git.pushes, 1)
	assert.Empty(t, got.Status.Outputs["pushIntent"], "no intent for a PR branch push")
}

// TestPushGuard_OtherNotFoundIsAStepError checks that only the push intent
// write's NotFound and Conflict end the reconcile quietly: the same errors
// from another step's call (here the push) are step errors, not a step left
// in Promoting with nothing recorded.
func TestPushGuard_OtherNotFoundIsAStepError(t *testing.T) {
	for name, err := range map[string]error{
		"not found": apierrors.NewNotFound(schema.GroupResource{Group: "argoproj.io", Resource: "applications"}, "demo"),
		"conflict":  apierrors.NewConflict(schema.GroupResource{Group: "argoproj.io", Resource: "applications"}, "demo", errors.New("changed")),
	} {
		t.Run(name, func(t *testing.T) {
			pl := makePipeline("nginx-demo")
			step := labelled(asPromoting(makeStep("step-b1", "nginx-demo", "b1", "test"), pl))
			c := newClient(t, step, pl, makeBundle("b1", "nginx-demo"))
			git := &pushRecorder{headGit: headGit{sha: newSHA}, pushErrs: []error{err}}
			r := &promotionstep.Reconciler{Client: c, APIReader: c, SCM: &mockSCM{}, GitClient: git,
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}
			reconcileStep(t, r, step.Name)
			got := getStep(t, c, step.Name)
			assert.Contains(t, got.Status.Message, "after error: step git-push: ", "the error is recorded on the step, which retries")
		})
	}
}

// TestPushGuard_FleetTargetRollback (#1603 with D1): the rollback of one
// fleet target does not supersede the fleet Bundle, but once it pushed to
// that target the fleet Bundle's step there does not push over it: it ends
// Superseded. The fleet's other targets are not affected.
//
// Covers FLEET-08.
func TestPushGuard_FleetTargetRollback(t *testing.T) {
	t0 := time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC)
	pl := makePipeline("nginx-demo")
	pl.Spec.Environments[0].Fleet = &v1alpha1.FleetSpec{Targets: []v1alpha1.FleetTarget{{Name: "eu"}, {Name: "us"}}}
	fleetBundle := makeBundle("b1", "nginx-demo")
	fleetBundle.CreationTimestamp = metav1.NewTime(t0)
	rollback := makeBundle("rb-eu", "nginx-demo")
	rollback.CreationTimestamp = metav1.NewTime(t0.Add(time.Minute))
	rollback.Labels = map[string]string{"kardinal.io/rollback": "true"}
	rollback.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: "test-eu"}
	rollbackStep := labelled(makeStep("step-rb-eu", "nginx-demo", "rb-eu", "test-eu"))
	rollbackStep.Status.State = "HealthChecking"
	rollbackStep.Status.Steps = []v1alpha1.StepStatus{{Name: "git-push", State: v1alpha1.StepExecutionCompleted}}
	for _, tc := range []struct{ env, want string }{
		{"test-eu", "Superseded"},
		{"test-us", "HealthChecking"},
	} {
		t.Run(tc.env, func(t *testing.T) {
			step := labelled(asPromoting(makeStep("step-b1", "nginx-demo", "b1", tc.env), pl))
			step.Labels["kardinal.io/fleet"] = "test"
			c := newClient(t, step, pl.DeepCopy(), fleetBundle.DeepCopy(), rollback.DeepCopy(), rollbackStep.DeepCopy())
			git := &pushRecorder{headGit: headGit{sha: newSHA}}
			r := &promotionstep.Reconciler{Client: c, APIReader: c, SCM: &mockSCM{}, GitClient: git,
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}
			reconcileStep(t, r, step.Name)
			got := getStep(t, c, step.Name)
			assert.Equal(t, tc.want, got.Status.State, got.Status.Message)
			if tc.want == "Superseded" {
				assert.Empty(t, git.pushes)
				assert.Contains(t, got.Status.Message, "newer bundle rb-eu already pushed to test-eu")
			}
		})
	}
}

// TestPushGuard_IntentConflictRequeues: the push intent write is locked on the
// resourceVersion the reconcile read. A Conflict because another reconcile
// wrote the step's status pushes nothing and requeues; the next reconcile, on the fresh copy, records
// the intent and pushes.
func TestPushGuard_IntentConflictRequeues(t *testing.T) {
	pl := makePipeline("nginx-demo")
	step := labelled(asPromoting(makeStep("step-b1", "nginx-demo", "b1", "test"), pl))
	var conflicted atomic.Bool
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.PRStatus{}, &v1alpha1.Bundle{}).
		WithObjects(step, pl, makeBundle("b1", "nginx-demo")).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object,
				patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if ps, ok := obj.(*v1alpha1.PromotionStep); ok && ps.Status.Outputs["pushIntent"] != "" &&
					conflicted.CompareAndSwap(false, true) {
					// Another reconcile wrote the step's status in between
					// (a conflict from a spec or metadata change alone is
					// written over the fresh copy, #1664).
					var cur v1alpha1.PromotionStep
					require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(obj), &cur))
					cur.Status.Message = "written by another reconcile"
					require.NoError(t, c.Status().Update(ctx, &cur))
					return apierrors.NewConflict(schema.GroupResource{Group: "kardinal.io", Resource: "promotionsteps"},
						ps.Name, errors.New("the object has been modified"))
				}
				return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	git := &pushRecorder{headGit: headGit{sha: newSHA}}
	r := &promotionstep.Reconciler{Client: c, APIReader: c, SCM: &mockSCM{}, GitClient: git,
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}

	res, err := r.Reconcile(context.Background(), reqFor(step.Name))
	require.NoError(t, err)
	assert.True(t, res.Requeue, "a Conflict on the intent write requeues") //nolint:staticcheck // the reconciler requeues at once
	assert.Empty(t, git.pushes, "and pushes nothing")
	got := getStep(t, c, step.Name)
	assert.Equal(t, "Promoting", got.Status.State)
	assert.Empty(t, got.Status.Outputs["pushIntent"])

	reconcileStep(t, r, step.Name)
	got = getStep(t, c, step.Name)
	assert.Equal(t, "HealthChecking", got.Status.State, got.Status.Message)
	assert.Len(t, git.pushes, 1, "the next reconcile pushes")
	assert.NotEmpty(t, got.Status.Outputs["pushIntent"])
}

// TestPushGuard_IntentOverMetadataChange: a metadata change between the read
// and the intent write (a label added) is no status conflict, so the intent
// is written over the fresh copy (patchStatusLocked). The reconcile then
// carries the fresh metadata: the push goes on and the label is kept.
func TestPushGuard_IntentOverMetadataChange(t *testing.T) {
	pl := makePipeline("nginx-demo")
	step := labelled(asPromoting(makeStep("step-b1", "nginx-demo", "b1", "test"), pl))
	var changed atomic.Bool
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.PRStatus{}, &v1alpha1.Bundle{}).
		WithObjects(step, pl, makeBundle("b1", "nginx-demo")).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object,
				patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if ps, ok := obj.(*v1alpha1.PromotionStep); ok && ps.Status.Outputs["pushIntent"] != "" &&
					changed.CompareAndSwap(false, true) {
					var cur v1alpha1.PromotionStep
					require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(obj), &cur))
					cur.Labels["e2e.kardinal.io/touched"] = "true"
					require.NoError(t, c.Update(ctx, &cur))
				}
				return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	git := &pushRecorder{headGit: headGit{sha: newSHA}}
	r := &promotionstep.Reconciler{Client: c, APIReader: c, SCM: &mockSCM{}, GitClient: git,
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}
	reconcileStep(t, r, step.Name)
	got := getStep(t, c, step.Name)
	assert.Equal(t, "HealthChecking", got.Status.State, got.Status.Message)
	assert.Len(t, git.pushes, 1)
	assert.NotEmpty(t, got.Status.Outputs["pushIntent"])
	assert.Equal(t, "true", got.Labels["e2e.kardinal.io/touched"], "the fresh metadata is kept")
}
