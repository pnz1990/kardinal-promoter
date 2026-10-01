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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

func hmacHex(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// testWebhookSecret is the webhook secret of the providers whose tests only
// parse payloads; signedFor signs a payload with it the way p's SCM does.
const testWebhookSecret = "s3cret"

func signedFor(p scm.SCMProvider, payload []byte) string {
	switch p.(type) {
	case *scm.GitLabProvider, *scm.AzureDevOpsProvider:
		return testWebhookSecret
	case *scm.ForgejoProvider:
		return hmacHex(testWebhookSecret, payload)
	default:
		return "sha256=" + hmacHex(testWebhookSecret, payload)
	}
}

// TestParseWebhookEvent_MergedNormalised signs a merge event the way each
// SCM does, reads the signature the way the webhook handler does
// (scm.WebhookSignature) and checks every provider reports the same merged
// pull_request event with the base repository in API format
// (C06-scm-health-12, C07-controller-04).
func TestParseWebhookEvent_MergedNormalised(t *testing.T) {
	const secret = "s3cret"
	tests := []struct {
		name     string
		provider scm.SCMProvider
		payload  string
		header   func(payload []byte) (string, string)
		wantRepo string
		wantPR   int
	}{
		{
			name:     "github",
			provider: scm.NewGitHubProvider("t", "", secret),
			payload:  `{"action":"closed","pull_request":{"number":7,"merged":true},"repository":{"full_name":"o/r"}}`,
			header:   func(p []byte) (string, string) { return "X-Hub-Signature-256", "sha256=" + hmacHex(secret, p) },
			wantRepo: "o/r", wantPR: 7,
		},
		{
			name:     "gitlab merge in a subgroup",
			provider: scm.NewGitLabProvider("t", "", secret),
			payload:  `{"object_kind":"merge_request","object_attributes":{"iid":8,"state":"merged","action":"merge"},"project":{"path_with_namespace":"group/sub/proj"}}`,
			header:   func([]byte) (string, string) { return "X-Gitlab-Token", secret },
			wantRepo: "group/sub/proj", wantPR: 8,
		},
		{
			name:     "bitbucket from a fork reports the destination repo",
			provider: scm.NewBitbucketProvider("t", "", secret),
			payload:  `{"pullrequest":{"id":9,"state":"MERGED","source":{"repository":{"full_name":"fork/r"}},"destination":{"repository":{"full_name":"ws/r"}}},"repository":{"full_name":"ws/r"}}`,
			header:   func(p []byte) (string, string) { return "X-Hub-Signature", "sha256=" + hmacHex(secret, p) },
			wantRepo: "ws/r", wantPR: 9,
		},
		{
			name:     "bitbucket without a top-level repository",
			provider: scm.NewBitbucketProvider("t", "", secret),
			payload:  `{"pullrequest":{"id":9,"state":"MERGED","source":{"repository":{"full_name":"fork/r"}},"destination":{"repository":{"full_name":"ws/r"}}}}`,
			header:   func(p []byte) (string, string) { return "X-Hub-Signature", "sha256=" + hmacHex(secret, p) },
			wantRepo: "ws/r", wantPR: 9,
		},
		{
			name:     "forgejo bare hex X-Gitea-Signature",
			provider: scm.NewForgejoProvider("t", "", secret),
			payload:  `{"action":"closed","number":10,"pull_request":{"merged":true},"repository":{"full_name":"o/r"}}`,
			header:   func(p []byte) (string, string) { return "X-Gitea-Signature", hmacHex(secret, p) },
			wantRepo: "o/r", wantPR: 10,
		},
		{
			name:     "forgejo X-Forgejo-Signature",
			provider: scm.NewForgejoProvider("t", "", secret),
			payload:  `{"action":"closed","number":10,"pull_request":{"merged":true},"repository":{"full_name":"o/r"}}`,
			header:   func(p []byte) (string, string) { return "X-Forgejo-Signature", hmacHex(secret, p) },
			wantRepo: "o/r", wantPR: 10,
		},
		{
			name:     "gitea sha256= prefixed X-Hub-Signature-256",
			provider: scm.NewForgejoProvider("t", "", secret),
			payload:  `{"action":"closed","number":10,"pull_request":{"merged":true},"repository":{"full_name":"o/r"}}`,
			header:   func(p []byte) (string, string) { return "X-Hub-Signature-256", "sha256=" + hmacHex(secret, p) },
			wantRepo: "o/r", wantPR: 10,
		},
		{
			name:     "azure devops completed PR",
			provider: scm.NewAzureDevOpsProvider("t", "", secret),
			payload:  `{"eventType":"git.pullrequest.updated","resource":{"pullRequestId":11,"status":"completed","repository":{"name":"repo","project":{"name":"proj"},"remoteUrl":"https://org@dev.azure.com/org/proj/_git/repo"}}}`,
			header:   func([]byte) (string, string) { return "X-AzureDevOps-Token", secret },
			wantRepo: "org/proj/repo", wantPR: 11,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(tc.payload)
			name, value := tc.header(payload)
			h := http.Header{}
			h.Set(name, value)

			ev, err := tc.provider.ParseWebhookEvent(payload, scm.WebhookSignature(h))
			require.NoError(t, err)
			assert.Equal(t, "pull_request", ev.EventType)
			assert.Equal(t, "closed", ev.Action)
			assert.True(t, ev.Merged)
			assert.Equal(t, tc.wantRepo, ev.RepoFullName)
			assert.Equal(t, tc.wantPR, ev.PRNumber)

			_, err = tc.provider.ParseWebhookEvent(payload, "sha256="+hmacHex("wrong", payload))
			require.Error(t, err, "a wrong signature must still be rejected")
		})
	}
}

// TestParseWebhookRequest_EventType proves the event type comes from the
// request's event header (X-Forgejo-Event, X-Gitea-Event, X-GitHub-Event) or
// GitLab's object_kind, and that only a merged pull request event is reported
// as a merge. A Gitea comment or label event on a merged PR also carries
// pull_request.merged=true.
func TestParseWebhookRequest_EventType(t *testing.T) {
	const secret = "s3cret"
	dynamic, err := scm.NewDynamicProvider("forgejo", "t", "", secret)
	require.NoError(t, err)
	merged := func(repo string, pr int) scm.WebhookEvent {
		return scm.WebhookEvent{EventType: "pull_request", Action: "closed", Merged: true, RepoFullName: repo, PRNumber: pr}
	}
	tests := []struct {
		name     string
		provider scm.SCMProvider
		headers  map[string]string
		payload  string
		want     scm.WebhookEvent
	}{
		{name: "forgejo push", provider: scm.NewForgejoProvider("t", "", secret),
			headers: map[string]string{"X-Forgejo-Event": "push"},
			payload: `{"ref":"refs/heads/main","repository":{"full_name":"o/r"}}`,
			want:    scm.WebhookEvent{EventType: "push", RepoFullName: "o/r"}},
		{name: "gitea comment on a merged PR takes the PR number from pull_request",
			provider: scm.NewForgejoProvider("t", "", secret),
			headers:  map[string]string{"X-Gitea-Event": "issue_comment", "X-GitHub-Event": "issue_comment"},
			payload:  `{"action":"created","issue":{"number":5},"pull_request":{"number":5,"merged":true},"is_pull":true,"repository":{"full_name":"o/r"}}`,
			want:     scm.WebhookEvent{EventType: "issue_comment", Action: "created", PRNumber: 5, RepoFullName: "o/r"}},
		{name: "gitea label change on a merged PR is not a merge",
			provider: scm.NewForgejoProvider("t", "", secret),
			headers:  map[string]string{"X-Gitea-Event": "pull_request"},
			payload:  `{"action":"label_updated","number":5,"pull_request":{"number":5,"merged":true},"repository":{"full_name":"o/r"}}`,
			want:     scm.WebhookEvent{EventType: "pull_request", Action: "label_updated", Merged: true, PRNumber: 5, RepoFullName: "o/r"}},
		{name: "forgejo merge", provider: scm.NewForgejoProvider("t", "", secret),
			headers: map[string]string{"X-Forgejo-Event": "pull_request", "X-Gitea-Event": "pull_request"},
			payload: `{"action":"closed","number":5,"pull_request":{"merged":true},"repository":{"full_name":"o/r"}}`,
			want:    merged("o/r", 5)},
		{name: "forgejo without an event header reads a pull request event",
			provider: scm.NewForgejoProvider("t", "", secret),
			payload:  `{"action":"closed","number":5,"pull_request":{"merged":true},"repository":{"full_name":"o/r"}}`,
			want:     merged("o/r", 5)},
		{name: "dynamic provider passes the event type on", provider: dynamic,
			headers: map[string]string{"X-Forgejo-Event": "push"},
			payload: `{"ref":"refs/heads/main","repository":{"full_name":"o/r"}}`,
			want:    scm.WebhookEvent{EventType: "push", RepoFullName: "o/r"}},
		{name: "github ping", provider: scm.NewGitHubProvider("t", "", secret),
			headers: map[string]string{"X-GitHub-Event": "ping"},
			payload: `{"zen":"z","hook_id":1,"repository":{"full_name":"o/r"}}`,
			want:    scm.WebhookEvent{EventType: "ping", RepoFullName: "o/r"}},
		{name: "github review of a merged PR is not a merge", provider: scm.NewGitHubProvider("t", "", secret),
			headers: map[string]string{"X-GitHub-Event": "pull_request_review"},
			payload: `{"action":"submitted","pull_request":{"number":5,"merged":true,"merge_commit_sha":"abc"},"repository":{"full_name":"o/r"}}`,
			want:    scm.WebhookEvent{EventType: "pull_request_review", Action: "submitted", PRNumber: 5, RepoFullName: "o/r"}},
		{name: "github merge", provider: scm.NewGitHubProvider("t", "", secret),
			headers: map[string]string{"X-GitHub-Event": "pull_request"},
			payload: `{"action":"closed","pull_request":{"number":5,"merged":true,"merge_commit_sha":"abc"},"repository":{"full_name":"o/r"}}`,
			want:    scm.WebhookEvent{EventType: "pull_request", Action: "closed", Merged: true, MergeCommitSHA: "abc", PRNumber: 5, RepoFullName: "o/r"}},
		{name: "gitlab note uses object_kind", provider: scm.NewGitLabProvider("t", "", secret),
			headers: map[string]string{"X-Gitlab-Event": "Note Hook"},
			payload: `{"object_kind":"note","object_attributes":{"note":"hi"},"project":{"path_with_namespace":"g/p"}}`,
			want:    scm.WebhookEvent{EventType: "note", RepoFullName: "g/p"}},
		{name: "gitlab without object_kind uses X-Gitlab-Event", provider: scm.NewGitLabProvider("t", "", secret),
			headers: map[string]string{"X-Gitlab-Event": "System Hook"},
			payload: `{"event_name":"repository_update","project":{"path_with_namespace":"g/p"}}`,
			want:    scm.WebhookEvent{EventType: "System Hook", RepoFullName: "g/p"}},
		{name: "bitbucket has no event parser and still reports a merge", provider: scm.NewBitbucketProvider("t", "", secret),
			headers: map[string]string{"X-Event-Key": "pullrequest:fulfilled"},
			payload: `{"pullrequest":{"id":9,"state":"MERGED","destination":{"repository":{"full_name":"ws/r"}}}}`,
			want:    merged("ws/r", 9)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(tc.payload)
			h := http.Header{}
			for k, v := range tc.headers {
				h.Set(k, v)
			}
			switch tc.provider.(type) {
			case *scm.GitLabProvider:
				h.Set("X-Gitlab-Token", secret)
			default:
				h.Set("X-Hub-Signature-256", "sha256="+hmacHex(secret, payload))
			}
			got, err := scm.ParseWebhookRequest(tc.provider, payload, h)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)

			h.Set("X-Hub-Signature-256", "sha256="+hmacHex("wrong", payload))
			h.Set("X-Gitlab-Token", "wrong")
			_, err = scm.ParseWebhookRequest(tc.provider, payload, h)
			require.Error(t, err, "a wrong signature must still be rejected")
		})
	}
}

// TestParseWebhookEvent_NotMerged proves events that are not a completed
// merge are not reported as merged.
func TestParseWebhookEvent_NotMerged(t *testing.T) {
	tests := []struct {
		name     string
		provider scm.SCMProvider
		payload  string
	}{
		{"gitlab opened", scm.NewGitLabProvider("t", "", testWebhookSecret),
			`{"object_kind":"merge_request","object_attributes":{"iid":1,"state":"opened","action":"open"},"project":{"path_with_namespace":"g/p"}}`},
		{"gitlab closed", scm.NewGitLabProvider("t", "", testWebhookSecret),
			`{"object_kind":"merge_request","object_attributes":{"iid":1,"state":"closed","action":"close"},"project":{"path_with_namespace":"g/p"}}`},
		{"bitbucket declined", scm.NewBitbucketProvider("t", "", testWebhookSecret),
			`{"pullrequest":{"id":1,"state":"DECLINED"},"repository":{"full_name":"ws/r"}}`},
		{"forgejo closed without merge", scm.NewForgejoProvider("t", "", testWebhookSecret),
			`{"action":"closed","number":1,"pull_request":{"merged":false},"repository":{"full_name":"o/r"}}`},
		{"azure devops merge attempted on an active PR", scm.NewAzureDevOpsProvider("t", "", testWebhookSecret),
			`{"eventType":"git.pullrequest.merged","resource":{"pullRequestId":1,"status":"active","mergeStatus":"succeeded","repository":{"remoteUrl":"https://dev.azure.com/org/proj/_git/repo"}}}`},
		{"azure devops abandoned", scm.NewAzureDevOpsProvider("t", "", testWebhookSecret),
			`{"eventType":"git.pullrequest.updated","resource":{"pullRequestId":1,"status":"abandoned","repository":{"remoteUrl":"https://dev.azure.com/org/proj/_git/repo"}}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := tc.provider.ParseWebhookEvent([]byte(tc.payload), signedFor(tc.provider, []byte(tc.payload)))
			require.NoError(t, err)
			assert.False(t, ev.Merged)
			assert.False(t, ev.EventType == "pull_request" && ev.Action == "closed" && ev.Merged)
		})
	}
}

// TestParseWebhookEvent_MergeCommitSHA proves the GitHub and GitLab parsers
// report the merge commit of a merged PR, so the webhook can record it with
// status.merged (#1307). Before the merge GitHub's merge_commit_sha is the
// test merge commit, which is not the promoted one.
func TestParseWebhookEvent_MergeCommitSHA(t *testing.T) {
	const sha = "e7ddb9e5a1b2c3d4e5f60718293a4b5c6d7e8f90"
	tests := []struct {
		name     string
		provider scm.SCMProvider
		payload  string
		want     string
	}{
		{"github merged", scm.NewGitHubProvider("t", "", testWebhookSecret),
			`{"action":"closed","pull_request":{"number":7,"merged":true,"merge_commit_sha":"` + sha + `"},"repository":{"full_name":"o/r"}}`,
			sha},
		{"github open with a test merge commit", scm.NewGitHubProvider("t", "", testWebhookSecret),
			`{"action":"synchronize","pull_request":{"number":7,"merged":false,"merge_commit_sha":"` + sha + `"},"repository":{"full_name":"o/r"}}`,
			""},
		{"gitlab merged", scm.NewGitLabProvider("t", "", testWebhookSecret),
			`{"object_kind":"merge_request","object_attributes":{"iid":8,"state":"merged","action":"merge","merge_commit_sha":"` + sha + `"},"project":{"path_with_namespace":"g/p"}}`,
			sha},
		{"gitlab fast-forward merge", scm.NewGitLabProvider("t", "", testWebhookSecret),
			`{"object_kind":"merge_request","object_attributes":{"iid":8,"state":"merged","action":"merge","merge_commit_sha":null},"project":{"path_with_namespace":"g/p"}}`,
			""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := tc.provider.ParseWebhookEvent([]byte(tc.payload), signedFor(tc.provider, []byte(tc.payload)))
			require.NoError(t, err)
			assert.Equal(t, tc.want, ev.MergeCommitSHA)
		})
	}
}

// TestParseWebhookEvent_RefusesEmptySecret proves every provider refuses every
// event when it has no webhook secret, instead of skipping the check: an
// unsigned event, and one signed with an empty key, which anyone can compute.
// The webhook handler fails closed without a secret too; the provider refusing
// on its own keeps every other caller safe (B74). Covers WEBHOOK-NOSECRET-02.
func TestParseWebhookEvent_RefusesEmptySecret(t *testing.T) {
	const secret = "s3cret"
	hubSignature := func(key string, p []byte) string { return "sha256=" + hmacHex(key, p) }
	plainToken := func(key string, _ []byte) string { return key }
	forgejoPayload := `{"action":"closed","number":7,"pull_request":{"merged":true},"repository":{"full_name":"o/r"}}`
	tests := []struct {
		provider string
		payload  string
		header   string
		sign     func(key string, payload []byte) string
	}{
		{"github", `{"action":"closed","pull_request":{"number":7,"merged":true},"repository":{"full_name":"o/r"}}`,
			"X-Hub-Signature-256", hubSignature},
		{"gitlab", `{"object_kind":"merge_request","object_attributes":{"iid":7,"state":"merged","action":"merge"},"project":{"path_with_namespace":"g/p"}}`,
			"X-Gitlab-Token", plainToken},
		{"forgejo", forgejoPayload, "X-Forgejo-Signature", hmacHex},
		{"gitea", forgejoPayload, "X-Gitea-Signature", hmacHex},
		{"bitbucket", `{"pullrequest":{"id":7,"state":"MERGED","destination":{"repository":{"full_name":"ws/r"}}},"repository":{"full_name":"ws/r"}}`,
			"X-Hub-Signature", hubSignature},
		{"azuredevops", `{"eventType":"git.pullrequest.merged","resource":{"pullRequestId":7,"status":"completed","repository":{"name":"repo","project":{"name":"proj"},"remoteUrl":"https://dev.azure.com/org/proj/_git/repo"}}}`,
			"X-AzureDevOps-Token", plainToken},
	}
	constructors := []struct {
		name string
		new  func(providerType, webhookSecret string) (scm.SCMProvider, error)
	}{
		{"NewProvider", func(typ, s string) (scm.SCMProvider, error) { return scm.NewProvider(typ, "t", "", s) }},
		{"NewDynamicProvider", func(typ, s string) (scm.SCMProvider, error) { return scm.NewDynamicProvider(typ, "t", "", s) }},
	}
	for _, tc := range tests {
		for _, c := range constructors {
			t.Run(tc.provider+"/"+c.name, func(t *testing.T) {
				payload := []byte(tc.payload)

				// Control: with a secret, the signed event is a merge.
				p, err := c.new(tc.provider, secret)
				require.NoError(t, err)
				h := http.Header{}
				h.Set(tc.header, tc.sign(secret, payload))
				ev, err := scm.ParseWebhookRequest(p, payload, h)
				require.NoError(t, err)
				require.True(t, ev.Merged)

				p, err = c.new(tc.provider, "")
				require.NoError(t, err)
				for _, s := range []struct{ name, signature string }{
					{"unsigned", ""},
					{"signed with an empty key", tc.sign("", payload)},
				} {
					h := http.Header{}
					if s.signature != "" {
						h.Set(tc.header, s.signature)
					}
					_, err := p.ParseWebhookEvent(payload, s.signature)
					assert.ErrorIs(t, err, scm.ErrNoWebhookSecret, "ParseWebhookEvent, %s", s.name)
					_, err = scm.ParseWebhookRequest(p, payload, h)
					assert.ErrorIs(t, err, scm.ErrNoWebhookSecret, "ParseWebhookRequest, %s", s.name)
				}
			})
		}
	}
}

// TestAzureDevOpsProvider_GetPRReviewStatus_Envelope decodes the
// {"count","value"} envelope and applies the vote rules (C06-scm-health-05).
func TestAzureDevOpsProvider_GetPRReviewStatus_Envelope(t *testing.T) {
	tests := []struct {
		name         string
		votes        []int
		wantApproved bool
		wantCount    int
	}{
		{"approved", []int{10}, true, 1},
		{"approved with suggestions", []int{5}, true, 1},
		{"two approvals", []int{10, 5, 0}, true, 2},
		{"waiting for author blocks", []int{10, -5}, false, 1},
		{"rejected blocks", []int{10, -10}, false, 1},
		{"no vote", []int{0}, false, 0},
		{"no reviewers", nil, false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Contains(t, r.URL.Path, "/org/proj/_apis/git/repositories/repo/pullrequests/7/reviewers")
				value := make([]map[string]int, 0, len(tc.votes))
				for _, v := range tc.votes {
					value = append(value, map[string]int{"vote": v})
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"count": len(value), "value": value})
			}))
			defer srv.Close()

			approved, count, err := scm.NewAzureDevOpsProvider("pat", srv.URL, "").GetPRReviewStatus(context.Background(), "org/proj/repo", 7)
			require.NoError(t, err)
			assert.Equal(t, tc.wantApproved, approved)
			assert.Equal(t, tc.wantCount, count)
		})
	}
}

// TestAzureDevOpsProvider_PRURL checks the PR web URL on both the create
// and the already-exists paths (C06-scm-health-06).
func TestAzureDevOpsProvider_PRURL(t *testing.T) {
	tests := []struct {
		name    string
		exists  bool
		webURL  string
		wantURL string
	}{
		{"created", false, "https://dev.azure.com/org/proj/_git/repo", "https://dev.azure.com/org/proj/_git/repo/pullrequest/7"},
		{"created, trailing slash", false, "https://dev.azure.com/org/proj/_git/repo/", "https://dev.azure.com/org/proj/_git/repo/pullrequest/7"},
		{"already exists", true, "https://dev.azure.com/org/proj/_git/repo", "https://dev.azure.com/org/proj/_git/repo/pullrequest/7"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				pr := map[string]interface{}{"pullRequestId": 7, "repository": map[string]string{"webUrl": tc.webURL}}
				switch {
				case r.Method == http.MethodPost && tc.exists:
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(`{"message":"TF401179: An active pull request for the source and target branch already exists."}`))
				case r.Method == http.MethodPost:
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(pr)
				default:
					assert.Equal(t, "refs/heads/kardinal/b/prod", r.URL.Query().Get("searchCriteria.sourceRefName"))
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"count": 1, "value": []interface{}{pr}})
				}
			}))
			defer srv.Close()

			prURL, n, err := scm.NewAzureDevOpsProvider("pat", srv.URL, "").OpenPR(context.Background(), "org/proj/repo", "t", "b", "kardinal/b/prod", "main")
			require.NoError(t, err)
			assert.Equal(t, 7, n)
			assert.Equal(t, tc.wantURL, prURL)

			repo, num, err := scm.ParsePRURL(prURL)
			require.NoError(t, err)
			assert.Equal(t, "org/proj/repo", repo)
			assert.Equal(t, 7, num)
		})
	}
}

// TestAzureDevOpsProvider_AddLabelsToPR posts each label to the PR labels
// API instead of silently doing nothing (C06-scm-health-21).
func TestAzureDevOpsProvider_AddLabelsToPR(t *testing.T) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/org/proj/_apis/git/repositories/repo/pullRequests/7/labels", r.URL.Path)
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		mu.Lock()
		got = append(got, body["name"])
		mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	p := scm.NewAzureDevOpsProvider("pat", srv.URL, "")
	require.NoError(t, p.AddLabelsToPR(context.Background(), "org/proj/repo", 7, []string{"kardinal", "kardinal/rollback"}))
	assert.Equal(t, []string{"kardinal", "kardinal/rollback"}, got)
}

// TestGetPRStatusAndReviews_EdgeCases covers GitLab's locked state
// (C06-scm-health-19), Bitbucket changes requested (C06-scm-health-20) and
// Forgejo dismissed reviews (C06-scm-health-23).
func TestGetPRStatusAndReviews_EdgeCases(t *testing.T) {
	t.Run("gitlab locked is still open", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"state":"locked"}`))
		}))
		defer srv.Close()
		merged, open, err := scm.NewGitLabProvider("t", srv.URL, "").GetPRStatus(context.Background(), "g/p", 1)
		require.NoError(t, err)
		assert.False(t, merged)
		assert.True(t, open)
	})
	t.Run("bitbucket changes requested blocks", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"participants":[{"approved":true,"state":"approved"},{"approved":false,"state":"changes_requested"}]}`))
		}))
		defer srv.Close()
		approved, count, err := scm.NewBitbucketProvider("t", srv.URL, "").GetPRReviewStatus(context.Background(), "ws/r", 1)
		require.NoError(t, err)
		assert.False(t, approved)
		assert.Equal(t, 1, count)
	})
	t.Run("forgejo dismissed approval does not count", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`[{"user":{"login":"alice"},"state":"APPROVED","dismissed":true}]`))
		}))
		defer srv.Close()
		approved, count, err := scm.NewForgejoProvider("t", srv.URL, "").GetPRReviewStatus(context.Background(), "o/r", 1)
		require.NoError(t, err)
		assert.False(t, approved)
		assert.Equal(t, 0, count)
	})
}

// TestGitHubProvider_GetPRReviewStatus_Paginated proves a CHANGES_REQUESTED
// on the second page blocks the approval (C06-scm-health-16).
func TestGitHubProvider_GetPRReviewStatus_Paginated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))
		var reviews []map[string]interface{}
		switch r.URL.Query().Get("page") {
		case "1":
			for i := 0; i < 100; i++ {
				reviews = append(reviews, map[string]interface{}{"user": map[string]string{"login": fmt.Sprintf("u%d", i)}, "state": "COMMENTED"})
			}
			reviews[0]["state"] = "APPROVED"
		case "2":
			reviews = append(reviews, map[string]interface{}{"user": map[string]string{"login": "bob"}, "state": "CHANGES_REQUESTED"})
		}
		_ = json.NewEncoder(w).Encode(reviews)
	}))
	defer srv.Close()

	approved, count, err := scm.NewGitHubProvider("t", srv.URL, "").GetPRReviewStatus(context.Background(), "o/r", 1)
	require.NoError(t, err)
	assert.False(t, approved)
	assert.Equal(t, 1, count)
}

// TestFindExistingPR_FiltersByBranch proves the already-exists lookup finds
// the PR when it is not on the first page of all open PRs
// (C06-scm-health-15), and that a Bitbucket 400 that is not a duplicate
// surfaces the real error (C06-scm-health-14).
func TestFindExistingPR_FiltersByBranch(t *testing.T) {
	const head = "kardinal/b/prod"
	t.Run("github filters by head", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"A pull request already exists for o:kardinal/b/prod."}`))
				return
			}
			if r.URL.Query().Get("head") != "o:"+head {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[{"number":3,"html_url":"https://github.com/o/r/pull/3","head":{"ref":"kardinal/b/prod"},"base":{"ref":"main"}}]`))
		}))
		defer srv.Close()
		u, n, err := scm.NewGitHubProvider("t", srv.URL, "").OpenPR(context.Background(), "o/r", "t", "b", head, "main")
		require.NoError(t, err)
		assert.Equal(t, 3, n)
		assert.Equal(t, "https://github.com/o/r/pull/3", u)
	})
	t.Run("gitlab filters by source branch", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"message":["Another open merge request already exists for this source branch"]}`))
				return
			}
			if r.URL.Query().Get("source_branch") != head {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[{"iid":4,"web_url":"https://gitlab.com/g/p/-/merge_requests/4","source_branch":"kardinal/b/prod","target_branch":"main"}]`))
		}))
		defer srv.Close()
		_, n, err := scm.NewGitLabProvider("t", srv.URL, "").OpenPR(context.Background(), "g/p", "t", "b", head, "main")
		require.NoError(t, err)
		assert.Equal(t, 4, n)
	})
	t.Run("bitbucket filters by source branch", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"There are already open pull requests for this branch"}}`))
				return
			}
			if !strings.Contains(r.URL.Query().Get("q"), `source.branch.name="kardinal/b/prod"`) {
				_, _ = w.Write([]byte(`{"values":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"values":[{"id":5,"source":{"branch":{"name":"kardinal/b/prod"}},"destination":{"branch":{"name":"main"}},` +
				`"links":{"html":{"href":"https://bitbucket.org/ws/r/pull-requests/5"}}}]}`))
		}))
		defer srv.Close()
		_, n, err := scm.NewBitbucketProvider("t", srv.URL, "").OpenPR(context.Background(), "ws/r", "t", "b", head, "main")
		require.NoError(t, err)
		assert.Equal(t, 5, n)
	})
	t.Run("bitbucket other 400 is the real error", func(t *testing.T) {
		lists := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"source: branch not found: kardinal/b/prod"}}`))
				return
			}
			lists++
			_, _ = w.Write([]byte(`{"values":[]}`))
		}))
		defer srv.Close()
		_, _, err := scm.NewBitbucketProvider("t", srv.URL, "").OpenPR(context.Background(), "ws/r", "t", "b", head, "main")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "branch not found")
		assert.Equal(t, 0, lists)
	})
	t.Run("forgejo reads the next page", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"message":"pull request already exists for these targets"}`))
				return
			}
			var prs []map[string]interface{}
			if r.URL.Query().Get("page") == "1" {
				for i := 0; i < 50; i++ {
					prs = append(prs, map[string]interface{}{"number": 100 + i, "head": map[string]string{"ref": fmt.Sprintf("renovate/%d", i)}})
				}
			} else {
				prs = append(prs, map[string]interface{}{"number": 6, "html_url": "https://f.example/o/r/pulls/6", "head": map[string]string{"ref": head, "label": head}, "base": map[string]string{"ref": "main"}})
			}
			_ = json.NewEncoder(w).Encode(prs)
		}))
		defer srv.Close()
		_, n, err := scm.NewForgejoProvider("t", srv.URL, "").OpenPR(context.Background(), "o/r", "t", "b", head, "main")
		require.NoError(t, err)
		assert.Equal(t, 6, n)
	})
}

// TestFindExistingPR_SameBaseOnly proves that, when the SCM refuses a new PR
// as a duplicate, GitHub, GitLab and Forgejo/Gitea reuse only the open PR
// from the same head branch into the same base branch. The refusal is about
// that head and base; an open PR from the kardinal branch into another branch
// must not become the promotion PR (the lookup used to match the head only).
func TestFindExistingPR_SameBaseOnly(t *testing.T) {
	const head = "kardinal/b/prod"
	type provider struct {
		name    string
		open    func(serverURL string) (string, int, error)
		refuse  func(w http.ResponseWriter)
		list    func(w http.ResponseWriter, r *http.Request, prs [][2]interface{})
		checkQS func(t *testing.T, q url.Values)
	}
	providers := []provider{
		{
			name: "github",
			open: func(u string) (string, int, error) {
				return scm.NewGitHubProvider("t", u, "").OpenPR(context.Background(), "o/r", "t", "b", head, "main")
			},
			refuse: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"message":"A pull request already exists for o:kardinal/b/prod."}]}`))
			},
			list: func(w http.ResponseWriter, _ *http.Request, prs [][2]interface{}) {
				var out []map[string]interface{}
				for _, p := range prs {
					out = append(out, map[string]interface{}{"number": p[0], "html_url": fmt.Sprintf("https://github.com/o/r/pull/%d", p[0]),
						"head": map[string]string{"ref": head}, "base": map[string]string{"ref": p[1].(string)}})
				}
				_ = json.NewEncoder(w).Encode(out)
			},
			checkQS: func(t *testing.T, q url.Values) {
				assert.Equal(t, "o:"+head, q.Get("head"))
				assert.Equal(t, "main", q.Get("base"))
			},
		},
		{
			name: "gitlab",
			open: func(u string) (string, int, error) {
				return scm.NewGitLabProvider("t", u, "").OpenPR(context.Background(), "g/p", "t", "b", head, "main")
			},
			refuse: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"message":["Another open merge request already exists for this source branch: !7"]}`))
			},
			list: func(w http.ResponseWriter, _ *http.Request, prs [][2]interface{}) {
				var out []map[string]interface{}
				for _, p := range prs {
					out = append(out, map[string]interface{}{"iid": p[0], "web_url": fmt.Sprintf("https://gitlab.com/g/p/-/merge_requests/%d", p[0]),
						"state": "opened", "source_branch": head, "target_branch": p[1]})
				}
				_ = json.NewEncoder(w).Encode(out)
			},
			checkQS: func(t *testing.T, q url.Values) {
				assert.Equal(t, head, q.Get("source_branch"))
				assert.Equal(t, "main", q.Get("target_branch"))
			},
		},
		{
			name: "forgejo",
			open: func(u string) (string, int, error) {
				return scm.NewForgejoProvider("t", u, "").OpenPR(context.Background(), "o/r", "t", "b", head, "main")
			},
			refuse: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"message":"pull request already exists for these targets"}`))
			},
			list: func(w http.ResponseWriter, _ *http.Request, prs [][2]interface{}) {
				var out []map[string]interface{}
				for _, p := range prs {
					out = append(out, map[string]interface{}{"number": p[0], "html_url": fmt.Sprintf("https://f.example/o/r/pulls/%d", p[0]),
						"head": map[string]string{"ref": head, "label": "o:" + head}, "base": map[string]string{"ref": p[1].(string)}})
				}
				_ = json.NewEncoder(w).Encode(out)
			},
		},
	}
	for _, p := range providers {
		serve := func(prs [][2]interface{}, q *url.Values) *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					p.refuse(w)
					return
				}
				*q = r.URL.Query()
				p.list(w, r, prs)
			}))
		}
		t.Run(p.name+" reuses the PR into the same base", func(t *testing.T) {
			var q url.Values
			// The PR into another branch is listed first (newest first).
			srv := serve([][2]interface{}{{9, "release"}, {3, "main"}}, &q)
			defer srv.Close()
			u, n, err := p.open(srv.URL)
			require.NoError(t, err)
			assert.Equal(t, 3, n)
			assert.True(t, strings.HasSuffix(u, "/3"), u)
			if p.checkQS != nil {
				p.checkQS(t, q)
			}
		})
		t.Run(p.name+" does not adopt a PR into another base", func(t *testing.T) {
			var q url.Values
			srv := serve([][2]interface{}{{9, "release"}}, &q)
			defer srv.Close()
			_, n, err := p.open(srv.URL)
			require.Error(t, err, "adopted #%d", n)
			assert.Contains(t, err.Error(), "into main")
		})
	}
}

// TestProviders_HTTPTimeout proves no provider can hang a reconcile worker
// on a stalled SCM endpoint (C06-scm-health-30).
func TestProviders_HTTPTimeout(t *testing.T) {
	for _, name := range []string{"github", "gitlab", "forgejo", "bitbucket", "azuredevops"} {
		p, err := scm.NewProvider(name, "t", "", "")
		require.NoError(t, err)
		timeout := scm.HTTPClientTimeoutForTest(p)
		assert.Positive(t, timeout, name)
	}
}

// TestGitHub403RateLimit_OpensCircuit proves a rate-limited controller stops
// calling GitHub (C06-scm-health-17).
func TestGitHub403RateLimit_OpensCircuit(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	defer srv.Close()

	p := scm.NewGitHubProvider("t", srv.URL, "")
	for i := 0; i < 10; i++ {
		_, _, err := p.GetPRStatus(context.Background(), "o/r", 1)
		require.Error(t, err)
	}
	assert.LessOrEqual(t, hits, 5)
}

// TestRenderPRBody_EscapesTableCells proves a CEL reason with "||" or a
// newline stays in its cell (C06-scm-health-25).
func TestRenderPRBody_EscapesTableCells(t *testing.T) {
	body, err := scm.RenderPRBody(scm.PRBody{
		PipelineName: "p", Environment: "prod", BundleName: "b",
		Bundle: v1alpha1.BundleSpec{
			Images:     []v1alpha1.ImageRef{{Repository: "ghcr.io/o/app", Tag: "v1"}},
			Provenance: &v1alpha1.BundleProvenance{Author: "a | b", CommitSHA: "abc"},
		},
		GateResults: []v1alpha1.GateResult{{
			GateName: "hours", Result: "pass",
			Reason:      "!schedule.isWeekend || bundle.provenance.author == 'x'\n= true",
			EvaluatedAt: metav1.Now(),
		}},
	})
	require.NoError(t, err)
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "| hours "):
			assert.Equal(t, 6, strings.Count(line, "|")-strings.Count(line, `\|`), line)
			assert.Contains(t, line, `\|\|`)
		case strings.HasPrefix(line, "| ghcr.io/o/app "):
			assert.Equal(t, 7, strings.Count(line, "|")-strings.Count(line, `\|`), line)
		}
	}
	assert.NotContains(t, body, "Source Diff")
}
