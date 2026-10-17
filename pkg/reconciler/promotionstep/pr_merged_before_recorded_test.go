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
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
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
	shas  [][]string
	since []time.Time
}

func (m *mergedFinderSCM) FindMergedPR(_ context.Context, _, head, _ string, headSHAs []string, since time.Time) (*scm.FoundPR, error) {
	m.heads = append(m.heads, head)
	m.shas = append(m.shas, headSHAs)
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
		assert.Equal(t, []string{pushedBeforeCrash}, s.shas[0], "only a PR with a commit the crashed run pushed")
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

	// #2092 (QA): a PR adopted by its commit list, with a reviewer's fixup
	// on top, is recorded as adopted, with its head, who opened it and the
	// commit kardinal did not push, in the step's message, the PRMerged
	// AuditEvent and the receipt.
	//
	// Covers SCM-MERGED-BEFORE-03.
	t.Run("an adopted PR with a fixup says so everywhere", func(t *testing.T) {
		const fixup = "5555555555555555555555555555555555555555"
		s := &mergedFinderSCM{mockSCM: &mockSCM{merged: true},
			found: &scm.FoundPR{Number: 7, URL: "https://github.com/test/repo/pull/7", HeadSHA: fixup, Author: "bob",
				OpenedAt: time.Now().Add(-5 * time.Minute), Unpushed: []scm.PRCommit{{SHA: fixup, Author: "bob"}}}}
		c, r, _ := setup(t, s)
		r.Receipts = &promotionstep.ReceiptConfig{ClusterID: "cluster-1"}
		reconcileStep(t, r, "step")
		got := getStep(t, c, "step")
		want := "head " + fixup + ", opened by bob, 1 commit not pushed by kardinal: " + fixup + " by bob"
		assert.Equal(t, fixup, got.Status.Outputs["prAdoptedHead"])
		assert.Equal(t, "bob", got.Status.Outputs["prOpenedBy"])
		assert.Equal(t, "PR #7 merged; recording the merge (adopted PR: "+want+")", got.Status.Message)

		var prs v1alpha1.PRStatus
		require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "prs-step"}, &prs))
		prs.Status = v1alpha1.PRStatusStatus{ObservedGeneration: prs.Generation, Merged: true, MergedBy: "reviewer",
			MergedVia: v1alpha1.PRMergedViaPoll, MergeCommitSHA: "abc1234"}
		require.NoError(t, c.Status().Update(context.Background(), &prs))
		for i := 0; i < 20 && got.Status.State != "Verified" && got.Status.State != "Failed"; i++ {
			reconcileStep(t, r, "step")
			got = getStep(t, c, "step")
		}
		require.Equal(t, "Verified", got.Status.State, got.Status.Message)
		records := merged(c)
		require.Len(t, records, 1)
		assert.Equal(t, "PR #7 merged by reviewer (seen by poll) as abc1234: https://github.com/test/repo/pull/7 (adopted PR: "+want+")",
			records[0].Spec.Message)

		var rcs v1alpha1.PromotionReceiptList
		require.NoError(t, c.List(context.Background(), &rcs))
		require.Len(t, rcs.Items, 1, "the receipt")
		st, err := receipt.Parse([]byte(rcs.Items[0].Spec.Statement))
		require.NoError(t, err)
		require.NotNil(t, st.Predicate.Kardinal.Change.PR)
		assert.Equal(t, &receipt.AdoptedPR{HeadSHA: fixup, OpenedBy: "bob",
			Unpushed: []receipt.PRCommit{{SHA: fixup, Author: "bob"}}}, st.Predicate.Kardinal.Change.PR.Adopted)
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

// graphPushGit is pushedHeadGit that counts pushes and reads the PR
// branch's commit graph.
type graphPushGit struct {
	pushedHeadGit
	graph  map[string]scm.GraphCommit
	pushes int
	// head is the PR branch's head on the remote; "" is kardinal's commit.
	head string
}

func (g *graphPushGit) RemoteBranchHead(context.Context, string, string, scm.GitAuth) (string, error) {
	if g.head == "" {
		return pushedBeforeCrash, nil
	}
	return g.head, nil
}

func (g *graphPushGit) Push(context.Context, string, string, string, scm.GitAuth, bool) error {
	g.pushes++
	return nil
}

func (g *graphPushGit) BranchGraph(context.Context, string, string, scm.GitAuth, int) (string, map[string]scm.GraphCommit, error) {
	return "", g.graph, nil
}

// openHeadSCM has PR #5 open on kardinal's branch with head head.
type openHeadSCM struct {
	*mockSCM
	head string
}

func (m *openHeadSCM) FindOpenPR(context.Context, string, string, string) (*scm.FoundPR, error) {
	if m.head == "" {
		return nil, nil // no open PR (closed)
	}
	return &scm.FoundPR{Number: 5, URL: "https://github.com/test/repo/pull/5", HeadSHA: m.head}, nil
}

const foreignHead = "deadbeefcafe0000000000000000000000000000"

// TestOpenPRWithAForeignHead (#2075 QA): an open PR of kardinal's branch
// whose head does not contain kardinal's commit (someone else committed to
// the branch) is not adopted, gets no create, and the branch is never pushed
// over it. Each try is a Warning Event naming the head and what to do, the
// head is kept in the step's evidence (status.outputs.prForeignHead), and the
// step fails once its retry budget is spent: retries resume at open-pr and
// push nothing. A head with commits on top of kardinal's is adopted.
//
// Covers SCM-OPEN-FIRST-01.
func TestOpenPRWithAForeignHead(t *testing.T) {
	setup := func(t *testing.T, head string, graph map[string]scm.GraphCommit) (client.Client, *promotionstep.Reconciler, *graphPushGit, *openHeadSCM, *events.FakeRecorder, *time.Time) {
		t.Helper()
		prs := stepPRStatus(v1alpha1.PRStatusSpec{}, 1, 1, v1alpha1.PRStatusStatus{})
		ps := asPromoting(labelled(makeStep("step", "p", "b1", "prod")), makePipeline("p"))
		ps.Status.CurrentStepIndex = 2 // git-commit
		ps.Spec.PRStatusRef = prs.Name
		c := newClient(t, ps, prs, makePipeline("p"), makeBundle("b1", "p"))
		g := &graphPushGit{graph: graph}
		s := &openHeadSCM{mockSCM: &mockSCM{open: true}, head: head}
		rec := events.NewFakeRecorder(100)
		now := time.Now()
		r := &promotionstep.Reconciler{Client: c, SCM: s, GitClient: g, Recorder: rec, NowFn: func() time.Time { return now },
			WorkDirFn: func(_, _ string) string { return t.TempDir() }}
		return c, r, g, s, rec, &now
	}

	t.Run("a foreign head fails loudly, bounded, without a push", func(t *testing.T) {
		c, r, g, s, rec, now := setup(t, foreignHead, map[string]scm.GraphCommit{foreignHead: {Parents: []string{"base"}}})
		reconcileStep(t, r, "step")
		got := getStep(t, c, "step")
		assert.Equal(t, "Promoting", got.Status.State, "retried")
		assert.Contains(t, got.Status.Message, "does not contain "+pushedBeforeCrash)
		assert.Contains(t, got.Status.Message, "reset the branch to kardinal's commit")
		assert.Contains(t, got.Status.Message, "promote again")
		assert.Equal(t, foreignHead, got.Status.Outputs["prForeignHead"], "the foreign head is in the step's evidence")
		require.NotEmpty(t, rec.Events)
		ev := <-rec.Events
		assert.Contains(t, ev, "Warning PRBranchForeignCommit")
		assert.Contains(t, ev, foreignHead)

		for i := 0; i < 10 && getStep(t, c, "step").Status.State != "Failed"; i++ {
			*now = now.Add(time.Hour) // past every retry delay
			reconcileStep(t, r, "step")
		}
		got = getStep(t, c, "step")
		assert.Equal(t, "Failed", got.Status.State, "the retry budget is spent: %s", got.Status.Message)
		assert.Equal(t, 1, g.pushes, "only kardinal's own push, before open-pr; never over the foreign commit")
		assert.Zero(t, s.openCalled, "no create")
		assert.Empty(t, got.Status.Outputs["prNumber"])
	})

	t.Run("no open PR, and the branch carries a foreign commit: nothing opened", func(t *testing.T) {
		// Someone pushed over kardinal's commit, and the PR was closed: a PR
		// opened now would carry kardinal's evidence without its change.
		c, r, g, s, rec, _ := setup(t, "", map[string]scm.GraphCommit{foreignHead: {Parents: []string{"base"}}})
		g.head = foreignHead
		reconcileStep(t, r, "step")
		got := getStep(t, c, "step")
		assert.Equal(t, "Promoting", got.Status.State, "retried")
		assert.Contains(t, got.Status.Message, "kardinal opens no PR from it")
		assert.Equal(t, foreignHead, got.Status.Outputs["prForeignHead"])
		require.NotEmpty(t, rec.Events)
		assert.Contains(t, <-rec.Events, "Warning PRBranchForeignCommit")
		assert.Zero(t, s.openCalled, "no PR opened from a branch without kardinal's change")
		assert.Equal(t, 1, g.pushes, "only kardinal's own push")
	})

	t.Run("a head on top of kardinal's commit is adopted", func(t *testing.T) {
		const fixup = "f1x0000000000000000000000000000000000000"
		c, r, g, s, _, _ := setup(t, fixup, map[string]scm.GraphCommit{
			fixup:             {Parents: []string{pushedBeforeCrash}},
			pushedBeforeCrash: {Parents: []string{"base"}},
		})
		reconcileStep(t, r, "step")
		got := getStep(t, c, "step")
		assert.Equal(t, "WaitingForMerge", got.Status.State, got.Status.Message)
		assert.Equal(t, "5", got.Status.Outputs["prNumber"])
		assert.Equal(t, 1, g.pushes)
		assert.Zero(t, s.openCalled)
	})
}

// TestPRMergedWithAnEarlierPush (#2065, found by the chaos scenario): the
// first run pushed C1, opened the PR and crashed; a reviewer merged it with
// C1; a restarted run, cloned before the merge landed, pushed a new commit
// C2 (its commit time differs) and crashed again. The run that then finds
// nothing to commit adopts the merged PR: its head is C1, one of the
// commits the step pushed (status.outputs.pushedSHAs), though not the
// latest (pushedSHA).
func TestPRMergedWithAnEarlierPush(t *testing.T) {
	const c1, c2 = "c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1", pushedBeforeCrash
	prs := stepPRStatus(v1alpha1.PRStatusSpec{}, 1, 1, v1alpha1.PRStatusStatus{})
	ps := asPromoting(labelled(makeStep("step", "p", "b1", "prod")), makePipeline("p"))
	ps.CreationTimestamp.Time = time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	ps.Status.CurrentStepIndex = 2
	ps.Spec.PRStatusRef = prs.Name
	ps.Status.Outputs = map[string]string{"pushedSHA": c2, "pushedSHAs": c1 + "," + c2}
	c := newClient(t, ps, prs, makePipeline("p"), makeBundle("b1", "p"))
	s := &mergedFinderSCM{mockSCM: &mockSCM{merged: true},
		found: &scm.FoundPR{Number: 7, URL: "https://github.com/test/repo/pull/7", HeadSHA: c1, OpenedAt: time.Now().Add(-5 * time.Minute)}}
	r := &promotionstep.Reconciler{Client: c, SCM: s, GitClient: &nothingToCommitGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}
	reconcileStep(t, r, "step")

	got := getStep(t, c, "step")
	assert.Equal(t, "WaitingForMerge", got.Status.State, got.Status.Message)
	assert.Equal(t, "7", got.Status.Outputs["prNumber"])
	require.Len(t, s.shas, 1)
	assert.Equal(t, []string{c1, c2}, s.shas[0], "every commit the step pushed")
}
