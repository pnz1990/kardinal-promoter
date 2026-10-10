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
