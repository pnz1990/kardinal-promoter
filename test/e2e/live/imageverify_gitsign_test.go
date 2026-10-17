//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification/signtest"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// TestGiteaFamily_GitsignCommit: a config commit signed the way gitsign
// signs (a CMS signature by a Fulcio certificate of the agent's workflow,
// its Rekor entry embedded) is verified by kardinal itself against
// commits.authorities, reading the payload from the Forgejo/Gitea API and
// the authority's trusted root from a Secret (a private Sigstore: the
// test's own Fulcio and Rekor). The ImageVerification records method
// gitsign, the signer, and the AgentIdentity whose signingIdentities match
// it, and the version promotes. A commit signed by another workflow fails.
//
// Covers IMGV-GITSIGN-01, IMGV-SIGNER-AGENT-01.
func TestGiteaFamily_GitsignCommit(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireGiteaFamily(t, e)
	ctx := context.Background()
	const issuer = "https://token.actions.githubusercontent.com"
	identity := "https://github.com/myorg/agents/.github/workflows/fixer.yml@refs/heads/main"
	vs, err := ca.NewVirtualSigstore()
	require.NoError(t, err)

	a := newArgoApp(t, e, "test")
	rootJSON, err := signtest.TrustedRootJSON(vs)
	require.NoError(t, err)
	require.NoError(t, e.Client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "sigstore", Namespace: a.ns, Labels: map[string]string{"kardinal.io/referenceable": "true"}},
		Data:       map[string][]byte{"trusted_root.json": rootJSON},
	}))
	agent := "fixer-" + a.ns[len(a.ns)-8:]
	id := &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: agent},
		Spec: v1alpha1.AgentIdentitySpec{Subjects: []v1alpha1.AgentSubject{{Kind: "User", Name: agent + "@agents.example.com"}},
			Sponsor: v1alpha1.AgentSponsor{User: "alice@example.com"}, Scope: v1alpha1.AgentScope{Namespaces: []string{a.ns}},
			SigningIdentities: []v1alpha1.AgentSigningIdentity{{Issuer: issuer, Subject: identity}}}}
	require.NoError(t, e.Client.Create(ctx, id))
	t.Cleanup(func() {
		var cur v1alpha1.AgentIdentity
		if err := e.Client.Get(context.Background(), client.ObjectKeyFromObject(id), &cur); err != nil {
			return
		}
		cur.Spec.Suspended = true
		_ = e.Client.Update(context.Background(), &cur)
		_ = e.Client.Delete(context.Background(), &cur)
	})
	framework.Eventually(t, time.Minute, "AgentIdentity "+agent+" ready", func(ctx context.Context) (bool, string) {
		var got v1alpha1.AgentIdentity
		if err := e.Client.Get(ctx, client.ObjectKeyFromObject(id), &got); err != nil {
			return false, err.Error()
		}
		return got.Status.Ready && got.Status.ObservedGeneration == got.Generation, fmt.Sprintf("ready=%v (%s)", got.Status.Ready, got.Status.Reason)
	})

	p := a.pipeline(nil)
	p.Spec.ImageVerification = &v1alpha1.ImageVerificationPolicy{
		Authorities: []v1alpha1.SignatureAuthority{{Name: "agents", Keyless: &v1alpha1.KeylessAuthority{Issuer: issuer, Subject: identity,
			TrustedRootRef: &v1alpha1.SecretKeyName{Name: "sigstore", Key: "trusted_root.json"}}}},
		Commits: &v1alpha1.CommitSignaturePolicy{RequireSigned: true, Authorities: []string{"agents"}},
	}
	a.apply(t, p)
	cfg, _ := a.configRepo(t, "test")
	sha, err := gitserver.CommitSignedWith(ctx, e.Git, cfg, "gitsign-"+a.ns[len(a.ns)-8:], "README.md", []byte("signed by the fixer\n"),
		signtest.GitsignSigner{VS: vs, Identity: identity, Issuer: issuer})
	require.NoError(t, err)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--type", "config", "--config-commit", sha, "--config-repo", cfg.CloneURL)
	iv := waitImageVerification(t, e, a.ns, bundle, "Verified")
	require.NotNil(t, iv.Status.Commit)
	c := iv.Status.Commit
	assert.True(t, c.Verified)
	assert.Equal(t, "gitsign", c.Method)
	assert.Equal(t, identity, c.Signer)
	assert.Equal(t, issuer, c.Issuer)
	assert.Equal(t, "agents", c.Authority)
	assert.Equal(t, agent, c.Agent, "the signer is the agent's workload")
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)

	// Another workflow's signature: the authority refuses it.
	other := "https://github.com/myorg/agents/.github/workflows/other.yml@refs/heads/main"
	sha2, err := gitserver.CommitSignedWith(ctx, e.Git, cfg, "gitsign2-"+a.ns[len(a.ns)-8:], "README.md", []byte("signed by another workflow\n"),
		signtest.GitsignSigner{VS: vs, Identity: other, Issuer: issuer})
	require.NoError(t, err)
	second := e.CreateBundle(t, a.ns, pipelineName, "--type", "config", "--config-commit", sha2, "--config-repo", cfg.CloneURL)
	iv = waitImageVerification(t, e, a.ns, second, "Failed")
	assert.Contains(t, iv.Status.Message, "does not verify against commits.authorities")
	require.NotNil(t, iv.Status.Commit)
	assert.Equal(t, "gitsign", iv.Status.Commit.Method)
	assert.False(t, iv.Status.Commit.Verified)
}
