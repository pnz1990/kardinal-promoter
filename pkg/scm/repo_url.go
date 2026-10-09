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

package scm

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// scpLikeURL matches the scp-like git remote syntax "user@host:path".
var scpLikeURL = regexp.MustCompile(`^([A-Za-z0-9._~-]+)@([A-Za-z0-9.-]+):(.+)$`)

// userinfoInURL matches the userinfo part of a URL-looking string ("//user:pass@").
var userinfoInURL = regexp.MustCompile(`//[^/@\s]+@`)

// splitRemoteURL returns the host and the path (without a leading slash) of a
// git remote URL. It accepts https://, http://, ssh://, git:// and the
// scp-like "git@host:owner/repo.git" form. Userinfo is discarded.
func splitRemoteURL(raw string) (host, path string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("empty repository URL")
	}
	if !strings.Contains(raw, "://") {
		m := scpLikeURL.FindStringSubmatch(raw)
		if m == nil {
			return "", "", fmt.Errorf("unrecognised repository URL %q", RedactURL(raw))
		}
		return strings.ToLower(m[2]), strings.Trim(m[3], "/"), nil
	}
	u, perr := url.Parse(raw)
	if perr != nil {
		return "", "", fmt.Errorf("parse repository URL %q: %w", RedactURL(raw), perr)
	}
	return strings.ToLower(u.Hostname()), strings.Trim(u.Path, "/"), nil
}

// RepoFromURL returns the repository identifier that the SCM provider APIs
// expect for a git remote URL:
//
//   - GitHub, GitHub Enterprise, Forgejo/Gitea, Bitbucket: "owner/repo"
//   - GitLab: the full project path, including subgroups ("group/sub/proj")
//   - Azure DevOps: "org/project/repo" for dev.azure.com, {org}.visualstudio.com,
//     Azure DevOps Server ("collection/project/repo") and the ssh "v3/" form
//
// A ".git" suffix, userinfo and a GitLab "/-/..." suffix are removed. It
// returns an error instead of the raw URL when no repository path is found,
// so a URL is never sent as a repository name to a provider API.
func RepoFromURL(raw string) (string, error) {
	host, path, err := splitRemoteURL(raw)
	if err != nil {
		return "", err
	}
	// GitLab web URLs put everything after "/-/" outside the project path.
	if i := strings.Index("/"+path+"/", "/-/"); i >= 0 {
		path = strings.Trim(("/" + path + "/")[:i], "/")
	}
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")

	segs := strings.Split(path, "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return "", fmt.Errorf("repository URL %q has no valid repository path", RedactURL(raw))
		}
	}

	// Azure DevOps.
	for i, s := range segs {
		if s != "_git" {
			continue
		}
		if i+1 >= len(segs) {
			return "", fmt.Errorf("azure devops URL %q has no repository after _git", RedactURL(raw))
		}
		repo := segs[i+1]
		if i < 1 {
			return "", fmt.Errorf("azure devops URL %q has no project before _git", RedactURL(raw))
		}
		project := segs[i-1]
		org := ""
		if i >= 2 && !strings.EqualFold(segs[i-2], "DefaultCollection") {
			org = segs[i-2]
		} else if sub, ok := strings.CutSuffix(host, ".visualstudio.com"); ok {
			org = sub
		}
		if org == "" {
			return "", fmt.Errorf("azure devops URL %q has no organization", RedactURL(raw))
		}
		return org + "/" + project + "/" + repo, nil
	}
	if host == "ssh.dev.azure.com" || strings.HasSuffix(host, "vs-ssh.visualstudio.com") {
		// git@ssh.dev.azure.com:v3/{org}/{project}/{repo}
		if len(segs) == 4 && segs[0] == "v3" {
			return strings.Join(segs[1:], "/"), nil
		}
		return "", fmt.Errorf("azure devops ssh URL %q must be v3/org/project/repo", RedactURL(raw))
	}

	if len(segs) < 2 {
		return "", fmt.Errorf("repository URL %q must include owner and repository", RedactURL(raw))
	}
	return strings.Join(segs, "/"), nil
}

// SameOrigin reports whether two http(s) git remote URLs have the same
// origin: scheme, host and port (default ports made explicit). It decides
// whether the credential for a may be sent to b, so a token for an https
// remote is never sent over plain http or to another port. ssh and scp-like
// remotes never match: the token is only used for http(s) basic auth.
func SameOrigin(a, b string) bool {
	oa, okA := httpOrigin(a)
	ob, okB := httpOrigin(b)
	return okA && okB && oa == ob
}

// httpOrigin returns "scheme://host:port" for an http or https URL.
func httpOrigin(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	switch {
	case scheme == "https" && port == "":
		port = "443"
	case scheme == "http" && port == "":
		port = "80"
	case scheme != "https" && scheme != "http":
		return "", false
	}
	return scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port, true
}

// prURLMarkers are the PR path markers of the supported providers, most
// specific first.
var prURLMarkers = []string{
	"/-/merge_requests/", // GitLab
	"/pullrequest/",      // Azure DevOps
	"/pull-requests/",    // Bitbucket
	"/pulls/",            // Forgejo / Gitea
	"/pull/",             // GitHub / GitHub Enterprise
}

// ParsePRURL returns the repository identifier (see RepoFromURL) and the PR
// number of a pull request web URL from any supported provider.
func ParsePRURL(prURL string) (repo string, number int, err error) {
	s := strings.TrimSpace(prURL)
	if q := strings.IndexAny(s, "?#"); q >= 0 {
		s = s[:q]
	}
	for _, m := range prURLMarkers {
		idx := strings.LastIndex(s, m)
		if idx < 0 {
			continue
		}
		rest := s[idx+len(m):]
		if end := strings.Index(rest, "/"); end >= 0 {
			rest = rest[:end]
		}
		n, convErr := strconv.Atoi(rest)
		if convErr != nil || n <= 0 {
			return "", 0, fmt.Errorf("PR URL %q has no valid PR number", RedactURL(prURL))
		}
		r, repoErr := RepoFromURL(s[:idx])
		if repoErr != nil {
			return "", 0, fmt.Errorf("PR URL: %w", repoErr)
		}
		return r, n, nil
	}
	return "", 0, fmt.Errorf("unrecognised PR URL %q", RedactURL(prURL))
}

// RedactURL removes userinfo (tokens, passwords) from a URL so it can be put
// in logs, status messages and errors. The scp-like "git@host:path" form is
// returned unchanged because its user is not a secret. For text that holds
// URLs, such as an error message, use RedactText.
func RedactURL(raw string) string {
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil {
			u.User = nil
			return u.String()
		}
	}
	return RedactText(raw)
}

// RedactText removes the userinfo of every URL in s, a text such as an error
// message or an HTTP response body. It never parses s as a URL: url.Parse
// takes many texts for a relative URL and returns them percent-encoded, with
// the credentials kept.
func RedactText(s string) string {
	return userinfoInURL.ReplaceAllString(s, "//")
}

// webhookSignatureHeaders lists the webhook authentication headers of the
// supported providers. The first one present in a request is used.
var webhookSignatureHeaders = []string{
	"X-Hub-Signature-256", // GitHub, Gitea >= 1.19 ("sha256=<hex>")
	"X-Gitea-Signature",   // Gitea / Forgejo (bare hex)
	"X-Forgejo-Signature", // Forgejo (bare hex)
	"X-Gitlab-Token",      // GitLab (shared secret token)
	"X-AzureDevOps-Token", // Azure DevOps service hook custom header
	"X-Hub-Signature",     // Bitbucket Cloud ("sha256=<hex>")
}

// WebhookSignature returns the webhook signature or token carried by an SCM
// webhook request, whichever provider sent it. The provider's
// ParseWebhookEvent validates it; an empty result means the request carries
// no signature.
func WebhookSignature(h http.Header) string {
	for _, name := range webhookSignatureHeaders {
		if v := h.Get(name); v != "" {
			return v
		}
	}
	return ""
}

// WebhookSignatureHeader returns the name of the header WebhookSignature reads
// the signature from, or "" when the request carries none. It is safe to log;
// the signature is not (GitLab and Azure DevOps send the secret itself).
func WebhookSignatureHeader(h http.Header) string {
	for _, name := range webhookSignatureHeaders {
		if h.Get(name) != "" {
			return name
		}
	}
	return ""
}

// webhookEventHeaders lists the headers that name the event type of a
// webhook. The first one present in a request is used. Gitea and Forgejo also
// send X-GitHub-Event with the same value, so their own headers come first.
var webhookEventHeaders = []string{
	"X-Forgejo-Event", // Forgejo ("pull_request", "push", "issue_comment", ...)
	"X-Gitea-Event",   // Gitea and Forgejo
	"X-Gitlab-Event",  // GitLab ("Merge Request Hook", "Push Hook", ...)
	"X-GitHub-Event",  // GitHub ("pull_request", "push", "ping", ...)
}

// webhookEventType returns the event type named by an SCM webhook request's
// headers, or "" when it carries none.
func webhookEventType(h http.Header) string {
	for _, name := range webhookEventHeaders {
		if v := h.Get(name); v != "" {
			return v
		}
	}
	return ""
}

// eventTypeParser is implemented by providers that need the event header to
// tell a pull request event from another event: the GitHub and Forgejo/Gitea
// payloads do not say what kind of event they are, and GitLab's object_kind
// can be missing. eventType is "" when the request has no event header.
type eventTypeParser interface {
	parseWebhookEvent(payload []byte, signature, eventType string) (WebhookEvent, error)
}

// ParseWebhookRequest validates and parses an SCM webhook request: it reads
// the signature and the event type from the headers and passes them to the
// provider. Providers that do not use an event header get
// ParseWebhookEvent(payload, signature).
func ParseWebhookRequest(p SCMProvider, payload []byte, h http.Header) (WebhookEvent, error) {
	signature := WebhookSignature(h)
	if tp, ok := p.(eventTypeParser); ok {
		return tp.parseWebhookEvent(payload, signature, webhookEventType(h))
	}
	return p.ParseWebhookEvent(payload, signature)
}

// SameRepo reports whether a and b name the same repository: the same
// origin (SameOrigin) and the same owner/name path, ignoring a ".git"
// suffix and letter case of the path.
func SameRepo(a, b string) bool {
	if !SameOrigin(a, b) {
		return false
	}
	ra, errA := RepoFromURL(a)
	rb, errB := RepoFromURL(b)
	return errA == nil && errB == nil && strings.EqualFold(ra, rb)
}
