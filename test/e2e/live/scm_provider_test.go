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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

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
		ObjectMeta: metav1.ObjectMeta{Namespace: a.ns, Name: "team-scm"},
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
		ObjectMeta: metav1.ObjectMeta{Namespace: b.ns, Name: "team-scm"},
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
