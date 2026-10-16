// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// nothingToCommitGit is a git client whose clone of the base branch already
// has the change: the merged PR brought it in.
type nothingToCommitGit struct{ mockGit }

func (nothingToCommitGit) CommitAll(context.Context, string, string, string, string) error {
	return scm.ErrNothingToCommit
}

// mergedFinderSCM is an SCM that finds a merged PR by its head branch.
type mergedFinderSCM struct {
	*mockSCM
	found *scm.FoundPR
	err   error
	heads []string
	shas  []string
	since []time.Time
}

func (m *mergedFinderSCM) FindMergedPR(_ context.Context, _, head, _, headSHA string, since time.Time) (*scm.FoundPR, error) {
	m.heads = append(m.heads, head)
	m.shas = append(m.shas, headSHA)
	m.since = append(m.since, since)
	return m.found, m.err
}

// pushedBeforeCrash is the commit the crashed run pushed to the PR branch,
// which git-push recorded (RecordPushed) before open-pr ran.
const pushedBeforeCrash = "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d"

// TestPRMergedBeforeTheStepRecordedIt (#2065): the controller crashed after
// open-pr created the PR and before the step recorded it, and a reviewer
// merged the PR before the restart. The restarted step finds nothing to
// commit. It looks up its own PR by its head branch, adopts it and waits for
// the PRStatus like any merged PR: the PRMerged AuditEvent names the
// SCM-verified merger and the merge commit, and the outputs carry the PR.
// No second PR is opened. Without such a PR the step has no changes, as
// before; a lookup that fails is retried, never taken as "no PR".
//
// Covers SCM-MERGED-BEFORE-01.
func TestPRMergedBeforeTheStepRecordedIt(t *testing.T) {
	setup := func(t *testing.T, s *mergedFinderSCM) (client.Client, *promotionstep.Reconciler, *v1alpha1.PromotionStep) {
		t.Helper()
		prs := stepPRStatus(v1alpha1.PRStatusSpec{}, 1, 1, v1alpha1.PRStatusStatus{})
		ps := asPromoting(labelled(makeStep("step", "p", "b1", "prod")), makePipeline("p"))
		ps.CreationTimestamp.Time = time.Now().Add(-10 * time.Minute).Truncate(time.Second)
		ps.Status.CurrentStepIndex = 2 // git-commit: the restart has cloned the base branch
		ps.Status.Outputs = map[string]string{"pushedSHA": pushedBeforeCrash}
		ps.Spec.PRStatusRef = prs.Name
		c := newClient(t, ps, prs, makePipeline("p"), makeBundle("b1", "p"))
		r := &promotionstep.Reconciler{Client: c, SCM: s, GitClient: &nothingToCommitGit{},
			WorkDirFn: func(_, _ string) string { return t.TempDir() }}
		return c, r, ps
	}
	merged := func(c client.Client) []v1alpha1.AuditEvent {
		var events v1alpha1.AuditEventList
		require.NoError(t, c.List(context.Background(), &events))
		var out []v1alpha1.AuditEvent
		for _, e := range events.Items {
			if e.Spec.Action == promotionstep.AuditActionPRMerged {
				out = append(out, e)
			}
		}
		return out
	}

	t.Run("its merged PR is adopted and its merge recorded", func(t *testing.T) {
		// The SCM says PR #7 is merged, as it is.
		s := &mergedFinderSCM{mockSCM: &mockSCM{merged: true},
			found: &scm.FoundPR{Number: 7, URL: "https://github.com/test/repo/pull/7", HeadSHA: pushedBeforeCrash, OpenedAt: time.Now().Add(-5 * time.Minute)}}
		c, r, ps := setup(t, s)
		reconcileStep(t, r, "step")

		got := getStep(t, c, "step")
		assert.Equal(t, "WaitingForMerge", got.Status.State, got.Status.Message)
		assert.Equal(t, "7", got.Status.Outputs["prNumber"])
		assert.Equal(t, "https://github.com/test/repo/pull/7", got.Status.Outputs["prURL"])
		assert.Equal(t, "false", got.Status.Outputs["noChanges"], "the change is this promotion's, through its PR")
		assert.Zero(t, s.openCalled, "no second PR")
		require.Len(t, s.heads, 1)
		assert.Contains(t, s.heads[0], "/b1/prod", "looked up by the step's own head branch")
		assert.Equal(t, ps.CreationTimestamp.Add(-time.Minute), s.since[0], "only PRs this promotion could have opened")
		assert.Equal(t, pushedBeforeCrash, s.shas[0], "only a PR with the commit the crashed run pushed")
		var prs v1alpha1.PRStatus
		require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "prs-step"}, &prs))
		assert.Equal(t, 7, prs.Spec.PRNumber, "the PRStatus polls the adopted PR")
		assert.Empty(t, merged(c), "no record until the PRStatus says who merged it")

		// The PRStatus reconciler polls PR #7 and asks the SCM who merged it.
		prs.Status = v1alpha1.PRStatusStatus{ObservedGeneration: prs.Generation, Merged: true, MergedBy: "reviewer",
			MergedVia: v1alpha1.PRMergedViaPoll, MergeCommitSHA: "abc1234"}
		require.NoError(t, c.Status().Update(context.Background(), &prs))
		reconcileStep(t, r, "step")
		reconcileStep(t, r, "step")

		got = getStep(t, c, "step")
		assert.NotEqual(t, "WaitingForMerge", got.Status.State, got.Status.Message)
		assert.Equal(t, "reviewer", got.Status.Outputs[promotionstep.OutputPRMergedBy])
		assert.Equal(t, "abc1234", got.Status.Outputs["mergeCommitSHA"])
		records := merged(c)
		require.Len(t, records, 1, "one PRMerged record")
		assert.Contains(t, records[0].Spec.Message, "PR #7 merged by reviewer (seen by poll) as abc1234")
	})

	t.Run("no PR of its own: no changes, as before", func(t *testing.T) {
		s := &mergedFinderSCM{mockSCM: &mockSCM{}}
		c, r, _ := setup(t, s)
		reconcileStep(t, r, "step")
		got := getStep(t, c, "step")
		assert.NotEqual(t, "WaitingForMerge", got.Status.State, got.Status.Message)
		assert.Equal(t, "true", got.Status.Outputs["noChanges"])
		assert.Empty(t, got.Status.Outputs["prNumber"])
		assert.Len(t, s.heads, 1, "it looked")
		assert.Empty(t, merged(c))
	})

	t.Run("a lookup that fails is retried", func(t *testing.T) {
		s := &mergedFinderSCM{mockSCM: &mockSCM{}, err: errors.New("connection reset")}
		c, r, _ := setup(t, s)
		reconcileStep(t, r, "step")
		got := getStep(t, c, "step")
		assert.Equal(t, "Promoting", got.Status.State, "not past the PR steps")
		assert.Contains(t, got.Status.Message, "looking up a merged PR of")
		assert.Contains(t, got.Status.Message, "retrying")
	})
}

// pushedHeadGit is a git client that reports the commit it pushed.
type pushedHeadGit struct{ mockGit }

func (pushedHeadGit) HeadCommit(context.Context, string) (string, error) {
	return pushedBeforeCrash, nil
}

// storedPushSCM checks, when open-pr creates the PR, that the step's stored
// status already names the commit git-push pushed.
type storedPushSCM struct {
	*mockSCM
	t      *testing.T
	c      client.Client
	stored []string
}

func (s *storedPushSCM) OpenPR(ctx context.Context, repo, title, body, head, base string) (string, int, error) {
	var ps v1alpha1.PromotionStep
	require.NoError(s.t, s.c.Get(ctx, client.ObjectKey{Namespace: "default", Name: "step"}, &ps))
	s.stored = append(s.stored, ps.Status.Outputs["pushedSHA"])
	return s.mockSCM.OpenPR(ctx, repo, title, body, head, base)
}

// TestPushedCommitStoredBeforeOpenPR (#2075 QA): git-push writes the commit
// it pushed to the PR branch (pushedSHA) to the PromotionStep's status
// before open-pr creates the PR. Every PR kardinal opens then has a
// recorded commit, also when the controller dies right after the create, so
// a restarted step can tell its own PR (that head commit) from anyone
// else's.
func TestPushedCommitStoredBeforeOpenPR(t *testing.T) {
	prs := stepPRStatus(v1alpha1.PRStatusSpec{}, 1, 1, v1alpha1.PRStatusStatus{})
	ps := asPromoting(labelled(makeStep("step", "p", "b1", "prod")), makePipeline("p"))
	ps.Status.CurrentStepIndex = 3 // git-push
	ps.Spec.PRStatusRef = prs.Name
	c := newClient(t, ps, prs, makePipeline("p"), makeBundle("b1", "p"))
	s := &storedPushSCM{mockSCM: &mockSCM{open: true, prURL: "https://github.com/test/repo/pull/9", prNumber: 9}, t: t, c: c}
	r := &promotionstep.Reconciler{Client: c, SCM: s, GitClient: &pushedHeadGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}
	reconcileStep(t, r, "step")

	require.Equal(t, []string{pushedBeforeCrash}, s.stored, "the pushed commit is stored when the PR is created")
	got := getStep(t, c, "step")
	assert.Equal(t, "WaitingForMerge", got.Status.State, got.Status.Message)
	assert.Equal(t, pushedBeforeCrash, got.Status.Outputs["pushedSHA"])
}
