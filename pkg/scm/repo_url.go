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

	"github.com/go-git/go-git/v5/plumbing/transport"
)

// scpLikeURL matches the scp-like git remote syntax "user@host:path".
var scpLikeURL = regexp.MustCompile(`^([A-Za-z0-9._~-]+)@([A-Za-z0-9.-]+):(.+)$`)

// userinfoInURL matches the userinfo part of a URL-looking string
// ("//user:pass@"), up to the last "@" before the path, query or fragment,
// so a password with an "@" in it is removed whole and an "@" in the query
// is left alone.
var userinfoInURL = regexp.MustCompile(`//[^/?#\s]*@`)

// quotedURL matches a quoted URL, as a *url.Error prints it
// ("Get \"https://u:p@host/...\""). Each one is redacted on its own with
// RedactURL: parsed when it parses, so an "@" in its path ("/@scope/pkg",
// "/@v/list") is kept, and cut to its last "@" when it does not (a password
// with a space or a "/").
// A quoted string may hold escaped quotes (strconv.Quote writes \"), so the
// match runs to the first unescaped quote, and only a string that starts
// with a scheme is a URL.
var quotedURL = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)

// urlScheme matches the start of a URL.
var urlScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)

// secretParams are the URL parameter names (compared lower-cased, after
// percent-decoding) whose values are credentials.
var secretParams = map[string]bool{
	"access_token": true, "token": true, "private_token": true, "password": true,
	"api_key": true, "client_secret": true, "x-amz-signature": true, "x-amz-credential": true,
	"x-amz-security-token": true, "sig": true, "refresh_token": true, "apikey": true, "key": true,
}

// urlParam matches one key=value pair of a query or fragment, after "?",
// "&", ";" or "#".
var urlParam = regexp.MustCompile(`([?&;#])([^=&;#\s"']+)=([^&;#\s"']*)`)

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
		// A *url.Error quotes the raw URL, userinfo and all.
		return "", "", fmt.Errorf("parse repository URL: %s", RedactText(perr.Error()))
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

// SameSSHHost reports whether two ssh git remotes (ssh:// or scp-like) are
// on the same host and port, so the ssh key for a may be used for b.
func SameSSHHost(a, b string) bool {
	ea, errA := transport.NewEndpoint(strings.TrimSpace(a))
	eb, errB := transport.NewEndpoint(strings.TrimSpace(b))
	if errA != nil || errB != nil || ea.Protocol != "ssh" || eb.Protocol != "ssh" {
		return false
	}
	pa, pb := ea.Port, eb.Port
	if pa == 0 {
		pa = 22
	}
	if pb == 0 {
		pb = 22
	}
	return strings.EqualFold(ea.Host, eb.Host) && pa == pb
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
		u, err := url.Parse(raw)
		if err != nil {
			// Not a URL (a space or a "/" in the password): drop everything
			// from "//" to the last "@" before the query or fragment.
			if i := strings.Index(raw, "//"); i >= 0 {
				rest := raw[i+2:]
				end := len(rest)
				if k := strings.IndexAny(rest, "?#"); k >= 0 {
					end = k
				}
				if at := strings.LastIndex(rest[:end], "@"); at >= 0 {
					raw = raw[:i+2] + rest[at+1:]
				}
			}
			return redactParams(raw)
		}
		u.User = nil
		return redactParams(u.String())
	}
	return RedactText(raw)
}

// redactParams replaces the value of every secretParams pair in s, in a
// query or a fragment, "&"- or ";"-separated, with its key percent-encoded
// or not.
func redactParams(s string) string {
	return urlParam.ReplaceAllStringFunc(s, func(m string) string {
		sub := urlParam.FindStringSubmatch(m)
		key, err := url.QueryUnescape(sub[2])
		if err != nil {
			key = sub[2]
		}
		if !secretParams[strings.ToLower(key)] {
			return m
		}
		return sub[1] + sub[2] + "=REDACTED"
	})
}

// RedactErr is err with RedactText applied to its message. errors.Is and
// errors.As still see err, so a timeout or a context error is still one.
// Nil stays nil. Use it on an HTTP client's error: a *url.Error quotes the
// request URL, userinfo and query included.
//
// Caveat: errors.As (and errors.Unwrap) return the original error, whose
// Error() is not redacted. Format the RedactErr value itself, never what
// errors.As finds in it.
func RedactErr(err error) error {
	if err == nil {
		return nil
	}
	return redactedError{err}
}

type redactedError struct{ err error }

func (e redactedError) Error() string { return RedactText(e.err.Error()) }
func (e redactedError) Unwrap() error { return e.err }

// RedactText removes the userinfo of every URL in s, a text such as an error
// message or an HTTP response body. It never parses s as a URL: url.Parse
// takes many texts for a relative URL and returns them percent-encoded, with
// the credentials kept.
func RedactText(s string) string {
	s = quotedURL.ReplaceAllStringFunc(s, func(m string) string {
		inner := m[1 : len(m)-1]
		if !urlScheme.MatchString(inner) || !strings.Contains(inner, "@") {
			return m
		}
		return `"` + RedactURL(inner) + `"`
	})
	return redactParams(userinfoInURL.ReplaceAllString(s, "//"))
}

// webhookSignatureHeaders lists the webhook authentication headers of the
// supported providers. The first one present in a request is used.
var webhookSignatureHeaders = []string{
	"X-Hub-Signature-256", // GitHub, Gitea >= 1.19 ("sha256=<hex>")
	"X-Gitea-Signature",   // Gitea / Forgejo (bare hex)
	"X-Forgejo-Signature", // Forgejo (bare hex)
	"X-Gitlab-Token",      // GitLab (shared secret token)
	"X-AzureDevOps-Token", // Azure DevOps service hook custom header
	"X-Hub-Signature",     // Bitbucket Cloud and Data Center ("sha256=<hex>")
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
	"X-Event-Key",     // Bitbucket Cloud and Data Center ("pr:merged", "pullrequest:fulfilled", ...)
}

// RepoCanonicalizer is implemented by providers whose repository can be
// named in more than one way (Bitbucket Data Center: an /scm/ clone path,
// a browse path or an ssh path). CanonicalRepo returns one form for all.
type RepoCanonicalizer interface {
	CanonicalRepo(repo string) string
}

// SameRepo reports whether repositories a and b, as named by a Pipeline URL
// (RepoFromURL) or a webhook payload, are the same for provider p: the
// provider's canonical forms are equal, or, for other providers, a and b are
// equal ignoring case.
func SameRepo(p SCMProvider, a, b string) bool {
	if d, ok := p.(*DynamicProvider); ok {
		p = d.current()
	}
	if c, ok := p.(RepoCanonicalizer); ok {
		return c.CanonicalRepo(a) == c.CanonicalRepo(b)
	}
	return strings.EqualFold(a, b)
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

// SameRepoURL reports whether repository URLs a and b name the same
// repository: the same origin (SameOrigin) and the same owner/name path,
// ignoring a ".git" suffix and letter case of the path. SameRepo compares
// repository names (owner/name) for a provider.
func SameRepoURL(a, b string) bool {
	ka, okA := RepoURLKey(a)
	kb, okB := RepoURLKey(b)
	return okA && okB && ka == kb
}

// RepoURLKey is the key SameRepoURL compares: the http(s) origin and the
// repository path (RepoFromURL) in lower case. Two URLs have the same key
// exactly when SameRepoURL holds, so an index on the key finds every
// Pipeline of a repository. ok is false for a URL SameRepoURL never matches
// (not http or https, or no repository path).
func RepoURLKey(raw string) (string, bool) {
	origin, ok := httpOrigin(raw)
	if !ok {
		return "", false
	}
	repo, err := RepoFromURL(raw)
	if err != nil {
		return "", false
	}
	return origin + "/" + strings.ToLower(repo), true
}

// canonicalOf is p's canonical form of repo: its CanonicalRepo when it has
// one, the repository in lower case otherwise (the SameRepo comparison).
func canonicalOf(p SCMProvider, repo string) string {
	if c, ok := p.(RepoCanonicalizer); ok {
		return c.CanonicalRepo(repo)
	}
	return strings.ToLower(repo)
}

// CanonicalRepo implements RepoCanonicalizer for the provider DynamicProvider
// holds, so a Data Center provider behind it still matches its webhooks and
// allowlist however the URL names the repository.
func (d *DynamicProvider) CanonicalRepo(repo string) string { return canonicalOf(d.current(), repo) }

// CanonicalRepo implements RepoCanonicalizer for the guarded provider.
func (g *guardedProvider) CanonicalRepo(repo string) string { return canonicalOf(g.inner, repo) }

var (
	_ RepoCanonicalizer = (*DynamicProvider)(nil)
	_ RepoCanonicalizer = (*guardedProvider)(nil)
)
