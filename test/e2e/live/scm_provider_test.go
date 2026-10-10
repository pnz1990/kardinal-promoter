//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// referenceable is the label every Secret an SCM provider names must carry.
var referenceable = map[string]string{"kardinal.io/referenceable": "true"}

// TestForgejo_ScmProvider checks a Pipeline whose spec.git.providerRef names
// a ScmProvider in its namespace, with its own token (another Forgejo user)
// and webhook secret, next to the controller's --scm-provider. The provider
// turns Ready; the translator writes its identity into the step, and the
// PRStatus gets it; the PR is opened as the provider's user; a merge event
// signed with the controller's webhook secret on /webhook/scm marks nothing,
// the same event on the provider's endpoint signed with the controller's
// secret is refused, and signed with the provider's secret it marks the
// PRStatus merged. The step then finishes. A second Pipeline names a
// ScmProvider that does not exist: its Bundle waits with the reason, and
// goes ahead once the provider is created.
//
// Covers SCM-PROVIDERCRD-01, SCM-PROVIDERCRD-02, SCM-PROVIDERCRD-03.
func TestForgejo_ScmProvider(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	ctx := context.Background()
	a := newArgoAppIn(t, e, e.RepoWithoutWebhook, "prod")

	user := "scmp-" + a.ns[len(a.ns)-8:]
	tok := e.GitUser(t, user, []string{"write:repository", "write:issue"})
	require.NoError(t, e.GitUsers(t).AddCollaborator(ctx, a.repo, user))
	const hookSecret = "provider-webhook-secret"
	require.NoError(t, e.Client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: a.ns, Name: "team-scm", Labels: referenceable},
		Data:       map[string][]byte{"token": []byte(tok), "webhook": []byte(hookSecret)},
	}))
	provider := func(name string) *v1alpha1.ScmProvider {
		return &v1alpha1.ScmProvider{
			ObjectMeta: metav1.ObjectMeta{Namespace: a.ns, Name: name},
			Spec: v1alpha1.ScmProviderSpec{Type: "forgejo", APIURL: os.Getenv(framework.EnvSCMAPI),
				SecretRef:           v1alpha1.ScmSecretKeyRef{Name: "team-scm"},
				WebhookSecretRef:    &v1alpha1.ScmSecretKeyRef{Name: "team-scm", Key: "webhook"},
				AllowedRepositories: []string{a.repo.Owner + "/*"}},
		}
	}
	require.NoError(t, e.Client.Create(ctx, provider("team")))
	framework.Eventually(t, time.Minute, "the ScmProvider to be Ready", func(ctx context.Context) (bool, string) {
		var p v1alpha1.ScmProvider
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: "team"}, &p); err != nil {
			return false, err.Error()
		}
		c := meta.FindStatusCondition(p.Status.Conditions, "Ready")
		return c != nil && c.Status == metav1.ConditionTrue, fmt.Sprintf("conditions %+v", p.Status.Conditions)
	})

	pl := a.pipeline(map[string]string{"prod": "pr-review"})
	pl.Spec.Git.ProviderRef = &v1alpha1.ScmProviderRef{Name: "team"}
	a.apply(t, pl)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	ps, pr := a.waitOpenPR(t, bundle, "prod")
	require.NotNil(t, ps.Spec.ScmProvider, "the translator wrote the provider into the step")
	assert.Equal(t, "team", ps.Spec.ScmProvider.Name)
	assert.Equal(t, user, pr.Author, "the PR is opened with the provider's token, not the controller's")
	prs, err := e.PRStatusOf(ctx, ps)
	require.NoError(t, err)
	assert.Equal(t, ps.Spec.ScmProvider, prs.Spec.ScmProvider, "the PRStatus is polled through the same provider")

	providerPath := []string{"namespaces", a.ns, "team"}
	a.mergeSeenByWebhook(t, ps, pr, func(prs *v1alpha1.PRStatus) {
		body := mergedEvent(t, "forgejo", prs.Spec.Repo, pr.Number, "")
		controllerSigned := signedPREvent(forgejoSignature, framework.WebhookSecret(t), body)
		assert.Equal(t, http.StatusNoContent, e.PostSCMWebhook(t, controllerSigned, body),
			"the controller's endpoint accepts the event")
		assert.Equal(t, http.StatusUnauthorized, e.PostSCMWebhookAt(t, providerPath, controllerSigned, body),
			"the provider's endpoint refuses the controller's secret")
		var p v1alpha1.PRStatus
		require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: prs.Namespace, Name: prs.Name}, &p))
		assert.False(t, p.Status.Merged, "the controller's endpoint does not mark a provider's PR")
		require.Equal(t, http.StatusNoContent,
			e.PostSCMWebhookAt(t, providerPath, signedPREvent(forgejoSignature, hookSecret, body), body),
			"the provider's endpoint with its secret")
	})
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)

	// A providerRef to a provider that does not exist yet: the Bundle waits
	// with the reason and goes ahead once it is created.
	b := newArgoAppIn(t, e, e.RepoWithoutWebhook, "prod")
	require.NoError(t, e.Client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: b.ns, Name: "team-scm", Labels: referenceable},
		Data:       map[string][]byte{"token": []byte(tok), "webhook": []byte(hookSecret)},
	}))
	require.NoError(t, e.GitUsers(t).AddCollaborator(ctx, b.repo, user))
	pl = b.pipeline(map[string]string{"prod": "pr-review"})
	pl.Spec.Git.ProviderRef = &v1alpha1.ScmProviderRef{Name: "later"}
	b.apply(t, pl)
	late := e.CreateBundle(t, b.ns, pipelineName, "--image", imageV2)
	framework.Eventually(t, 2*time.Minute, "the Bundle to name the missing provider", func(ctx context.Context) (bool, string) {
		var bu v1alpha1.Bundle
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: b.ns, Name: late}, &bu); err != nil {
			return false, err.Error()
		}
		for _, c := range bu.Status.Conditions {
			if c.Status == metav1.ConditionFalse && strings.Contains(c.Message, "the ScmProvider "+b.ns+"/later is not found") {
				return true, ""
			}
		}
		return false, fmt.Sprintf("phase=%q conditions=%+v", bu.Status.Phase, bu.Status.Conditions)
	})
	p := provider("later")
	p.Namespace = b.ns
	p.Spec.AllowedRepositories = nil
	require.NoError(t, e.Client.Create(ctx, p))
	_, pr = b.waitOpenPR(t, late, "prod")
	assert.Equal(t, user, pr.Author)
	b.merge(t, pr)
	e.WaitStepState(t, b.ns, pipelineName, late, "prod", "Verified", promoteTimeout)
}

// TestForgejo_ClusterScmProvider checks a ClusterScmProvider whose Secret is
// in the Pipeline's namespace (any namespace: only admins create the kind)
// and whose allowedNamespaces selects namespaces by a label. Before the
// namespace has the label, the Bundle waits with the reason; once it has it,
// the PR is opened as the provider's user. The provider is then deleted and
// created again under the same name (another UID): the step waiting for its
// PR fails with the reason instead of polling the PR through the new
// provider.
//
// Covers SCM-PROVIDERCRD-04, SCM-PROVIDERCRD-05.
func TestForgejo_ClusterScmProvider(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	ctx := context.Background()
	a := newArgoAppIn(t, e, e.RepoWithoutWebhook, "prod")
	suffix := a.ns[len(a.ns)-8:]
	user := "cscmp-" + suffix
	tok := e.GitUser(t, user, []string{"write:repository", "write:issue"})
	require.NoError(t, e.GitUsers(t).AddCollaborator(ctx, a.repo, user))
	require.NoError(t, e.Client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: a.ns, Name: "shared-scm", Labels: referenceable},
		Data:       map[string][]byte{"token": []byte(tok)},
	}))
	selectorKey := "e2e.kardinal.io/scm-" + suffix
	name := "e2e-" + suffix
	newProvider := func() *v1alpha1.ClusterScmProvider {
		return &v1alpha1.ClusterScmProvider{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: v1alpha1.ClusterScmProviderSpec{
				ScmProviderSpec: v1alpha1.ScmProviderSpec{Type: "forgejo", APIURL: os.Getenv(framework.EnvSCMAPI),
					SecretRef: v1alpha1.ScmSecretKeyRef{Name: "shared-scm", Namespace: a.ns}},
				AllowedNamespaces: &metav1.LabelSelector{MatchLabels: map[string]string{selectorKey: "on"}},
			},
		}
	}
	deleteProvider := func() {
		p := &v1alpha1.ClusterScmProvider{ObjectMeta: metav1.ObjectMeta{Name: name}}
		if err := e.Client.Delete(context.Background(), p); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete ClusterScmProvider %s: %v", name, err)
		}
	}
	t.Cleanup(deleteProvider)
	require.NoError(t, e.Client.Create(ctx, newProvider()))

	pl := a.pipeline(map[string]string{"prod": "pr-review"})
	pl.Spec.Git.ProviderRef = &v1alpha1.ScmProviderRef{Kind: v1alpha1.KindClusterScmProvider, Name: name}
	a.apply(t, pl)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	waitBundleCondition(t, e, a.ns, bundle, "ClusterScmProvider "+name+", namespace "+a.ns+": the ClusterScmProvider does not allow this namespace")

	var ns corev1.Namespace
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Name: a.ns}, &ns))
	patch := client.MergeFrom(ns.DeepCopy())
	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}
	ns.Labels[selectorKey] = "on"
	require.NoError(t, e.Client.Patch(ctx, &ns, patch))

	ps, pr := a.waitOpenPR(t, bundle, "prod")
	require.NotNil(t, ps.Spec.ScmProvider)
	assert.Equal(t, v1alpha1.KindClusterScmProvider, ps.Spec.ScmProvider.Kind)
	assert.Equal(t, user, pr.Author, "the PR is opened with the ClusterScmProvider's token")

	// Recreate the provider under the same name: another UID.
	oldUID := ps.Spec.ScmProvider.UID
	deleteProvider()
	framework.Eventually(t, time.Minute, "the ClusterScmProvider to be gone", func(ctx context.Context) (bool, string) {
		var p v1alpha1.ClusterScmProvider
		err := e.Client.Get(ctx, types.NamespacedName{Name: name}, &p)
		return apierrors.IsNotFound(err), fmt.Sprintf("get: %v", err)
	})
	require.NoError(t, e.Client.Create(ctx, newProvider()))
	got := e.WaitStep(t, a.ns, pipelineName, bundle, "prod", 3*time.Minute, "the step to fail on the recreated provider",
		func(s *v1alpha1.PromotionStep) (bool, string) {
			return s.Status.State == "Failed", fmt.Sprintf("state=%q message=%q", s.Status.State, s.Status.Message)
		})
	// The step may poll in the gap between the delete and the create ("is
	// not found") or after it ("deleted and created again"): either way the
	// provider it started with is gone, and it fails rather than polling
	// through the new one.
	assert.Contains(t, got.Status.Message, "the SCM provider the step started with is gone")
	assert.Regexp(t, `deleted and created again|is not found`, got.Status.Message)
	var p v1alpha1.ClusterScmProvider
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Name: name}, &p))
	assert.NotEqual(t, oldUID, string(p.UID))
}

// waitBundleCondition waits until a False condition of the Bundle contains
// want.
func waitBundleCondition(t *testing.T, e *framework.Env, ns, bundle, want string) {
	t.Helper()
	framework.Eventually(t, 2*time.Minute, "the Bundle to say: "+want, func(ctx context.Context) (bool, string) {
		var bu v1alpha1.Bundle
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: bundle}, &bu); err != nil {
			return false, err.Error()
		}
		for _, c := range bu.Status.Conditions {
			if c.Status == metav1.ConditionFalse && strings.Contains(c.Message, want) {
				return true, ""
			}
		}
		return false, fmt.Sprintf("phase=%q conditions=%+v", bu.Status.Phase, bu.Status.Conditions)
	})
}

// TestForgejo_ScmProviderChecksSignedCommits (#1618): with
// commits.requireSigned, a Pipeline whose providerRef names a ScmProvider
// has its config commit checked through that provider, with its token and
// its checks, not with the controller's --scm-provider. The ImageVerification
// names the provider. While the provider's allowedRepositories does not
// allow the config repository, the verification fails with that reason (the
// controller's provider would have verified it); once it does, a Bundle of
// the same signed commit verifies and test is promoted.
//
// Covers IMGV-SCMPROVIDER-01.
func TestForgejo_ScmProviderChecksSignedCommits(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	cfg, _ := a.configRepo(t, "test")

	user := "ivp-" + a.ns[len(a.ns)-8:]
	tok := e.GitUser(t, user, []string{"write:repository", "write:issue", "read:user"})
	require.NoError(t, e.GitUsers(t).AddCollaborator(ctx, a.repo, user))
	require.NoError(t, e.GitUsers(t).AddCollaborator(ctx, cfg, user))
	require.NoError(t, e.Client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: a.ns, Name: "team-scm", Labels: referenceable},
		Data:       map[string][]byte{"token": []byte(tok)},
	}))
	prov := &v1alpha1.ScmProvider{
		ObjectMeta: metav1.ObjectMeta{Namespace: a.ns, Name: "team"},
		Spec: v1alpha1.ScmProviderSpec{Type: "forgejo", APIURL: os.Getenv(framework.EnvSCMAPI),
			SecretRef:           v1alpha1.ScmSecretKeyRef{Name: "team-scm"},
			AllowedRepositories: []string{a.repo.Owner + "/" + a.repo.Name}},
	}
	require.NoError(t, e.Client.Create(ctx, prov))

	signer := "ivsigner-" + a.ns[len(a.ns)-8:]
	p := a.pipeline(nil)
	p.Spec.Git.ProviderRef = &v1alpha1.ScmProviderRef{Name: "team"}
	p.Spec.ImageVerification = &v1alpha1.ImageVerificationPolicy{
		Commits: &v1alpha1.CommitSignaturePolicy{RequireSigned: true, AllowedSigners: []string{signer}}}
	a.apply(t, p)
	sha, err := gitserver.CommitAs(ctx, e.Git, cfg, signer, "README.md", []byte("signed\n"), true)
	require.NoError(t, err)

	first := e.CreateBundle(t, a.ns, pipelineName, "--type", "config", "--config-commit", sha, "--config-repo", cfg.CloneURL)
	iv := waitImageVerification(t, e, a.ns, first, "Failed")
	require.NotNil(t, iv.Spec.Commit)
	require.NotNil(t, iv.Spec.Commit.ScmProvider, "the ImageVerification names the Pipeline's provider")
	assert.Equal(t, "team", iv.Spec.Commit.ScmProvider.Name)
	assert.Contains(t, iv.Status.Message, "repository "+cfg.Owner+"/"+cfg.Name+" is not in its spec.allowedRepositories",
		"checked through the ScmProvider, not the controller's provider (which would verify it)")
	e.WaitBundlePhase(t, a.ns, first, "Failed", time.Minute)

	require.NoError(t, e.Client.Get(ctx, client.ObjectKeyFromObject(prov), prov))
	prov.Spec.AllowedRepositories = []string{a.repo.Owner + "/*"}
	require.NoError(t, e.Client.Update(ctx, prov))
	second := e.CreateBundle(t, a.ns, pipelineName, "--type", "config", "--config-commit", sha, "--config-repo", cfg.CloneURL)
	iv = waitImageVerification(t, e, a.ns, second, "Verified")
	require.NotNil(t, iv.Status.Commit)
	assert.Equal(t, signer, iv.Status.Commit.Signer)
	e.WaitStepState(t, a.ns, pipelineName, second, "test", "Verified", promoteTimeout)
}
