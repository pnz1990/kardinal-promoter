// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// circuitStep is a prod (pr-review) step whose open-pr the test fails with an
// open circuit, and the reconciler and clock that drive it.
type circuitStep struct {
	t    *testing.T
	c    client.Client
	r    *promotionstep.Reconciler
	scm  *mockSCM
	rec  *events.FakeRecorder
	now  time.Time
	name string
}

func newCircuitStep(t *testing.T, edit func(*v1alpha1.Pipeline)) *circuitStep {
	pipeline := makePipeline("nginx-demo")
	if edit != nil {
		edit(pipeline)
	}
	ps := asPromoting(makeStep("step-cw", "nginx-demo", "b1", "prod"), pipeline)
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
		WithObjects(ps, pipeline, makeBundle("b1", "nginx-demo")).Build()
	cs := &circuitStep{t: t, c: c, scm: &mockSCM{prURL: "https://github.com/org/repo/pull/7", prNumber: 7},
		rec: events.NewFakeRecorder(100), now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), name: "step-cw"}
	workDir := filepath.Join(t.TempDir(), "w")
	cs.r = &promotionstep.Reconciler{Client: c, SCM: cs.scm, GitClient: &mockGit{},
		Recorder: cs.rec, WorkDirFn: func(_, _ string) string { return workDir },
		NowFn: func() time.Time { return cs.now }}
	return cs
}

func (cs *circuitStep) open(d time.Duration) {
	cs.scm.openPRErr = fmt.Errorf("open PR: forgejo scm: %w", &scm.ErrCircuitOpen{RetryAfter: cs.now.Add(d)})
}

func (cs *circuitStep) reconcile() time.Duration {
	cs.t.Helper()
	res, err := cs.r.Reconcile(context.Background(), reqFor(cs.name))
	require.NoError(cs.t, err)
	return res.RequeueAfter
}

func (cs *circuitStep) step() v1alpha1.PromotionStep { return getStep(cs.t, cs.c, cs.name) }

func (cs *circuitStep) events(reason string) int {
	n := 0
	for {
		select {
		case e := <-cs.rec.Events:
			if strings.Contains(e, reason) {
				n++
			}
		default:
			return n
		}
	}
}

// inJitter asserts got is want plus at most a fifth of want.
func inJitter(t *testing.T, want, got time.Duration, msg string) {
	t.Helper()
	assert.GreaterOrEqual(t, got, want, msg)
	assert.LessOrEqual(t, got, want+want/5+time.Nanosecond, msg)
}

// TestCircuitOpen_DoesNotSpendRetries covers #1476: a step whose open-pr
// meets an open SCM circuit waits until the circuit lets a call through (with
// jitter) and runs again, without counting a retry: 30 waits leave all five
// retries for real failures. The wait sets SCMUnavailable with one Warning
// Event, and a call the SCM answers ends it. Covers SCM-CIRCUIT-WAIT-01.
func TestCircuitOpen_DoesNotSpendRetries(t *testing.T) {
	cs := newCircuitStep(t, nil)
	start := cs.now
	distinct := map[time.Duration]bool{}
	for i := 0; i < 30; i++ {
		cs.open(7 * time.Second)
		wait := cs.reconcile()
		inJitter(t, 7*time.Second, wait, fmt.Sprintf("wait %d: requeue when the circuit lets a call through", i))
		distinct[wait] = true
		got := cs.step()
		require.Equal(t, "Promoting", got.Status.State, got.Status.Message)
		require.Zero(t, got.Status.RetryCount, "an open circuit is not a retry")
		require.Contains(t, got.Status.Message, "not counted as a retry")
		require.NotNil(t, got.Status.SCMWaitSince)
		require.True(t, got.Status.SCMWaitSince.Time.Equal(start), "the wait started at the first open circuit")
		require.True(t, meta.IsStatusConditionTrue(got.Status.Conditions, promotionstep.ConditionSCMUnavailable))
		cs.now = cs.now.Add(wait)
	}
	assert.Equal(t, 1, cs.events(promotionstep.ConditionSCMUnavailable), "one Warning Event per wait")
	require.Equal(t, 30, cs.scm.openCalled)
	// The same circuit, the same wait: only the jitter tells the waits
	// apart, so the steps waiting for one circuit do not all call at once.
	assert.Greater(t, len(distinct), 10, "jittered waits differ: %v", distinct)

	// A circuit open for an hour (a rate-limit reset) is looked at again at
	// least every retryMaxDelay.
	cs.open(time.Hour)
	inJitter(t, 2*time.Minute, cs.reconcile(), "capped at retryMaxDelay")
	cs.now = cs.now.Add(3 * time.Minute)

	// The outage turns into a real failure: that counts, and the wait ends.
	cs.scm.openPRErr = fmt.Errorf("open PR: %w", errors.New("503 Service Unavailable"))
	assert.Equal(t, 10*time.Second, cs.reconcile())
	got := cs.step()
	assert.Equal(t, 1, got.Status.RetryCount)
	assert.Nil(t, got.Status.SCMWaitSince)
	assert.True(t, meta.IsStatusConditionFalse(got.Status.Conditions, promotionstep.ConditionSCMUnavailable))
	cs.now = cs.now.Add(10 * time.Second)

	cs.scm.openPRErr, cs.scm.open = nil, true
	cs.reconcile()
	got = cs.step()
	assert.Equal(t, "WaitingForMerge", got.Status.State, got.Status.Message)
}

// TestCircuitOpen_WaitIsBounded: an SCM that stays down fails the step
// after the wait bound, the environment's stepTimeoutSeconds when set, else
// the reconciler's SCMWaitTimeout. Covers SCM-CIRCUIT-WAIT-02.
func TestCircuitOpen_WaitIsBounded(t *testing.T) {
	cases := []struct {
		name        string
		stepTimeout int
		waitTimeout time.Duration
		bound       time.Duration
	}{
		{name: "the default, 30 minutes", bound: 30 * time.Minute},
		{name: "--scm-wait-timeout", waitTimeout: 5 * time.Minute, bound: 5 * time.Minute},
		{name: "stepTimeoutSeconds wins", stepTimeout: 90, waitTimeout: 5 * time.Minute, bound: 90 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := newCircuitStep(t, func(p *v1alpha1.Pipeline) {
				for i := range p.Spec.Environments {
					p.Spec.Environments[i].StepTimeoutSeconds = tc.stepTimeout
				}
			})
			cs.r.SCMWaitTimeout = tc.waitTimeout
			start := cs.now
			for cs.now.Sub(start) < tc.bound {
				cs.open(time.Minute)
				wait := cs.reconcile()
				require.NotZero(t, wait)
				require.Equal(t, "Promoting", cs.step().Status.State)
				cs.now = cs.now.Add(wait)
			}
			cs.open(time.Minute)
			cs.reconcile()
			got := cs.step()
			assert.Equal(t, "Failed", got.Status.State)
			assert.Contains(t, got.Status.Message, "the SCM was unavailable for")
			assert.Contains(t, got.Status.Message, "SCM circuit open")
			assert.NotContains(t, got.Status.Message, "gave up after", "the bound fails the step, not the retries")
			assert.Nil(t, got.Status.SCMWaitSince, "the wait is over")
			cond := meta.FindStatusCondition(got.Status.Conditions, promotionstep.ConditionSCMUnavailable)
			require.NotNil(t, cond)
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, "TimedOut", cond.Reason)
		})
	}
}

// TestCircuitOpen_SupersededCloseWaits covers the branches #1476 left
// behind: a superseded step whose PR close meets an open circuit waits for
// it without spending its close retries, then closes the PR.
// Covers SCM-CIRCUIT-WAIT-03.
func TestCircuitOpen_SupersededCloseWaits(t *testing.T) {
	defer promotionstep.NoCircuitJitter()()
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
	r := promotionstep.Reconciler{Client: c, SCM: mock, GitClient: &mockGit{}, Recorder: events.NewFakeRecorder(50),
		WorkDirFn: func(_, _ string) string { return t.TempDir() }, NowFn: func() time.Time { return now }}

	for i := 0; i < 12; i++ {
		res, err := r.Reconcile(context.Background(), reqFor("old-prod"))
		require.NoError(t, err)
		// Each error's RetryAfter is 20s after the reconcile it answers.
		require.Equal(t, 20*time.Second, res.RequeueAfter, fmt.Sprintf("wait %d", i))
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

// TestCircuitOpen_SupersededCloseBounded: a superseded step whose PR close
// meets a circuit that stays open waits for the bound, then spends its close
// retries and fails, asking for the PR to be closed by hand. The wait emits
// exactly one SCMUnavailable Event, sent after the status patch that starts
// the wait, and the failed step's SCMUnavailable is False with reason
// TimedOut. Covers SCM-CIRCUIT-WAIT-03.
func TestCircuitOpen_SupersededCloseBounded(t *testing.T) {
	defer promotionstep.NoCircuitJitter()()
	bundle := makeBundle("old", "my-pipeline")
	bundle.Status.Phase = "Superseded"
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "old-prod", Namespace: "default"},
		Spec:       v1alpha1.PromotionStepSpec{PipelineName: "my-pipeline", BundleName: "old", Environment: "prod"},
		Status: v1alpha1.PromotionStepStatus{State: "WaitingForMerge",
			Outputs: map[string]string{"prURL": "https://github.com/org/repo/pull/42"}},
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mock := &mockSCM{open: true}
	failPatch := true
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(step, bundle, makePipeline("my-pipeline")).
		WithStatusSubresource(step).WithInterceptorFuncs(interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object,
			patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if failPatch {
				failPatch = false
				return errors.New("etcdserver: request timed out")
			}
			return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
		},
	}).Build()
	rec := events.NewFakeRecorder(100)
	r := promotionstep.Reconciler{Client: c, SCM: mock, GitClient: &mockGit{}, Recorder: rec,
		WorkDirFn: func(_, _ string) string { return t.TempDir() }, NowFn: func() time.Time { return now },
		SCMWaitTimeout: 2 * time.Minute}
	cs := &circuitStep{t: t, rec: rec}

	// The patch that would start the wait is lost: no Event for it.
	mock.closeErrs = []error{fmt.Errorf("close PR: %w", &scm.ErrCircuitOpen{RetryAfter: now.Add(20 * time.Second)})}
	_, err := r.Reconcile(context.Background(), reqFor("old-prod"))
	require.Error(t, err)
	assert.Zero(t, cs.events(promotionstep.ConditionSCMUnavailable), "no Event before the status patch succeeds")

	state := ""
	for i := 0; i < 40 && state != "Failed"; i++ {
		mock.closeErrs = []error{fmt.Errorf("close PR: %w", &scm.ErrCircuitOpen{RetryAfter: now.Add(20 * time.Second)})}
		res, err := r.Reconcile(context.Background(), reqFor("old-prod"))
		require.NoError(t, err)
		got := getStep(t, c, "old-prod")
		state = got.Status.State
		now = now.Add(res.RequeueAfter)
	}
	got := getStep(t, c, "old-prod")
	require.Equal(t, "Failed", got.Status.State, "the bound and then the close retries are spent")
	assert.Contains(t, got.Status.Message, "by hand")
	assert.Equal(t, 1, cs.events(promotionstep.ConditionSCMUnavailable), "exactly one SCMUnavailable Event")
	assert.Nil(t, got.Status.SCMWaitSince)
	cond := meta.FindStatusCondition(got.Status.Conditions, promotionstep.ConditionSCMUnavailable)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "TimedOut", cond.Reason)
}
