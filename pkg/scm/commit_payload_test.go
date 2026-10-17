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

// TestCommitPayload reads each provider's signed commit payload API.
func TestCommitPayload(t *testing.T) {
	const sig = "-----BEGIN SIGNED MESSAGE-----\nMIIE\n-----END SIGNED MESSAGE-----\n"
	const payload = "tree abc\nauthor A <a@x> 1 +0000\ncommitter A <a@x> 1 +0000\n\nmsg\n"
	cases := []struct {
		name     string
		path     string
		body     string
		code     int
		provider func(url string) scm.CommitPayloadReader
		want     scm.CommitPayload
		wantErr  bool
	}{
		{
			name: "github", path: "/repos/org/config/git/commits/abc", code: 200,
			body:     `{"sha":"abc","verification":{"verified":false,"reason":"unknown_signature_type","signature":` + quote(sig) + `,"payload":` + quote(payload) + `}}`,
			provider: func(u string) scm.CommitPayloadReader { return scm.NewGitHubProvider("t", u, "") },
			want:     scm.CommitPayload{SHA: "abc", Payload: payload, Signature: sig},
		},
		{
			name: "github unsigned", path: "/repos/org/config/git/commits/abc", code: 200,
			body:     `{"sha":"abc","verification":{"verified":false,"reason":"unsigned","signature":null,"payload":null}}`,
			provider: func(u string) scm.CommitPayloadReader { return scm.NewGitHubProvider("t", u, "") },
			want:     scm.CommitPayload{SHA: "abc"},
		},
		{
			name: "github missing commit", path: "/repos/org/config/git/commits/abc", code: 404, body: `{"message":"Not Found"}`,
			provider: func(u string) scm.CommitPayloadReader { return scm.NewGitHubProvider("t", u, "") },
			wantErr:  true,
		},
		{
			name: "forgejo", path: "/api/v1/repos/org/config/git/commits/abc", code: 200,
			body:     `{"sha":"abc","commit":{"verification":{"verified":false,"reason":"gpg.error.not_signed_commit","signature":` + quote(sig) + `,"payload":` + quote(payload) + `}}}`,
			provider: func(u string) scm.CommitPayloadReader { return scm.NewForgejoProvider("t", u, "") },
			want:     scm.CommitPayload{SHA: "abc", Payload: payload, Signature: sig},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.EscapedPath() != tc.path {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			got, err := tc.provider(srv.URL).CommitPayload(context.Background(), "org/config", "abc")
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestCommitPayload_Wrappers: the repository guard checks the repository
// and forwards; a provider without the API is unsupported.
func TestCommitPayload_Wrappers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sha":"abc","verification":{"payload":"p","signature":"s"}}`))
	}))
	defer srv.Close()
	allow, err := scm.ParseRepositoryAllowlist([]string{"github.com/org/config"})
	require.NoError(t, err)
	guarded := allow.Guard(scm.NewGitHubProvider("t", srv.URL, ""), "github.com")
	r, ok := guarded.(scm.CommitPayloadReader)
	require.True(t, ok, "the guard forwards CommitPayload")
	got, err := r.CommitPayload(context.Background(), "org/config", "abc")
	require.NoError(t, err)
	assert.Equal(t, scm.CommitPayload{SHA: "abc", Payload: "p", Signature: "s"}, got)
	_, err = r.CommitPayload(context.Background(), "org/other", "abc")
	assert.Error(t, err, "a repository the allowlist does not hold")

	gitlab := allow.Guard(scm.NewGitLabProvider("t", srv.URL, ""), "github.com").(scm.CommitPayloadReader)
	_, err = gitlab.CommitPayload(context.Background(), "org/config", "abc")
	assert.ErrorIs(t, err, scm.ErrCommitPayloadUnsupported)
}

func quote(s string) string {
	b := []byte{'"'}
	for _, r := range s {
		switch r {
		case '\n':
			b = append(b, '\\', 'n')
		case '"':
			b = append(b, '\\', '"')
		default:
			b = append(b, string(r)...)
		}
	}
	return string(append(b, '"'))
}
