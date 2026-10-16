//go:build envtest

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/uiauth"
)

// TestTokenReviewAudienceOnAPIServer (#1930) runs kardinal's TokenReview
// rules against a real kube-apiserver, with the tokens of the issue:
//   - a Pod's default ServiceAccount token, which is what a TokenRequest
//     with no audience returns, for the API server's own audience;
//   - a person's token from an authenticator that is not audience-aware,
//     as OIDC is (here the static token file, which ignores audiences too);
//   - a CI ServiceAccount token minted for kardinal-promoter.
//
// Each API's opt-in to the API server's audience is tried alone: the UI's
// (SSO), and the Bundle API's (bundleAPI.acceptAPIServerAudience), which
// admits the person's token there while the UI refuses it. The APIs share
// one reviewer, as the controller builds them, and every refusal names its
// rule.
//
// Covers TOKENREVIEW-AUD-02.
func TestTokenReviewAudienceOnAPIServer(t *testing.T) {
	require.NotEmpty(t, os.Getenv("KUBEBUILDER_ASSETS"), "KUBEBUILDER_ASSETS names the envtest binaries (make envtest-install)")
	tokenFile := filepath.Join(t.TempDir(), "tokens.csv")
	require.NoError(t, os.WriteFile(tokenFile, []byte(`alice-token,alice,1001,"release-managers"`+"\n"), 0o600))
	env := &envtest.Environment{}
	env.ControlPlane.GetAPIServer().Configure().Append("token-auth-file", tokenFile)
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	cs, err := kubernetes.NewForConfig(cfg)
	require.NoError(t, err)
	ctx := context.Background()

	_, err = cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}}, metav1.CreateOptions{})
	require.NoError(t, err)
	mint := func(sa string, audiences ...string) string {
		_, err := cs.CoreV1().ServiceAccounts("team-a").Create(ctx,
			&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: sa}}, metav1.CreateOptions{})
		require.NoError(t, err)
		tr, err := cs.CoreV1().ServiceAccounts("team-a").CreateToken(ctx, sa,
			&authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{Audiences: audiences}}, metav1.CreateOptions{})
		require.NoError(t, err)
		return tr.Status.Token
	}
	tokens := map[string]string{
		"pod-default": mint("default"),
		"ci":          mint("ci", uiauth.DefaultAudience),
		"person":      "alice-token",
	}

	audiences := []string{uiauth.DefaultAudience}
	base, err := uiauth.NewKubeTokenReviewer(cfg, audiences, true)
	require.NoError(t, err)
	// The premise: the API server authenticates every one of them.
	for name, tok := range tokens {
		st, err := base.Review(ctx, tok)
		require.NoError(t, err)
		require.True(t, st.Authenticated, "the API server authenticates %s: %+v", name, st)
	}

	// Each configuration: which API opts in to the API server's audience,
	// and what each API then does with each token: accepts it, refuses it
	// because the API did not opt in, or refuses it as a ServiceAccount's.
	const ok, noOptIn, saToken = "accepted", "this API does not accept the API server's audience",
		"a ServiceAccount token for the API server's audience is refused"
	for _, cfgCase := range []struct {
		name               string
		uiOptIn, bundleOpt bool
		ui, bundles        map[string]string
	}{
		{"the UI opts in (SSO)", true, false,
			map[string]string{"pod-default": saToken, "person": ok, "ci": ok},
			map[string]string{"pod-default": noOptIn, "person": noOptIn, "ci": ok}},
		{"only the Bundle API opts in", false, true,
			map[string]string{"pod-default": noOptIn, "person": noOptIn, "ci": ok},
			map[string]string{"pod-default": saToken, "person": ok, "ci": ok}},
	} {
		// The base accepts the API server's audience when either API opts
		// in, as the controller builds it (cmd/kardinal-controller newReviewers).
		shared, err := uiauth.NewKubeTokenReviewer(cfg, audiences, cfgCase.uiOptIn || cfgCase.bundleOpt)
		require.NoError(t, err)
		for api, c := range map[string]struct {
			accept bool
			want   map[string]string
		}{"UI API": {cfgCase.uiOptIn, cfgCase.ui}, "Bundle API": {cfgCase.bundleOpt, cfgCase.bundles}} {
			reviewer := uiauth.RequireAudience(shared, audiences, c.accept)
			for name, tok := range tokens {
				st, err := reviewer.Review(ctx, tok)
				require.NoError(t, err)
				where := cfgCase.name + ", " + api + ", " + name
				if c.want[name] == ok {
					assert.True(t, st.Authenticated, "%s: %+v", where, st)
					continue
				}
				assert.False(t, st.Authenticated, "%s: %+v", where, st)
				reason, refused := uiauth.AudienceRefusal(st)
				assert.True(t, refused, "%s: refused by the audience rule: %+v", where, st)
				assert.Contains(t, reason, c.want[name], where)
			}
		}
	}
}
