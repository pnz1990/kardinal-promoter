// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// statusSCM is a mockSCM that also sets commit statuses.
type statusSCM struct {
	mockSCM
	posted []scm.CommitStatus
	shas   []string
	calls  int
	err    error
	token  string
}

func (s *statusSCM) TokenID() string { return s.token }

func (s *statusSCM) SetPRCommitStatus(_ context.Context, _ string, _ int, sha string, st scm.CommitStatus) error {
	s.calls++
	if s.err != nil {
		return s.err
	}
	s.posted = append(s.posted, st)
	s.shas = append(s.shas, sha)
	return nil
}

const pushedSHA = "0123456789abcdef"

func waitingStep(live *v1alpha1.PromotionStepLive, required ...string) (*v1alpha1.PromotionStep, *v1alpha1.PRStatus) {
	ps := makeStep("app-v1-prod", "app", "app-v1", "prod")
	ps.Spec.PRStatusRef = "prs"
	ps.Spec.RequiredGates = required
	ps.Spec.Live = live
	ps.Status.State = "WaitingForMerge"
	ps.Status.PRURL = "https://forgejo.example/org/repo/pulls/5"
	ps.Status.Outputs = map[string]string{"prNumber": "5", "prURL": ps.Status.PRURL, "prHeadSHA": pushedSHA}
	prs := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: "prs", Namespace: "default"},
		Spec:       v1alpha1.PRStatusSpec{PRURL: ps.Status.PRURL, PRNumber: 5, Repo: "org/repo"},
		Status:     v1alpha1.PRStatusStatus{Open: true},
	}
	return ps, prs
}

// gateStatusFixture builds a fake client with ps and prs and a Reconciler
// that posts through s, at a clock the test moves.
func gateStatusFixture(t *testing.T, ps *v1alpha1.PromotionStep, prs *v1alpha1.PRStatus, s scm.SCMProvider) (
	*promotionstep.Reconciler, ctrl.Request, *time.Time) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.PRStatus{}).
		WithObjects(ps, prs, makePipeline("app"), makeBundle("app-v1", "app")).Build()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	r := &promotionstep.Reconciler{Client: c, SCM: s, GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() }, NowFn: func() time.Time { return now }}
	return r, ctrl.Request{NamespacedName: types.NamespacedName{Name: ps.Name, Namespace: "default"}}, &now
}

func reconcileGot(t *testing.T, r *promotionstep.Reconciler, req ctrl.Request) v1alpha1.PromotionStep {
	t.Helper()
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	var got v1alpha1.PromotionStep
	require.NoError(t, r.Get(context.Background(), req.NamespacedName, &got))
	return got
}

// TestGatesCommitStatus posts the kardinal/gates status of a waiting step
// from spec.live.gates on the commit kardinal pushed: unknown (error) when
// the Graph does not mirror them, success with no gates or all passing,
// failure naming the first blocking gate. A second reconcile with nothing new
// posts nothing (idempotent); a change posts again.
func TestGatesCommitStatus(t *testing.T) {
	pass := v1alpha1.LiveGate{Name: "app-v1-soak-prod", Ready: true, Reason: "soak ok"}
	block := v1alpha1.LiveGate{Name: "app-v1-freeze-prod", Ready: false, Reason: "prod is frozen: release freeze"}
	cases := []struct {
		name     string
		live     *v1alpha1.PromotionStepLive
		required []string
		want     scm.CommitStatus
	}{
		{name: "no gates", want: scm.CommitStatus{State: "success", Description: "no kardinal gates on prod"}},
		{name: "not mirrored", required: []string{"app-v1-soak-prod"}, want: scm.CommitStatus{State: "error",
			Description: "gate results unknown: the Graph does not mirror them to this step (created before the upgrade?)"}},
		{name: "all pass", required: []string{"a", "b"}, live: &v1alpha1.PromotionStepLive{Gates: []v1alpha1.LiveGate{pass, pass}},
			want: scm.CommitStatus{State: "success", Description: "all 2 gates pass"}},
		{name: "one blocks", required: []string{"a", "b"}, live: &v1alpha1.PromotionStepLive{Gates: []v1alpha1.LiveGate{pass, block}},
			want: scm.CommitStatus{State: "failure", Description: "app-v1-freeze-prod: prod is frozen: release freeze"}},
		{name: "two block", required: []string{"a", "b"}, live: &v1alpha1.PromotionStepLive{Gates: []v1alpha1.LiveGate{block, block}},
			want: scm.CommitStatus{State: "failure", Description: "2 gates block; app-v1-freeze-prod: prod is frozen: release freeze"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps, prs := waitingStep(tc.live, tc.required...)
			s := &statusSCM{}
			r, req, _ := gateStatusFixture(t, ps, prs, s)
			reconcileGot(t, r, req)
			got := reconcileGot(t, r, req)
			require.Len(t, s.posted, 1, "posted once")
			tc.want.Context = "kardinal/gates"
			assert.Equal(t, tc.want, s.posted[0])
			assert.Equal(t, pushedSHA, s.shas[0], "on the commit kardinal pushed")
			assert.Equal(t, "WaitingForMerge", got.Status.State)
			assert.Contains(t, got.Status.Outputs["gatesStatus"], tc.want.State+":")
			assert.Contains(t, got.Status.Outputs["gatesStatus"], "@0123456789ab")

			// The Graph mirrors a change: one more post.
			got.Spec.Live = &v1alpha1.PromotionStepLive{Gates: []v1alpha1.LiveGate{block}}
			require.NoError(t, r.Update(context.Background(), &got))
			reconcileGot(t, r, req)
			if tc.want.Description != "app-v1-freeze-prod: prod is frozen: release freeze" {
				require.Len(t, s.posted, 2)
				assert.Equal(t, "failure", s.posted[1].State)
			} else {
				assert.Len(t, s.posted, 1, "same status, nothing new")
			}
		})
	}
}

// TestGatesCommitStatus_HeadMove (QA #1518): the status is keyed on the
// commit. When kardinal pushes the PR branch again (prHeadSHA changes) the
// same gate result is posted on the new commit. A step with no pushed commit
// recorded (a PR opened before the upgrade) posts nothing: kardinal never
// sets its status on a head it did not push.
func TestGatesCommitStatus_HeadMove(t *testing.T) {
	ps, prs := waitingStep(nil)
	s := &statusSCM{}
	r, req, _ := gateStatusFixture(t, ps, prs, s)
	got := reconcileGot(t, r, req)
	require.Equal(t, []string{pushedSHA}, s.shas)

	got.Status.Outputs["pushedSHA"] = "fedcba9876543210" // a rebuild (#1504) pushed a new commit
	require.NoError(t, r.Status().Update(context.Background(), &got))
	reconcileGot(t, r, req)
	reconcileGot(t, r, req)
	assert.Equal(t, []string{pushedSHA, "fedcba9876543210"}, s.shas, "posted again on the new commit, once")
	assert.Equal(t, s.posted[0], s.posted[1])

	ps2, prs2 := waitingStep(nil)
	delete(ps2.Status.Outputs, "prHeadSHA")
	s2 := &statusSCM{}
	r2, req2, _ := gateStatusFixture(t, ps2, prs2, s2)
	got = reconcileGot(t, r2, req2)
	assert.Zero(t, s2.calls, "no pushed commit: no status")
	assert.Empty(t, got.Status.Outputs["gatesStatus"])
}

// TestGatesCommitStatus_PermanentError (QA #1518): a 403 that is not a rate
// limit (a token without the commit-status permission) is recorded against
// the token with a retry an hour away and one Warning Event. The polls in
// between send nothing, and neither does a new gate result: the token still
// lacks the permission. A rotated token is tried at once.
//
// Covers SCM-GATESTATUS-04.
func TestGatesCommitStatus_PermanentError(t *testing.T) {
	ps, prs := waitingStep(nil)
	s := &statusSCM{token: "aaaaaaaaaaaa", err: fmt.Errorf("set: %w", &scm.APIError{Provider: "GitHub", StatusCode: http.StatusForbidden,
		Body: `{"message":"Resource not accessible by integration"}`})}
	r, req, now := gateStatusFixture(t, ps, prs, s)
	rec := events.NewFakeRecorder(5)
	r.Recorder = rec
	got := reconcileGot(t, r, req)
	assert.Equal(t, 1, s.calls)
	assert.Equal(t, "WaitingForMerge", got.Status.State, "the step does not fail")
	assert.Equal(t, "error:token-aaaaaaaaaaaa@2026-10-09T13:00:00Z#1", got.Status.Outputs["gatesStatus"])
	require.Len(t, rec.Events, 1)
	assert.Contains(t, <-rec.Events, "GatesStatusFailed")

	for range 3 {
		*now = now.Add(10 * time.Minute)
		reconcileGot(t, r, req)
	}
	assert.Equal(t, 1, s.calls, "no call before the retry time")

	got.Spec.Live = &v1alpha1.PromotionStepLive{Gates: []v1alpha1.LiveGate{{Name: "freeze", Reason: "frozen"}}}
	require.NoError(t, r.Update(context.Background(), &got))
	got = reconcileGot(t, r, req)
	assert.Equal(t, 1, s.calls, "a new gate result does not retry a token without the permission")

	*now = now.Add(31 * time.Minute)
	got = reconcileGot(t, r, req)
	assert.Equal(t, 2, s.calls, "retried after an hour")
	assert.Contains(t, got.Status.Outputs["gatesStatus"], "#2")
	assert.Empty(t, rec.Events, "one Event per failing status")

	s.token, s.err = "bbbbbbbbbbbb", nil
	reconcileGot(t, r, req)
	assert.Equal(t, 3, s.calls, "a rotated token is tried at once")
	require.Len(t, s.posted, 1)
	assert.Equal(t, "failure", s.posted[0].State)
}

// TestGatesCommitStatus_CommitNotInPR: Azure DevOps has no PR iteration for
// kardinal's commit (the head moved). The post waits an hour; a new kardinal
// push (another prHeadSHA) is tried at once.
//
// Covers SCM-GATESTATUS-04.
func TestGatesCommitStatus_CommitNotInPR(t *testing.T) {
	ps, prs := waitingStep(nil)
	s := &statusSCM{err: fmt.Errorf("set: %w", scm.ErrCommitNotInPR)}
	r, req, now := gateStatusFixture(t, ps, prs, s)
	got := reconcileGot(t, r, req)
	assert.Equal(t, 1, s.calls)
	assert.Regexp(t, `^error:[0-9a-f]{12}@2026-10-09T13:00:00Z#1$`, got.Status.Outputs["gatesStatus"])

	*now = now.Add(59 * time.Minute)
	reconcileGot(t, r, req)
	assert.Equal(t, 1, s.calls, "not before an hour")
	*now = now.Add(2 * time.Minute)
	got = reconcileGot(t, r, req)
	assert.Equal(t, 2, s.calls, "retried after an hour")

	got.Status.Outputs["prHeadSHA"] = "fedcba9876543210"
	require.NoError(t, r.Status().Update(context.Background(), &got))
	reconcileGot(t, r, req)
	assert.Equal(t, 3, s.calls, "a new kardinal push is tried at once")
}

// TestGatesCommitStatus_TransientError: a 503 leaves the step waiting and
// is retried after a backoff that doubles from 30s; then the status posts.
func TestGatesCommitStatus_TransientError(t *testing.T) {
	ps, prs := waitingStep(nil)
	s := &statusSCM{err: errors.New("503 Service Unavailable")}
	r, req, now := gateStatusFixture(t, ps, prs, s)
	got := reconcileGot(t, r, req)
	assert.Equal(t, "WaitingForMerge", got.Status.State)
	assert.Contains(t, got.Status.Outputs["gatesStatus"], "@2026-10-09T12:00:30Z#1")
	reconcileGot(t, r, req)
	assert.Equal(t, 1, s.calls, "not before 30s")
	*now = now.Add(30 * time.Second)
	got = reconcileGot(t, r, req)
	assert.Equal(t, 2, s.calls)
	assert.Contains(t, got.Status.Outputs["gatesStatus"], "@2026-10-09T12:01:30Z#2", "then 60s")

	s.err = nil
	*now = now.Add(time.Minute)
	got = reconcileGot(t, r, req)
	assert.Len(t, s.posted, 1, "posted on the retry")
	assert.Equal(t, "success:", got.Status.Outputs["gatesStatus"][:8])
}

// TestGatesCommitStatus_Config: --gates-commit-status=false posts nothing;
// --gates-status-context names the status; a provider without commit
// statuses posts nothing and fails nothing; a closed PR gets no status.
//
// Covers SCM-GATESTATUS-04.
func TestGatesCommitStatus_Config(t *testing.T) {
	ps, prs := waitingStep(nil)
	s := &statusSCM{}
	r, req, _ := gateStatusFixture(t, ps, prs, s)
	r.GatesStatusDisabled = true
	reconcileGot(t, r, req)
	assert.Zero(t, s.calls, "disabled")

	r.GatesStatusDisabled, r.GatesStatusContext = false, "acme/gates"
	reconcileGot(t, r, req)
	require.Len(t, s.posted, 1)
	assert.Equal(t, "acme/gates", s.posted[0].Context)

	ps2, prs2 := waitingStep(nil)
	r2, req2, _ := gateStatusFixture(t, ps2, prs2, &mockSCM{})
	reconcileGot(t, r2, req2)

	ps3, prs3 := waitingStep(nil)
	checked := metav1.Now()
	prs3.Status = v1alpha1.PRStatusStatus{Open: false, LastCheckedAt: &checked, ClosedAt: &checked}
	s3 := &statusSCM{}
	r3, req3, _ := gateStatusFixture(t, ps3, prs3, s3)
	reconcileGot(t, r3, req3)
	assert.Zero(t, s3.calls, "closed PR")
}

// TestGatesCommitStatus_MergedWhileBlocked: a PR merged while a gate blocks
// moves to HealthChecking as any merged PR does, and the step records the
// blocking gate in status.outputs.mergedWhileBlocked.
func TestGatesCommitStatus_MergedWhileBlocked(t *testing.T) {
	ps, prs := waitingStep(&v1alpha1.PromotionStepLive{Gates: []v1alpha1.LiveGate{{Name: "freeze", Ready: false, Reason: "frozen"}}}, "freeze")
	prs.Status = v1alpha1.PRStatusStatus{Merged: true}
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.PRStatus{}).
		WithObjects(ps, prs, makePipeline("app"), makeBundle("app-v1", "app")).Build()
	s := &statusSCM{}
	r := promotionstep.Reconciler{Client: c, SCM: s, GitClient: &mockGit{}, WorkDirFn: func(_, _ string) string { return t.TempDir() }}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: ps.Name, Namespace: "default"}})
	require.NoError(t, err)
	var got v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: ps.Name, Namespace: "default"}, &got))
	assert.Equal(t, "HealthChecking", got.Status.State)
	assert.Equal(t, "freeze: frozen", got.Status.Outputs["mergedWhileBlocked"])
	assert.Empty(t, s.posted, "nothing is posted on a merged PR")
}

// pushedStatusSCM opens PRs (mockSCM) and records the commits it sets
// commit statuses on.
type pushedStatusSCM struct {
	*mockSCM
	shas []string
}

func (s *pushedStatusSCM) SetPRCommitStatus(_ context.Context, _ string, _ int, sha string, _ scm.CommitStatus) error {
	s.shas = append(s.shas, sha)
	return nil
}

// TestGatesCommitStatus_OnPushedCommit (QA #1518): a pr-review step records
// the commit git-push pushed (its persisted pushedSHA output) as prHeadSHA
// when it opens the PR, and the kardinal/gates status goes on that commit.
// With a git client that cannot report the pushed commit, nothing is
// recorded, nothing is posted, and a GatesStatusNoCommit Warning says why.
func TestGatesCommitStatus_OnPushedCommit(t *testing.T) {
	run := func(t *testing.T, git scm.GitClient) (v1alpha1.PromotionStep, *pushedStatusSCM, []string) {
		pl, b := makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo")
		pl.Spec.Git.Branch = "main"
		step := builtStep(t, pl, b, "prod")
		step.Status.State = "Promoting"
		c := newClient(t, step, pl, b, openPRStatus(step.Spec.PRStatusRef, "", 0))
		s := &pushedStatusSCM{mockSCM: &mockSCM{open: true, prURL: "https://github.com/test/repo/pull/5", prNumber: 5}}
		rec := events.NewFakeRecorder(20)
		r := &promotionstep.Reconciler{Client: c, SCM: s, GitClient: git, Recorder: rec,
			WorkDirFn: func(_, _ string) string { return t.TempDir() }}
		reconcileStep(t, r, step.Name) // records the step list
		reconcileStep(t, r, step.Name) // runs it: WaitingForMerge
		reconcileStep(t, r, step.Name) // waits: posts the status
		got := getStep(t, c, step.Name)
		require.Equal(t, "WaitingForMerge", got.Status.State, got.Status.Message)
		return got, s, drain(rec)
	}

	got, s, evs := run(t, &pushRecorder{headGit: headGit{sha: newSHA}})
	assert.Equal(t, newSHA, got.Status.Outputs["pushedSHA"], "git-push persists the pushed commit")
	assert.Equal(t, newSHA, got.Status.Outputs["prHeadSHA"])
	assert.Equal(t, []string{newSHA}, s.shas, "the status is set on the pushed commit")
	for _, e := range evs {
		assert.NotContains(t, e, "GatesStatusNoCommit")
	}

	got, s, evs = run(t, &mockGit{})
	assert.Empty(t, got.Status.Outputs["prHeadSHA"])
	assert.Empty(t, s.shas, "no pushed commit, no status")
	assert.Condition(t, func() bool {
		for _, e := range evs {
			if strings.Contains(e, "GatesStatusNoCommit") {
				return true
			}
		}
		return false
	}, "events: %v", evs)
}

// TestGatesCommitStatus_UsesTheStepsProvider: a step whose PR was opened on
// a ScmProvider (spec.scmProvider, #1517) posts the gates status through that
// provider, never the controller's default one. Here the controller has no
// provider registry, so nothing is posted at all.
func TestGatesCommitStatus_UsesTheStepsProvider(t *testing.T) {
	ps, prs := waitingStep(nil)
	ps.Spec.ScmProvider = &v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindScmProvider, Name: "team-gitlab", UID: "u1"}
	s := &statusSCM{}
	r, req, _ := gateStatusFixture(t, ps, prs, s)
	_, _ = r.Reconcile(context.Background(), req)
	_, _ = r.Reconcile(context.Background(), req)
	assert.Zero(t, s.calls, "the default provider is not the step's")
}
