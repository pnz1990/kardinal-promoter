// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MergedPR is a merged pull request MergedPRFinder found.
type MergedPR struct {
	Number int
	URL    string
	// OpenedAt is when the pull request was created.
	OpenedAt time.Time
}

// MergedPRFinder is implemented by SCM providers that can find the merged
// pull request of a head branch (#2065). A promotion step that opens a PR
// and then finds nothing to commit asks it whether its own PR (the head
// branch is deterministic, PRBranch) was merged before the step recorded
// it: a controller that crashed after open-pr, while a reviewer merged the
// PR. The merge is then recorded as for any other PR (the PRStatus reads who
// merged it), instead of taking "the base branch already has the change" as
// a promotion without a PR.
//
// FindMergedPR returns the newest pull request from head into base that is
// merged and was opened at or after openedSince, or nil when there is none.
// A provider wrapper whose provider does not implement it returns
// errors.ErrUnsupported.
type MergedPRFinder interface {
	FindMergedPR(ctx context.Context, repo, head, base string, openedSince time.Time) (*MergedPR, error)
}

// newestMerged keeps the newest of prs opened at or after since.
func newestMerged(prs []MergedPR, since time.Time) *MergedPR {
	var best *MergedPR
	for i := range prs {
		pr := prs[i]
		if pr.OpenedAt.Before(since) {
			continue
		}
		if best == nil || pr.OpenedAt.After(best.OpenedAt) {
			best = &pr
		}
	}
	return best
}

// FindMergedPR lists the closed pull requests of head into base, newest
// first, and keeps the merged ones.
func (g *GitHubProvider) FindMergedPR(ctx context.Context, repo, head, base string, since time.Time) (*MergedPR, error) {
	var prs []struct {
		Number    int        `json:"number"`
		HTMLURL   string     `json:"html_url"`
		CreatedAt time.Time  `json:"created_at"`
		MergedAt  *time.Time `json:"merged_at"`
		Head      struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	owner, _, _ := strings.Cut(repo, "/")
	q := url.Values{"state": {"closed"}, "per_page": {"100"}, "head": {owner + ":" + head}, "base": {base},
		"sort": {"created"}, "direction": {"desc"}}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls?%s", repo, q.Encode()), nil, &prs); err != nil {
		return nil, fmt.Errorf("list merged PRs of %s: %w", head, err)
	}
	var out []MergedPR
	for _, pr := range prs {
		if pr.MergedAt != nil && pr.Head.Ref == head && pr.Base.Ref == base {
			out = append(out, MergedPR{Number: pr.Number, URL: pr.HTMLURL, OpenedAt: pr.CreatedAt})
		}
	}
	return newestMerged(out, since), nil
}

// FindMergedPR lists the merged merge requests of the source branch into
// the target branch.
func (g *GitLabProvider) FindMergedPR(ctx context.Context, repo, head, base string, since time.Time) (*MergedPR, error) {
	var mrs []struct {
		IID          int       `json:"iid"`
		WebURL       string    `json:"web_url"`
		CreatedAt    time.Time `json:"created_at"`
		State        string    `json:"state"`
		SourceBranch string    `json:"source_branch"`
		TargetBranch string    `json:"target_branch"`
	}
	q := url.Values{"state": {"merged"}, "per_page": {"100"}, "source_branch": {head}, "target_branch": {base},
		"order_by": {"created_at"}, "sort": {"desc"}}
	if err := g.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v4/projects/%s/merge_requests?%s", encodeProjectID(repo), q.Encode()), nil, &mrs); err != nil {
		return nil, fmt.Errorf("list merged MRs of %s: %w", head, err)
	}
	var out []MergedPR
	for _, mr := range mrs {
		if mr.State == "merged" && mr.SourceBranch == head && mr.TargetBranch == base {
			out = append(out, MergedPR{Number: mr.IID, URL: mr.WebURL, OpenedAt: mr.CreatedAt})
		}
	}
	return newestMerged(out, since), nil
}

// FindMergedPR reads the closed pull requests, every page (the list API
// has no head filter), and keeps the merged ones of head into base.
func (f *ForgejoProvider) FindMergedPR(ctx context.Context, repo, head, base string, since time.Time) (*MergedPR, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	type pull struct {
		Number    int       `json:"number"`
		HTMLURL   string    `json:"html_url"`
		Merged    bool      `json:"merged"`
		CreatedAt time.Time `json:"created_at"`
		Head      struct {
			Ref   string `json:"ref"`
			Label string `json:"label"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	var out []MergedPR
	for page := 1; page <= maxListPages; page++ {
		var prs []pull
		if err := f.do(ctx, http.MethodGet,
			fmt.Sprintf("/api/v1/repos/%s/%s/pulls?state=closed&limit=%d&page=%d", owner, name, forgejoPageSize, page),
			nil, &prs); err != nil {
			return nil, fmt.Errorf("list merged PRs of %s: %w", head, err)
		}
		for _, pr := range prs {
			sameHead := pr.Head.Ref == head || pr.Head.Label == head || strings.HasSuffix(pr.Head.Label, ":"+head)
			if pr.Merged && sameHead && pr.Base.Ref == base {
				out = append(out, MergedPR{Number: pr.Number, URL: pr.HTMLURL, OpenedAt: pr.CreatedAt})
			}
		}
		if len(prs) < forgejoPageSize {
			break
		}
	}
	return newestMerged(out, since), nil
}

// FindMergedPR queries the merged pull requests of the source branch into
// the destination branch, newest first.
func (b *BitbucketProvider) FindMergedPR(ctx context.Context, repo, head, base string, since time.Time) (*MergedPR, error) {
	workspace, repoSlug, err := splitBitbucketRepo(repo)
	if err != nil {
		return nil, err
	}
	var result struct {
		Values []struct {
			ID          int               `json:"id"`
			State       string            `json:"state"`
			CreatedOn   time.Time         `json:"created_on"`
			Source      bitbucketPRBranch `json:"source"`
			Destination bitbucketPRBranch `json:"destination"`
			Links       struct {
				HTML struct {
					Href string `json:"href"`
				} `json:"html"`
			} `json:"links"`
		} `json:"values"`
	}
	quote := func(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }
	q := fmt.Sprintf(`source.branch.name="%s" AND destination.branch.name="%s" AND state="MERGED"`, quote(head), quote(base))
	path := fmt.Sprintf("/2.0/repositories/%s/%s/pullrequests?pagelen=50&state=MERGED&sort=-created_on&q=%s",
		workspace, repoSlug, url.QueryEscape(q))
	if err := b.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, fmt.Errorf("list merged Bitbucket PRs of %s: %w", head, err)
	}
	var out []MergedPR
	for _, pr := range result.Values {
		if pr.State == "MERGED" && pr.Source.Branch.Name == head && pr.Destination.Branch.Name == base {
			out = append(out, MergedPR{Number: pr.ID, URL: pr.Links.HTML.Href, OpenedAt: pr.CreatedOn})
		}
	}
	return newestMerged(out, since), nil
}

// FindMergedPR lists the merged outgoing pull requests of head, newest
// first, and keeps those into base.
func (b *BitbucketDCProvider) FindMergedPR(ctx context.Context, repo, head, base string, since time.Time) (*MergedPR, error) {
	path, err := b.prPath(repo)
	if err != nil {
		return nil, err
	}
	var out []MergedPR
	start := 0
	for page := 0; page < maxListPages; page++ {
		q := url.Values{"state": {"MERGED"}, "direction": {"OUTGOING"}, "at": {"refs/heads/" + head},
			"order": {"NEWEST"}, "limit": {"100"}, "start": {fmt.Sprint(start)}}
		var res struct {
			Values []struct {
				bbdcPR
				CreatedDate int64 `json:"createdDate"`
			} `json:"values"`
			IsLastPage    bool `json:"isLastPage"`
			NextPageStart int  `json:"nextPageStart"`
		}
		if err := b.do(ctx, http.MethodGet, path+"?"+q.Encode(), nil, &res); err != nil {
			return nil, fmt.Errorf("list merged PRs of %s: %w", head, err)
		}
		for _, pr := range res.Values {
			if pr.State == "MERGED" && pr.FromRef.ID == "refs/heads/"+head && pr.ToRef.ID == "refs/heads/"+base {
				out = append(out, MergedPR{Number: pr.ID, URL: pr.webURL(), OpenedAt: time.UnixMilli(pr.CreatedDate)})
			}
		}
		if res.IsLastPage {
			break
		}
		start = res.NextPageStart
	}
	return newestMerged(out, since), nil
}

// FindMergedPR searches the completed pull requests of head into base. A
// completed pull request is merged; an abandoned one is not.
func (a *AzureDevOpsProvider) FindMergedPR(ctx context.Context, repo, head, base string, since time.Time) (*MergedPR, error) {
	org, project, repoName, err := splitADORepo(repo)
	if err != nil {
		return nil, err
	}
	query := url.Values{
		"searchCriteria.sourceRefName": {"refs/heads/" + head},
		"searchCriteria.targetRefName": {"refs/heads/" + base},
		"searchCriteria.status":        {"completed"},
		"api-version":                  {azureDevOpsAPIVersion},
	}
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests?%s", org, project, repoName, query.Encode())
	var result struct {
		Value []struct {
			PullRequestID int       `json:"pullRequestId"`
			Status        string    `json:"status"`
			CreationDate  time.Time `json:"creationDate"`
			SourceRefName string    `json:"sourceRefName"`
			TargetRefName string    `json:"targetRefName"`
			Repository    struct {
				WebURL string `json:"webUrl"`
			} `json:"repository"`
		} `json:"value"`
	}
	if err := a.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, fmt.Errorf("list completed ADO PRs of %s: %w", head, err)
	}
	var out []MergedPR
	for _, pr := range result.Value {
		if pr.Status == "completed" && pr.SourceRefName == "refs/heads/"+head && pr.TargetRefName == "refs/heads/"+base {
			out = append(out, MergedPR{Number: pr.PullRequestID, OpenedAt: pr.CreationDate,
				URL: a.prWebURL(pr.Repository.WebURL, org, project, repoName, pr.PullRequestID)})
		}
	}
	return newestMerged(out, since), nil
}

// FindMergedPR forwards to the active provider.
func (d *DynamicProvider) FindMergedPR(ctx context.Context, repo, head, base string, since time.Time) (*MergedPR, error) {
	f, ok := d.current().(MergedPRFinder)
	if !ok {
		return nil, errors.ErrUnsupported
	}
	return f.FindMergedPR(ctx, repo, head, base, since)
}

// FindMergedPR implements MergedPRFinder.
func (g *guardedProvider) FindMergedPR(ctx context.Context, repo, head, base string, since time.Time) (*MergedPR, error) {
	if err := g.check("find merged PRs in", repo); err != nil {
		return nil, err
	}
	f, ok := g.inner.(MergedPRFinder)
	if !ok {
		return nil, errors.ErrUnsupported
	}
	return f.FindMergedPR(ctx, repo, head, base, since)
}

var (
	_ MergedPRFinder = (*GitHubProvider)(nil)
	_ MergedPRFinder = (*GitLabProvider)(nil)
	_ MergedPRFinder = (*ForgejoProvider)(nil)
	_ MergedPRFinder = (*BitbucketProvider)(nil)
	_ MergedPRFinder = (*BitbucketDCProvider)(nil)
	_ MergedPRFinder = (*AzureDevOpsProvider)(nil)
	_ MergedPRFinder = (*DynamicProvider)(nil)
	_ MergedPRFinder = (*guardedProvider)(nil)
)

// errNoOpenPR ends the error of a lookup that found no open pull request of
// a head branch (findExistingPR and its peers).
var errNoOpenPR = errors.New("no open pull request")

// OpenPRFinder is implemented by SCM providers that can find the open pull
// request of a head branch into a base branch. open-pr asks it before it
// creates one, so a run of a step that already opened its PR adopts that
// PR. It does not rely on the SCM refusing a second PR: Bitbucket Cloud
// updates the existing PR's title and description instead. The SCM's
// refusal stays handled for a PR created between the lookup and the create.
// FindOpenPR returns found false when there is none.
type OpenPRFinder interface {
	FindOpenPR(ctx context.Context, repo, head, base string) (prURL string, prNumber int, found bool, err error)
}

// openPR is the result of a findExistingPR-style lookup as FindOpenPR's.
func openPR(prURL string, number int, err error) (string, int, bool, error) {
	switch {
	case errors.Is(err, errNoOpenPR):
		return "", 0, false, nil
	case err != nil:
		return "", 0, false, err
	}
	return prURL, number, true, nil
}

// FindOpenPR implements OpenPRFinder.
func (g *GitHubProvider) FindOpenPR(ctx context.Context, repo, head, base string) (string, int, bool, error) {
	return openPR(g.findExistingPR(ctx, repo, head, base))
}

// FindOpenPR implements OpenPRFinder.
func (g *GitLabProvider) FindOpenPR(ctx context.Context, repo, head, base string) (string, int, bool, error) {
	return openPR(g.findExistingMR(ctx, repo, head, base))
}

// FindOpenPR implements OpenPRFinder.
func (f *ForgejoProvider) FindOpenPR(ctx context.Context, repo, head, base string) (string, int, bool, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", 0, false, err
	}
	return openPR(f.findExistingPR(ctx, owner, name, head, base))
}

// FindOpenPR implements OpenPRFinder.
func (b *BitbucketProvider) FindOpenPR(ctx context.Context, repo, head, base string) (string, int, bool, error) {
	workspace, repoSlug, err := splitBitbucketRepo(repo)
	if err != nil {
		return "", 0, false, err
	}
	return openPR(b.findExistingPR(ctx, workspace, repoSlug, head, base))
}

// FindOpenPR implements OpenPRFinder.
func (b *BitbucketDCProvider) FindOpenPR(ctx context.Context, repo, head, base string) (string, int, bool, error) {
	return openPR(b.findExistingPR(ctx, repo, head, base))
}

// FindOpenPR implements OpenPRFinder.
func (a *AzureDevOpsProvider) FindOpenPR(ctx context.Context, repo, head, base string) (string, int, bool, error) {
	org, project, repoName, err := splitADORepo(repo)
	if err != nil {
		return "", 0, false, err
	}
	return openPR(a.findExistingPR(ctx, org, project, repoName, head, base))
}

// FindOpenPR forwards to the active provider.
func (d *DynamicProvider) FindOpenPR(ctx context.Context, repo, head, base string) (string, int, bool, error) {
	f, ok := d.current().(OpenPRFinder)
	if !ok {
		return "", 0, false, errors.ErrUnsupported
	}
	return f.FindOpenPR(ctx, repo, head, base)
}

// FindOpenPR implements OpenPRFinder.
func (g *guardedProvider) FindOpenPR(ctx context.Context, repo, head, base string) (string, int, bool, error) {
	if err := g.check("find open PRs in", repo); err != nil {
		return "", 0, false, err
	}
	f, ok := g.inner.(OpenPRFinder)
	if !ok {
		return "", 0, false, errors.ErrUnsupported
	}
	return f.FindOpenPR(ctx, repo, head, base)
}

var (
	_ OpenPRFinder = (*GitHubProvider)(nil)
	_ OpenPRFinder = (*GitLabProvider)(nil)
	_ OpenPRFinder = (*ForgejoProvider)(nil)
	_ OpenPRFinder = (*BitbucketProvider)(nil)
	_ OpenPRFinder = (*BitbucketDCProvider)(nil)
	_ OpenPRFinder = (*AzureDevOpsProvider)(nil)
	_ OpenPRFinder = (*DynamicProvider)(nil)
	_ OpenPRFinder = (*guardedProvider)(nil)
)
