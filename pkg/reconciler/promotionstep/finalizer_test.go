// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// prStep is a prod pr-review step in state with PRStatus prs-step and, when
// prNumber > 0, PR prNumber of test/repo in its status. A step past Pending
// has the pr-review step sequence handlePending writes.
func prStep(state string, prNumber int) *v1alpha1.PromotionStep {
	ps := makeStep("step", "nginx-demo", "bundle-1", "prod")
	ps.Spec.StepType = "pr-review"
	ps.Spec.PRStatusRef = "prs-step"
	ps.Status.State = state
	if state != "" && state != "Pending" {
		for _, name := range steps.DefaultSequenceForBundle("pr-review", "image", "", "") {
			ps.Status.Steps = append(ps.Status.Steps, v1alpha1.StepStatus{Name: name, State: v1alpha1.StepExecutionPending})
		}
	}
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
			name: "an auto step entering Promoting does not get it, though it has a prStatusRef",
			step: func() *v1alpha1.PromotionStep {
				ps := makeStep("step", "nginx-demo", "bundle-1", "test")
				ps.Spec.PRStatusRef = "prs-step"
				return ps
			}(),
			prs:       openPRStatus("prs-step", "", 0),
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

// builtStep returns the PromotionStep the Graph builder renders for env of
// bundle, as kro creates it: the CEL placeholders resolved (prStatusRef to the
// PRStatus node's name), in the Bundle's namespace.
func builtStep(t *testing.T, pl *v1alpha1.Pipeline, b *v1alpha1.Bundle, env string) *v1alpha1.PromotionStep {
	t.Helper()
	withImage := b.DeepCopy() // the builder requires one; the step needs none to run
	withImage.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/test/app", Tag: "1.2.3"}}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: pl, Bundle: withImage})
	require.NoError(t, err)
	raw, err := json.Marshal(res.Graph.Spec.Nodes)
	require.NoError(t, err)
	var nodes []struct {
		ID       string                 `json:"id"`
		Template map[string]interface{} `json:"template"`
	}
	require.NoError(t, json.Unmarshal(raw, &nodes))
	names := map[string]string{}
	for _, n := range nodes {
		if name, ok, _ := unstructured.NestedString(n.Template, "metadata", "name"); ok {
			names[n.ID] = name
		}
	}
	for _, n := range nodes {
		u := unstructured.Unstructured{Object: n.Template}
		if u.GetKind() != "PromotionStep" || u.GetLabels()["kardinal.io/environment"] != env {
			continue
		}
		ref, _, _ := unstructured.NestedString(u.Object, "spec", "prStatusRef")
		id := strings.TrimSuffix(strings.TrimPrefix(ref, "${"), ".metadata.name}")
		require.Contains(t, names, id, "prStatusRef %q names a node", ref)
		require.NoError(t, unstructured.SetNestedField(u.Object, names[id], "spec", "prStatusRef"))
		require.NoError(t, unstructured.SetNestedField(u.Object, b.Name, "spec", "bundleName"))
		if ups, ok, _ := unstructured.NestedSlice(u.Object, "spec", "upstreamStates"); ok {
			for i := range ups {
				ups[i] = "Verified"
			}
			require.NoError(t, unstructured.SetNestedSlice(u.Object, ups, "spec", "upstreamStates"))
		}
		var ps v1alpha1.PromotionStep
		require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &ps))
		ps.Namespace = b.Namespace
		return &ps
	}
	t.Fatalf("the builder rendered no PromotionStep for %s", env)
	return nil
}

// TestPRFinalizer_BuiltSteps covers the #1387 review with steps as the Graph
// builder renders them, which all have spec.prStatusRef: the pr-review step
// gets kardinal.io/close-pr on entering Promoting, before its PR is opened,
// and keeps it while it waits for the merge. The auto step never gets it.
func TestPRFinalizer_BuiltSteps(t *testing.T) {
	tests := []struct {
		env        string
		wantStates []string // after each reconcile
		want       []bool   // the finalizer after each reconcile
	}{
		{env: "test", wantStates: []string{"Promoting", "HealthChecking"}, want: []bool{false, false}},
		{env: "prod", wantStates: []string{"Promoting", "WaitingForMerge"}, want: []bool{true, true}},
	}
	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			pl, b := makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo")
			step := builtStep(t, pl, b, tt.env)
			require.NotEmpty(t, step.Spec.PRStatusRef, "the builder sets prStatusRef on every step")
			api := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.PRStatus{}, &v1alpha1.Bundle{}).
				WithObjects(step, pl, b, openPRStatus(step.Spec.PRStatusRef, "", 0)).Build()
			everHeld := false
			c := interceptor.NewClient(api, interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
					err := c.Patch(ctx, obj, p, opts...)
					if _, ok := obj.(*v1alpha1.PromotionStep); ok && slices.Contains(obj.GetFinalizers(), promotionstep.FinalizerClosePR) {
						everHeld = true
					}
					return err
				},
			})
			m := &mockSCM{open: true, prURL: "https://github.com/test/repo/pull/5", prNumber: 5}
			r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{},
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}

			for i, wantState := range tt.wantStates {
				if i == 0 {
					assert.Zero(t, m.openCalled)
				}
				reconcileStep(t, r, step.Name)
				got := getStep(t, api, step.Name)
				assert.Equal(t, wantState, got.Status.State, got.Status.Message)
				assert.Equal(t, tt.want[i], slices.Contains(got.Finalizers, promotionstep.FinalizerClosePR),
					"reconcile %d: finalizers %v", i+1, got.Finalizers)
			}
			if tt.env == "test" {
				assert.False(t, everHeld, "the auto step never holds the finalizer")
				assert.Zero(t, m.openCalled, "the auto step opens no PR")
			} else {
				assert.Equal(t, 1, m.openCalled, "the PR is opened after the finalizer is on")
			}
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
			m := &mockSCM{open: true, closeErrs: tt.closeErrs}
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

// TestPRFinalizer_DeleteDeadlineUsesClock covers the #1387 review: the
// close-PR deadline is measured with the reconciler's clock, not the wall
// clock. A step deleted ten minutes ago by the wall clock but one minute ago
// by the reconciler's clock is retried, not given up on.
func TestPRFinalizer_DeleteDeadlineUsesClock(t *testing.T) {
	step := prStep("WaitingForMerge", 5)
	step.Finalizers = []string{promotionstep.FinalizerClosePR}
	deleted := metav1.NewTime(time.Now().Add(-10 * time.Minute).Truncate(time.Second)) // as stored
	step.DeletionTimestamp = &deleted
	c := newClient(t, step, openPRStatus("prs-step", "test/repo", 5), makePipeline("nginx-demo"))
	m := &mockSCM{open: true, closeErrs: []error{errors.New("HTTP 502")}}
	rec := events.NewFakeRecorder(5)
	r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{}, Recorder: rec,
		NowFn:     func() time.Time { return deleted.Add(time.Minute) },
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}

	res, err := r.Reconcile(context.Background(), reqFor("step"))
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, res.RequeueAfter, "half the minute spent so far")
	assert.Contains(t, getStep(t, c, "step").Finalizers, promotionstep.FinalizerClosePR, "the step stays")
	assert.Empty(t, rec.Events, "no ClosePRFailed Event before the deadline")
}

// TestPRFinalizer_DeleteWithoutPR covers the #1387 review: a step that has no
// PR is deleted at once, whatever its state, and the SCM is not asked about
// anything. That is an auto step (it never holds kardinal.io/close-pr), and a
// pr-review step deleted before it opened its PR or after it ended.
func TestPRFinalizer_DeleteWithoutPR(t *testing.T) {
	auto := func(state string) *v1alpha1.PromotionStep {
		ps := makeStep("step", "nginx-demo", "bundle-1", "test")
		ps.Spec.PRStatusRef = "prs-step"
		ps.Status.State = state
		return ps
	}
	tests := []struct {
		name string
		step *v1alpha1.PromotionStep
	}{
		{name: "an auto step in Pending", step: auto("Pending")},
		{name: "an auto step in Promoting", step: auto("Promoting")},
		{name: "an auto step in HealthChecking", step: auto("HealthChecking")},
		{name: "an auto step in Failed", step: auto("Failed")},
		{name: "a pr-review step in Pending", step: prStep("Pending", 0)},
		{name: "a pr-review step in Promoting, before it opened its PR", step: prStep("Promoting", 0)},
		{name: "a pr-review step in Failed", step: prStep("Failed", 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClient(t, tt.step, openPRStatus("prs-step", "", 0), makePipeline("nginx-demo"),
				makeBundle("bundle-1", "nginx-demo"))
			m := &mockSCM{open: true}
			r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{},
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}
			ctx := context.Background()
			// syncPRFinalizer runs first: the step gets the finalizer only if it
			// needs it. Then it is deleted and reconciled until it is gone.
			got := getStep(t, c, "step")
			if needs := got.Status.State == "Promoting" && got.Spec.Environment == "prod"; needs {
				got.Finalizers = []string{promotionstep.FinalizerClosePR}
				require.NoError(t, c.Update(ctx, &got))
			}
			require.NoError(t, c.Delete(ctx, &got))
			res, err := r.Reconcile(ctx, reqFor("step"))
			require.NoError(t, err)
			assert.Zero(t, res.RequeueAfter)
			var gone v1alpha1.PromotionStep
			assert.True(t, apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(tt.step), &gone)),
				"the step is gone after one reconcile")
			assert.Zero(t, m.getPRCalled, "the SCM is not asked")
			assert.Empty(t, m.closed)
			assert.Empty(t, m.comments)
		})
	}
}

// TestPRFinalizer_DeleteAsksSCM covers the #1387 review: a deleted step asks
// the SCM whether its PR is still open before it closes it. A PR merged or
// closed outside the controller's view (the PRStatus lags or is gone), or
// closed by an earlier attempt that did not finish, is not closed or commented
// on again. A failed status read is retried like a failed close.
func TestPRFinalizer_DeleteAsksSCM(t *testing.T) {
	tests := []struct {
		name        string
		prs         *v1alpha1.PRStatus // nil: the PRStatus is gone
		scm         mockSCM
		wantClosed  []string
		wantComment bool
		wantRequeue bool
	}{
		{name: "merged, with the PRStatus gone: not closed, no comment",
			scm: mockSCM{merged: true}},
		{name: "merged since the PRStatus was polled: not closed, no comment",
			prs: openPRStatus("prs-step", "test/repo", 5), scm: mockSCM{merged: true}},
		{name: "closed by a human: not closed again, no comment",
			prs: openPRStatus("prs-step", "test/repo", 5)},
		{name: "still open: closed, then commented",
			prs: openPRStatus("prs-step", "test/repo", 5), scm: mockSCM{open: true},
			wantClosed: []string{"test/repo#5"}, wantComment: true},
		{name: "the status read fails: retried, nothing closed",
			prs: openPRStatus("prs-step", "test/repo", 5), scm: mockSCM{open: true, getPRErr: errors.New("HTTP 502")},
			wantRequeue: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			step := prStep("WaitingForMerge", 5)
			step.Finalizers = []string{promotionstep.FinalizerClosePR}
			deleted := metav1.NewTime(time.Now().Add(-time.Second))
			step.DeletionTimestamp = &deleted
			objs := []client.Object{step, makePipeline("nginx-demo")}
			if tt.prs != nil {
				objs = append(objs, tt.prs)
			}
			c := newClient(t, objs...)
			m := &tt.scm
			r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{},
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}

			res, err := r.Reconcile(context.Background(), reqFor("step"))
			require.NoError(t, err)
			assert.Equal(t, 1, m.getPRCalled, "the SCM is asked once")
			assert.Equal(t, tt.wantClosed, m.closed)
			if tt.wantComment {
				require.Len(t, m.comments, 1)
				assert.Contains(t, m.comments[0], "kardinal closed this PR: bundle bundle-1 was deleted.")
			} else {
				assert.Empty(t, m.comments)
			}
			var got v1alpha1.PromotionStep
			err = c.Get(context.Background(), client.ObjectKeyFromObject(step), &got)
			if tt.wantRequeue {
				require.NoError(t, err, "the step stays while its PR may be open")
				assert.Positive(t, res.RequeueAfter)
			} else {
				assert.True(t, apierrors.IsNotFound(err), "the step is gone: %v", err)
				assert.Zero(t, res.RequeueAfter)
			}
		})
	}
}

// TestPRFinalizer_DeleteMergedPRProviders covers the #1387 review with the
// real Bitbucket and Azure DevOps providers: deleting a step whose PR merged
// after the PRStatus was last polled does not ask Bitbucket to decline it or
// Azure DevOps to abandon it, and posts no comment. An open PR is still
// declined or abandoned, then commented on.
func TestPRFinalizer_DeleteMergedPRProviders(t *testing.T) {
	tests := []struct {
		name      string
		provider  func(url string) scm.SCMProvider
		repo      string
		state     string // the PR state the API returns
		wantCalls []string
	}{
		{name: "Bitbucket, merged", repo: "ws/repo", state: `{"state":"MERGED"}`,
			provider:  func(u string) scm.SCMProvider { return scm.NewBitbucketProvider("t", u, "") },
			wantCalls: []string{"GET /2.0/repositories/ws/repo/pullrequests/5"}},
		{name: "Bitbucket, open", repo: "ws/repo", state: `{"state":"OPEN"}`,
			provider: func(u string) scm.SCMProvider { return scm.NewBitbucketProvider("t", u, "") },
			wantCalls: []string{"GET /2.0/repositories/ws/repo/pullrequests/5",
				"POST /2.0/repositories/ws/repo/pullrequests/5/decline",
				"POST /2.0/repositories/ws/repo/pullrequests/5/comments"}},
		{name: "Azure DevOps, completed", repo: "org/proj/repo", state: `{"status":"completed"}`,
			provider:  func(u string) scm.SCMProvider { return scm.NewAzureDevOpsProvider("t", u, "") },
			wantCalls: []string{"GET /org/proj/_apis/git/repositories/repo/pullrequests/5"}},
		{name: "Azure DevOps, active", repo: "org/proj/repo", state: `{"status":"active"}`,
			provider: func(u string) scm.SCMProvider { return scm.NewAzureDevOpsProvider("t", u, "") },
			wantCalls: []string{"GET /org/proj/_apis/git/repositories/repo/pullrequests/5",
				"PATCH /org/proj/_apis/git/repositories/repo/pullrequests/5",
				"POST /org/proj/_apis/git/repositories/repo/pullrequests/5/threads"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				mu    sync.Mutex
				calls []string
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				calls = append(calls, req.Method+" "+req.URL.Path)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if req.Method == http.MethodGet {
					_, _ = w.Write([]byte(tt.state))
					return
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			step := prStep("WaitingForMerge", 5)
			step.Finalizers = []string{promotionstep.FinalizerClosePR}
			deleted := metav1.NewTime(time.Now().Add(-time.Second))
			step.DeletionTimestamp = &deleted
			c := newClient(t, step, openPRStatus("prs-step", tt.repo, 5), makePipeline("nginx-demo"))
			r := &promotionstep.Reconciler{Client: c, SCM: tt.provider(srv.URL), GitClient: &mockGit{},
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}

			res, err := r.Reconcile(context.Background(), reqFor("step"))
			require.NoError(t, err)
			assert.Zero(t, res.RequeueAfter)
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, tt.wantCalls, calls)
		})
	}
}

// TestPRFinalizer_DeleteRestartAfterClose covers the #1387 review: when the
// reconcile that closed a deleted step's PR does not finish (the close
// response is lost, or the finalizer removal fails), the next one finds the
// PR closed and neither closes nor comments on it again. The PR gets at most
// one comment.
func TestPRFinalizer_DeleteRestartAfterClose(t *testing.T) {
	tests := []struct {
		name         string
		lostClose    int  // close responses lost
		failRemove   bool // the first finalizer removal fails
		wantComments int
	}{
		{name: "the close response was lost", lostClose: 1, wantComments: 0},
		{name: "the finalizer removal failed after the comment", failRemove: true, wantComments: 1},
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
			failRemove := tt.failRemove
			c := interceptor.NewClient(api, interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
					if _, ok := obj.(*v1alpha1.PromotionStep); ok && failRemove {
						failRemove = false
						return apierrors.NewInternalError(errors.New("etcdserver: request timed out"))
					}
					return c.Patch(ctx, obj, p, opts...)
				},
			})
			m := &mockSCM{open: true, lostClose: tt.lostClose}
			r := &promotionstep.Reconciler{Client: c, APIReader: api, SCM: m, GitClient: &mockGit{},
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}

			_, _ = r.Reconcile(context.Background(), reqFor("step"))
			_, err := r.Reconcile(context.Background(), reqFor("step"))
			require.NoError(t, err)
			assert.Equal(t, []string{"test/repo#5"}, m.closed, "the PR is closed once")
			assert.Len(t, m.comments, tt.wantComments, "comments")
			var gone v1alpha1.PromotionStep
			assert.True(t, apierrors.IsNotFound(api.Get(context.Background(), client.ObjectKeyFromObject(step), &gone)),
				"the step is gone")
		})
	}
}

// TestPRFinalizer_GraphRecreatedKeepsPR covers B40 with GRAPH-HEAL-01: a step
// deleted with its Graph while the Bundle goes on promoting comes back (the
// Bundle reconciler recreates the Graph), and the new step reuses its PR, so
// the PR is left open and uncommented. In every other case the PR is closed:
// the step was deleted on its own (its Graph is still there), its Bundle or
// namespace is being deleted, the Bundle is past Promoting, or the Pipeline no
// longer has the step's environment.
func TestPRFinalizer_GraphRecreatedKeepsPR(t *testing.T) {
	deleted := metav1.NewTime(time.Now().Add(-time.Second).Truncate(time.Second))
	type graphState int
	const (
		graphGone graphState = iota
		graphDeleting
		graphOld        // the Graph the step was in
		graphSameSecond // recreated in the second the step was deleted
		graphNew        // recreated after the step was deleted
	)
	tests := []struct {
		name        string
		graph       graphState
		nsDeleting  bool
		bundle      func(*v1alpha1.Bundle) // nil: the Bundle is gone
		dropEnv     bool                   // the Pipeline no longer has prod
		noPipeline  bool
		readErr     string        // the kind whose read fails with an error other than NotFound
		later       time.Duration // how long after the delete the reconcile runs (default 1s)
		wantClosed  bool
		wantRetry   bool // the step stays and is reconciled again
		wantComment string
	}{
		{name: "the Graph is being deleted: the PR is kept", graph: graphDeleting, bundle: func(*v1alpha1.Bundle) {}},
		{name: "the Graph is gone: the PR is kept", graph: graphGone, bundle: func(*v1alpha1.Bundle) {}},
		{name: "the Graph was recreated: the PR is kept", graph: graphNew, bundle: func(*v1alpha1.Bundle) {}},
		{name: "the Graph was recreated in the same second: the PR is kept", graph: graphSameSecond,
			bundle: func(*v1alpha1.Bundle) {}},
		{name: "only the step was deleted: the PR is closed", graph: graphOld, bundle: func(*v1alpha1.Bundle) {},
			wantClosed: true, wantComment: "PromotionStep step was deleted"},
		{name: "the namespace is being deleted: the PR is closed", graph: graphDeleting, nsDeleting: true,
			bundle: func(*v1alpha1.Bundle) {}, wantClosed: true,
			wantComment: "kardinal closed this PR: namespace default was deleted."},
		// The namespace deletion deletes the Bundle and the steps in no set
		// order: the comment names the namespace either way.
		{name: "the namespace is being deleted and the Bundle is gone: the comment names the namespace",
			graph: graphGone, nsDeleting: true, wantClosed: true,
			wantComment: "kardinal closed this PR: namespace default was deleted."},
		{name: "the Bundle is being deleted: the PR is closed", graph: graphDeleting,
			bundle: func(b *v1alpha1.Bundle) {
				b.Finalizers = []string{"test/hold"}
				b.DeletionTimestamp = &deleted
			}, wantClosed: true, wantComment: "bundle bundle-1 was deleted"},
		{name: "the Bundle is gone: the PR is closed", graph: graphGone,
			wantClosed: true, wantComment: "bundle bundle-1 was deleted"},
		{name: "the Bundle is past Promoting: the PR is closed", graph: graphDeleting,
			bundle: func(b *v1alpha1.Bundle) { b.Status.Phase = "Failed" }, wantClosed: true},
		{name: "the Pipeline dropped the environment: the PR is closed", graph: graphDeleting,
			bundle: func(*v1alpha1.Bundle) {}, dropEnv: true, wantClosed: true},
		{name: "the Pipeline is gone: the PR is closed", graph: graphDeleting,
			bundle: func(*v1alpha1.Bundle) {}, noPipeline: true, wantClosed: true},
		{name: "the Graph cannot be read: retried, the PR is kept", graph: graphDeleting,
			bundle: func(*v1alpha1.Bundle) {}, readErr: "Graph", wantRetry: true},
		{name: "the Bundle cannot be read: retried, the PR is kept", graph: graphDeleting,
			bundle: func(*v1alpha1.Bundle) {}, readErr: "Bundle", wantRetry: true},
		{name: "the namespace cannot be read: retried, the PR is kept", graph: graphDeleting,
			bundle: func(*v1alpha1.Bundle) {}, readErr: "Namespace", wantRetry: true},
		{name: "the Pipeline cannot be read: retried, the PR is kept", graph: graphDeleting,
			bundle: func(*v1alpha1.Bundle) {}, readErr: "Pipeline", wantRetry: true},
		{name: "the Graph still cannot be read after five minutes: the PR is closed", graph: graphDeleting,
			bundle: func(*v1alpha1.Bundle) {}, readErr: "Graph", later: 6 * time.Minute, wantClosed: true,
			wantComment: "PromotionStep step was deleted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			step := prStep("WaitingForMerge", 5)
			step.Finalizers = []string{promotionstep.FinalizerClosePR}
			step.DeletionTimestamp = &deleted
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default", Finalizers: []string{"kubernetes"}}}
			if tt.nsDeleting {
				ns.DeletionTimestamp = &deleted
				ns.Status.Phase = corev1.NamespaceTerminating
			}
			objs := []client.Object{step, ns, openPRStatus("prs-step", "test/repo", 5)}
			if !tt.noPipeline {
				pl := makePipeline("nginx-demo")
				if tt.dropEnv {
					pl.Spec.Environments = pl.Spec.Environments[:1]
				}
				objs = append(objs, pl)
			}
			if tt.bundle != nil {
				b := makeBundle("bundle-1", "nginx-demo")
				b.Status.GraphRef = "nginx-demo-bundle-1"
				tt.bundle(b)
				objs = append(objs, b)
			}
			if tt.graph != graphGone {
				g := &unstructured.Unstructured{}
				g.SetGroupVersionKind(graph.GraphGVK)
				g.SetNamespace("default")
				g.SetName("nginx-demo-bundle-1")
				switch tt.graph {
				case graphDeleting:
					g.SetCreationTimestamp(metav1.NewTime(deleted.Add(-time.Hour)))
					g.SetFinalizers([]string{"kro.run/graph-finalizer"})
					g.SetDeletionTimestamp(&deleted)
				case graphOld:
					g.SetCreationTimestamp(metav1.NewTime(deleted.Add(-time.Hour)))
				case graphSameSecond:
					g.SetCreationTimestamp(deleted)
				case graphNew:
					g.SetCreationTimestamp(metav1.NewTime(deleted.Add(time.Second)))
				}
				objs = append(objs, g)
			}
			api := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.PRStatus{}, &v1alpha1.Bundle{}).
				WithObjects(objs...).Build()
			reader := interceptor.NewClient(api, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					kind := ""
					switch obj.(type) {
					case *unstructured.Unstructured:
						kind = "Graph"
					case *v1alpha1.Bundle:
						kind = "Bundle"
					case *corev1.Namespace:
						kind = "Namespace"
					case *v1alpha1.Pipeline:
						kind = "Pipeline"
					}
					switch {
					case kind == "" || kind != tt.readErr:
					case kind == "Namespace":
						return apierrors.NewForbidden(corev1.Resource("namespaces"), key.Name, errors.New("RBAC: access denied"))
					default:
						return apierrors.NewInternalError(errors.New("etcdserver: request timed out"))
					}
					return c.Get(ctx, key, obj, opts...)
				},
			})
			later := tt.later
			if later == 0 {
				later = time.Second
			}
			m := &mockSCM{open: true}
			r := &promotionstep.Reconciler{Client: api, APIReader: reader, SCM: m, GitClient: &mockGit{},
				NowFn:     func() time.Time { return deleted.Add(later) },
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}

			res, err := r.Reconcile(context.Background(), reqFor("step"))
			require.NoError(t, err)
			if tt.wantRetry {
				assert.Positive(t, res.RequeueAfter)
				assert.Empty(t, m.closed, "the PR is not closed yet")
				assert.Empty(t, m.comments)
				assert.Contains(t, getStep(t, api, "step").Finalizers, promotionstep.FinalizerClosePR)
				return
			}
			assert.Zero(t, res.RequeueAfter)
			if tt.wantClosed {
				assert.Equal(t, []string{"test/repo#5"}, m.closed)
				require.Len(t, m.comments, 1)
				assert.Contains(t, m.comments[0], tt.wantComment)
			} else {
				assert.Empty(t, m.closed, "the PR is left open")
				assert.Empty(t, m.comments, "the PR is not commented on")
			}
			var gone v1alpha1.PromotionStep
			assert.True(t, apierrors.IsNotFound(api.Get(context.Background(), client.ObjectKeyFromObject(step), &gone)),
				"the step is gone")
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
			m := &mockSCM{open: true}
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
	m := &mockSCM{open: true}
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
