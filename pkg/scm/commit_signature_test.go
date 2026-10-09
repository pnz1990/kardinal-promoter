// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestVerifyCommit reads each provider's commit signature API.
func TestVerifyCommit(t *testing.T) {
	cases := []struct {
		name   string
		routes map[string]struct {
			code int
			body string
		}
		provider func(url string) scm.CommitVerifier
		repo     string
		want     scm.CommitSignature
		wantErr  bool
	}{
		{
			name: "github verified",
			routes: map[string]struct {
				code int
				body string
			}{"/repos/org/config/commits/abc": {200, `{"sha":"abc","commit":{"committer":{"email":"a@x"},"verification":{"verified":true,"reason":"valid"}},"committer":{"login":"alice"}}`}},
			provider: func(u string) scm.CommitVerifier { return scm.NewGitHubProvider("t", u, "") },
			repo:     "org/config", want: scm.CommitSignature{Verified: true, Signer: "alice", Reason: "valid", SHA: "abc", Identities: []string{"alice", "a@x"}},
		},
		{
			name: "github web-flow (a commit GitHub signed)",
			routes: map[string]struct {
				code int
				body string
			}{"/repos/org/config/commits/abc": {200, `{"sha":"abc","commit":{"committer":{"email":"noreply@github.com"},"verification":{"verified":true,"reason":"valid"}},"committer":{"login":"web-flow"}}`}},
			provider: func(u string) scm.CommitVerifier { return scm.NewGitHubProvider("t", u, "") },
			repo:     "org/config", want: scm.CommitSignature{Verified: true, Signer: "web-flow", Reason: "valid", SHA: "abc", Identities: []string{scm.PlatformSignerGitHub}},
		},
		{
			name: "github unsigned",
			routes: map[string]struct {
				code int
				body string
			}{"/repos/org/config/commits/abc": {200, `{"sha":"abc","commit":{"committer":{"email":"a@x"},"verification":{"verified":false,"reason":"unsigned"}}}`}},
			provider: func(u string) scm.CommitVerifier { return scm.NewGitHubProvider("t", u, "") },
			repo:     "org/config", want: scm.CommitSignature{Signer: "a@x", Reason: "unsigned", SHA: "abc", Identities: []string{"a@x"}},
		},
		{
			name: "github missing commit",
			routes: map[string]struct {
				code int
				body string
			}{"/repos/org/config/commits/abc": {404, `{"message":"No commit found"}`}},
			provider: func(u string) scm.CommitVerifier { return scm.NewGitHubProvider("t", u, "") },
			repo:     "org/config", wantErr: true,
		},
		{
			name: "forgejo verified",
			routes: map[string]struct {
				code int
				body string
			}{"/api/v1/repos/org/config/git/commits/abc": {200, `{"sha":"abc","commit":{"verification":{"verified":true,"reason":"","signer":{"username":"bob","email":"b@x"}}}}`}},
			provider: func(u string) scm.CommitVerifier { return scm.NewForgejoProvider("t", u, "") },
			repo:     "org/config", want: scm.CommitSignature{Verified: true, Signer: "bob", SHA: "abc", Identities: []string{"bob", "b@x"}},
		},
		{
			name: "gitlab verified",
			routes: map[string]struct {
				code int
				body string
			}{
				"/api/v4/projects/grp%2Fconfig/repository/commits/abc":           {200, `{"id":"abc"}`},
				"/api/v4/projects/grp%2Fconfig/repository/commits/abc/signature": {200, `{"signature_type":"PGP","verification_status":"verified","gpg_key_user_email":"c@x","gpg_key_primary_keyid":"ABCD1234"}`},
			},
			provider: func(u string) scm.CommitVerifier { return scm.NewGitLabProvider("t", u, "") },
			repo:     "grp/config", want: scm.CommitSignature{Verified: true, Signer: "c@x", Reason: "verified", SHA: "abc", Identities: []string{"c@x", "ABCD1234"}},
		},
		{
			name: "gitlab verified_system (a commit GitLab signed)",
			routes: map[string]struct {
				code int
				body string
			}{
				"/api/v4/projects/grp%2Fconfig/repository/commits/abc":           {200, `{"id":"abc"}`},
				"/api/v4/projects/grp%2Fconfig/repository/commits/abc/signature": {200, `{"signature_type":"SSH","verification_status":"verified_system"}`},
			},
			provider: func(u string) scm.CommitVerifier { return scm.NewGitLabProvider("t", u, "") },
			repo:     "grp/config", want: scm.CommitSignature{Verified: true, Signer: "gitlab-system", Reason: "verified_system", SHA: "abc", Identities: []string{scm.PlatformSignerGitLab}},
		},
		{
			name: "gitlab unsigned (404 on signature)",
			routes: map[string]struct {
				code int
				body string
			}{
				"/api/v4/projects/grp%2Fconfig/repository/commits/abc":           {200, `{"id":"abc"}`},
				"/api/v4/projects/grp%2Fconfig/repository/commits/abc/signature": {404, `{"message":"404 GPG Signature Not Found"}`},
			},
			provider: func(u string) scm.CommitVerifier { return scm.NewGitLabProvider("t", u, "") },
			repo:     "grp/config", want: scm.CommitSignature{Reason: "unsigned", SHA: "abc"},
		},
		{
			name: "gitlab missing commit",
			routes: map[string]struct {
				code int
				body string
			}{"/api/v4/projects/grp%2Fconfig/repository/commits/abc": {404, `{"message":"404 Commit Not Found"}`}},
			provider: func(u string) scm.CommitVerifier { return scm.NewGitLabProvider("t", u, "") },
			repo:     "grp/config", wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				route, ok := tc.routes[r.URL.EscapedPath()]
				if !ok {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(route.code)
				_, _ = w.Write([]byte(route.body))
			}))
			defer srv.Close()
			got, err := tc.provider(srv.URL).VerifyCommit(context.Background(), tc.repo, "abc")
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
