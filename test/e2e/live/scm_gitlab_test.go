//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// The GitLab tests run in the gitlab suite, whose controller runs with
// --scm-provider=gitlab against GitLab CE. GitLab calls a PR a merge request
// (MR); kardinal's PR steps and fields cover both.
//
// Not covered here: a label failure (SCM-GL-02). kardinal labels an MR by
// editing it, which any token that may open the MR may also do.

// TestGitLab_PromotionPR checks the MR a pr-review environment opens on
// GitLab, and that rerunning open-pr finds it instead of opening another.
//
// Covers SCM-GL-01, SCM-GL-08.
func TestGitLab_PromotionPR(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitlab")
	assert.Contains(t, controllerArgs(t, e), "--scm-provider=gitlab")
	scmPromotionPR(t, e, nil)
}

// TestGitLab_MergeByPolling checks that without a webhook the PRStatus poll
// finds a merge on GitLab.
//
// Covers SCM-GL-03.
func TestGitLab_MergeByPolling(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitlab")
	scmMergeByPolling(t, e, nil)
}

// TestGitLab_MergeWebhook merges an MR on a project whose webhook GitLab
// delivers with X-Gitlab-Token: the event marks the PRStatus merged before a
// poll would, with the merge commit it carries.
//
// Covers SCM-GL-04.
func TestGitLab_MergeWebhook(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitlab")
	a := newArgoApp(t, e, "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	a.mergeByWebhook(t, bundle, "prod", "", true)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// TestGitLab_WebhookBadSignature checks that a merge event with a wrong or
// no X-Gitlab-Token is refused and changes nothing.
//
// Covers SCM-GL-05.
func TestGitLab_WebhookBadSignature(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitlab")
	a, bundle, pr, _ := scmBadSignature(t, e, gitlabToken)
	a.merge(t, pr)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
}

// TestGitLab_ClosesPRs checks that a newer Bundle and waitForMergeTimeout
// each close the open MR with a comment, delete its source branch, and fail
// the step, and that the closed MR cannot be merged.
//
// Covers SCM-GL-06.
func TestGitLab_ClosesPRs(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitlab")
	scmClosesPRs(t, e)
}

// TestGitLab_Approvals checks that GitLab MR approvals reach bundle.pr.
//
// Covers SCM-GL-07.
func TestGitLab_Approvals(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitlab")
	scmApprovals(t, e)
}

// TestGitLab_TokenCheck checks the startup token check on GitLab: a token
// GitLab rejects and a token with read_api only (no api) each get the scope
// warning, and the controller still serves.
//
// Covers SCM-GL-09.
func TestGitLab_TokenCheck(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitlab")
	ns := e.Namespace(t)
	scmRejectedToken(t, e, ns)

	user := "ro-" + ns[len(ns)-8:]
	tok := e.GitUser(t, user, []string{"read_api"})
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-" + user, Namespace: framework.ControllerNamespace},
		Data:       map[string][]byte{"token": []byte(tok)},
	}
	require.NoError(t, e.Client.Create(context.Background(), sec))
	t.Cleanup(func() {
		if err := e.Client.Delete(context.Background(), sec); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete Secret %s: %v", sec.Name, err)
		}
	})
	since := time.Now()
	v := e.ControllerVariantSpec(t, ns, "GITHUB_TOKEN from a read_api token", func(spec *corev1.PodSpec) {
		framework.SetEnv(spec, "GITHUB_TOKEN", &corev1.EnvVar{Name: "GITHUB_TOKEN", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: sec.Name}, Key: "token"},
		}})
	})
	l := e.VariantLog(v, since).Wait(t, time.Minute, "the warning for the missing api scope",
		framework.LogMessage("SCM TOKEN SCOPE WARNING", "provider", "gitlab", "missing_scope", "api"))
	assert.NotEmpty(t, l.Str("consequence"))
}

// TestGitLab_SCMAPIURL checks that --scm-api-url points the controller at
// the suite's GitLab, and at another host when set to it.
//
// Covers SCM-GL-10.
func TestGitLab_SCMAPIURL(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitlab")
	scmAPIURL(t, e)
}

// TestGitLab_SubgroupProject promotes through a project in a subgroup
// (owner/sub/name): the controller resolves the nested path, opens the MR
// there and sees it merged.
//
// Covers SCM-GL-11.
func TestGitLab_SubgroupProject(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitlab")
	a := newArgoAppIn(t, e, func(t *testing.T, ns string, files map[string][]byte) gitserver.Repo {
		return e.SubgroupRepo(t, "sub", ns, files)
	}, "prod")
	assert.Contains(t, a.repo.CloneURL, "/sub/"+a.repo.Name, "the project is in the subgroup")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	ps, pr := a.waitOpenPR(t, bundle, "prod")
	prs, err := e.PRStatusOf(context.Background(), ps)
	require.NoError(t, err)
	assert.Equal(t, a.repo.Owner+"/"+a.repo.Name, prs.Spec.Repo, "the PRStatus names the nested project")
	assert.Contains(t, prs.Spec.Repo, "/sub/")
	a.merge(t, pr)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// TestGitLab_FastForwardMergeCommit merges an MR on a project that merges by
// fast-forward: GitLab's webhook marks the PRStatus merged without a merge
// commit, and the reconciler reads the commit from the API.
//
// Covers SCM-GL-12.
func TestGitLab_FastForwardMergeCommit(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitlab")
	ff, ok := e.Git.(gitserver.FastForwarder)
	require.True(t, ok, "the suite's git server %T cannot set fast-forward merges", e.Git)
	a := newArgoApp(t, e, "prod")
	require.NoError(t, ff.SetFastForwardMerge(context.Background(), a.repo))
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	a.mergeByWebhook(t, bundle, "prod", "", false)
	assertEnvAt(t, a, "prod", fixtures.V2)
}
