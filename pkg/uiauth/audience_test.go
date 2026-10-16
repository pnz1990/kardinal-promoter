// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package uiauth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// apiServerAudience is what a kubeconfig or default ServiceAccount token is
// for in the fake API server below.
const apiServerAudience = "https://kubernetes.default.svc"

// fakeAPIServer answers TokenReviews the way the API server does: a token
// "aud:<a>" is valid for audience a; the review returns the intersection of
// spec.audiences and the token's audiences, or the API server audience when
// spec.audiences is empty. A token "legacy" comes from an authenticator that
// is not audience-aware: authenticated, no audiences returned.
func fakeAPIServer(t *testing.T, specs *[][]string) *fake.Clientset {
	cs := fake.NewClientset()
	cs.PrependReactor("create", "tokenreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		tr := a.(k8stesting.CreateAction).GetObject().(*authv1.TokenReview)
		*specs = append(*specs, tr.Spec.Audiences)
		want := tr.Spec.Audiences
		if len(want) == 0 {
			want = []string{apiServerAudience}
		}
		out := tr.DeepCopy()
		switch {
		case tr.Spec.Token == "legacy":
			out.Status = authv1.TokenReviewStatus{Authenticated: true, User: authv1.UserInfo{Username: "legacy"}}
		case len(tr.Spec.Token) > 4 && tr.Spec.Token[:4] == "aud:":
			for _, w := range want {
				if w == tr.Spec.Token[4:] {
					out.Status = authv1.TokenReviewStatus{Authenticated: true,
						User: authv1.UserInfo{Username: "alice"}, Audiences: []string{w}}
				}
			}
		}
		return true, out, nil
	})
	return cs
}

// TestKubeTokenReviewer_Audiences: by default only tokens minted for the
// kardinal-promoter audience authenticate; tokens for the API server's
// audience (kubeconfig tokens) and tokens from authenticators that return no
// audience only with acceptAPIServer.
func TestKubeTokenReviewer_Audiences(t *testing.T) {
	tests := []struct {
		name            string
		audiences       []string
		acceptAPIServer bool
		token           string
		want            bool
		wantReviews     int
	}{
		{"kardinal audience", []string{DefaultAudience}, false, "aud:" + DefaultAudience, true, 1},
		{"API server token refused", []string{DefaultAudience}, false, "aud:" + apiServerAudience, false, 1},
		{"audience-unaware authenticator refused", []string{DefaultAudience}, false, "legacy", false, 1},
		{"other audience refused", []string{DefaultAudience}, false, "aud:vault", false, 1},
		{"opt-in: API server token", []string{DefaultAudience}, true, "aud:" + apiServerAudience, true, 2},
		{"opt-in: kardinal token needs one review", []string{DefaultAudience}, true, "aud:" + DefaultAudience, true, 1},
		{"opt-in: other audience still refused", []string{DefaultAudience}, true, "aud:vault", false, 2},
		{"API server audience only", nil, true, "aud:" + apiServerAudience, true, 1},
		{"custom audience", []string{"ci", "people"}, false, "aud:people", true, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var specs [][]string
			r, err := newKubeTokenReviewer(fakeAPIServer(t, &specs), tt.audiences, tt.acceptAPIServer)
			require.NoError(t, err)
			st, err := r.Review(context.Background(), tt.token)
			require.NoError(t, err)
			assert.Equal(t, tt.want, st.Authenticated, "%+v", st)
			assert.Len(t, specs, tt.wantReviews)
			if len(tt.audiences) > 0 {
				assert.Equal(t, tt.audiences, specs[0], "the first review asks for the configured audiences")
			}
		})
	}
}

func TestNewKubeTokenReviewer_NeedsAnAudience(t *testing.T) {
	_, err := newKubeTokenReviewer(fake.NewClientset(), nil, false)
	require.Error(t, err)
}

// staticReviewer answers every review with st or err.
type staticReviewer struct {
	st  *authv1.TokenReviewStatus
	err error
}

func (s staticReviewer) Review(context.Context, string) (*authv1.TokenReviewStatus, error) {
	return s.st, s.err
}

// TestRequireAudience (#1930): one API's audience rule over a shared review.
// A token for kardinal's audience passes. Any other authenticated token
// passes only with the API's opt-in, and never for a ServiceAccount.
//
// Covers TOKENREVIEW-AUD-02.
func TestRequireAudience(t *testing.T) {
	human := authv1.UserInfo{Username: "oidc:alice"}
	sa := authv1.UserInfo{Username: "system:serviceaccount:team-a:default"}
	kardinal := []string{DefaultAudience}
	tests := []struct {
		name      string
		audiences []string
		accept    bool
		st        authv1.TokenReviewStatus
		want      bool
		wantError string
	}{
		{"kardinal audience", kardinal, false, authv1.TokenReviewStatus{Authenticated: true, User: sa, Audiences: kardinal}, true, ""},
		{"API server token, no opt-in", kardinal, false,
			authv1.TokenReviewStatus{Authenticated: true, User: human, Audiences: []string{apiServerAudience}}, false, "audience is not kardinal-promoter"},
		{"OIDC (no audience), no opt-in", kardinal, false, authv1.TokenReviewStatus{Authenticated: true, User: human}, false, "audience is not"},
		{"API server token, opt-in", kardinal, true,
			authv1.TokenReviewStatus{Authenticated: true, User: human, Audiences: []string{apiServerAudience}}, true, ""},
		{"OIDC (no audience), opt-in", kardinal, true, authv1.TokenReviewStatus{Authenticated: true, User: human}, true, ""},
		{"Pod's default token, opt-in", kardinal, true,
			authv1.TokenReviewStatus{Authenticated: true, User: sa, Audiences: []string{apiServerAudience}}, false, "ServiceAccount token"},
		{"Pod's default token, no audience configured", nil, true,
			authv1.TokenReviewStatus{Authenticated: true, User: sa, Audiences: []string{apiServerAudience}}, false, "ServiceAccount token"},
		{"human, no audience configured", nil, true, authv1.TokenReviewStatus{Authenticated: true, User: human}, true, ""},
		{"not authenticated stays so", kardinal, true, authv1.TokenReviewStatus{Error: "bad"}, false, "bad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := tt.st
			got, err := RequireAudience(staticReviewer{st: &st}, tt.audiences, tt.accept).Review(context.Background(), "t")
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.Authenticated, "%+v", got)
			if tt.wantError != "" {
				assert.Contains(t, got.Error, tt.wantError)
			}
		})
	}
	_, err := RequireAudience(staticReviewer{err: assert.AnError}, kardinal, true).Review(context.Background(), "t")
	assert.ErrorIs(t, err, assert.AnError, "an API failure stays an error (503)")
}
