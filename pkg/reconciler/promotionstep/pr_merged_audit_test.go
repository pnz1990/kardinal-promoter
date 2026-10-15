// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// TestPRMergedAudit (#1939): the merge of a step's pull request, the
// approval of a pr-review environment, is a PRMerged AuditEvent naming who
// merged it, how kardinal learned of it and the merge commit; the merger is
// in the step's outputs (prMergedBy) too. A re-run writes no second record.
func TestPRMergedAudit(t *testing.T) {
	ctx := context.Background()
	ps, prs, gate := waitingAutoMergeStep()
	ps.Spec.RequiredGates = nil
	ps.Labels = map[string]string{"kardinal.io/pipeline": "nginx-demo", "kardinal.io/bundle": "bundle-1",
		"kardinal.io/environment": "prod"}
	ps.Status.Outputs = map[string]string{"prURL": "https://github.com/test/repo/pull/5", "prNumber": "5"}
	ps.Status.PRURL = "https://github.com/test/repo/pull/5"
	prs.Status.Open = false
	prs.Status.Merged = true
	prs.Status.MergedBy = "user1"
	prs.Status.MergedVia = v1alpha1.PRMergedViaWebhook
	prs.Status.MergeCommitSHA = "abc1234"
	c := newClient(t, ps, prs, gate, makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo"))
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{}, WorkDirFn: func(_, _ string) string { return t.TempDir() }}
	reconcileStep(t, r, "step")
	reconcileStep(t, r, "step")

	got := getStep(t, c, "step")
	assert.Equal(t, "user1", got.Status.Outputs[promotionstep.OutputPRMergedBy])
	var events v1alpha1.AuditEventList
	require.NoError(t, c.List(ctx, &events))
	var merged []v1alpha1.AuditEvent
	for _, e := range events.Items {
		if e.Spec.Action == promotionstep.AuditActionPRMerged {
			merged = append(merged, e)
		}
	}
	require.Len(t, merged, 1, "once")
	assert.Equal(t, "PR #5 merged by user1 (seen by webhook) as abc1234: https://github.com/test/repo/pull/5", merged[0].Spec.Message)
	assert.Equal(t, "prod", merged[0].Spec.Environment)
}

// TestPRMergedAudit_WaitsForTheMerger (#2003 QA B2): while the PRStatus
// reconciler is still asking the SCM who merged the PR, the step stays in
// WaitingForMerge and writes no record; the one record then names the
// merger. Recorded unavailable, the step goes on and the record says so.
func TestPRMergedAudit_WaitsForTheMerger(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		ctx := context.Background()
		ps, prs, gate := waitingAutoMergeStep()
		ps.Spec.RequiredGates = nil
		ps.Labels = map[string]string{"kardinal.io/pipeline": "nginx-demo", "kardinal.io/bundle": "bundle-1",
			"kardinal.io/environment": "prod"}
		ps.Status.Outputs = map[string]string{"prURL": "https://github.com/test/repo/pull/5", "prNumber": "5"}
		prs.Status.Open, prs.Status.Merged, prs.Status.MergedVia = false, true, v1alpha1.PRMergedViaPoll
		c := newClient(t, ps, prs, gate, makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo"))
		r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{}, WorkDirFn: func(_, _ string) string { return t.TempDir() }}
		reconcileStep(t, r, "step")
		assert.Equal(t, "WaitingForMerge", getStep(t, c, "step").Status.State, "waits for the merger")
		records := func() []v1alpha1.AuditEvent {
			var events v1alpha1.AuditEventList
			require.NoError(t, c.List(ctx, &events))
			var out []v1alpha1.AuditEvent
			for _, e := range events.Items {
				if e.Spec.Action == promotionstep.AuditActionPRMerged {
					out = append(out, e)
				}
			}
			return out
		}
		assert.Empty(t, records())

		var cur v1alpha1.PRStatus
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(prs), &cur))
		if unavailable {
			cur.Status.MergedByUnavailable = true
		} else {
			cur.Status.MergedBy = "user1"
		}
		require.NoError(t, c.Status().Update(ctx, &cur))
		reconcileStep(t, r, "step")
		assert.NotEqual(t, "WaitingForMerge", getStep(t, c, "step").Status.State)
		got := records()
		require.Len(t, got, 1)
		if unavailable {
			assert.Contains(t, got[0].Spec.Message, "by (unknown: ")
		} else {
			assert.Contains(t, got[0].Spec.Message, "merged by user1 (seen by poll)")
		}
	}
}

// TestPRMerged_NotFailedByTheDeadlineOrAHold (#2003 QA N1): a step whose PR
// its PRStatus reports merged, still waiting for the merger's name, is
// neither failed by waitForMergeTimeout nor cancelled by a hold: its change
// is in git. QA's probe.
func TestPRMerged_NotFailedByTheDeadlineOrAHold(t *testing.T) {
	for _, held := range []bool{false, true} {
		ps, prs, gate := waitingAutoMergeStep()
		ps.Spec.RequiredGates = nil
		past := metav1.NewTime(time.Now().Add(-time.Minute))
		ps.Status.WaitForMergeExpiry = &past
		prs.Status.Open, prs.Status.Merged, prs.Status.MergedVia = false, true, v1alpha1.PRMergedViaWebhook
		p := makePipeline("nginx-demo")
		p.Spec.Environments[1].WaitForMergeTimeout = "1h"
		if held {
			at := metav1.Now()
			p.Spec.Holds = []v1alpha1.EnvironmentHold{{Environment: "prod", Bundle: "rollback-x", Reason: "INC",
				CreatedBy: "alice", CreatedAt: &at}}
		}
		c := newClient(t, ps, prs, gate, p, makeBundle("bundle-1", "nginx-demo"))
		r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{}, WorkDirFn: func(_, _ string) string { return t.TempDir() }}
		reconcileStep(t, r, "step")
		got := getStep(t, c, "step")
		assert.Equal(t, "WaitingForMerge", got.Status.State, "held=%v: %s", held, got.Status.Message)
		assert.Contains(t, got.Status.Message, "waiting for the SCM to say who merged it")
	}
}
