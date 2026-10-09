//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// waitGatesStatus waits until the newest kardinal/gates status on the head
// of PR pr has state and a description containing desc.
func waitGatesStatus(t *testing.T, e *framework.Env, a *app, pr gitserver.PR, state, desc string) gitserver.CommitStatus {
	t.Helper()
	reader, ok := e.Git.(gitserver.StatusReader)
	require.True(t, ok, "the %s server lists commit statuses", e.Git.Kind())
	var got gitserver.CommitStatus
	framework.Eventually(t, 2*time.Minute, fmt.Sprintf("kardinal/gates %s on PR #%d", state, pr.Number), func(ctx context.Context) (bool, string) {
		statuses, err := reader.CommitStatuses(ctx, a.repo, pr.HeadSHA)
		if err != nil {
			return false, err.Error()
		}
		var seen []string
		for _, s := range statuses {
			if s.Context == "kardinal/gates" {
				got = s
				return s.State == state && strings.Contains(s.Description, desc), fmt.Sprintf("%s %q", s.State, s.Description)
			}
			seen = append(seen, s.Context)
		}
		return false, fmt.Sprintf("no kardinal/gates status yet (contexts %v)", seen)
	})
	return got
}

// setWindow makes the ChangeWindow cw active (blocking) or not.
func setWindow(t *testing.T, e *framework.Env, cw string, active bool) {
	t.Helper()
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.ChangeWindow
		if err := e.Client.Get(context.Background(), types.NamespacedName{Name: cw}, &cur); err != nil {
			return err
		}
		now := time.Now()
		if active {
			cur.Spec.Start, cur.Spec.End = metav1.NewTime(now.Add(-time.Minute)), metav1.NewTime(now.Add(time.Hour))
		} else {
			cur.Spec.Start, cur.Spec.End = metav1.NewTime(now.Add(-2*time.Hour)), metav1.NewTime(now.Add(-time.Hour))
		}
		return e.Client.Update(context.Background(), &cur)
	})
	require.NoError(t, err, "set ChangeWindow %s active=%v", cw, active)
}

// TestForgejo_GatesCommitStatus mirrors prod's gates to its PR as the
// kardinal/gates commit status while the PR waits for the merge. The status
// is success while the change-window gate passes; a blackout that starts
// while the PR waits flips it to failure, naming the gate and its reason,
// without failing the step or closing the PR; the end of the blackout flips
// it back to success. A merge while the gate blocks (no branch protection
// requiring the check) is not refused: the change is live, so the step is
// health-checked and Verified, and status.outputs.mergedWhileBlocked names
// the gate. The status goes on the commit kardinal pushed; a commit someone
// else pushes to the PR branch gets none.
//
// Covers SCM-GATESTATUS-01, SCM-GATESTATUS-02.
func TestForgejo_GatesCommitStatus(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	require.Contains(t, []string{"forgejo", "gitea"}, e.Git.Kind())
	a := newArgoApp(t, e, "test", "prod")
	ctx := context.Background()
	cw := &v1alpha1.ChangeWindow{
		ObjectMeta: metav1.ObjectMeta{Name: "freeze-" + a.ns},
		Spec: v1alpha1.ChangeWindowSpec{Type: "blackout", Reason: "e2e release freeze",
			Start: metav1.NewTime(time.Now().Add(-2 * time.Hour)), End: metav1.NewTime(time.Now().Add(-time.Hour))},
	}
	require.NoError(t, e.Client.Create(ctx, cw))
	t.Cleanup(func() {
		if err := e.Client.Delete(context.Background(), cw); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete ChangeWindow %s: %v", cw.Name, err)
		}
	})
	g := framework.Gate(a.ns, "freeze", "prod", fmt.Sprintf("!changewindow.isBlocked(%q)", cw.Name), recheck)
	g.Spec.Message = "prod is frozen"
	e.CreateGate(t, g)
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	pr := a.openPR(t, bundle, "prod")
	require.NotEmpty(t, pr.HeadSHA)
	waitGatesStatus(t, e, a, pr, "success", "all 1 gates pass")

	setWindow(t, e, cw.Name, true)
	failed := waitGatesStatus(t, e, a, pr, "failure", "prod is frozen")
	assert.Contains(t, failed.Description, "freeze")
	ps, ok, err := e.Step(ctx, a.ns, pipelineName, bundle, "prod")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "WaitingForMerge", ps.Status.State, "a gate that turns false does not fail a waiting step")
	require.NotNil(t, ps.Spec.Live, "the Graph mirrors the gates onto the step")
	require.Len(t, ps.Spec.Live.Gates, 1)
	assert.False(t, ps.Spec.Live.Gates[0].Ready)
	e.WaitPRState(t, a.repo, pr.Number, "open", time.Minute)

	setWindow(t, e, cw.Name, false)
	waitGatesStatus(t, e, a, pr, "success", "all 1 gates pass")
	ps, _, err = e.Step(ctx, a.ns, pipelineName, bundle, "prod")
	require.NoError(t, err)
	assert.Equal(t, pr.HeadSHA, ps.Status.Outputs["prHeadSHA"], "the status is set on the commit kardinal pushed")

	// Someone pushes to the PR branch: kardinal never sets its status on a
	// head it did not push, so branch protection requiring kardinal/gates
	// holds that head (QA #1518). The gate results keep going to kardinal's
	// commit.
	foreign := e.PushBranch(t, a.repo, prHead(a.ns, bundle, "prod"), "e2e: a commit kardinal did not push", func(dir string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "NOTES.md"), []byte("hand edit\n"), 0o600))
	})
	setWindow(t, e, cw.Name, true)
	waitGatesStatus(t, e, a, pr, "failure", "prod is frozen")
	statuses, err := e.Git.(gitserver.StatusReader).CommitStatuses(ctx, a.repo, foreign)
	require.NoError(t, err)
	for _, s := range statuses {
		assert.NotEqual(t, "kardinal/gates", s.Context, "no kardinal/gates status on a head kardinal did not push: %+v", s)
	}

	// Frozen, and merged anyway.
	a.merge(t, pr)
	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assert.Contains(t, ps.Status.Outputs["mergedWhileBlocked"], "freeze")
	assert.Contains(t, ps.Status.Outputs["mergedWhileBlocked"], "prod is frozen")
	assertEnvAt(t, a, "prod", fixtures.V2)
}
