// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAPI answers each "METHOD path" with a canned body and records the
// requests it saw. The live suites check these clients against real servers;
// this test pins the request shapes so a refactor can't silently break them.
type fakeAPI struct {
	t      *testing.T
	routes map[string]string
	seen   []string
	bodies map[string]map[string]interface{}
	header http.Header
	// fail holds, per route, status codes to answer before the canned body.
	fail map[string][]int
}

func newFake(t *testing.T, routes map[string]string) (*fakeAPI, *httptest.Server) {
	f := &fakeAPI{t: t, routes: routes, bodies: map[string]map[string]interface{}{}, fail: map[string][]int{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		key += "?" + r.URL.RawQuery
	}
	f.seen = append(f.seen, key)
	f.header = r.Header.Clone()
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		var m map[string]interface{}
		_ = json.Unmarshal(raw, &m)
		f.bodies[key] = m
	}
	if codes := f.fail[key]; len(codes) > 0 {
		f.fail[key] = codes[1:]
		http.Error(w, `{"message":"not yet"}`, codes[0])
		return
	}
	body, ok := f.routes[key]
	if !ok {
		http.Error(w, `{"message":"no route"}`, http.StatusNotFound)
		return
	}
	_, _ = io.WriteString(w, body)
}

func server(t *testing.T, kind, api, repo string) Server {
	t.Helper()
	s, err := newServer(kind, client{api: api, cloneBase: "http://git.example", owner: "e2e", token: "tok", http: http.DefaultClient}, repo)
	require.NoError(t, err)
	return s
}

func TestForgejo(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t, map[string]string{
		"POST /api/v1/orgs/e2e/repos":                                               `{}`,
		"POST /api/v1/repos/e2e/r/contents":                                         `{}`,
		"GET /api/v1/repos/e2e/r/raw/environments/prod/kustomization.yaml?ref=main": "newTag: 1.2.3\n",
		"GET /api/v1/repos/e2e/r/pulls?state=all&limit=50&page=1": `[
			{"number":1,"title":"a","state":"closed","merged":true,"head":{"ref":"kardinal/x"},"base":{"ref":"main"},"labels":[{"name":"kardinal"}]},
			{"number":2,"title":"b","state":"open","head":{"ref":"kardinal/y"},"base":{"ref":"main"}},
			{"number":3,"title":"c","state":"open","head":{"ref":"z"},"base":{"ref":"other"}}]`,
		"POST /api/v1/repos/e2e/r/pulls/2/merge":    `{}`,
		"PATCH /api/v1/repos/e2e/r/pulls/2":         `{}`,
		"POST /api/v1/repos/e2e/r/hooks":            `{}`,
		"GET /api/v1/repos/e2e/r/issues/2/comments": `[{"body":"one"},{"body":"two"}]`,
	})
	s := server(t, "gitea", srv.URL, "")
	assert.Equal(t, "gitea", s.Kind())

	r, err := s.CreateRepo(ctx, "r", map[string][]byte{"b.yaml": []byte("b"), "a.yaml": []byte("a")})
	require.NoError(t, err)
	assert.Equal(t, Repo{Owner: "e2e", Name: "r", Branch: "main", CloneURL: "http://git.example/e2e/r.git"}, r)
	assert.Equal(t, "token tok", f.header.Get("Authorization"))
	files := f.bodies["POST /api/v1/repos/e2e/r/contents"]["files"].([]interface{})
	assert.Equal(t, "a.yaml", files[0].(map[string]interface{})["path"], "files are sorted")

	raw, err := s.ReadFile(ctx, r, "main", "environments/prod/kustomization.yaml")
	require.NoError(t, err)
	assert.Equal(t, "newTag: 1.2.3\n", string(raw))

	prs, err := s.PullRequests(ctx, r)
	require.NoError(t, err)
	require.Len(t, prs, 2, "PRs against other branches are dropped")
	assert.Equal(t, "merged", prs[0].State)
	assert.Equal(t, []string{"kardinal"}, prs[0].Labels)
	assert.Equal(t, "open", prs[1].State)

	require.NoError(t, s.MergePR(ctx, r, 2))
	require.NoError(t, s.ClosePR(ctx, r, 2))
	assert.Equal(t, "closed", f.bodies["PATCH /api/v1/repos/e2e/r/pulls/2"]["state"])
	require.NoError(t, s.ReopenPR(ctx, r, 2))
	assert.Equal(t, "open", f.bodies["PATCH /api/v1/repos/e2e/r/pulls/2"]["state"])
	comments, err := s.Comments(ctx, r, 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"one", "two"}, comments)
	require.NoError(t, s.AddWebhook(ctx, r, "http://hook", "sec"))
	assert.Equal(t, "gitea", f.bodies["POST /api/v1/repos/e2e/r/hooks"]["type"])

	require.NoError(t, s.DeleteRepo(ctx, Repo{Owner: "e2e", Name: "gone"}), "404 on delete is success")
}

func TestForgejoMergeRetriesWhileChecking(t *testing.T) {
	defer func(d, e time.Duration) { mergeRetry, mergeRetryEvery = d, e }(mergeRetry, mergeRetryEvery)
	mergeRetry, mergeRetryEvery = 5*time.Second, time.Millisecond
	for name, tc := range map[string]struct {
		answers []string
		calls   int
		wantErr string
	}{
		"checking, then merged": {answers: []string{"try", "try", "ok"}, calls: 3},
		"not mergeable":         {answers: []string{"no"}, calls: 1, wantErr: "HTTP 405"},
		"other error":           {answers: []string{"500"}, calls: 1, wantErr: "HTTP 500"},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				a := tc.answers[min(calls, len(tc.answers)-1)]
				calls++
				switch a {
				case "try":
					http.Error(w, `{"message":"Please try again later"}`, http.StatusMethodNotAllowed)
				case "no":
					http.Error(w, `{"message":"The PR is not mergeable"}`, http.StatusMethodNotAllowed)
				case "500":
					http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
				default:
					_, _ = io.WriteString(w, `{}`)
				}
			}))
			t.Cleanup(srv.Close)
			err := server(t, "gitea", srv.URL, "").MergePR(context.Background(), Repo{Owner: "e2e", Name: "r"}, 2)
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantErr)
			}
			assert.Equal(t, tc.calls, calls)
		})
	}
}

func TestGitLab(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t, map[string]string{
		"GET /api/v4/namespaces/e2e":                       `{"id":7}`,
		"POST /api/v4/projects":                            `{}`,
		"POST /api/v4/projects/e2e%2Fr/repository/commits": `{}`,
		"GET /api/v4/projects/e2e%2Fr/repository/files/environments%2Fprod%2Fk.yaml/raw?ref=main": "x",
		"GET /api/v4/projects/e2e%2Fr/merge_requests?state=all&target_branch=main&per_page=100&page=1": `[
			{"iid":4,"state":"opened","source_branch":"kardinal/x","target_branch":"main","labels":["kardinal"]},
			{"iid":5,"state":"merged","source_branch":"kardinal/y","target_branch":"main"}]`,
		"PUT /api/v4/projects/e2e%2Fr/merge_requests/4/merge": `{}`,
		"PUT /api/v4/projects/e2e%2Fr/merge_requests/4":       `{}`,
		"POST /api/v4/projects/e2e%2Fr/hooks":                 `{}`,
		"GET /api/v4/projects/e2e%2Fr/merge_requests/4/notes?sort=asc&order_by=created_at&per_page=100": `[
			{"body":"closed","system":true},{"body":"kardinal says hi","system":false}]`,
	})
	s := server(t, "gitlab", srv.URL, "")

	r, err := s.CreateRepo(ctx, "r", map[string][]byte{"a.yaml": []byte("a")})
	require.NoError(t, err)
	assert.Equal(t, "tok", f.header.Get("PRIVATE-TOKEN"))
	assert.EqualValues(t, 7, f.bodies["POST /api/v4/projects"]["namespace_id"])

	raw, err := s.ReadFile(ctx, r, "main", "environments/prod/k.yaml")
	require.NoError(t, err)
	assert.Equal(t, "x", string(raw))

	prs, err := s.PullRequests(ctx, r)
	require.NoError(t, err)
	require.Len(t, prs, 2)
	assert.Equal(t, PR{Number: 4, State: "open", Head: "kardinal/x", Base: "main", Labels: []string{"kardinal"}}, prs[0])
	assert.Equal(t, "merged", prs[1].State)

	require.NoError(t, s.MergePR(ctx, r, 4))
	require.NoError(t, s.ClosePR(ctx, r, 4))
	assert.Equal(t, "close", f.bodies["PUT /api/v4/projects/e2e%2Fr/merge_requests/4"]["state_event"])
	require.NoError(t, s.ReopenPR(ctx, r, 4))
	assert.Equal(t, "reopen", f.bodies["PUT /api/v4/projects/e2e%2Fr/merge_requests/4"]["state_event"])
	comments, err := s.Comments(ctx, r, 4)
	require.NoError(t, err)
	assert.Equal(t, []string{"kardinal says hi"}, comments, "system notes are left out")
	require.NoError(t, s.AddWebhook(ctx, r, "http://hook", "sec"))
	assert.Equal(t, "sec", f.bodies["POST /api/v4/projects/e2e%2Fr/hooks"]["token"])
}

func TestGitHub(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t, map[string]string{
		"POST /repos/o/demo/git/trees":                                    `{"sha":"t1"}`,
		"POST /repos/o/demo/git/commits":                                  `{"sha":"c1"}`,
		"POST /repos/o/demo/git/refs":                                     `{}`,
		"GET /repos/o/demo/contents/environments/prod/k.yaml?ref=e2e%2Fr": `{"encoding":"base64","content":"aGVs\nbG8="}`,
		"GET /repos/o/demo/pulls?state=all&base=e2e%2Fr&per_page=100&page=1": `[
			{"number":8,"state":"open","head":{"ref":"kardinal/a"},"base":{"ref":"e2e/r"}},
			{"number":9,"state":"closed","merged_at":"2026-09-30T00:00:00Z","head":{"ref":"kardinal/b"},"base":{"ref":"e2e/r"}}]`,
		"PATCH /repos/o/demo/pulls/8":                      `{}`,
		"DELETE /repos/o/demo/git/refs/heads/kardinal/a":   `{}`,
		"DELETE /repos/o/demo/git/refs/heads/e2e/r":        `{}`,
		"GET /repos/o/demo/issues/8/comments?per_page=100": `[{"body":"c"}]`,
	})
	s := server(t, "github", srv.URL, "o/demo")

	r, err := s.CreateRepo(ctx, "r", map[string][]byte{"a.yaml": []byte("a")})
	require.NoError(t, err)
	assert.Equal(t, Repo{Owner: "o", Name: "demo", Branch: "e2e/r", CloneURL: "http://git.example/o/demo.git"}, r)
	assert.Equal(t, "Bearer tok", f.header.Get("Authorization"))
	assert.Equal(t, "c1", f.bodies["POST /repos/o/demo/git/refs"]["sha"])
	assert.Equal(t, "t1", f.bodies["POST /repos/o/demo/git/commits"]["tree"])
	assert.Empty(t, f.bodies["POST /repos/o/demo/git/commits"]["parents"], "orphan commit")

	raw, err := s.ReadFile(ctx, r, r.Branch, "environments/prod/k.yaml")
	require.NoError(t, err)
	assert.Equal(t, "hello", string(raw))

	prs, err := s.PullRequests(ctx, r)
	require.NoError(t, err)
	require.Len(t, prs, 2)
	assert.Equal(t, "open", prs[0].State)
	assert.Equal(t, "merged", prs[1].State)

	require.NoError(t, s.ReopenPR(ctx, r, 8))
	assert.Equal(t, "open", f.bodies["PATCH /repos/o/demo/pulls/8"]["state"])
	comments, err := s.Comments(ctx, r, 8)
	require.NoError(t, err)
	assert.Equal(t, []string{"c"}, comments)

	assert.ErrorIs(t, s.AddWebhook(ctx, r, "u", "s"), ErrNoWebhookDelivery)

	f.seen = nil
	require.NoError(t, s.DeleteRepo(ctx, r), "422/404 on an already-deleted head branch is success")
	assert.Contains(t, f.seen, "PATCH /repos/o/demo/pulls/8", "open PR closed")
	assert.NotContains(t, f.seen, "PATCH /repos/o/demo/pulls/9", "merged PR left alone")
	assert.Contains(t, f.seen, "DELETE /repos/o/demo/git/refs/heads/e2e/r")

	err = s.DeleteRepo(ctx, Repo{Owner: "o", Name: "demo", Branch: "main"})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "refusing"), err.Error())
}

func TestFromEnv(t *testing.T) {
	for _, k := range []string{EnvKind, EnvAPI, EnvCloneBase, EnvOwner, EnvToken, EnvRepo} {
		t.Setenv(k, "")
	}
	_, err := FromEnv()
	require.Error(t, err)

	t.Setenv(EnvKind, "github")
	t.Setenv(EnvToken, "tok")
	t.Setenv(EnvRepo, "pnz1990/kardinal-demo")
	s, err := FromEnv()
	require.NoError(t, err)
	gh := s.(*github)
	assert.Equal(t, "https://api.github.com", gh.api)
	assert.Equal(t, "https://github.com", gh.cloneBase)
	assert.Equal(t, "pnz1990", gh.owner)

	t.Setenv(EnvKind, "bitbucket")
	t.Setenv(EnvAPI, "http://x")
	t.Setenv(EnvOwner, "o")
	_, err = FromEnv()
	require.Error(t, err)
}

// TestForgejoCreateRepoRetries (#1557): a create or seed that fails on a
// timeout or a 5xx is retried from scratch, deleting the half-made repo
// first; a 4xx is not retried, and the tries are bounded.
func TestForgejoCreateRepoRetries(t *testing.T) {
	const create, seed, del = "POST /api/v1/orgs/e2e/repos", "POST /api/v1/repos/e2e/r/contents", "DELETE /api/v1/repos/e2e/r"
	for name, tc := range map[string]struct {
		fail    map[string][]int
		want    []string
		wantErr string
	}{
		"seed 500, then ok": {fail: map[string][]int{seed: {500}},
			want: []string{create, seed, del, create, seed}},
		"create 502 twice, then ok": {fail: map[string][]int{create: {502, 502}},
			want: []string{create, del, create, del, create, seed}},
		"every try fails": {fail: map[string][]int{create: {503, 503, 503}},
			want: []string{create, del, create, del, create}, wantErr: "HTTP 503"},
		"4xx is not retried": {fail: map[string][]int{create: {422}},
			want: []string{create}, wantErr: "HTTP 422"},
	} {
		t.Run(name, func(t *testing.T) {
			f, srv := newFake(t, map[string]string{create: `{}`, seed: `{}`, del: ``})
			f.fail = tc.fail
			s := server(t, "forgejo", srv.URL, "").(*forgejo)
			s.retry = time.Millisecond
			_, err := s.CreateRepo(context.Background(), "r", map[string][]byte{"a.yaml": []byte("a")})
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantErr)
			}
			assert.Equal(t, tc.want, f.seen)
		})
	}

	t.Run("a create that times out", func(t *testing.T) {
		var (
			mu   sync.Mutex
			seen []string
			slow = true
		)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen = append(seen, r.Method+" "+r.URL.Path)
			wait := r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/orgs/e2e/repos") && slow
			slow = slow && !wait
			mu.Unlock()
			if wait {
				time.Sleep(200 * time.Millisecond) // past the client timeout; the repo is made anyway
			}
			_, _ = io.WriteString(w, `{}`)
		}))
		t.Cleanup(srv.Close)
		s, err := newServer("forgejo", client{api: srv.URL, cloneBase: "http://git.example", owner: "e2e", token: "tok",
			http: &http.Client{Timeout: 50 * time.Millisecond}}, "")
		require.NoError(t, err)
		s.(*forgejo).retry = time.Millisecond
		_, err = s.CreateRepo(context.Background(), "r", nil)
		require.NoError(t, err)
		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, []string{"POST /api/v1/orgs/e2e/repos", "DELETE /api/v1/repos/e2e/r",
			"POST /api/v1/orgs/e2e/repos", "POST /api/v1/repos/e2e/r/contents"}, seen)
	})
}
