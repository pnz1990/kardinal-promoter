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
	"net/http"
	"net/http/httptest"
	"testing"

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
