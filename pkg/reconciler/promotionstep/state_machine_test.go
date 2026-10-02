// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// labelled adds the labels the Graph builder sets on every PromotionStep;
// AuditEvents are written only for labelled steps.
func labelled(ps *v1alpha1.PromotionStep) *v1alpha1.PromotionStep {
	ps.Labels = map[string]string{
		"kardinal.io/pipeline":    ps.Spec.PipelineName,
		"kardinal.io/bundle":      ps.Spec.BundleName,
		"kardinal.io/environment": ps.Spec.Environment,
	}
	return ps
}

func newClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.PRStatus{}, &v1alpha1.Bundle{}).
		WithObjects(objs...).Build()
}

// auditActions returns the sorted actions of every AuditEvent in the namespace.
func auditActions(t *testing.T, c client.Client) []string {
	t.Helper()
	var list v1alpha1.AuditEventList
	require.NoError(t, c.List(context.Background(), &list, client.InNamespace("default")))
	actions := []string{}
	for _, ae := range list.Items {
		actions = append(actions, ae.Spec.Action)
	}
	sort.Strings(actions)
	return actions
}

// drain returns every Event recorded so far.
func drain(rec *events.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func openPRStatus(name, repo string, number int) *v1alpha1.PRStatus {
	now := metav1.Now()
	return &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       v1alpha1.PRStatusSpec{Repo: repo, PRNumber: number},
		Status:     v1alpha1.PRStatusStatus{Open: true, LastCheckedAt: &now},
	}
}

func stepStates(ps v1alpha1.PromotionStep) map[string]v1alpha1.StepExecutionState {
	out := map[string]v1alpha1.StepExecutionState{}
	for _, s := range ps.Status.Steps {
		out[s.Name] = s.State
	}
	return out
}

// TestStepErrorRetry proves C03-promotionstep-06: an error returned by a step
// (network, API, git) is retried with backoff instead of failing the step at
// once; once the retries are used up the step fails, closes the PR it opened
// and writes a PromotionFailed AuditEvent and a Warning Event. A step that
// reports StepFailed on its own fails at once.
func TestStepErrorRetry(t *testing.T) {
	prURL := "https://github.com/org/repo/pull/7"
	tests := []struct {
		name        string
		env         string
		startIdx    int
		retryCount  int
		outputs     map[string]string
		noGit       bool
		cloneErr    error
		wantState   string
		wantRetry   int
		wantRequeue time.Duration
		wantMsg     []string
		wantNoMsg   string
		wantClosed  []string
		wantAudit   []string
		wantEvent   string
	}{
		{name: "first transient error is retried", env: "test", cloneErr: errors.New("HTTP 502"),
			wantState: "Promoting", wantRetry: 1, wantRequeue: 10 * time.Second,
			wantMsg: []string{"retrying in 10s (1/5)", "HTTP 502"}, wantAudit: []string{}},
		{name: "backoff doubles", env: "test", retryCount: 2, cloneErr: errors.New("HTTP 502"),
			wantState: "Promoting", wantRetry: 3, wantRequeue: 40 * time.Second,
			wantMsg: []string{"retrying in 40s (3/5)"}, wantAudit: []string{}},
		{name: "backoff is capped at 2m", env: "test", retryCount: 4, cloneErr: errors.New("HTTP 502"),
			wantState: "Promoting", wantRetry: 5, wantRequeue: 2 * time.Minute,
			wantMsg: []string{"retrying in 2m0s (5/5)"}, wantAudit: []string{}},
		{name: "gives up after the retries and closes the PR", env: "prod", retryCount: 5,
			outputs:   map[string]string{"prURL": prURL, "prNumber": "7"},
			cloneErr:  errors.New("connection reset by peer"),
			wantState: "Failed", wantMsg: []string{"connection reset by peer", "gave up after 5 retries"},
			wantClosed: []string{"org/repo#7"}, wantAudit: []string{"PromotionFailed"},
			wantEvent: "Warning Failed"},
		{name: "a step that reports failure fails at once", env: "test", noGit: true,
			wantState: "Failed", wantMsg: []string{"GitClient not configured"}, wantNoMsg: "gave up",
			wantAudit: []string{"PromotionFailed"}, wantEvent: "Warning Failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := asPromoting(labelled(makeStep("step", "p", "b1", tt.env)), makePipeline("p"))
			ps.Status.CurrentStepIndex = tt.startIdx
			ps.Status.RetryCount = tt.retryCount
			ps.Status.Outputs = tt.outputs
			c := newClient(t, ps, makePipeline("p"), makeBundle("b1", "p"))
			m := &mockSCM{open: true}
			rec := events.NewFakeRecorder(20)
			r := &promotionstep.Reconciler{Client: c, SCM: m, Recorder: rec,
				WorkDirFn: func(_, _ string) string { return filepath.Join(t.TempDir(), "w") }}
			if !tt.noGit {
				r.GitClient = &mockGit{cloneErr: tt.cloneErr}
			}

			res, err := r.Reconcile(context.Background(), reqFor("step"))
			require.NoError(t, err)

			got := getStep(t, c, "step")
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Equal(t, tt.wantRetry, got.Status.RetryCount)
			assert.Equal(t, tt.wantRequeue, res.RequeueAfter)
			for _, s := range tt.wantMsg {
				assert.Contains(t, got.Status.Message, s)
			}
			if tt.wantNoMsg != "" {
				assert.NotContains(t, got.Status.Message, tt.wantNoMsg)
			}
			assert.Equal(t, tt.wantClosed, m.closed)
			assert.Equal(t, tt.wantAudit, auditActions(t, c))
			events := strings.Join(drain(rec), "\n")
			if tt.wantEvent != "" {
				assert.Contains(t, events, tt.wantEvent)
			} else {
				assert.Empty(t, events)
			}
		})
	}
}

// TestStepErrorAfterOpenPR proves the second half of C03-promotionstep-06: a
// PR opened in a reconcile whose next step does not complete is recorded in
// the status, so it is neither lost nor opened twice, and a later failure can
// close it (see TestStepErrorRetry).
func TestStepErrorAfterOpenPR(t *testing.T) {
	ps := asPromoting(labelled(makeStep("step", "p", "b1", "prod")), makePipeline("p"))
	ps.Status.CurrentStepIndex = 4 // open-pr, then wait-for-merge
	ps.Spec.PRStatusRef = "prs"    // the Graph creates the PRStatus before the PR exists
	c := newClient(t, ps, openPRStatus("prs", "", 0), makePipeline("p"), makeBundle("b1", "p"))
	m := &mockSCM{prURL: "https://github.com/org/repo/pull/7", prNumber: 7,
		getPRErr: errors.New("connection reset by peer")}
	r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}

	for i := 0; i < 2; i++ {
		reconcileStep(t, r, "step")
		got := getStep(t, c, "step")
		require.NotEqual(t, "Failed", got.Status.State, got.Status.Message)
		assert.Equal(t, "https://github.com/org/repo/pull/7", got.Status.Outputs["prURL"])
		assert.Equal(t, "7", got.Status.Outputs["prNumber"])
		assert.Equal(t, "https://github.com/org/repo/pull/7", got.Status.PRURL)
	}
	assert.Equal(t, 1, m.openCalled, "the PR is not opened twice")
}

func firstFailed(ps v1alpha1.PromotionStep) string {
	for _, s := range ps.Status.Steps {
		if s.State == v1alpha1.StepExecutionFailed {
			return s.Name
		}
	}
	return ""
}

// TestSupersession proves C03-promotionstep-08 and -28: every unfinished step
// of a superseded Bundle, HealthChecking included, is cancelled; its PR is
// closed, found through the PRStatus or, for a placeholder PRStatus, the step
// outputs; a failed close is retried; and the AuditEvent is
// PromotionSuperseded. A step that never left Pending did no work and is
// failed without an AuditEvent, so it is not counted as a superseded
// promotion (E2E-R20).
func TestSupersession(t *testing.T) {
	tests := []struct {
		name        string
		state       string
		retryCount  int
		gitBackoff  bool // a git retry's nextRetryAt is still ahead
		closing     bool // an earlier close failed (retryingClose)
		prStatus    *v1alpha1.PRStatus
		outputs     map[string]string
		closeErrs   []error
		wantState   string
		wantRetry   int
		wantRequeue time.Duration
		wantMsg     string
		wantClosed  []string
		wantAudit   []string
		wantCond    bool // ConditionSupersededCloseFailed is True
	}{
		{name: "step never reconciled is failed without an audit", state: "",
			wantState: "Failed", wantMsg: "superseded before this step started", wantAudit: []string{}},
		{name: "pending step is failed without an audit", state: "Pending",
			wantState: "Failed", wantMsg: "superseded before this step started", wantAudit: []string{}},
		{name: "health checking step is cancelled", state: "HealthChecking",
			wantState: "Failed", wantMsg: "was superseded", wantAudit: []string{"PromotionSuperseded"}},
		{name: "open PR is closed through the PRStatus", state: "WaitingForMerge",
			prStatus:  openPRStatus("prs", "org/repo", 42),
			wantState: "Failed", wantMsg: "was superseded",
			wantClosed: []string{"org/repo#42"}, wantAudit: []string{"PromotionSuperseded"}},
		{name: "placeholder PRStatus falls back to the outputs", state: "WaitingForMerge",
			prStatus:  openPRStatus("prs", "", 0),
			outputs:   map[string]string{"prURL": "https://github.com/org/repo/pull/9", "prNumber": "9"},
			wantState: "Failed", wantClosed: []string{"org/repo#9"}, wantAudit: []string{"PromotionSuperseded"}},
		{name: "merged PR is not closed", state: "WaitingForMerge",
			prStatus: func() *v1alpha1.PRStatus {
				p := openPRStatus("prs", "org/repo", 42)
				p.Status.Open, p.Status.Merged = false, true
				return p
			}(),
			wantState: "Failed", wantAudit: []string{"PromotionSuperseded"}},
		{name: "failed close is retried", state: "WaitingForMerge",
			prStatus: openPRStatus("prs", "org/repo", 42), closeErrs: []error{errors.New("HTTP 502")},
			wantState: "WaitingForMerge", wantRetry: 1, wantRequeue: 10 * time.Second,
			wantMsg:    "closing its PR failed, retrying in 10s (1/5)",
			wantClosed: []string{"org/repo#42"}, wantAudit: []string{}, wantCond: true},
		{name: "failed close after the retries fails the step", state: "WaitingForMerge", retryCount: 5, closing: true,
			prStatus: openPRStatus("prs", "org/repo", 42), closeErrs: []error{errors.New("HTTP 502")},
			wantState: "Failed", wantMsg: "close it by hand",
			wantClosed: []string{"org/repo#42"}, wantAudit: []string{"PromotionSuperseded"}, wantCond: true},
		// B89: the guard does not wait for a git retry's nextRetryAt, and the
		// git retries do not use up the close's.
		{name: "a step superseded during a git retry's backoff is cancelled at once", state: "Promoting",
			retryCount: 3, gitBackoff: true, prStatus: openPRStatus("prs", "org/repo", 42),
			wantState: "Failed", wantMsg: "promotion cancelled",
			wantClosed: []string{"org/repo#42"}, wantAudit: []string{"PromotionSuperseded"}},
		{name: "the close gets its retries after git retries", state: "Promoting",
			retryCount: 3, gitBackoff: true, prStatus: openPRStatus("prs", "org/repo", 42),
			closeErrs: []error{errors.New("HTTP 502")},
			wantState: "Promoting", wantRetry: 1, wantRequeue: 10 * time.Second,
			wantMsg:    "closing its PR failed, retrying in 10s (1/5)",
			wantClosed: []string{"org/repo#42"}, wantAudit: []string{}, wantCond: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := labelled(makeStep("step", "p", "b1", "prod"))
			ps.Status.State = tt.state
			ps.Status.RetryCount = tt.retryCount
			ps.Status.Outputs = tt.outputs
			if tt.gitBackoff {
				next := metav1.NewTime(time.Now().Add(time.Minute))
				ps.Status.NextRetryAt = &next
			}
			if tt.closing {
				retryingClose(ps)
			}
			bundle := makeBundle("b1", "p")
			bundle.Status.Phase = "Superseded"
			objs := []client.Object{ps, makePipeline("p"), bundle}
			if tt.prStatus != nil {
				ps.Spec.PRStatusRef = tt.prStatus.Name
				objs = append(objs, tt.prStatus)
			}
			c := newClient(t, objs...)
			m := &mockSCM{open: true, closeErrs: tt.closeErrs}
			r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{}}

			res, err := r.Reconcile(context.Background(), reqFor("step"))
			require.NoError(t, err)

			got := getStep(t, c, "step")
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Equal(t, tt.wantRetry, got.Status.RetryCount)
			assert.Equal(t, tt.wantRequeue, res.RequeueAfter)
			assert.Contains(t, got.Status.Message, tt.wantMsg)
			assert.Equal(t, tt.wantClosed, m.closed)
			assert.Equal(t, tt.wantAudit, auditActions(t, c))
			assert.Equal(t, tt.wantCond, meta.IsStatusConditionTrue(got.Status.Conditions, promotionstep.ConditionSupersededCloseFailed))
			if tt.wantState == "Failed" {
				assert.Nil(t, got.Status.NextRetryAt)
			}

			// Idempotent: a second reconcile changes nothing more.
			if tt.wantState == "Failed" {
				_, err = r.Reconcile(context.Background(), reqFor("step"))
				require.NoError(t, err)
				assert.Equal(t, got.Status, getStep(t, c, "step").Status)
				assert.Equal(t, tt.wantAudit, auditActions(t, c))
			}
		})
	}
}

// retryingClose marks ps as a superseded step whose PR close failed before,
// so that its retryCount counts the close's retries.
func retryingClose(ps *v1alpha1.PromotionStep) {
	meta.SetStatusCondition(&ps.Status.Conditions, metav1.Condition{
		Type: promotionstep.ConditionSupersededCloseFailed, Status: metav1.ConditionTrue,
		Reason: "CloseFailed", Message: "HTTP 502",
	})
}

// TestSupersession_CacheLagsOwnWrite proves that the supersession guard
// cancels a step from the status the API server has, not the cached one. The
// cache can lag this reconciler's own status patch: the rollback Bundle that a
// health failure creates (applyHealthFailurePolicy) supersedes the step's
// Bundle at once, and that Bundle event wakes the step (bundleMapper) before
// the cache has its RollingBack transition. Cancelling from the cached
// HealthChecking overwrote RollingBack with Failed, "superseded — promotion
// cancelled". A step that already finished is left as it is, with no
// PromotionSuperseded AuditEvent and no Event; a step still in flight is
// cancelled from its fresh status, so the PR it recorded since the cached
// read is closed.
//
// Covers ONFAIL-ROLLBACK-03.
func TestSupersession_CacheLagsOwnWrite(t *testing.T) {
	const alarm = "health alarm via resource (onHealthFailure=rollback): Deployment default/app rollout failed: " +
		"ProgressDeadlineExceeded: ReplicaSet \"app-7f94745cdf\" has timed out progressing. — rollback Bundle b1-rollback-alarm created"
	tests := []struct {
		name       string
		server     func(ps *v1alpha1.PromotionStep) // the status the API server has
		cached     func(ps *v1alpha1.PromotionStep) // the status the informer cache still has
		prStatus   *v1alpha1.PRStatus
		wantState  string
		wantMsg    string
		wantClosed []string
		wantAudit  []string
		wantEvent  string
	}{
		{name: "a step that already finished is left as it is",
			server: func(ps *v1alpha1.PromotionStep) {
				ps.Status.State, ps.Status.Message = "RollingBack", alarm
				ps.Status.Steps = []v1alpha1.StepStatus{
					{Name: "git-clone", State: v1alpha1.StepExecutionCompleted},
					{Name: "health-check", State: v1alpha1.StepExecutionFailed, Message: alarm},
				}
			},
			cached: func(ps *v1alpha1.PromotionStep) {
				ps.Status.State, ps.Status.Message = "HealthChecking", "waiting for resource: Deployment default/app not updated yet"
				ps.Status.Steps[1] = v1alpha1.StepStatus{Name: "health-check", State: v1alpha1.StepExecutionInProgress}
			},
			wantState: "RollingBack", wantMsg: alarm, wantAudit: []string{}},
		{name: "a step still in flight is cancelled from its fresh status",
			server: func(ps *v1alpha1.PromotionStep) {
				ps.Status.State = "WaitingForMerge"
				ps.Status.Outputs = map[string]string{"prURL": "https://github.com/org/repo/pull/9", "prNumber": "9"}
			},
			cached: func(ps *v1alpha1.PromotionStep) {
				ps.Status.State, ps.Status.Outputs = "Promoting", nil
			},
			prStatus:  openPRStatus("prs", "", 0),
			wantState: "Failed", wantMsg: "was superseded", wantClosed: []string{"org/repo#9"},
			wantAudit: []string{"PromotionSuperseded"}, wantEvent: "Warning Failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := labelled(makeStep("step", "p", "b1", "prod"))
			tt.server(ps)
			bundle := makeBundle("b1", "p")
			bundle.Status.Phase = "Superseded"
			objs := []client.Object{ps, makePipeline("p"), bundle}
			if tt.prStatus != nil {
				ps.Spec.PRStatusRef = tt.prStatus.Name
				objs = append(objs, tt.prStatus)
			}
			api := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.PRStatus{}, &v1alpha1.Bundle{}).
				WithObjects(objs...).Build()
			before := getStep(t, api, "step").Status
			// The cached client serves the step as the cache had it before the
			// previous reconcile's status patch; every write goes to api.
			cache := interceptor.NewClient(api, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if err := c.Get(ctx, key, obj, opts...); err != nil {
						return err
					}
					if step, ok := obj.(*v1alpha1.PromotionStep); ok {
						tt.cached(step)
					}
					return nil
				},
			})
			m := &mockSCM{open: true}
			rec := events.NewFakeRecorder(20)
			r := &promotionstep.Reconciler{Client: cache, APIReader: api, SCM: m, GitClient: &mockGit{}, Recorder: rec}

			res, err := r.Reconcile(context.Background(), reqFor("step"))
			require.NoError(t, err)
			assert.Zero(t, res.RequeueAfter)

			got := getStep(t, api, "step")
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Contains(t, got.Status.Message, tt.wantMsg)
			if tt.wantState == before.State {
				assert.Equal(t, before, got.Status, "the finished step is left as it is")
			}
			assert.Equal(t, tt.wantClosed, m.closed)
			assert.Equal(t, tt.wantAudit, auditActions(t, api))
			if tt.wantEvent != "" {
				assert.Contains(t, strings.Join(drain(rec), "\n"), tt.wantEvent)
			} else {
				assert.Empty(t, drain(rec))
			}
		})
	}
}

// TestWaitForMerge proves C03-promotionstep-07, -22 and -30, records the
// merge commit for E2E-01, and fails the step when its PRStatus reports an SCM
// error that polling cannot fix (status.pollError).
func TestWaitForMerge(t *testing.T) {
	past := metav1.NewTime(time.Now().Add(-time.Minute))
	merged := openPRStatus("prs", "org/repo", 42)
	merged.Status.Open, merged.Status.Merged, merged.Status.MergeCommitSHA = false, true, "abc1234"
	tests := []struct {
		name        string
		prStatus    *v1alpha1.PRStatus
		noRef       bool
		timeout     string
		expiry      *metav1.Time
		outputs     map[string]string
		wantState   string
		wantMsg     string
		wantClosed  []string
		wantComment string
		wantRequeue time.Duration
		check       func(t *testing.T, c client.Client, ps v1alpha1.PromotionStep)
	}{
		{name: "timeout closes the PR (C03-22)", prStatus: openPRStatus("prs", "org/repo", 42),
			timeout: "1m", expiry: &past,
			wantState: "Failed", wantMsg: "wait-for-merge timeout after 1m0s",
			wantClosed: []string{"org/repo#42"}, wantComment: "waitForMergeTimeout (1m0s)"},
		{name: "placeholder PRStatus is filled in (C03-07)", prStatus: openPRStatus("prs", "", 0),
			outputs:   map[string]string{"prURL": "https://github.com/org/repo/pull/7", "prNumber": "7"},
			wantState: "WaitingForMerge", wantRequeue: 30 * time.Second,
			check: func(t *testing.T, c client.Client, _ v1alpha1.PromotionStep) {
				var prs v1alpha1.PRStatus
				require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "prs", Namespace: "default"}, &prs))
				assert.Equal(t, 7, prs.Spec.PRNumber)
				assert.Equal(t, "org/repo", prs.Spec.Repo)
				assert.Equal(t, "https://github.com/org/repo/pull/7", prs.Spec.PRURL)
			}},
		{name: "empty prStatusRef fails (C03-30)", noRef: true,
			wantState: "Failed", wantMsg: "spec.prStatusRef is empty"},
		{name: "a PR that cannot be polled fails the step", prStatus: func() *v1alpha1.PRStatus {
			p := openPRStatus("prs", "org/repo", 42)
			p.Status.PollError = "get PR status: GitHub API GET /repos/org/repo/pulls/42: status 401: Bad credentials"
			return p
		}(),
			wantState: "Failed", wantMsg: "PR #42 cannot be polled: get PR status: GitHub API GET /repos/org/repo/pulls/42: status 401"},
		{name: "merge commit is recorded (E2E-01)", prStatus: merged,
			wantState: "HealthChecking", wantMsg: "PR #42 merged",
			check: func(t *testing.T, _ client.Client, ps v1alpha1.PromotionStep) {
				assert.Equal(t, "abc1234", ps.Status.Outputs["mergeCommitSHA"])
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pipeline := makePipeline("p")
			pipeline.Spec.Environments[1].WaitForMergeTimeout = tt.timeout
			ps := makeStep("step", "p", "b1", "prod")
			ps.Spec.StepType = "pr-review"
			ps.Status.State = "WaitingForMerge"
			ps.Status.WaitForMergeExpiry = tt.expiry
			ps.Status.Outputs = tt.outputs
			objs := []client.Object{pipeline, makeBundle("b1", "p")}
			if !tt.noRef {
				ps.Spec.PRStatusRef = "prs"
			}
			if tt.prStatus != nil {
				objs = append(objs, tt.prStatus)
			}
			c := newClient(t, append(objs, ps)...)
			m := &mockSCM{open: true}
			r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{}}

			res, err := r.Reconcile(context.Background(), reqFor("step"))
			require.NoError(t, err)

			got := getStep(t, c, "step")
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Contains(t, got.Status.Message, tt.wantMsg)
			assert.Equal(t, tt.wantClosed, m.closed)
			if tt.wantComment != "" {
				require.Len(t, m.comments, 1)
				assert.Contains(t, m.comments[0], tt.wantComment)
			}
			if tt.wantRequeue != 0 {
				assert.Equal(t, tt.wantRequeue, res.RequeueAfter)
			}
			if tt.check != nil {
				tt.check(t, c, got)
			}
		})
	}
}

// TestStepStatusesFollowTheStateMachine proves E2E-12: status.steps entries
// driven by the state machine (wait-for-merge, health-check) are closed when
// the step leaves those phases, so a Verified step shows no InProgress or
// Pending entry and a failed one shows which entry failed.
func TestStepStatusesFollowTheStateMachine(t *testing.T) {
	seq := []string{"git-clone", "kustomize-set-image", "git-commit", "git-push", "open-pr", "wait-for-merge", "health-check"}
	started := metav1.NewTime(time.Now().Add(-time.Minute))
	initial := func() []v1alpha1.StepStatus {
		out := make([]v1alpha1.StepStatus, len(seq))
		for i, n := range seq {
			out[i] = v1alpha1.StepStatus{Name: n, State: v1alpha1.StepExecutionCompleted, StartedAt: &started, CompletedAt: &started}
		}
		out[5] = v1alpha1.StepStatus{Name: "wait-for-merge", State: v1alpha1.StepExecutionInProgress, StartedAt: &started}
		out[6] = v1alpha1.StepStatus{Name: "health-check", State: v1alpha1.StepExecutionPending}
		return out
	}

	t.Run("merged, then verified", func(t *testing.T) {
		merged := openPRStatus("prs", "org/repo", 42)
		merged.Status.Open, merged.Status.Merged = false, true
		ps := makeStep("step", "p", "b1", "prod")
		ps.Spec.PRStatusRef = "prs"
		ps.Status.State = "WaitingForMerge"
		ps.Status.Steps = initial()
		c := newClient(t, ps, merged, makePipeline("p"), makeBundle("b1", "p"))
		r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{}}

		reconcileStep(t, r, "step")
		got := getStep(t, c, "step")
		require.Equal(t, "HealthChecking", got.Status.State)
		states := stepStates(got)
		assert.Equal(t, v1alpha1.StepExecutionCompleted, states["wait-for-merge"])
		assert.Equal(t, v1alpha1.StepExecutionInProgress, states["health-check"])
		assert.NotNil(t, got.Status.Steps[5].CompletedAt)

		reconcileStep(t, r, "step") // no health adapter: Verified
		got = getStep(t, c, "step")
		require.Equal(t, "Verified", got.Status.State)
		for _, s := range got.Status.Steps {
			assert.Equal(t, v1alpha1.StepExecutionCompleted, s.State, s.Name)
			assert.NotNil(t, s.CompletedAt, s.Name)
		}
	})

	t.Run("closed without merging", func(t *testing.T) {
		closed := openPRStatus("prs", "org/repo", 42)
		closed.Status.Open = false
		ps := makeStep("step", "p", "b1", "prod")
		ps.Spec.PRStatusRef = "prs"
		ps.Status.State = "WaitingForMerge"
		ps.Status.Steps = initial()
		c := newClient(t, ps, closed, makePipeline("p"), makeBundle("b1", "p"))
		r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{}}

		reconcileStep(t, r, "step")
		got := getStep(t, c, "step")
		require.Equal(t, "Failed", got.Status.State)
		assert.Equal(t, "wait-for-merge", firstFailed(got))
		assert.Contains(t, got.Status.Steps[5].Message, "closed without merging")
		assert.Equal(t, v1alpha1.StepExecutionPending, got.Status.Steps[6].State)
	})
}

// TestTransitionsAreRecorded proves C03-promotionstep-14 and -15: every state
// change emits a Kubernetes Event, and the start and the success of a
// promotion write AuditEvents, whichever path reaches Verified.
func TestTransitionsAreRecorded(t *testing.T) {
	ps := labelled(makeStep("step", "p", "b1", "test"))
	c := newClient(t, ps, makePipeline("p"), makeBundle("b1", "p"))
	rec := events.NewFakeRecorder(50)
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{}, Recorder: rec,
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}

	for i := 0; i < 10 && getStep(t, c, "step").Status.State != "Verified"; i++ {
		reconcileStep(t, r, "step")
	}
	require.Equal(t, "Verified", getStep(t, c, "step").Status.State)

	events := drain(rec)
	var reasons []string
	for _, e := range events {
		reasons = append(reasons, strings.Join(strings.Fields(e)[:2], " "))
	}
	assert.Equal(t, []string{"Normal Promoting", "Normal HealthChecking", "Normal Verified"}, reasons)
	assert.Equal(t, []string{"PromotionStarted", "PromotionSucceeded"}, auditActions(t, c))

	// A re-run reconcile of the terminal step records nothing new.
	reconcileStep(t, r, "step")
	assert.Empty(t, drain(rec))
}
