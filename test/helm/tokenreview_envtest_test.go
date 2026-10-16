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
// The UI API opts in to the API server's audience (SSO) and the Bundle API
// does not. They share one reviewer, as the controller builds them.
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

	tests := []struct {
		api    string
		accept bool
		want   map[string]bool
	}{
		{"UI API (SSO opt-in)", true, map[string]bool{"pod-default": false, "person": true, "ci": true}},
		{"Bundle API (no opt-in)", false, map[string]bool{"pod-default": false, "person": false, "ci": true}},
	}
	for _, tt := range tests {
		reviewer := uiauth.RequireAudience(base, audiences, tt.accept)
		for name, tok := range tokens {
			st, err := reviewer.Review(ctx, tok)
			require.NoError(t, err)
			assert.Equal(t, tt.want[name], st.Authenticated, "%s, %s: %+v", tt.api, name, st)
		}
	}
}
