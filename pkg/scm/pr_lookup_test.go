// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

const (
	lookupHead   = "kardinal/abc/b1/prod"
	lookupBase   = "main"
	pushedCommit = "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d"
	otherCommit  = "9f8e7d6c5b4a39281706f5e4d3c2b1a098765432"
)

// lookupPR is one pull request in a provider fixture.
type lookupPR struct {
	num        int
	head, base string
	fork       bool   // from a fork's branch of the same name
	state      string // open, merged, or closed (closed without merging)
	sha        string
	created    time.Time
}

// lookupProvider renders lookupPRs as one provider's list API does, and
// knows which request is a merged-PR lookup.
type lookupProvider struct {
	name, repo string
	provider   func(url string) interface {
		scm.OpenPRFinder
		scm.MergedPRFinder
	}
	isMergedQuery func(q string) bool
	list          func(prs []lookupPR) string
	url           func(num int) string
	// wantQuery holds request substrings that show the server-side filter.
	wantQuery []string
	// extra answers other GET paths (Azure DevOps reads the repository ID).
	extra map[string]string
}

func iso(t time.Time) string { return t.UTC().Format(time.RFC3339) }

var lookupProviders = []lookupProvider{
	{name: "github", repo: "o/r",
		provider: func(u string) interface {
			scm.OpenPRFinder
			scm.MergedPRFinder
		} {
			return scm.NewGitHubProvider("t", u, "")
		},
		isMergedQuery: func(q string) bool { return strings.Contains(q, "state=closed") },
		wantQuery:     []string{"head=o%3Akardinal%2Fabc%2Fb1%2Fprod", "base=main"},
		url:           func(n int) string { return fmt.Sprintf("https://github.com/o/r/pull/%d", n) },
		list: func(prs []lookupPR) string {
			var out []string
			for _, p := range prs {
				merged, headRepo := "null", "o/r"
				if p.state == "merged" {
					merged = `"` + iso(p.created.Add(time.Minute)) + `"`
				}
				if p.fork {
					headRepo = "attacker/r"
				}
				out = append(out, fmt.Sprintf(`{"number":%d,"html_url":"https://github.com/o/r/pull/%d","created_at":"%s","merged_at":%s,`+
					`"head":{"ref":"%s","sha":"%s","repo":{"full_name":"%s"}},"base":{"ref":"%s","repo":{"full_name":"o/r"}}}`,
					p.num, p.num, iso(p.created), merged, p.head, p.sha, headRepo, p.base))
			}
			return "[" + strings.Join(out, ",") + "]"
		}},
	{name: "gitlab", repo: "g/p",
		provider: func(u string) interface {
			scm.OpenPRFinder
			scm.MergedPRFinder
		} {
			return scm.NewGitLabProvider("t", u, "")
		},
		isMergedQuery: func(q string) bool { return strings.Contains(q, "state=merged") },
		wantQuery:     []string{"source_branch=kardinal%2Fabc%2Fb1%2Fprod", "target_branch=main"},
		url:           func(n int) string { return fmt.Sprintf("https://gitlab.com/g/p/-/merge_requests/%d", n) },
		list: func(prs []lookupPR) string {
			var out []string
			for _, p := range prs {
				state := map[string]string{"open": "opened", "merged": "merged", "closed": "closed"}[p.state]
				source := 1
				if p.fork {
					source = 999
				}
				out = append(out, fmt.Sprintf(`{"iid":%d,"web_url":"https://gitlab.com/g/p/-/merge_requests/%d","created_at":"%s","state":"%s","sha":"%s",`+
					`"source_branch":"%s","target_branch":"%s","source_project_id":%d,"target_project_id":1}`,
					p.num, p.num, iso(p.created), state, p.sha, p.head, p.base, source))
			}
			return "[" + strings.Join(out, ",") + "]"
		}},
	{name: "forgejo and gitea", repo: "o/r",
		provider: func(u string) interface {
			scm.OpenPRFinder
			scm.MergedPRFinder
		} {
			return scm.NewForgejoProvider("t", u, "")
		},
		isMergedQuery: func(q string) bool { return strings.Contains(q, "state=closed") },
		wantQuery:     []string{"sort=recentupdate"},
		url:           func(n int) string { return fmt.Sprintf("http://forgejo/o/r/pulls/%d", n) },
		list: func(prs []lookupPR) string {
			var out []string
			for _, p := range prs {
				state, label, repoID, repoName := "closed", p.head, 1, "o/r"
				if p.state == "open" {
					state = "open"
				}
				if p.fork {
					// A fork's PR: its own repository, and a head label
					// that ends in ":"+branch.
					label, repoID, repoName = "attacker:"+p.head, 99, "attacker/r"
				}
				out = append(out, fmt.Sprintf(`{"number":%d,"html_url":"http://forgejo/o/r/pulls/%d","state":"%s","merged":%t,"created_at":"%s",`+
					`"head":{"ref":"%s","label":"%s","sha":"%s","repo_id":%d,"repo":{"full_name":"%s"}},"base":{"ref":"%s","repo_id":1}}`,
					p.num, p.num, state, p.state == "merged", iso(p.created), p.head, label, p.sha, repoID, repoName, p.base))
			}
			return "[" + strings.Join(out, ",") + "]"
		}},
	{name: "bitbucket cloud", repo: "ws/r",
		provider: func(u string) interface {
			scm.OpenPRFinder
			scm.MergedPRFinder
		} {
			return scm.NewBitbucketProvider("t", u, "")
		},
		isMergedQuery: func(q string) bool { return strings.Contains(q, "state=MERGED") },
		wantQuery:     []string{"source.branch.name%3D%22kardinal%2Fabc%2Fb1%2Fprod%22"},
		url:           func(n int) string { return fmt.Sprintf("https://bitbucket.org/ws/r/pull-requests/%d", n) },
		list: func(prs []lookupPR) string {
			var out []string
			for _, p := range prs {
				state := map[string]string{"open": "OPEN", "merged": "MERGED", "closed": "DECLINED"}[p.state]
				src := "ws/r"
				if p.fork {
					src = "attacker/r"
				}
				// Bitbucket Cloud reports 12-character commit hashes.
				out = append(out, fmt.Sprintf(`{"id":%d,"state":"%s","created_on":"%s",`+
					`"source":{"branch":{"name":"%s"},"commit":{"hash":"%s"},"repository":{"full_name":"%s"}},`+
					`"destination":{"branch":{"name":"%s"},"repository":{"full_name":"ws/r"}},"links":{"html":{"href":"https://bitbucket.org/ws/r/pull-requests/%d"}}}`,
					p.num, state, p.created.UTC().Format("2006-01-02T15:04:05.000000-07:00"), p.head, p.sha[:12], src, p.base, p.num))
			}
			return `{"values":[` + strings.Join(out, ",") + `]}`
		}},
	{name: "bitbucket data center", repo: "PROJ/repo",
		provider: func(u string) interface {
			scm.OpenPRFinder
			scm.MergedPRFinder
		} {
			return scm.NewBitbucketDCProvider("t", u, "")
		},
		isMergedQuery: func(q string) bool { return strings.Contains(q, "state=MERGED") },
		wantQuery:     []string{"at=refs%2Fheads%2Fkardinal%2Fabc%2Fb1%2Fprod", "direction=OUTGOING"},
		url: func(n int) string {
			return fmt.Sprintf("https://bb.example/projects/PROJ/repos/repo/pull-requests/%d", n)
		},
		list: func(prs []lookupPR) string {
			var out []string
			for _, p := range prs {
				state := map[string]string{"open": "OPEN", "merged": "MERGED", "closed": "DECLINED"}[p.state]
				slug, key := "repo", "PROJ"
				if p.fork {
					slug, key = "repo", "~ATTACKER"
				}
				out = append(out, fmt.Sprintf(`{"id":%d,"state":"%s","createdDate":%d,`+
					`"fromRef":{"id":"refs/heads/%s","latestCommit":"%s","repository":{"slug":"%s","project":{"key":"%s"}}},`+
					`"toRef":{"id":"refs/heads/%s","repository":{"slug":"repo","project":{"key":"PROJ"}}},`+
					`"links":{"self":[{"href":"https://bb.example/projects/PROJ/repos/repo/pull-requests/%d"}]}}`,
					p.num, state, p.created.UnixMilli(), p.head, p.sha, slug, key, p.base, p.num))
			}
			return `{"isLastPage":true,"values":[` + strings.Join(out, ",") + `]}`
		}},
	{name: "azure devops", repo: "org/proj/repo",
		provider: func(u string) interface {
			scm.OpenPRFinder
			scm.MergedPRFinder
		} {
			return scm.NewAzureDevOpsProvider("t", u, "")
		},
		isMergedQuery: func(q string) bool { return strings.Contains(q, "searchCriteria.status=completed") },
		wantQuery: []string{"searchCriteria.sourceRefName=refs%2Fheads%2Fkardinal%2Fabc%2Fb1%2Fprod", "searchCriteria.targetRefName=refs%2Fheads%2Fmain",
			"searchCriteria.sourceRepositoryId=repo-id"},
		extra: map[string]string{"/org/proj/_apis/git/repositories/repo": `{"id":"repo-id","name":"repo"}`},
		url:   func(n int) string { return fmt.Sprintf("https://dev.azure.com/org/proj/_git/repo/pullrequest/%d", n) },
		list: func(prs []lookupPR) string {
			var out []string
			for _, p := range prs {
				status := map[string]string{"open": "active", "merged": "completed", "closed": "abandoned"}[p.state]
				fork := ""
				if p.fork {
					fork = `"forkSource":{"repository":{"name":"repo-fork","id":"f0"}},`
				}
				out = append(out, fmt.Sprintf(`{"pullRequestId":%d,"status":"%s","creationDate":"%s",%s`+
					`"sourceRefName":"refs/heads/%s","targetRefName":"refs/heads/%s","lastMergeSourceCommit":{"commitId":"%s"},`+
					`"repository":{"id":"repo-id","webUrl":"https://dev.azure.com/org/proj/_git/repo"}}`,
					p.num, status, iso(p.created), fork, p.head, p.base, p.sha))
			}
			return `{"value":[` + strings.Join(out, ",") + `]}`
		}},
}

// serveLookup answers every list request with mergedList or openList, by
// the query, and records the queries.
func serveLookup(t *testing.T, lp lookupProvider, openList, mergedList []lookupPR) (string, *[]string) {
	t.Helper()
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method, "a lookup only reads")
		w.Header().Set("Content-Type", "application/json")
		if body, ok := lp.extra[r.URL.Path]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		queries = append(queries, r.URL.RawQuery)
		if lp.isMergedQuery(r.URL.RawQuery) {
			_, _ = w.Write([]byte(lp.list(mergedList)))
			return
		}
		_, _ = w.Write([]byte(lp.list(openList)))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &queries
}

// TestFindMergedPR (#2065): each provider finds the merged PR of
// kardinal's own branch with the commit the step pushed as its head, the
// newest opened at or after since. Every other PR in the list is newer than
// the one expected, so dropping any one check picks it instead:
//   - a fork's PR from a branch of the same name (#2075 QA: any forker
//     could otherwise stand in for kardinal's PR);
//   - one closed without merging (abandoned on Azure DevOps, declined on
//     Bitbucket);
//   - another head branch; another base branch;
//   - another head commit (someone pushed to the branch);
//   - an older matching PR (the newest is taken); one opened before since.
//
// Covers SCM-MERGED-BEFORE-01.
func TestFindMergedPR(t *testing.T) {
	since := time.Date(2026, 10, 16, 5, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return since.Add(time.Duration(m) * time.Minute) }
	merged := []lookupPR{
		{num: 31, head: lookupHead, base: lookupBase, fork: true, state: "merged", sha: pushedCommit, created: at(30)},
		{num: 30, head: lookupHead, base: lookupBase, state: "merged", sha: otherCommit, created: at(28)},
		{num: 29, head: lookupHead, base: "release", state: "merged", sha: pushedCommit, created: at(27)},
		{num: 28, head: "kardinal/abc/b1/staging", base: lookupBase, state: "merged", sha: pushedCommit, created: at(26)},
		{num: 27, head: lookupHead, base: lookupBase, state: "closed", sha: pushedCommit, created: at(25)},
		{num: 8, head: lookupHead, base: lookupBase, state: "merged", sha: pushedCommit, created: at(20)},
		{num: 6, head: lookupHead, base: lookupBase, state: "merged", sha: pushedCommit, created: at(10)},
		{num: 3, head: lookupHead, base: lookupBase, state: "merged", sha: pushedCommit, created: at(-24 * 60)},
	}
	for _, lp := range lookupProviders {
		t.Run(lp.name, func(t *testing.T) {
			u, queries := serveLookup(t, lp, nil, merged)
			f := lp.provider(u)
			pr, err := f.FindMergedPR(context.Background(), lp.repo, lookupHead, lookupBase, []string{pushedCommit}, since)
			require.NoError(t, err)
			require.NotNil(t, pr)
			assert.Equal(t, 8, pr.Number, "the newest merged PR of kardinal's own branch with the pushed commit")
			assert.Equal(t, lp.url(8), pr.URL)
			assert.True(t, scm.SameCommit(pr.HeadSHA, pushedCommit), pr.HeadSHA)
			require.NotEmpty(t, *queries)
			assert.True(t, lp.isMergedQuery((*queries)[0]), "asks for merged PRs: %s", (*queries)[0])
			for _, q := range lp.wantQuery {
				assert.Contains(t, (*queries)[0], q, "filtered on the server")
			}

			pr, err = f.FindMergedPR(context.Background(), lp.repo, lookupHead, lookupBase, []string{pushedCommit}, at(21))
			require.NoError(t, err)
			assert.Nil(t, pr, "nothing of kardinal's after since")
			pr, err = f.FindMergedPR(context.Background(), lp.repo, lookupHead, lookupBase, nil, since)
			require.NoError(t, err)
			assert.Nil(t, pr, "without the pushed commit nothing is adopted")
		})
	}
}

// TestFindOpenPR (#2075 QA): each provider finds the open PR of kardinal's
// own branch into the base branch, not a fork's PR from a branch of the same
// name, nor one of another branch or base. The fork's PR comes first in the
// list, the others before kardinal's: dropping a check picks one of them.
// The head commit is returned for open-pr to compare with the one it pushed.
//
// Covers SCM-OPEN-FIRST-01.
func TestFindOpenPR(t *testing.T) {
	now := time.Date(2026, 10, 16, 5, 0, 0, 0, time.UTC)
	open := []lookupPR{
		{num: 41, head: lookupHead, base: lookupBase, fork: true, state: "open", sha: pushedCommit, created: now.Add(3 * time.Minute)},
		{num: 40, head: lookupHead, base: "release", state: "open", sha: pushedCommit, created: now.Add(2 * time.Minute)},
		{num: 39, head: "kardinal/abc/b1/staging", base: lookupBase, state: "open", sha: pushedCommit, created: now.Add(time.Minute)},
		{num: 9, head: lookupHead, base: lookupBase, state: "open", sha: pushedCommit, created: now},
	}
	for _, lp := range lookupProviders {
		t.Run(lp.name, func(t *testing.T) {
			u, queries := serveLookup(t, lp, open, nil)
			pr, err := lp.provider(u).FindOpenPR(context.Background(), lp.repo, lookupHead, lookupBase)
			require.NoError(t, err)
			require.NotNil(t, pr)
			assert.Equal(t, 9, pr.Number, "kardinal's own open PR, not the fork's")
			assert.Equal(t, lp.url(9), pr.URL)
			assert.True(t, scm.SameCommit(pr.HeadSHA, pushedCommit), pr.HeadSHA)
			require.NotEmpty(t, *queries)
			assert.False(t, lp.isMergedQuery((*queries)[0]), "asks for open PRs: %s", (*queries)[0])

			// Only the fork's PR: none.
			u, _ = serveLookup(t, lp, open[:1], nil)
			pr, err = lp.provider(u).FindOpenPR(context.Background(), lp.repo, lookupHead, lookupBase)
			require.NoError(t, err)
			assert.Nil(t, pr, "a fork's PR is never kardinal's")
		})
	}
}

// TestFindPR_ForgejoPagesAndLimit: Forgejo's list has no head filter, so the
// lookup pages through the PRs, most recently updated first; a list longer
// than its page limit is an error, never "no PR".
func TestFindPR_ForgejoPagesAndLimit(t *testing.T) {
	filler := func(n int) string {
		var out []string
		for i := 0; i < n; i++ {
			out = append(out, fmt.Sprintf(`{"number":%d,"state":"open","head":{"ref":"other-%d","repo_id":1},"base":{"ref":"main","repo_id":1}}`, 1000+i, i))
		}
		return "[" + strings.Join(out, ",") + "]"
	}
	t.Run("found on page 2", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("page") == "1" {
				_, _ = w.Write([]byte(filler(50)))
				return
			}
			_, _ = w.Write([]byte(`[{"number":9,"html_url":"http://forgejo/o/r/pulls/9","state":"open","head":{"ref":"` + lookupHead +
				`","sha":"` + pushedCommit + `","repo_id":1},"base":{"ref":"main","repo_id":1}}]`))
		}))
		defer srv.Close()
		pr, err := scm.NewForgejoProvider("t", srv.URL, "").FindOpenPR(context.Background(), "o/r", lookupHead, lookupBase)
		require.NoError(t, err)
		require.NotNil(t, pr)
		assert.Equal(t, 9, pr.Number)
	})
	t.Run("past the page limit is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(filler(50)))
		}))
		defer srv.Close()
		pr, err := scm.NewForgejoProvider("t", srv.URL, "").FindMergedPR(context.Background(), "o/r", lookupHead, lookupBase, []string{pushedCommit}, time.Time{})
		require.Error(t, err)
		assert.Nil(t, pr)
		assert.Contains(t, err.Error(), "longer than kardinal reads")
	})
}

// TestFindPR_ForgejoWithoutRepoIDs: a Forgejo or Gitea that sends no
// repo_id is matched on the head repository's full name, so a fork's PR is
// still not kardinal's.
func TestFindPR_ForgejoWithoutRepoIDs(t *testing.T) {
	pr := func(num int, repo string) string {
		return fmt.Sprintf(`{"number":%d,"html_url":"http://forgejo/o/r/pulls/%d","state":"open","head":{"ref":"%s","label":"%s","sha":"%s","repo":{"full_name":"%s"}},"base":{"ref":"main"}}`,
			num, num, lookupHead, lookupHead, pushedCommit, repo)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("[" + pr(41, "attacker/r") + "," + pr(9, "O/R") + "]"))
	}))
	defer srv.Close()
	got, err := scm.NewForgejoProvider("t", srv.URL, "").FindOpenPR(context.Background(), "o/r", lookupHead, lookupBase)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 9, got.Number)
}

// TestFindPR_AzureDevOpsFailsClosed (#2075 QA): Azure DevOps names a PR's
// source repository only on a fork's PR (forkSource), so the lookup asks for
// PRs whose source branch is in this repository, by its ID. A repository
// whose ID cannot be read is an error, never "no PR", and a PR the response
// says targets another repository is not kardinal's.
//
// Covers SCM-OPEN-FIRST-01.
func TestFindPR_AzureDevOpsFailsClosed(t *testing.T) {
	pr := `{"value":[{"pullRequestId":5,"status":"active","creationDate":"2026-10-16T05:00:00Z","sourceRefName":"refs/heads/` + lookupHead +
		`","targetRefName":"refs/heads/main","lastMergeSourceCommit":{"commitId":"` + pushedCommit + `"},"repository":{"id":"%s"}}]}`
	serve := func(repo string, prRepoID string) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/repositories/repo") {
				if repo == "" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_, _ = w.Write([]byte(repo))
				return
			}
			_, _ = fmt.Fprintf(w, pr, prRepoID)
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	got, err := scm.NewAzureDevOpsProvider("t", serve(`{"id":"repo-id"}`, "repo-id"), "").FindOpenPR(context.Background(), "org/proj/repo", lookupHead, lookupBase)
	require.NoError(t, err)
	require.NotNil(t, got, "this repository's own PR")

	got, err = scm.NewAzureDevOpsProvider("t", serve("", "repo-id"), "").FindOpenPR(context.Background(), "org/proj/repo", lookupHead, lookupBase)
	require.Error(t, err, "the repository ID cannot be read")
	assert.Nil(t, got)

	got, err = scm.NewAzureDevOpsProvider("t", serve(`{"name":"repo"}`, "repo-id"), "").FindOpenPR(context.Background(), "org/proj/repo", lookupHead, lookupBase)
	require.Error(t, err, "no ID in the response")
	assert.Nil(t, got)

	got, err = scm.NewAzureDevOpsProvider("t", serve(`{"id":"repo-id"}`, "other-repo"), "").FindOpenPR(context.Background(), "org/proj/repo", lookupHead, lookupBase)
	require.NoError(t, err)
	assert.Nil(t, got, "a PR of another repository")
}

// TestFindMergedPR_ErrorsAreErrors: a failing lookup is an error, never "no
// merged PR", so the step retries instead of recording no changes.
func TestFindMergedPR_ErrorsAreErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	pr, err := scm.NewGitHubProvider("t", srv.URL, "").FindMergedPR(context.Background(), "o/r", "h", "main", []string{pushedCommit}, time.Time{})
	require.Error(t, err)
	assert.Nil(t, pr)
	assert.Contains(t, err.Error(), "502")
}

// TestSameCommit: full IDs compare equal regardless of case, and a short
// hash of at least 7 characters (Bitbucket Cloud's 12) matches its full ID.
func TestSameCommit(t *testing.T) {
	assert.True(t, scm.SameCommit(pushedCommit, strings.ToUpper(pushedCommit)))
	assert.True(t, scm.SameCommit(pushedCommit[:12], pushedCommit))
	assert.False(t, scm.SameCommit(pushedCommit[:6], pushedCommit), "too short to tell")
	assert.False(t, scm.SameCommit(otherCommit, pushedCommit))
	assert.False(t, scm.SameCommit("", pushedCommit))
}

// TestFindMergedPR_AnyPushedCommit: a merged PR whose head is any of the
// commits the step pushed is the step's (a restarted run pushes a new one).
func TestFindMergedPR_AnyPushedCommit(t *testing.T) {
	lp := lookupProviders[0]
	since := time.Date(2026, 10, 16, 5, 0, 0, 0, time.UTC)
	u, _ := serveLookup(t, lp, nil, []lookupPR{{num: 8, head: lookupHead, base: lookupBase, state: "merged", sha: pushedCommit, created: since.Add(time.Minute)}})
	pr, err := lp.provider(u).FindMergedPR(context.Background(), lp.repo, lookupHead, lookupBase, []string{pushedCommit, otherCommit}, since)
	require.NoError(t, err)
	require.NotNil(t, pr)
	assert.Equal(t, 8, pr.Number)
	pr, err = lp.provider(u).FindMergedPR(context.Background(), lp.repo, lookupHead, lookupBase, []string{otherCommit}, since)
	require.NoError(t, err)
	assert.Nil(t, pr)
}
