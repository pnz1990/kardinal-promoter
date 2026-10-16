// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// FoundPR is a pull request a lookup of a head branch found.
type FoundPR struct {
	Number int
	URL    string
	// HeadSHA is the PR's head commit: the commit its branch had when it
	// was merged, or has now for an open PR. Bitbucket Cloud reports a
	// short hash.
	HeadSHA string
	// OpenedAt is when the pull request was created.
	OpenedAt time.Time
}

// OpenPRFinder is implemented by SCM providers that can find the open pull
// request of a head branch into a base branch. open-pr asks it before it
// creates one, so a run of a step that already opened its PR adopts that PR
// instead of relying on the SCM to refuse a second one (Bitbucket Cloud
// updates the existing PR's title and description instead).
//
// Only a pull request from the repository's own branch is found: never one
// from a fork whose branch has the same name. Each provider checks the
// source repository by its own fields (a repository ID, a project ID, a
// full name, a fork source), not by the branch name. FindOpenPR returns nil
// when there is none. A provider wrapper whose provider does not implement
// it returns errors.ErrUnsupported.
type OpenPRFinder interface {
	FindOpenPR(ctx context.Context, repo, head, base string) (*FoundPR, error)
}

// MergedPRFinder is implemented by SCM providers that can find the merged
// pull request of a head branch (#2065). A promotion step that opens a PR
// and then finds nothing to commit asks it whether its own PR was merged
// before the step recorded it: a controller that crashed after open-pr while
// a reviewer merged the PR. The merge is then recorded as for any PR (the
// PRStatus reads who merged it), instead of taking "the base branch already
// has the change" as a promotion without a PR.
//
// FindMergedPR returns the newest pull request from the repository's own
// branch head (never a fork's) into base that is merged, whose head commit is
// one of headSHAs (the commits the step pushed to its branch) and that was
// opened at or after openedSince; or nil. A provider wrapper whose provider does not implement
// it returns errors.ErrUnsupported.
type MergedPRFinder interface {
	FindMergedPR(ctx context.Context, repo, head, base string, headSHAs []string, openedSince time.Time) (*FoundPR, error)
}

// SameCommit reports whether commit IDs a and b name the same commit: equal,
// or one a prefix of the other of at least 7 characters (Bitbucket Cloud
// reports 12-character hashes).
func SameCommit(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if a == "" || b == "" {
		return false
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return a == b || (len(a) >= 7 && strings.HasPrefix(b, a))
}

// errNoOpenPR ends the error of a lookup that found no open pull request of
// a head branch from the repository itself.
var errNoOpenPR = errors.New("no open pull request")

// errTooManyPRs is a lookup that read its page limit without reaching the
// last page: the PR it looks for may be further, so the result is unknown.
var errTooManyPRs = errors.New("the pull request list is longer than kardinal reads")

// prLister lists the pull requests of the repository's own branch head into
// base: open ones, or merged ones. Every check a lookup relies on is here:
// the head and base branch, the source repository (the repository itself,
// not a fork) and the state.
type prLister interface {
	listPRs(ctx context.Context, repo, head, base string, merged bool) ([]FoundPR, error)
}

func findOpenPR(ctx context.Context, l prLister, repo, head, base string) (*FoundPR, error) {
	prs, err := l.listPRs(ctx, repo, head, base, false)
	if err != nil || len(prs) == 0 {
		return nil, err
	}
	return &prs[0], nil
}

func findMergedPR(ctx context.Context, l prLister, repo, head, base string, headSHAs []string, since time.Time) (*FoundPR, error) {
	if len(headSHAs) == 0 {
		return nil, nil
	}
	prs, err := l.listPRs(ctx, repo, head, base, true)
	if err != nil {
		return nil, err
	}
	var best *FoundPR
	for i := range prs {
		pr := prs[i]
		if pr.OpenedAt.Before(since) || !slices.ContainsFunc(headSHAs, func(sha string) bool { return SameCommit(pr.HeadSHA, sha) }) {
			continue
		}
		if best == nil || pr.OpenedAt.After(best.OpenedAt) {
			best = &pr
		}
	}
	return best, nil
}

// adoptRefused is the open PR of the repository's branch head into base,
// after the SCM refused to create it as a duplicate (createErr). A refusal
// that has no such PR behind it keeps the SCM's reason.
func adoptRefused(ctx context.Context, l prLister, repo, head, base string, createErr error) (string, int, error) {
	pr, err := findOpenPR(ctx, l, repo, head, base)
	switch {
	case err != nil:
		return "", 0, fmt.Errorf("the SCM refused to open the PR of %s (%w), and looking up the open one failed: %w", head, createErr, err)
	case pr == nil:
		return "", 0, fmt.Errorf("the SCM refused to open the PR of %s into %s, but no open PR from this repository's branch exists: %w; refused with: %w",
			head, base, errNoOpenPR, createErr)
	}
	return pr.URL, pr.Number, nil
}

// --- GitHub ---

func (g *GitHubProvider) listPRs(ctx context.Context, repo, head, base string, merged bool) ([]FoundPR, error) {
	type ref struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	}
	var prs []struct {
		Number    int        `json:"number"`
		HTMLURL   string     `json:"html_url"`
		CreatedAt time.Time  `json:"created_at"`
		MergedAt  *time.Time `json:"merged_at"`
		Head      ref        `json:"head"`
		Base      ref        `json:"base"`
	}
	// head=owner:branch filters on the server; a fork owned by the same
	// owner still matches it, so the head repository is checked below.
	owner, _, _ := strings.Cut(repo, "/")
	q := url.Values{"state": {"open"}, "per_page": {"100"}, "head": {owner + ":" + head}, "base": {base}}
	if merged {
		q.Set("state", "closed")
		q.Set("sort", "created")
		q.Set("direction", "desc")
	}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls?%s", repo, q.Encode()), nil, &prs); err != nil {
		return nil, fmt.Errorf("list PRs of %s: %w", head, err)
	}
	var out []FoundPR
	for _, pr := range prs {
		sameRepo := pr.Head.Repo != nil && strings.EqualFold(pr.Head.Repo.FullName, repo)
		if !sameRepo || pr.Head.Ref != head || pr.Base.Ref != base || merged != (pr.MergedAt != nil) {
			continue
		}
		out = append(out, FoundPR{Number: pr.Number, URL: pr.HTMLURL, HeadSHA: pr.Head.SHA, OpenedAt: pr.CreatedAt})
	}
	return out, nil
}

// FindOpenPR implements OpenPRFinder.
func (g *GitHubProvider) FindOpenPR(ctx context.Context, repo, head, base string) (*FoundPR, error) {
	return findOpenPR(ctx, g, repo, head, base)
}

// FindMergedPR implements MergedPRFinder.
func (g *GitHubProvider) FindMergedPR(ctx context.Context, repo, head, base string, headSHAs []string, since time.Time) (*FoundPR, error) {
	return findMergedPR(ctx, g, repo, head, base, headSHAs, since)
}

func (g *GitHubProvider) adoptRefused(ctx context.Context, repo, head, base string, createErr error) (string, int, error) {
	return adoptRefused(ctx, g, repo, head, base, createErr)
}

// --- GitLab ---

func (g *GitLabProvider) listPRs(ctx context.Context, repo, head, base string, merged bool) ([]FoundPR, error) {
	var mrs []struct {
		IID             int       `json:"iid"`
		WebURL          string    `json:"web_url"`
		CreatedAt       time.Time `json:"created_at"`
		State           string    `json:"state"`
		SHA             string    `json:"sha"`
		SourceBranch    string    `json:"source_branch"`
		TargetBranch    string    `json:"target_branch"`
		SourceProjectID int       `json:"source_project_id"`
		TargetProjectID int       `json:"target_project_id"`
	}
	state := "opened"
	if merged {
		state = "merged"
	}
	q := url.Values{"state": {state}, "per_page": {"100"}, "source_branch": {head}, "target_branch": {base},
		"order_by": {"created_at"}, "sort": {"desc"}}
	if err := g.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v4/projects/%s/merge_requests?%s", encodeProjectID(repo), q.Encode()), nil, &mrs); err != nil {
		return nil, fmt.Errorf("list MRs of %s: %w", head, err)
	}
	var out []FoundPR
	for _, mr := range mrs {
		// A fork's MR has the fork as its source project.
		sameRepo := mr.SourceProjectID != 0 && mr.SourceProjectID == mr.TargetProjectID
		if !sameRepo || mr.State != state || mr.SourceBranch != head || mr.TargetBranch != base {
			continue
		}
		out = append(out, FoundPR{Number: mr.IID, URL: mr.WebURL, HeadSHA: mr.SHA, OpenedAt: mr.CreatedAt})
	}
	return out, nil
}

// FindOpenPR implements OpenPRFinder.
func (g *GitLabProvider) FindOpenPR(ctx context.Context, repo, head, base string) (*FoundPR, error) {
	return findOpenPR(ctx, g, repo, head, base)
}

// FindMergedPR implements MergedPRFinder.
func (g *GitLabProvider) FindMergedPR(ctx context.Context, repo, head, base string, headSHAs []string, since time.Time) (*FoundPR, error) {
	return findMergedPR(ctx, g, repo, head, base, headSHAs, since)
}

func (g *GitLabProvider) adoptRefused(ctx context.Context, repo, head, base string, createErr error) (string, int, error) {
	return adoptRefused(ctx, g, repo, head, base, createErr)
}

// --- Forgejo and Gitea ---

// listPRs pages through the repository's open or closed pull requests,
// most recently updated first (the list API has no head filter): a
// just-merged PR is on the first page. A list longer than the page limit is
// an error, never "none".
func (f *ForgejoProvider) listPRs(ctx context.Context, repo, head, base string, merged bool) ([]FoundPR, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	type ref struct {
		Ref    string `json:"ref"`
		Label  string `json:"label"`
		SHA    string `json:"sha"`
		RepoID int64  `json:"repo_id"`
		Repo   *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	}
	type pull struct {
		Number    int       `json:"number"`
		HTMLURL   string    `json:"html_url"`
		State     string    `json:"state"`
		Merged    bool      `json:"merged"`
		CreatedAt time.Time `json:"created_at"`
		Head      ref       `json:"head"`
		Base      ref       `json:"base"`
	}
	state := "open"
	if merged {
		state = "closed"
	}
	var out []FoundPR
	for page := 1; ; page++ {
		if page > maxListPages {
			return nil, fmt.Errorf("list PRs of %s: %w (%d pages of %d)", head, errTooManyPRs, maxListPages, forgejoPageSize)
		}
		var prs []pull
		if err := f.do(ctx, http.MethodGet,
			fmt.Sprintf("/api/v1/repos/%s/%s/pulls?state=%s&sort=recentupdate&limit=%d&page=%d", owner, name, state, forgejoPageSize, page),
			nil, &prs); err != nil {
			return nil, fmt.Errorf("list PRs of %s: %w", head, err)
		}
		for _, pr := range prs {
			// A fork's PR has the fork as its head repository; its head
			// label is "<fork owner>:<branch>", so the branch name alone
			// cannot tell them apart.
			sameRepo := pr.Head.RepoID != 0 && pr.Head.RepoID == pr.Base.RepoID
			if pr.Head.RepoID == 0 || pr.Base.RepoID == 0 {
				sameRepo = pr.Head.Repo != nil && strings.EqualFold(pr.Head.Repo.FullName, owner+"/"+name)
			}
			if !sameRepo || pr.Head.Ref != head || pr.Base.Ref != base || pr.Merged != merged || (!merged && pr.State != "open") {
				continue
			}
			out = append(out, FoundPR{Number: pr.Number, URL: pr.HTMLURL, HeadSHA: pr.Head.SHA, OpenedAt: pr.CreatedAt})
		}
		if len(prs) < forgejoPageSize {
			return out, nil
		}
	}
}

// FindOpenPR implements OpenPRFinder.
func (f *ForgejoProvider) FindOpenPR(ctx context.Context, repo, head, base string) (*FoundPR, error) {
	return findOpenPR(ctx, f, repo, head, base)
}

// FindMergedPR implements MergedPRFinder.
func (f *ForgejoProvider) FindMergedPR(ctx context.Context, repo, head, base string, headSHAs []string, since time.Time) (*FoundPR, error) {
	return findMergedPR(ctx, f, repo, head, base, headSHAs, since)
}

func (f *ForgejoProvider) adoptRefused(ctx context.Context, repo, head, base string, createErr error) (string, int, error) {
	return adoptRefused(ctx, f, repo, head, base, createErr)
}

// --- Bitbucket Cloud ---

func (b *BitbucketProvider) listPRs(ctx context.Context, repo, head, base string, merged bool) ([]FoundPR, error) {
	workspace, repoSlug, err := splitBitbucketRepo(repo)
	if err != nil {
		return nil, err
	}
	type end struct {
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
		Commit struct {
			Hash string `json:"hash"`
		} `json:"commit"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	var result struct {
		Values []struct {
			ID          int       `json:"id"`
			State       string    `json:"state"`
			CreatedOn   time.Time `json:"created_on"`
			Source      end       `json:"source"`
			Destination end       `json:"destination"`
			Links       struct {
				HTML struct {
					Href string `json:"href"`
				} `json:"html"`
			} `json:"links"`
		} `json:"values"`
	}
	state := "OPEN"
	if merged {
		state = "MERGED"
	}
	quote := func(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }
	q := fmt.Sprintf(`source.branch.name="%s" AND destination.branch.name="%s" AND state="%s"`, quote(head), quote(base), state)
	path := fmt.Sprintf("/2.0/repositories/%s/%s/pullrequests?pagelen=50&state=%s&sort=-created_on&q=%s",
		workspace, repoSlug, state, url.QueryEscape(q))
	if err := b.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, fmt.Errorf("list Bitbucket PRs of %s: %w", head, err)
	}
	var out []FoundPR
	for _, pr := range result.Values {
		// A fork's PR has the fork as its source repository.
		sameRepo := strings.EqualFold(pr.Source.Repository.FullName, workspace+"/"+repoSlug) &&
			strings.EqualFold(pr.Destination.Repository.FullName, workspace+"/"+repoSlug)
		if !sameRepo || pr.State != state || pr.Source.Branch.Name != head || pr.Destination.Branch.Name != base {
			continue
		}
		out = append(out, FoundPR{Number: pr.ID, URL: pr.Links.HTML.Href, HeadSHA: pr.Source.Commit.Hash, OpenedAt: pr.CreatedOn})
	}
	return out, nil
}

// FindOpenPR implements OpenPRFinder.
func (b *BitbucketProvider) FindOpenPR(ctx context.Context, repo, head, base string) (*FoundPR, error) {
	return findOpenPR(ctx, b, repo, head, base)
}

// FindMergedPR implements MergedPRFinder.
func (b *BitbucketProvider) FindMergedPR(ctx context.Context, repo, head, base string, headSHAs []string, since time.Time) (*FoundPR, error) {
	return findMergedPR(ctx, b, repo, head, base, headSHAs, since)
}

func (b *BitbucketProvider) adoptRefused(ctx context.Context, repo, head, base string, createErr error) (string, int, error) {
	return adoptRefused(ctx, b, repo, head, base, createErr)
}

// --- Bitbucket Data Center ---

func (b *BitbucketDCProvider) listPRs(ctx context.Context, repo, head, base string, merged bool) ([]FoundPR, error) {
	path, err := b.prPath(repo)
	if err != nil {
		return nil, err
	}
	project, slug, err := splitBitbucketDCRepo(repo)
	if err != nil {
		return nil, err
	}
	state := "OPEN"
	if merged {
		state = "MERGED"
	}
	type ref struct {
		bbdcRef
		LatestCommit string `json:"latestCommit"`
	}
	this := func(r ref) bool {
		return strings.EqualFold(r.Repository.Slug, slug) && strings.EqualFold(r.Repository.Project.Key, project)
	}
	var out []FoundPR
	start := 0
	for page := 0; ; page++ {
		if page >= maxListPages {
			return nil, fmt.Errorf("list PRs of %s: %w (%d pages)", head, errTooManyPRs, maxListPages)
		}
		q := url.Values{"state": {state}, "direction": {"OUTGOING"}, "at": {"refs/heads/" + head},
			"order": {"NEWEST"}, "limit": {"100"}, "start": {fmt.Sprint(start)}}
		var res struct {
			Values []struct {
				ID          int    `json:"id"`
				State       string `json:"state"`
				CreatedDate int64  `json:"createdDate"`
				FromRef     ref    `json:"fromRef"`
				ToRef       ref    `json:"toRef"`
				Links       struct {
					Self []struct {
						Href string `json:"href"`
					} `json:"self"`
				} `json:"links"`
			} `json:"values"`
			IsLastPage    bool `json:"isLastPage"`
			NextPageStart int  `json:"nextPageStart"`
		}
		if err := b.do(ctx, http.MethodGet, path+"?"+q.Encode(), nil, &res); err != nil {
			return nil, fmt.Errorf("list PRs of %s: %w", head, err)
		}
		for _, pr := range res.Values {
			if !this(pr.FromRef) || !this(pr.ToRef) || pr.State != state ||
				pr.FromRef.ID != "refs/heads/"+head || pr.ToRef.ID != "refs/heads/"+base {
				continue
			}
			var href string
			if len(pr.Links.Self) > 0 {
				href = pr.Links.Self[0].Href
			}
			out = append(out, FoundPR{Number: pr.ID, URL: href, HeadSHA: pr.FromRef.LatestCommit, OpenedAt: time.UnixMilli(pr.CreatedDate)})
		}
		if res.IsLastPage {
			return out, nil
		}
		start = res.NextPageStart
	}
}

// FindOpenPR implements OpenPRFinder.
func (b *BitbucketDCProvider) FindOpenPR(ctx context.Context, repo, head, base string) (*FoundPR, error) {
	return findOpenPR(ctx, b, repo, head, base)
}

// FindMergedPR implements MergedPRFinder.
func (b *BitbucketDCProvider) FindMergedPR(ctx context.Context, repo, head, base string, headSHAs []string, since time.Time) (*FoundPR, error) {
	return findMergedPR(ctx, b, repo, head, base, headSHAs, since)
}

func (b *BitbucketDCProvider) adoptRefused(ctx context.Context, repo, head, base string, createErr error) (string, int, error) {
	return adoptRefused(ctx, b, repo, head, base, createErr)
}

// --- Azure DevOps ---

func (a *AzureDevOpsProvider) listPRs(ctx context.Context, repo, head, base string, merged bool) ([]FoundPR, error) {
	org, project, repoName, err := splitADORepo(repo)
	if err != nil {
		return nil, err
	}
	// active is an open PR; completed is a merged one (abandoned is
	// neither).
	status := "active"
	if merged {
		status = "completed"
	}
	// The PR's source repository is checked positively: Azure DevOps names
	// it only on a fork's PR (forkSource), and omits it on every other one,
	// so "no forkSource" alone would trust a field's absence. The list asks
	// for PRs whose source branch is in this repository, by its ID; a
	// repository whose ID cannot be read is an error, never "no PR".
	repoID, err := a.repositoryID(ctx, org, project, repoName)
	if err != nil {
		return nil, err
	}
	query := url.Values{
		"searchCriteria.sourceRefName":      {"refs/heads/" + head},
		"searchCriteria.targetRefName":      {"refs/heads/" + base},
		"searchCriteria.sourceRepositoryId": {repoID},
		"searchCriteria.repositoryId":       {repoID},
		"searchCriteria.status":             {status},
		"api-version":                       {azureDevOpsAPIVersion},
	}
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests?%s", org, project, repoName, query.Encode())
	var result struct {
		Value []struct {
			PullRequestID int       `json:"pullRequestId"`
			Status        string    `json:"status"`
			CreationDate  time.Time `json:"creationDate"`
			SourceRefName string    `json:"sourceRefName"`
			TargetRefName string    `json:"targetRefName"`
			// ForkSource is set on a PR from a fork, with the fork as its
			// repository.
			ForkSource            json.RawMessage `json:"forkSource"`
			LastMergeSourceCommit struct {
				CommitID string `json:"commitId"`
			} `json:"lastMergeSourceCommit"`
			Repository struct {
				ID     string `json:"id"`
				WebURL string `json:"webUrl"`
			} `json:"repository"`
		} `json:"value"`
	}
	if err := a.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, fmt.Errorf("list ADO PRs of %s: %w", head, err)
	}
	var out []FoundPR
	for _, pr := range result.Value {
		fork := len(pr.ForkSource) > 0 && string(pr.ForkSource) != "null"
		// The target is this repository: a response that names another, or
		// none, is not trusted.
		thisRepo := strings.EqualFold(pr.Repository.ID, repoID)
		if fork || !thisRepo || pr.Status != status || pr.SourceRefName != "refs/heads/"+head || pr.TargetRefName != "refs/heads/"+base {
			continue
		}
		out = append(out, FoundPR{Number: pr.PullRequestID, HeadSHA: pr.LastMergeSourceCommit.CommitID, OpenedAt: pr.CreationDate,
			URL: a.prWebURL(pr.Repository.WebURL, org, project, repoName, pr.PullRequestID)})
	}
	return out, nil
}

// repositoryID is the ID of org/project/repoName.
func (a *AzureDevOpsProvider) repositoryID(ctx context.Context, org, project, repoName string) (string, error) {
	var repo struct {
		ID string `json:"id"`
	}
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s?api-version=%s", org, project, repoName, azureDevOpsAPIVersion)
	if err := a.do(ctx, http.MethodGet, path, nil, &repo); err != nil {
		return "", fmt.Errorf("read the ID of ADO repository %s/%s/%s: %w", org, project, repoName, err)
	}
	if repo.ID == "" {
		return "", fmt.Errorf("ADO repository %s/%s/%s: the API returned no ID", org, project, repoName)
	}
	return repo.ID, nil
}

// FindOpenPR implements OpenPRFinder.
func (a *AzureDevOpsProvider) FindOpenPR(ctx context.Context, repo, head, base string) (*FoundPR, error) {
	return findOpenPR(ctx, a, repo, head, base)
}

// FindMergedPR implements MergedPRFinder.
func (a *AzureDevOpsProvider) FindMergedPR(ctx context.Context, repo, head, base string, headSHAs []string, since time.Time) (*FoundPR, error) {
	return findMergedPR(ctx, a, repo, head, base, headSHAs, since)
}

func (a *AzureDevOpsProvider) adoptRefused(ctx context.Context, repo, head, base string, createErr error) (string, int, error) {
	return adoptRefused(ctx, a, repo, head, base, createErr)
}

// --- wrappers ---

// FindOpenPR forwards to the active provider.
func (d *DynamicProvider) FindOpenPR(ctx context.Context, repo, head, base string) (*FoundPR, error) {
	f, ok := d.current().(OpenPRFinder)
	if !ok {
		return nil, errors.ErrUnsupported
	}
	return f.FindOpenPR(ctx, repo, head, base)
}

// FindMergedPR forwards to the active provider.
func (d *DynamicProvider) FindMergedPR(ctx context.Context, repo, head, base string, headSHAs []string, since time.Time) (*FoundPR, error) {
	f, ok := d.current().(MergedPRFinder)
	if !ok {
		return nil, errors.ErrUnsupported
	}
	return f.FindMergedPR(ctx, repo, head, base, headSHAs, since)
}

// FindOpenPR implements OpenPRFinder.
func (g *guardedProvider) FindOpenPR(ctx context.Context, repo, head, base string) (*FoundPR, error) {
	if err := g.check("find open PRs in", repo); err != nil {
		return nil, err
	}
	f, ok := g.inner.(OpenPRFinder)
	if !ok {
		return nil, errors.ErrUnsupported
	}
	return f.FindOpenPR(ctx, repo, head, base)
}

// FindMergedPR implements MergedPRFinder.
func (g *guardedProvider) FindMergedPR(ctx context.Context, repo, head, base string, headSHAs []string, since time.Time) (*FoundPR, error) {
	if err := g.check("find merged PRs in", repo); err != nil {
		return nil, err
	}
	f, ok := g.inner.(MergedPRFinder)
	if !ok {
		return nil, errors.ErrUnsupported
	}
	return f.FindMergedPR(ctx, repo, head, base, headSHAs, since)
}

var (
	_ prLister = (*GitHubProvider)(nil)
	_ prLister = (*GitLabProvider)(nil)
	_ prLister = (*ForgejoProvider)(nil)
	_ prLister = (*BitbucketProvider)(nil)
	_ prLister = (*BitbucketDCProvider)(nil)
	_ prLister = (*AzureDevOpsProvider)(nil)

	_ OpenPRFinder   = (*GitHubProvider)(nil)
	_ OpenPRFinder   = (*GitLabProvider)(nil)
	_ OpenPRFinder   = (*ForgejoProvider)(nil)
	_ OpenPRFinder   = (*BitbucketProvider)(nil)
	_ OpenPRFinder   = (*BitbucketDCProvider)(nil)
	_ OpenPRFinder   = (*AzureDevOpsProvider)(nil)
	_ OpenPRFinder   = (*DynamicProvider)(nil)
	_ OpenPRFinder   = (*guardedProvider)(nil)
	_ MergedPRFinder = (*GitHubProvider)(nil)
	_ MergedPRFinder = (*GitLabProvider)(nil)
	_ MergedPRFinder = (*ForgejoProvider)(nil)
	_ MergedPRFinder = (*BitbucketProvider)(nil)
	_ MergedPRFinder = (*BitbucketDCProvider)(nil)
	_ MergedPRFinder = (*AzureDevOpsProvider)(nil)
	_ MergedPRFinder = (*DynamicProvider)(nil)
	_ MergedPRFinder = (*guardedProvider)(nil)
)
