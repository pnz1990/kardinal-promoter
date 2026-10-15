// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
