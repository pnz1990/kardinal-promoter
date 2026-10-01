// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package scm_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestValidateGitHubTokenScopes_EmptyToken verifies that an empty token returns
// a warning without making an HTTP call.
func TestValidateGitHubTokenScopes_EmptyToken(t *testing.T) {
	warnings, err := scm.ValidateGitHubTokenScopes(context.Background(), "", "")
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0].MissingScope, "token")
}

// TestValidateGitHubTokenScopes_Unauthorized verifies that a 401 response
// returns a "token rejected" warning.
func TestValidateGitHubTokenScopes_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	warnings, err := scm.ValidateGitHubTokenScopes(context.Background(), "bad-token", srv.URL)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0].MissingScope, "valid token")
}

// TestValidateGitHubTokenScopes_MissingRepoScope verifies that a response with
// X-OAuth-Scopes that does not include "repo" or "public_repo" returns a warning.
func TestValidateGitHubTokenScopes_MissingRepoScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Token with only read:user scope — insufficient for PR operations.
		w.Header().Set("X-OAuth-Scopes", "read:user, read:org")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"login": "test-user"})
	}))
	defer srv.Close()

	warnings, err := scm.ValidateGitHubTokenScopes(context.Background(), "token-with-read-only", srv.URL)
	require.NoError(t, err)
	require.Len(t, warnings, 1, "expected one warning for missing 'repo' scope")
	assert.Equal(t, "repo", warnings[0].MissingScope)
	assert.Contains(t, warnings[0].Consequence, "pull requests")
}

// TestValidateGitHubTokenScopes_HasRepoScope verifies that a token with the
// "repo" scope returns no warnings.
func TestValidateGitHubTokenScopes_HasRepoScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-OAuth-Scopes", "repo, read:user")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"login": "test-user"})
	}))
	defer srv.Close()

	warnings, err := scm.ValidateGitHubTokenScopes(context.Background(), "good-token", srv.URL)
	require.NoError(t, err)
	assert.Empty(t, warnings, "token with 'repo' scope should produce no warnings")
}

// TestValidateGitHubTokenScopes_HasPublicRepoScope verifies that a token with
// "public_repo" scope (for public-only repos) returns no warnings.
func TestValidateGitHubTokenScopes_HasPublicRepoScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-OAuth-Scopes", "public_repo")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"login": "test-user"})
	}))
	defer srv.Close()

	warnings, err := scm.ValidateGitHubTokenScopes(context.Background(), "public-repo-token", srv.URL)
	require.NoError(t, err)
	assert.Empty(t, warnings)
}

// TestValidateGitHubTokenScopes_FineGrainedPAT verifies that a fine-grained PAT
// (no X-OAuth-Scopes header) reports that its scopes were not verified instead
// of passing silently (C06-scm-health-26).
func TestValidateGitHubTokenScopes_FineGrainedPAT(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fine-grained PAT: no X-OAuth-Scopes header.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"login": "test-user"})
	}))
	defer srv.Close()

	warnings, err := scm.ValidateGitHubTokenScopes(context.Background(), "fine-grained-pat", srv.URL)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Equal(t, "<unverified>", warnings[0].MissingScope)
	assert.Contains(t, warnings[0].Consequence, "pull_requests:write")
}

// TestValidateGitHubTokenScopes_ServerError verifies that a 5xx response returns
// an error (non-fatal — caller should log at debug level).
func TestValidateGitHubTokenScopes_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := scm.ValidateGitHubTokenScopes(context.Background(), "token", srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

// TestValidateGitLabTokenScopes_EmptyToken verifies that an empty GitLab token
// returns a warning.
func TestValidateGitLabTokenScopes_EmptyToken(t *testing.T) {
	warnings, err := scm.ValidateGitLabTokenScopes(context.Background(), "", "")
	require.NoError(t, err)
	require.Len(t, warnings, 1)
}

// TestValidateGitLabTokenScopes_MissingAPIScope verifies that a GitLab token
// response without "api" scope returns a warning.
func TestValidateGitLabTokenScopes_MissingAPIScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Token with read_repository only — no "api" scope.
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"scopes": []string{"read_repository"},
		})
	}))
	defer srv.Close()

	warnings, err := scm.ValidateGitLabTokenScopes(context.Background(), "read-only-token", srv.URL)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Equal(t, "api", warnings[0].MissingScope)
}

// TestValidateGitLabTokenScopes_HasAPIScope verifies that a GitLab token with
// "api" in the response body returns no warnings.
func TestValidateGitLabTokenScopes_HasAPIScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"scopes": []string{"api"},
		})
	}))
	defer srv.Close()

	warnings, err := scm.ValidateGitLabTokenScopes(context.Background(), "api-token", srv.URL)
	require.NoError(t, err)
	assert.Empty(t, warnings)
}

// TestValidateForgejoTokenScopes_Unauthorized verifies that a 401 Forgejo response
// returns a warning.
func TestValidateForgejoTokenScopes_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	warnings, err := scm.ValidateForgejoTokenScopes(context.Background(), "bad-token", srv.URL)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0].MissingScope, "valid token")
}

// TestValidateForgejoTokenScopes_ValidToken verifies that a 200 response
// from Forgejo returns no warnings.
func TestValidateForgejoTokenScopes_ValidToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"login": "test-user"})
	}))
	defer srv.Close()

	warnings, err := scm.ValidateForgejoTokenScopes(context.Background(), "valid-token", srv.URL)
	require.NoError(t, err)
	assert.Empty(t, warnings)
}

// TestTokenValidators_Hardening covers a GitLab token whose name is "api"
// but has only read_api, and a Forgejo server error (C06-scm-health-26).
func TestTokenValidators_Hardening(t *testing.T) {
	t.Run("gitlab token named api without the api scope", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"name": "api", "scopes": []string{"read_api"}})
		}))
		defer srv.Close()
		warnings, err := scm.ValidateGitLabTokenScopes(context.Background(), "tok", srv.URL)
		require.NoError(t, err)
		require.Len(t, warnings, 1)
		assert.Equal(t, "api", warnings[0].MissingScope)
	})
	t.Run("gitlab malformed body is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`not json`))
		}))
		defer srv.Close()
		_, err := scm.ValidateGitLabTokenScopes(context.Background(), "tok", srv.URL)
		require.Error(t, err)
	})
	t.Run("forgejo server error is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		_, err := scm.ValidateForgejoTokenScopes(context.Background(), "tok", srv.URL)
		require.Error(t, err)
	})
}

// TestValidateGiteaFamilyTokenScopes_DocumentedScopes covers a token with only
// the documented scopes (write:repository, write:issue). /api/v1/user needs
// read:user, so Forgejo and Gitea answer 403: the token works, but its scopes
// cannot be checked this way. That is ErrTokenScopesNotChecked, not a warning
// and not a generic HTTP error. Messages name the provider that was called.
func TestValidateGiteaFamilyTokenScopes_DocumentedScopes(t *testing.T) {
	validators := []struct {
		name     string
		validate func(context.Context, string, string) ([]scm.TokenScopeWarning, error)
		other    string
	}{
		{"Forgejo", scm.ValidateForgejoTokenScopes, "Gitea"},
		{"Gitea", scm.ValidateGiteaTokenScopes, "Forgejo"},
	}
	for _, v := range validators {
		t.Run(v.name+" 403 is not checked", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			}))
			defer srv.Close()
			warnings, err := v.validate(context.Background(), "tok", srv.URL)
			assert.Empty(t, warnings)
			require.ErrorIs(t, err, scm.ErrTokenScopesNotChecked)
			assert.Equal(t, "token scopes not checked: /user returned 403 (the documented scopes don't include read:user)", err.Error())
		})
		t.Run(v.name+" 401 names the provider", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer srv.Close()
			warnings, err := v.validate(context.Background(), "tok", srv.URL)
			require.NoError(t, err)
			require.Len(t, warnings, 1)
			assert.Contains(t, warnings[0].Consequence, v.name)
			assert.NotContains(t, warnings[0].Consequence, v.other)
		})
		t.Run(v.name+" 500 names the provider", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer srv.Close()
			_, err := v.validate(context.Background(), "tok", srv.URL)
			require.Error(t, err)
			assert.NotErrorIs(t, err, scm.ErrTokenScopesNotChecked)
			assert.Contains(t, err.Error(), v.name)
			assert.NotContains(t, err.Error(), v.other)
		})
	}
}

// connRecorder records the state of every connection an httptest.Server
// accepts, in the order the connections were opened.
type connRecorder struct {
	mu    sync.Mutex
	order []net.Conn
	last  map[net.Conn]http.ConnState
}

func newConnRecorder() *connRecorder {
	return &connRecorder{last: map[net.Conn]http.ConnState{}}
}

// record is the server's ConnState hook.
func (r *connRecorder) record(c net.Conn, s http.ConnState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s == http.StateNew {
		r.order = append(r.order, c)
	}
	r.last[c] = s
}

// states returns the last state of each connection, oldest first.
func (r *connRecorder) states() []http.ConnState {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]http.ConnState, len(r.order))
	for i, c := range r.order {
		out[i] = r.last[c]
	}
	return out
}

// TestTokenValidators_ConnectionDoesNotOutliveCheck covers B78: the startup
// token check must not leave its connection where another client in the
// process can reuse it. The controller's clone and push go through go-git's
// HTTP transport, which pools in http.DefaultTransport. A check through that
// pool left its connection idle there: a bodiless 401, 403 or 404 on every Go
// version, and since Go 1.27 also a response whose body the check did not
// read, which net/http now drains and keeps. The first clone to the same host
// then reused a connection opened before the chart's NetworkPolicy was
// enforced on the pod, and the policy did not apply to git traffic until the
// pod restarted.
//
// The server records its connections' states. After the check returns, its
// connection must be closed, not idle, and a request through
// http.DefaultClient to the same server must open a connection of its own.
func TestTokenValidators_ConnectionDoesNotOutliveCheck(t *testing.T) {
	validators := []struct {
		name     string
		validate func(context.Context, string, string) ([]scm.TokenScopeWarning, error)
	}{
		{"GitHub", scm.ValidateGitHubTokenScopes},
		{"GitLab", scm.ValidateGitLabTokenScopes},
		{"Forgejo", scm.ValidateForgejoTokenScopes},
		{"Gitea", scm.ValidateGiteaTokenScopes},
	}
	responses := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"403 without a body", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}},
		{"403 with a JSON body", func(w http.ResponseWriter, _ *http.Request) {
			// Forgejo's answer to /user for a token without read:user.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"message": "token does not have at least one of required scope(s): [read:user]",
			})
		}},
		{"200 with a JSON body", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-OAuth-Scopes", "repo")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"login": "test-user", "scopes": []string{"api"}})
		}},
	}
	for _, v := range validators {
		for _, r := range responses {
			t.Run(v.name+" "+r.name, func(t *testing.T) {
				conns := newConnRecorder()
				srv := httptest.NewUnstartedServer(r.handler)
				srv.Config.ConnState = conns.record
				srv.Start()
				defer srv.Close()
				t.Cleanup(http.DefaultTransport.(*http.Transport).CloseIdleConnections)

				// The warnings and errors are covered by the other tests.
				_, _ = v.validate(context.Background(), "tok", srv.URL)

				require.Len(t, conns.states(), 1, "the check opens one connection")
				closed := assert.Eventually(t, func() bool { return conns.states()[0] == http.StateClosed },
					2*time.Second, 10*time.Millisecond,
					"the check's connection must be closed once the check returns, not left idle for another client to reuse")
				if !closed {
					t.Logf("the check's connection is %s", conns.states()[0])
				}

				// go-git's clone and push use http.DefaultTransport; they must
				// not find the check's connection in its pool.
				req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
				require.NoError(t, err)
				resp, err := http.DefaultClient.Do(req)
				require.NoError(t, err)
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				assert.Len(t, conns.states(), 2, "http.DefaultClient must open a connection of its own")
			})
		}
	}
}
