// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// countingSCM verifies commits from a table and records the repositories it
// was asked about.
type countingSCM struct {
	scm.SCMProvider
	sig   scm.CommitSignature
	repos []string
}

func (f *countingSCM) VerifyCommit(_ context.Context, repo, _ string) (scm.CommitSignature, error) {
	f.repos = append(f.repos, repo)
	return f.sig, nil
}

// TestImageVerification_CommitThroughPipelineProvider (#1618): a commit
// whose spec names the Pipeline's provider is checked with that provider,
// resolved like a PromotionStep's (Registry.ForIdentity): its own token,
// host, allowedRepositories and instance signers, never the controller's
// --scm-provider. A provider that is gone or refuses the namespace or the
// repository fails the verification; one that cannot be used yet (its
// Secret not referenceable) is retried, and goes on once it is fixed.
func TestImageVerification_CommitThroughPipelineProvider(t *testing.T) {
	sha := strings.Repeat("abc1", 10)
	person := scm.CommitSignature{Verified: true, Signer: "alice", SHA: sha, Identities: []string{"alice"}}
	const teamURL = "https://git.team.example/org/config"
	provider := func(mut func(*v1alpha1.ScmProviderSpec)) *v1alpha1.ScmProvider {
		p := &v1alpha1.ScmProvider{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "team", UID: "uid-1"},
			Spec: v1alpha1.ScmProviderSpec{Type: "forgejo", APIURL: "https://git.team.example",
				SecretRef: v1alpha1.ScmSecretKeyRef{Name: "team-token"}}}
		if mut != nil {
			mut(&p.Spec)
		}
		return p
	}
	token := func(referenceable bool) *corev1.Secret {
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "team-token"}, Data: map[string][]byte{"token": []byte("t")}}
		if referenceable {
			s.Labels = map[string]string{scm.LabelReferenceable: "true"}
		}
		return s
	}
	teamID := &v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindScmProvider, Name: "team", UID: "uid-1"}
	clusterID := &v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindClusterScmProvider, Name: "shared", UID: "uid-c"}
	cluster := &v1alpha1.ClusterScmProvider{ObjectMeta: metav1.ObjectMeta{Name: "shared", UID: "uid-c"},
		Spec: v1alpha1.ClusterScmProviderSpec{
			ScmProviderSpec: v1alpha1.ScmProviderSpec{Type: "forgejo", APIURL: "https://git.team.example",
				SecretRef: v1alpha1.ScmSecretKeyRef{Name: "team-token", Namespace: ns}},
			AllowedNamespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"scm": "shared"}}}}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}

	cases := []struct {
		name    string
		repo    string
		id      *v1alpha1.ScmProviderIdentity
		objs    []client.Object
		sig     scm.CommitSignature
		phase   string
		msg     string
		asked   bool
		allowed []string
	}{
		{name: "the provider's client on the provider's host", repo: teamURL, id: teamID,
			objs: []client.Object{provider(nil), token(true)}, sig: person,
			phase: v1alpha1.ImageVerificationVerified, msg: "signed by alice", asked: true},
		{name: "allowedRepositories allows it (guarded client)", repo: teamURL, id: teamID,
			objs: []client.Object{provider(func(s *v1alpha1.ScmProviderSpec) { s.AllowedRepositories = []string{"org/*"} }), token(true)}, sig: person,
			phase: v1alpha1.ImageVerificationVerified, msg: "signed by alice", asked: true},
		{name: "a repository the provider does not allow", repo: teamURL, id: teamID,
			objs: []client.Object{provider(func(s *v1alpha1.ScmProviderSpec) { s.AllowedRepositories = []string{"org/app"} }), token(true)}, sig: person,
			phase: v1alpha1.ImageVerificationFailed, msg: "repository org/config is not in its spec.allowedRepositories"},
		{name: "a repository on another host than the provider's", repo: "https://github.com/org/config", id: teamID,
			objs: []client.Object{provider(nil), token(true)}, sig: person,
			phase: v1alpha1.ImageVerificationFailed, msg: `is on github.com, not on the ScmProvider team's SCM host "git.team.example"`},
		{name: "provider deleted and created again", repo: teamURL,
			id:   &v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindScmProvider, Name: "team", UID: "uid-old"},
			objs: []client.Object{provider(nil), token(true)}, sig: person,
			phase: v1alpha1.ImageVerificationFailed, msg: "deleted and created again"},
		{name: "provider missing", repo: teamURL, id: teamID, sig: person,
			phase: v1alpha1.ImageVerificationFailed, msg: "the ScmProvider team-a/team is not found"},
		{name: "ClusterScmProvider that does not select the namespace", repo: teamURL, id: clusterID,
			objs: []client.Object{cluster, token(true), namespace}, sig: person,
			phase: v1alpha1.ImageVerificationFailed, msg: "does not allow"},
		{name: "http:// apiURL without the opt-in: fails at once", repo: "http://git.team.example/org/config", id: teamID,
			objs: []client.Object{provider(func(s *v1alpha1.ScmProviderSpec) { s.APIURL = "http://git.team.example" }), token(true)}, sig: person,
			phase: v1alpha1.ImageVerificationFailed, msg: "is http://, which would send the token in clear text"},
		{name: "apiURL that is not a URL: fails at once", repo: teamURL, id: teamID,
			objs: []client.Object{provider(func(s *v1alpha1.ScmProviderSpec) { s.APIURL = "https://" }), token(true)}, sig: person,
			phase: v1alpha1.ImageVerificationFailed, msg: "is not a URL"},
		{name: "token Secret not referenceable: retried", repo: teamURL, id: teamID,
			objs: []client.Object{provider(nil), token(false)}, sig: person,
			phase: v1alpha1.ImageVerificationPending, msg: "SCM provider: ScmProvider team"},
		{name: "the provider's instance signers, not the controller's", repo: teamURL, id: teamID,
			objs: []client.Object{provider(func(s *v1alpha1.ScmProviderSpec) { s.InstanceSigners = []string{"alice"} }), token(true)}, sig: person,
			phase: v1alpha1.ImageVerificationFailed, msg: "signed by the SCM platform (forgejo-instance", asked: true},
		{name: "the provider's instance signer allowed when listed", repo: teamURL, id: teamID,
			objs: []client.Object{provider(func(s *v1alpha1.ScmProviderSpec) { s.InstanceSigners = []string{"alice"} }), token(true)}, sig: person,
			allowed: []string{scm.PlatformSignerForgejo},
			phase:   v1alpha1.ImageVerificationVerified, msg: "signed by forgejo-instance", asked: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newIV(nil, &v1alpha1.VerifiedCommit{Repo: tc.repo, SHA: sha, ScmProvider: tc.id}, "")
			v.Spec.Policy.Commits.AllowedSigners = tc.allowed
			// The controller's provider would answer "verified by alice"
			// for anything, on github.com, with alice as an instance signer
			// of its own instance: none of that may be used.
			controller := &countingSCM{sig: person}
			h := newHarness(t, controller, append([]client.Object{v}, tc.objs...)...)
			h.r.InstanceSigners = nil
			team := &countingSCM{sig: tc.sig}
			h.r.Providers = &scm.Registry{Client: h.c, New: func(typ, tok, apiURL, _ string) (scm.SCMProvider, error) {
				assert.Equal(t, "forgejo", typ)
				assert.Equal(t, "t", tok, "the provider's own token")
				assert.Equal(t, "https://git.team.example", apiURL)
				return team, nil
			}}
			got, _ := h.reconcile()
			assert.Equal(t, tc.phase, got.Status.Phase, got.Status.Message)
			assert.Contains(t, got.Status.Message, tc.msg)
			assert.Empty(t, controller.repos, "the controller's --scm-provider is never asked")
			if tc.asked {
				assert.Equal(t, []string{"org/config"}, team.repos)
			} else {
				assert.Empty(t, team.repos)
			}
		})
	}

	// A token Secret labelled later: the next check goes on with it.
	t.Run("fixed provider goes on", func(t *testing.T) {
		v := newIV(nil, &v1alpha1.VerifiedCommit{Repo: teamURL, SHA: sha, ScmProvider: teamID}, "")
		tok := token(false)
		h := newHarness(t, &countingSCM{sig: person}, v, provider(nil), tok)
		team := &countingSCM{sig: person}
		h.r.Providers = &scm.Registry{Client: h.c, Now: func() time.Time { return h.now },
			New: func(_, _, _, _ string) (scm.SCMProvider, error) { return team, nil }}
		got, _ := h.reconcile()
		require.Equal(t, v1alpha1.ImageVerificationPending, got.Status.Phase, got.Status.Message)
		require.NoError(t, h.c.Get(context.Background(), client.ObjectKeyFromObject(tok), tok))
		tok.Labels = map[string]string{scm.LabelReferenceable: "true"}
		require.NoError(t, h.c.Update(context.Background(), tok))
		h.now = h.now.Add(scm.DefaultSecretTTL + time.Second)
		got, _ = h.reconcile()
		assert.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase, got.Status.Message)
		assert.Equal(t, []string{"org/config"}, team.repos)
	})

	// Without the registry, a commit with a provider fails: it is never
	// checked with the controller's provider instead.
	t.Run("no registry", func(t *testing.T) {
		v := newIV(nil, &v1alpha1.VerifiedCommit{Repo: teamURL, SHA: sha, ScmProvider: teamID}, "")
		controller := &countingSCM{sig: person}
		h := newHarness(t, controller, v)
		got, _ := h.reconcile()
		assert.Equal(t, v1alpha1.ImageVerificationFailed, got.Status.Phase)
		assert.Contains(t, got.Status.Message, "the controller has no ScmProvider registry")
		assert.Empty(t, controller.repos)
	})
}
