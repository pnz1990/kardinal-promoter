// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package prstatus_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/prstatus"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// pollAPI is a fake SCM API that answers GET path with its body and counts
// the requests per path.
func pollAPI(t *testing.T, bodies map[string]string) (*httptest.Server, func() map[string]int) {
	t.Helper()
	var mu sync.Mutex
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls[r.Method+" "+r.URL.Path]++
		mu.Unlock()
		body, ok := bodies[r.URL.Path]
		if r.Method != http.MethodGet || !ok {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]int{}
		for k, v := range calls {
			out[k] = v
		}
		return out
	}
}

// providers builds the provider both ways the controller can: directly, and
// behind the Secret-watching DynamicProvider.
func providers(t *testing.T, providerType, token, apiURL string) map[string]scm.SCMProvider {
	t.Helper()
	p, err := scm.NewProvider(providerType, token, apiURL, "")
	require.NoError(t, err)
	d, err := scm.NewDynamicProvider(providerType, token, apiURL, "")
	require.NoError(t, err)
	return map[string]scm.SCMProvider{"provider": p, "dynamic provider": d}
}

// reconcilePoll reconciles a PRStatus that was never polled and returns it.
func reconcilePoll(t *testing.T, p scm.SCMProvider, spec v1alpha1.PRStatusSpec) v1alpha1.PRStatus {
	t.Helper()
	return reconcilePollFrom(t, p, spec, v1alpha1.PRStatusStatus{Open: true})
}

// reconcilePollFrom reconciles a PRStatus whose status is status and
// returns it.
func reconcilePollFrom(t *testing.T, p scm.SCMProvider, spec v1alpha1.PRStatusSpec, status v1alpha1.PRStatusStatus) v1alpha1.PRStatus {
	t.Helper()
	prs := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: "pr", Namespace: "default"},
		Spec:       spec,
		Status:     status,
	}
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(prs).
		WithStatusSubresource(&v1alpha1.PRStatus{}).Build()
	r := &prstatus.Reconciler{Client: c, SCM: p}
	key := types.NamespacedName{Name: "pr", Namespace: "default"}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	var after v1alpha1.PRStatus
	require.NoError(t, c.Get(context.Background(), key, &after))
	require.NotNil(t, after.Status.LastCheckedAt, "the PR was polled")
	assert.Empty(t, after.Status.PollError)
	return after
}

// TestReconciler_BitbucketCloudPolling polls a fake Bitbucket Cloud API the
// way the controller does when no webhook arrives: a MERGED pullrequest marks
// the PRStatus merged and records merge_commit.hash, an OPEN one stays open
// with no merge commit, and a DECLINED one is closed. Covers SCM-BB-03.
func TestReconciler_BitbucketCloudPolling(t *testing.T) {
	const path = "/2.0/repositories/acme/web-app/pullrequests/7"
	pr := func(state, mergeCommit string) string {
		mc := "null"
		if mergeCommit != "" {
			mc = `{"type":"commit","hash":"` + mergeCommit + `"}`
		}
		return `{"type":"pullrequest","id":7,"state":"` + state + `","merge_commit":` + mc + `,` +
			`"participants":[{"type":"participant","role":"REVIEWER","approved":true,"state":"approved"}],` +
			`"links":{"html":{"href":"https://bitbucket.org/acme/web-app/pull-requests/7"}}}`
	}
	tests := []struct {
		state, mergeCommit   string
		wantMerged, wantOpen bool
		wantGets             int
	}{
		{state: "MERGED", mergeCommit: "9f8e7d6c5b4a", wantMerged: true, wantGets: 3},
		{state: "OPEN", wantOpen: true, wantGets: 2},
		{state: "DECLINED", wantGets: 2},
	}
	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			srv, calls := pollAPI(t, map[string]string{path: pr(tt.state, tt.mergeCommit)})
			for name, p := range providers(t, "bitbucket", "bb-access-token", srv.URL) {
				t.Run(name, func(t *testing.T) {
					before := calls()["GET "+path]
					got := reconcilePoll(t, p, v1alpha1.PRStatusSpec{
						PRURL: "https://bitbucket.org/acme/web-app/pull-requests/7", PRNumber: 7, Repo: "acme/web-app"})
					assert.Equal(t, tt.wantMerged, got.Status.Merged, "merged")
					assert.Equal(t, tt.wantOpen, got.Status.Open, "open")
					assert.Equal(t, tt.mergeCommit, got.Status.MergeCommitSHA, "merge commit")
					assert.Equal(t, !tt.wantMerged && !tt.wantOpen, got.Status.ClosedAt != nil, "closed")
					assert.True(t, got.Status.Approved)
					assert.Equal(t, 1, got.Status.ApprovalCount)
					assert.Equal(t, tt.wantGets, calls()["GET "+path]-before, "status, reviews and, once merged, the merge commit")
				})
			}
		})
	}
}

// TestReconciler_AzureDevOpsPolling polls a fake Azure DevOps API the way the
// controller does: status completed marks the PRStatus merged and records
// lastMergeCommit.commitId, an active PR stays open and its lastMergeCommit,
// which is only the test merge, is not recorded, and an abandoned PR is
// closed. Covers SCM-ADO-03.
func TestReconciler_AzureDevOpsPolling(t *testing.T) {
	const (
		path      = "/contoso/Web/_apis/git/repositories/web-app/pullrequests/12"
		reviewers = path + "/reviewers"
		sha       = "6a1f0c9e8d7b6a5f4e3d2c1b0a9f8e7d6c5b4a39"
	)
	pr := func(status string) string {
		return `{"pullRequestId":12,"status":"` + status + `","mergeStatus":"succeeded",` +
			`"lastMergeCommit":{"commitId":"` + sha + `"},` +
			`"repository":{"name":"web-app","project":{"name":"Web"}}}`
	}
	tests := []struct {
		status               string
		wantMerged, wantOpen bool
		wantSHA              string
		wantPRGets           int
	}{
		{status: "completed", wantMerged: true, wantSHA: sha, wantPRGets: 2},
		{status: "active", wantOpen: true, wantPRGets: 1},
		{status: "abandoned", wantPRGets: 1},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			srv, calls := pollAPI(t, map[string]string{
				path:      pr(tt.status),
				reviewers: `{"count":1,"value":[{"vote":10,"displayName":"Ana Reviewer","isRequired":false}]}`,
			})
			for name, p := range providers(t, "azuredevops", "ado-pat", srv.URL) {
				t.Run(name, func(t *testing.T) {
					before := calls()
					got := reconcilePoll(t, p, v1alpha1.PRStatusSpec{
						PRURL: "https://dev.azure.com/contoso/Web/_git/web-app/pullrequest/12", PRNumber: 12, Repo: "contoso/Web/web-app"})
					assert.Equal(t, tt.wantMerged, got.Status.Merged, "merged")
					assert.Equal(t, tt.wantOpen, got.Status.Open, "open")
					assert.Equal(t, tt.wantSHA, got.Status.MergeCommitSHA, "merge commit")
					assert.Equal(t, !tt.wantMerged && !tt.wantOpen, got.Status.ClosedAt != nil, "closed")
					assert.True(t, got.Status.Approved)
					assert.Equal(t, 1, got.Status.ApprovalCount)
					after := calls()
					assert.Equal(t, tt.wantPRGets, after["GET "+path]-before["GET "+path], "status and, once merged, the merge commit")
					assert.Equal(t, 1, after["GET "+reviewers]-before["GET "+reviewers])
				})
			}
		})
	}
}

// TestReconciler_GitHubApprovals polls a fake GitHub API for an open PR whose
// reviews (GET /repos/{repo}/pulls/{n}/reviews, 100 per page) are those of
// each case. The PRStatus gets approved and approvalCount, which PolicyGates
// read as bundle.pr["<env>"].isApproved and approvalCount: each reviewer's
// latest APPROVED or CHANGES_REQUESTED review counts, COMMENTED and DISMISSED
// do not, any reviewer's change request blocks, and a review on page two
// counts as one on page one. The PRStatus starts with a stale approval, so a
// failed read, which keeps it, cannot pass. Covers SCM-GH-12.
func TestReconciler_GitHubApprovals(t *testing.T) {
	const (
		pr      = "/repos/acme/web-app/pulls/7"
		reviews = pr + "/reviews"
	)
	type review struct{ login, state string }
	times := func(n int, r review) []review {
		out := make([]review, n)
		for i := range out {
			out[i] = r
		}
		return out
	}
	tests := []struct {
		name      string
		pages     [][]review
		wantOK    bool
		wantCount int
	}{
		{name: "no reviews", pages: [][]review{{}}},
		{name: "one approval", pages: [][]review{{{"alice", "APPROVED"}}}, wantOK: true, wantCount: 1},
		{name: "two reviewers, one approving twice",
			pages:  [][]review{{{"alice", "APPROVED"}, {"bob", "APPROVED"}, {"alice", "APPROVED"}}},
			wantOK: true, wantCount: 2},
		{name: "comments and dismissed reviews do not count",
			pages: [][]review{{{"alice", "COMMENTED"}, {"bob", "DISMISSED"}}}},
		{name: "a comment after an approval keeps it",
			pages:  [][]review{{{"alice", "APPROVED"}, {"alice", "COMMENTED"}}},
			wantOK: true, wantCount: 1},
		{name: "a change request blocks",
			pages:     [][]review{{{"alice", "APPROVED"}, {"bob", "CHANGES_REQUESTED"}}},
			wantCount: 1},
		{name: "an approval after a change request counts",
			pages:  [][]review{{{"alice", "CHANGES_REQUESTED"}, {"alice", "APPROVED"}}},
			wantOK: true, wantCount: 1},
		{name: "a change request after an approval withdraws it",
			pages: [][]review{{{"alice", "APPROVED"}, {"alice", "CHANGES_REQUESTED"}}}},
		{name: "a change request on page two blocks",
			pages:     [][]review{times(100, review{"alice", "APPROVED"}), {{"bob", "CHANGES_REQUESTED"}}},
			wantCount: 1},
		{name: "an approval on page two counts",
			pages:  [][]review{times(100, review{"alice", "COMMENTED"}), {{"alice", "APPROVED"}}},
			wantOK: true, wantCount: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var asked []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer gh-token", r.Header.Get("Authorization"), r.URL.String())
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == pr:
					_, _ = io.WriteString(w, `{"number":7,"state":"open","merged":false,"html_url":"https://github.com/acme/web-app/pull/7"}`)
				case r.Method == http.MethodGet && r.URL.Path == reviews:
					q := r.URL.Query()
					mu.Lock()
					asked = append(asked, "per_page="+q.Get("per_page")+"&page="+q.Get("page"))
					mu.Unlock()
					page, err := strconv.Atoi(q.Get("page"))
					assert.NoError(t, err, r.URL.String())
					var out []map[string]interface{}
					if page >= 1 && page <= len(tt.pages) {
						for i, rv := range tt.pages[page-1] {
							out = append(out, map[string]interface{}{"id": page*1000 + i,
								"user": map[string]string{"login": rv.login}, "state": rv.state})
						}
					}
					if out == nil {
						out = []map[string]interface{}{}
					}
					assert.NoError(t, json.NewEncoder(w).Encode(out))
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(srv.Close)

			var wantPages []string
			for i := range tt.pages {
				wantPages = append(wantPages, "per_page=100&page="+strconv.Itoa(i+1))
			}
			for name, p := range providers(t, "github", "gh-token", srv.URL) {
				t.Run(name, func(t *testing.T) {
					mu.Lock()
					asked = nil
					mu.Unlock()
					got := reconcilePollFrom(t, p,
						v1alpha1.PRStatusSpec{PRURL: "https://github.com/acme/web-app/pull/7", PRNumber: 7, Repo: "acme/web-app"},
						v1alpha1.PRStatusStatus{Open: true, Approved: !tt.wantOK, ApprovalCount: 9})
					assert.True(t, got.Status.Open)
					assert.Equal(t, tt.wantOK, got.Status.Approved, "approved")
					assert.Equal(t, tt.wantCount, got.Status.ApprovalCount, "approvalCount")
					mu.Lock()
					defer mu.Unlock()
					assert.Equal(t, wantPages, asked, "every page, until one has fewer than 100: %s", strings.Join(asked, ", "))
				})
			}
		})
	}
}
