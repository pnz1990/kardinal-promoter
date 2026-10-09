//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// TestForgejo_PRControls runs scmPRControls on Forgejo.
//
// Covers SCM-PRCTL-FJ-02.
func TestForgejo_PRControls(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	scmPRControls(t, e, []string{"read:user"})
}

// TestGitea_PRControls runs scmPRControls on Gitea.
//
// Covers SCM-PRCTL-GT-02.
func TestGitea_PRControls(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitea")
	scmPRControls(t, e, []string{"read:user"})
}

// TestGitLab_PRControls runs scmPRControls on GitLab.
//
// Covers SCM-PRCTL-GL-02.
func TestGitLab_PRControls(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitlab")
	scmPRControls(t, e, []string{"read_user"})
}

// prControlsCommit is the Bundle's provenance commit in scmPRControls.
const prControlsCommit = "0123456789abcdef0123456789abcdef01234567"

// scmPRControls promotes prod through a PR with every pr control a
// self-hosted git server applies (#1453): a templated title and body that
// keeps two evidence sections, templated labels, a reviewer, the Bundle's
// author as assignee, and auto-merge with squash, a templated commit
// message and allowImmediate. The repo has no required check or review, so
// nothing is pending: kardinal merges the PR at once, the step is Verified
// without a merge by hand, and prod runs the new version. The PR has every label, the reviewer and the assignee, and the
// squash commit the rendered message. A template that does not render then
// sets the Pipeline Ready=False ValidationFailed.
func scmPRControls(t *testing.T, e *framework.Env, scopes []string) {
	t.Helper()
	a := newArgoApp(t, e, "prod")
	user := "rev-" + a.ns[len(a.ns)-8:]
	e.GitUser(t, user, scopes)
	require.NoError(t, e.GitUsers(t).AddCollaborator(context.Background(), a.repo, user))

	p := a.pipeline(map[string]string{"prod": "pr-review"})
	p.Spec.Environments[0].PR = &v1alpha1.PRConfig{
		TitleTemplate: "Deploy {{ .Bundle.Version }} to {{ .Environment }} ({{ .Bundle.CommitSHA | truncate 7 }})",
		BodyTemplate:  "Requested by @{{ .Bundle.Author }}.\n\n{{ provenanceTable }}\n\n{{ gatesTable }}\n",
		Labels:        []string{"env/{{ .Environment }}", "version/{{ (index .Bundle.Images 0).Tag }}"},
		Reviewers:     []string{user},
		Assignees:     []string{"{{ .Bundle.Author }}"},
		// The test repo has no required check or review, so nothing is
		// pending: allowImmediate lets kardinal merge it at once.
		Merge: &v1alpha1.PRMergeConfig{Auto: true, AllowImmediate: true, Method: "squash",
			CommitMessageTemplate: "{{ .PR.Title }} (#{{ .PR.Number }})\n\nPromoted by kardinal: {{ .Bundle.Name }}"},
	}
	a.apply(t, p)
	waitPipeline(t, e, a.ns, pipelineName, time.Minute, "Ready=True", func(p *v1alpha1.Pipeline) (bool, string) {
		return framework.CondIs(p.Status.Conditions, "Ready", metav1.ConditionTrue, "Valid")
	})

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2, "--author", user, "--commit", prControlsCommit)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assert.Equal(t, "enabled", ps.Status.Outputs["prAutoMerge"], "status.outputs.prAutoMerge")
	assert.Empty(t, ps.Status.Outputs["prControlsError"], "status.outputs.prControlsError")
	assert.Empty(t, ps.Status.Outputs["prLabelsError"], "status.outputs.prLabelsError")
	assertEnvAt(t, a, "prod", fixtures.V2)

	pr := e.WaitPR(t, a.repo, time.Minute, "the merged prod PR of "+bundle, func(pr gitserver.PR) bool {
		return pr.State == "merged" && pr.Head == prHead(bundle, "prod")
	})
	title := "Deploy " + fixtures.V2 + " to prod (0123456)"
	assert.Equal(t, title, pr.Title)
	assert.Subset(t, pr.Labels, []string{"kardinal", "kardinal/promotion", "env/prod", "version/" + fixtures.V2})
	body := prBody(pr.Body)
	assert.True(t, strings.HasPrefix(body, "<!-- kardinal-promoter auto-generated PR -->\nRequested by @"+user+
		".\n\n### Artifact Provenance\n\n| Image | Tag |"), "body:\n%s", body)
	assert.Contains(t, body, "| "+fixtures.Image+" | "+fixtures.V2+" | — | — | "+prControlsCommit+" | "+user+" |")
	assert.Contains(t, body, "### Policy Gate Compliance")
	assert.NotContains(t, body, "### Upstream Verification", "only the sections the template asks for")
	assert.NotContains(t, body, prBodyFooter)

	reviewers, assignees := e.PRPeople(t, a.repo, pr.Number)
	assert.Contains(t, reviewers, user, "the reviewer is requested")
	assert.Equal(t, []string{user}, assignees, "the Bundle's author is the assignee")

	msg, parents := e.Commit(t, a.repo, pr.MergeCommit)
	assert.True(t, strings.HasPrefix(msg, title+" (#"), "merge commit message:\n%s", msg)
	assert.Contains(t, msg, "Promoted by kardinal: "+bundle)
	if e.Git.Kind() != "gitlab" {
		// GitLab's default merge method adds a merge commit over the squash.
		assert.Equal(t, 1, parents, "a squash merge leaves one commit with one parent")
	}

	// A template that does not render is caught before the next Bundle.
	var cur v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(context.Background(), types.NamespacedName{Namespace: a.ns, Name: pipelineName}, &cur))
	cur.Spec.Environments[0].PR.TitleTemplate = "{{ .Bundle.Nope }}"
	require.NoError(t, e.Client.Update(context.Background(), &cur))
	waitPipeline(t, e, a.ns, pipelineName, time.Minute, "Ready=False ValidationFailed", func(p *v1alpha1.Pipeline) (bool, string) {
		ok, seen := framework.CondIs(p.Status.Conditions, "Ready", metav1.ConditionFalse, "ValidationFailed")
		return ok && strings.Contains(findCond(p.Status.Conditions, "Ready").Message, `environment "prod": pr.titleTemplate:`), seen
	})
}

// TestForgejo_AutoMergeFollowsPause checks that kardinal never lets the SCM
// merge a promotion PR while the promotion may not merge (QA on #1477). The
// repo's main branch needs one approval, so auto-merge has something to wait
// for and kardinal turns it on (no allowImmediate). Pausing the Pipeline
// turns it off: an approval then does not merge the PR. Resuming turns it on
// again, and Forgejo merges the approved PR on the next approval event; the
// step is Verified and prod runs the new version.
//
// Covers SCM-PRCTL-FJ-03.
func TestForgejo_AutoMergeFollowsPause(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	ctx := context.Background()
	a := newArgoApp(t, e, "prod")
	protector, ok := e.Git.(gitserver.BranchProtector)
	require.True(t, ok)
	reviewer, ok := e.Git.(gitserver.Reviewer)
	require.True(t, ok)
	require.NoError(t, protector.ProtectBranch(ctx, a.repo, 1))

	p := a.pipeline(map[string]string{"prod": "pr-review"})
	p.Spec.Environments[0].PR = &v1alpha1.PRConfig{Merge: &v1alpha1.PRMergeConfig{Auto: true}}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	autoMerge := func(state, inMessage string) {
		t.Helper()
		e.WaitStep(t, a.ns, pipelineName, bundle, "prod", 2*time.Minute, "auto-merge "+state,
			func(ps *v1alpha1.PromotionStep) (bool, string) {
				return ps.Status.State == "WaitingForMerge" && ps.Status.Outputs["prAutoMerge"] == state &&
						strings.Contains(ps.Status.Message, inMessage),
					fmt.Sprintf("state=%q prAutoMerge=%q message=%q", ps.Status.State, ps.Status.Outputs["prAutoMerge"], ps.Status.Message)
			})
	}
	autoMerge("enabled", "; auto-merge enabled")
	pr := a.openPR(t, bundle, "prod")

	e.MustKardinal(t, a.ns, "pause", pipelineName)
	autoMerge("suspended", "; auto-merge off: pipeline "+pipelineName+" is paused")
	require.NoError(t, reviewer.ApprovePR(ctx, a.repo, pr.Number, "e2e: approved while paused"))
	framework.Consistently(t, 45*time.Second, "the approved PR stays open while the pipeline is paused", func(ctx context.Context) (bool, string) {
		prs, err := e.Git.PullRequests(ctx, a.repo)
		if err != nil {
			return true, err.Error()
		}
		for _, p := range prs {
			if p.Number == pr.Number {
				return p.State == "open", p.State
			}
		}
		return false, "PR gone"
	})

	e.MustKardinal(t, a.ns, "resume", pipelineName)
	autoMerge("enabled", "; auto-merge enabled")
	// Forgejo runs a scheduled merge on a review or status event.
	require.NoError(t, reviewer.ApprovePR(ctx, a.repo, pr.Number, "e2e: approved after resume"))
	e.WaitPRState(t, a.repo, pr.Number, "merged", 2*time.Minute)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}
