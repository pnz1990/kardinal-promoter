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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// branchRequest is one request a provider sent to the fake API.
type branchRequest struct {
	Method, URI, Body string
}

// branchAPI answers every request with status and body and records it.
func branchAPI(t *testing.T, status int, body string) (*httptest.Server, func() []branchRequest) {
	t.Helper()
	var mu sync.Mutex
	var reqs []branchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, branchRequest{r.Method, r.RequestURI, string(b)})
		mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []branchRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]branchRequest(nil), reqs...)
	}
}

// TestDeleteBranch checks the request each provider sends to delete a
// kardinal head branch, that a branch already gone is success, and that any
// other refusal is an error (B70).
func TestDeleteBranch(t *testing.T) {
	const branch = "kardinal/my-app-v2/prod"
	type provider func(apiURL string) scm.BranchDeleter
	providers := []struct {
		name    string
		new     provider
		repo    string
		wantURI string
		gone    []struct {
			status int
			body   string
		}
	}{
		{
			name:    "github",
			new:     func(u string) scm.BranchDeleter { return scm.NewGitHubProvider("t", u, "s") },
			repo:    "o/r",
			wantURI: "/repos/o/r/git/refs/heads/kardinal/my-app-v2/prod",
			gone: []struct {
				status int
				body   string
			}{
				{http.StatusUnprocessableEntity, `{"message":"Reference does not exist"}`},
				{http.StatusNotFound, `{"message":"Not Found"}`},
			},
		},
		{
			name:    "gitlab",
			new:     func(u string) scm.BranchDeleter { return scm.NewGitLabProvider("t", u, "s") },
			repo:    "group/sub/proj",
			wantURI: "/api/v4/projects/group%2Fsub%2Fproj/repository/branches/kardinal%2Fmy-app-v2%2Fprod",
			gone: []struct {
				status int
				body   string
			}{{http.StatusNotFound, `{"message":"404 Branch Not Found"}`}},
		},
		{
			name:    "forgejo",
			new:     func(u string) scm.BranchDeleter { return scm.NewForgejoProvider("t", u, "s") },
			repo:    "o/r",
			wantURI: "/api/v1/repos/o/r/branches/kardinal/my-app-v2/prod",
			gone: []struct {
				status int
				body   string
			}{{http.StatusNotFound, `{"message":"branch not found"}`}},
		},
		{
			name:    "bitbucket",
			new:     func(u string) scm.BranchDeleter { return scm.NewBitbucketProvider("t", u, "s") },
			repo:    "ws/r",
			wantURI: "/2.0/repositories/ws/r/refs/branches/kardinal/my-app-v2/prod",
			gone: []struct {
				status int
				body   string
			}{{http.StatusNotFound, `{"type":"error"}`}},
		},
		{
			name: "dynamic forwards",
			new: func(u string) scm.BranchDeleter {
				d, err := scm.NewDynamicProvider("forgejo", "t", u, "s")
				require.NoError(t, err)
				return d
			},
			repo:    "o/r",
			wantURI: "/api/v1/repos/o/r/branches/kardinal/my-app-v2/prod",
			gone: []struct {
				status int
				body   string
			}{{http.StatusNotFound, `{}`}},
		},
	}
	for _, p := range providers {
		t.Run(p.name+"/deleted", func(t *testing.T) {
			srv, reqs := branchAPI(t, http.StatusNoContent, "")
			require.NoError(t, p.new(srv.URL).DeleteBranch(context.Background(), p.repo, branch))
			// Forgejo reads the branch first (#1476); every provider ends with
			// one DELETE.
			rs := reqs()
			require.NotEmpty(t, rs)
			last := rs[len(rs)-1]
			assert.Equal(t, http.MethodDelete, last.Method)
			assert.Equal(t, p.wantURI, last.URI)
			for _, r := range rs[:len(rs)-1] {
				assert.Equal(t, http.MethodGet, r.Method)
			}
		})
		for _, g := range p.gone {
			t.Run(p.name+"/already gone", func(t *testing.T) {
				srv, _ := branchAPI(t, g.status, g.body)
				assert.NoError(t, p.new(srv.URL).DeleteBranch(context.Background(), p.repo, branch))
			})
		}
		t.Run(p.name+"/refused", func(t *testing.T) {
			srv, _ := branchAPI(t, http.StatusForbidden, `{"message":"forbidden"}`)
			err := p.new(srv.URL).DeleteBranch(context.Background(), p.repo, branch)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "status 403")
		})
	}

	t.Run("github/422 other than a missing ref is an error", func(t *testing.T) {
		srv, _ := branchAPI(t, http.StatusUnprocessableEntity, `{"message":"Cannot delete a protected branch"}`)
		assert.Error(t, scm.NewGitHubProvider("t", srv.URL, "s").DeleteBranch(context.Background(), "o/r", branch))
	})
}

// TestDeleteBranch_GitHub404Warns covers the B70 review note: GitHub reports a
// ref that is gone with 422 "Reference does not exist", so a 404 on the delete
// more likely means the token cannot see the ref or the repository (a classic
// token without `repo`, say). The 404 stays non-fatal, since the caller has
// just closed a PR there with the same token, but it is logged at warn with
// the repo and branch, through the logger of the context; a 422 for a missing
// ref and a successful delete log nothing.
func TestDeleteBranch_GitHub404Warns(t *testing.T) {
	const branch = "kardinal/my-app-v2/prod"
	tests := []struct {
		name     string
		status   int
		body     string
		wantWarn bool
	}{
		{name: "404 is taken as gone, with a warning", status: http.StatusNotFound, body: `{"message":"Not Found"}`, wantWarn: true},
		{name: "422 for a missing ref is gone, no warning", status: http.StatusUnprocessableEntity, body: `{"message":"Reference does not exist"}`},
		{name: "deleted, no warning", status: http.StatusNoContent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := branchAPI(t, tc.status, tc.body)
			var logs bytes.Buffer
			ctx := zerolog.New(&logs).WithContext(context.Background())
			require.NoError(t, scm.NewGitHubProvider("t", srv.URL, "s").DeleteBranch(ctx, "o/r", branch))
			if !tc.wantWarn {
				assert.Empty(t, logs.String())
				return
			}
			var line map[string]any
			require.NoError(t, json.Unmarshal(logs.Bytes(), &line), "one JSON log line, got %q", logs.String())
			assert.Equal(t, "warn", line["level"])
			assert.Equal(t, "o/r", line["repo"])
			assert.Equal(t, branch, line["branch"])
			assert.Contains(t, line["message"], "404")
		})
	}
}

// TestDeleteBranch_ForgejoMissingBranch covers B90: Forgejo answers 500
// "object does not exist" to the delete of a branch that is not there, so a
// failed delete reads the branch, and only a branch that reads 404 is gone.
// A branch that is still there keeps the delete's error.
func TestDeleteBranch_ForgejoMissingBranch(t *testing.T) {
	const (
		branch = "kardinal/my-app-v2/prod"
		uri    = "/api/v1/repos/o/r/branches/kardinal/my-app-v2/prod"
	)
	missing := `{"message":"object does not exist [id: refs/heads/kardinal/my-app-v2/prod, rel_path: ]"}`
	tests := []struct {
		name      string
		delStatus int
		delBody   string
		getStatus int
		wantErr   string
		// goneBefore makes the read before the delete answer 404.
		goneBefore bool
	}{
		{name: "a 500 for a branch that reads 404 is gone", delStatus: http.StatusInternalServerError, delBody: missing,
			getStatus: http.StatusNotFound},
		{name: "a 500 for a branch that is there is an error", delStatus: http.StatusInternalServerError, delBody: missing,
			getStatus: http.StatusOK, wantErr: "status 500"},
		{name: "an archived repository's refusal is an error", delStatus: http.StatusLocked, delBody: `{"message":"repo is archived"}`,
			getStatus: http.StatusOK, wantErr: "status 423"},
		{name: "a branch the read cannot find either way is an error", delStatus: http.StatusInternalServerError, delBody: missing,
			getStatus: http.StatusInternalServerError, wantErr: "status 500"},
		// The read before the delete finds no branch: nothing is sent that
		// Forgejo would answer with a 500 the SCM circuit counts (#1476).
		{name: "a branch already gone is not deleted again", goneBefore: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var reqs []branchRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				reqs = append(reqs, branchRequest{Method: r.Method, URI: r.RequestURI})
				mu.Unlock()
				if r.Method == http.MethodDelete {
					w.WriteHeader(tc.delStatus)
					_, _ = io.WriteString(w, tc.delBody)
					return
				}
				switch {
				case len(reqs) > 1:
					w.WriteHeader(tc.getStatus) // the read after the delete
				case tc.goneBefore:
					w.WriteHeader(http.StatusNotFound)
				}
				_, _ = io.WriteString(w, `{"name":"`+branch+`"}`)
			}))
			t.Cleanup(srv.Close)

			err := scm.NewForgejoProvider("t", srv.URL, "s").DeleteBranch(context.Background(), "o/r", branch)
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr, "the delete's error")
			}
			mu.Lock()
			defer mu.Unlock()
			want := []branchRequest{{Method: http.MethodGet, URI: uri}, {Method: http.MethodDelete, URI: uri}, {Method: http.MethodGet, URI: uri}}
			if tc.goneBefore {
				want = want[:1]
			}
			assert.Equal(t, want, reqs)
		})
	}
}

// TestDeleteBranch_AzureDevOps checks that the Azure DevOps provider reads the
// branch's commit and updates the ref from it to the zero object ID, that a
// branch it cannot find is already deleted, and that a ref update the API
// reports as failed is an error (B70).
func TestDeleteBranch_AzureDevOps(t *testing.T) {
	const branch = "kardinal/my-app-v2/prod"
	type call struct {
		Method, URI string
		Body        []map[string]string
	}
	run := func(t *testing.T, refs string, update string) ([]call, error) {
		t.Helper()
		var mu sync.Mutex
		var calls []call
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c := call{Method: r.Method, URI: r.RequestURI}
			if r.Method == http.MethodPost {
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&c.Body))
				_, _ = io.WriteString(w, update)
			} else {
				_, _ = io.WriteString(w, refs)
			}
			mu.Lock()
			calls = append(calls, c)
			mu.Unlock()
		}))
		defer srv.Close()
		err := scm.NewAzureDevOpsProvider("t", srv.URL, "s").DeleteBranch(context.Background(), "org/proj/repo", branch)
		mu.Lock()
		defer mu.Unlock()
		return calls, err
	}

	t.Run("deleted", func(t *testing.T) {
		calls, err := run(t,
			`{"value":[{"name":"refs/heads/kardinal/my-app-v2/prod-old","objectId":"bbb"},{"name":"refs/heads/kardinal/my-app-v2/prod","objectId":"aaa"}],"count":2}`,
			`{"value":[{"name":"refs/heads/kardinal/my-app-v2/prod","success":true,"updateStatus":"succeeded"}],"count":1}`)
		require.NoError(t, err)
		require.Len(t, calls, 2)
		assert.Equal(t, "/org/proj/_apis/git/repositories/repo/refs?api-version=7.1&filter=heads%2Fkardinal%2Fmy-app-v2%2Fprod", calls[0].URI)
		assert.Equal(t, http.MethodPost, calls[1].Method)
		assert.Equal(t, "/org/proj/_apis/git/repositories/repo/refs?api-version=7.1", calls[1].URI)
		assert.Equal(t, []map[string]string{{
			"name":        "refs/heads/kardinal/my-app-v2/prod",
			"oldObjectId": "aaa",
			"newObjectId": "0000000000000000000000000000000000000000",
		}}, calls[1].Body)
	})
	t.Run("already gone", func(t *testing.T) {
		calls, err := run(t, `{"value":[{"name":"refs/heads/kardinal/my-app-v2/prod-old","objectId":"bbb"}],"count":1}`, "")
		require.NoError(t, err)
		assert.Len(t, calls, 1, "no ref update for a branch that is not there")
	})
	t.Run("update refused", func(t *testing.T) {
		_, err := run(t, `{"value":[{"name":"refs/heads/kardinal/my-app-v2/prod","objectId":"aaa"}]}`,
			`{"value":[{"success":false,"updateStatus":"staleOldObjectId"}]}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "staleOldObjectId")
	})
}
