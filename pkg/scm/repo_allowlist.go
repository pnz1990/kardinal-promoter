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
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// ReasonRepositoryNotAllowed is the Pipeline Ready=False reason, and the
// cause in a PromotionStep's failure, for a Pipeline that would have the
// controller's shared SCM token act on a repository outside
// --scm-allowed-repositories (#1332).
const ReasonRepositoryNotAllowed = "RepositoryNotAllowed"

// ErrRepositoryNotAllowed is wrapped by every error of the allowlist: a
// Pipeline check (CheckPipeline) and a refused SCM API call (Guard).
// IsPermanentError reports it as permanent: retrying cannot fix it.
var ErrRepositoryNotAllowed = errors.New("repository not allowed")

// RepositoryAllowlist is the controller's --scm-allowed-repositories (Helm
// value scm.allowedRepositories): the repositories the controller's own SCM
// token may act on. That token opens, labels, comments on, polls and closes
// every PR and deletes kardinal/ branches, whatever git.secretRef says (the
// Pipeline's own token is used only for git clone and push), so without a
// list anyone who can create a Pipeline can reach every repository the token
// can write to.
//
// A pattern is host/repository: the host of the SCM, and the repository as
// the SCM API names it (RepoFromURL): owner/repo on GitHub, Forgejo, Gitea
// and Bitbucket, the full project path on GitLab, organization/project/repo
// on Azure DevOps, whose host is always dev.azure.com (an
// {org}.visualstudio.com or ssh.dev.azure.com URL is matched as
// dev.azure.com/{org}/{project}/{repo}). Matching ignores case and a scheme,
// userinfo, port or ".git" in the pattern; an IPv6 host may keep its URL
// brackets ("[fd00::1]:3000/acme/*"). "*" matches one path segment as in
// path.Match, and a pattern ending in "/**" every repository below it, GitLab
// subgroups included. A URL that does not parse to a host and a repository
// is never allowed.
//
// The list is enforced twice: up front, where the Pipeline and PromotionStep
// reconcilers check spec.git.url (CheckPipeline), and on every SCM API call
// the shared token makes (Guard), so no code path can reach another
// repository.
//
// A nil *RepositoryAllowlist allows every repository (the flag is unset,
// today's behaviour).
type RepositoryAllowlist struct {
	patterns []repoPattern
	// canon, when set (WithCanonicalRepo), is the provider's canonical form
	// of a repository (RepoCanonicalizer): a Bitbucket Data Center
	// repository is matched as KEY/slug however the URL names it.
	canon func(string) string
}

// WithCanonicalRepo returns a copy of a that matches repositories in p's
// canonical form (RepoCanonicalizer), or a when p has none. A nil
// allowlist stays nil.
func (a *RepositoryAllowlist) WithCanonicalRepo(p SCMProvider) *RepositoryAllowlist {
	c, ok := p.(RepoCanonicalizer)
	if a == nil || !ok {
		return a
	}
	out := *a
	out.canon = c.CanonicalRepo
	return &out
}

type repoPattern struct {
	host, repo string // repo may end in "/**"
}

func (p repoPattern) String() string { return p.host + "/" + p.repo }

// ParseRepositoryAllowlist returns the allowlist for patterns, or nil when
// there is none (empty strings are skipped). A malformed pattern is an error.
func ParseRepositoryAllowlist(patterns []string) (*RepositoryAllowlist, error) {
	var out []repoPattern
	for _, raw := range patterns {
		p := normalizeRepoPattern(raw)
		if p == "" {
			continue
		}
		host, repo, ok := strings.Cut(p, "/")
		if !ok || host == "" || repo == "" {
			return nil, fmt.Errorf("allowed repository pattern %q: want host/repository, e.g. github.com/acme/*", raw)
		}
		prefix := strings.TrimSuffix(repo, "/**")
		if repo == "**" {
			prefix = ""
		}
		for _, part := range []string{host, prefix} {
			if _, err := path.Match(part, ""); err != nil {
				return nil, fmt.Errorf("allowed repository pattern %q: %w", raw, err)
			}
			if strings.Contains(part, "**") {
				return nil, fmt.Errorf("allowed repository pattern %q: ** is allowed only as the last path segment", raw)
			}
		}
		out = append(out, repoPattern{host: host, repo: repo})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &RepositoryAllowlist{patterns: out}, nil
}

// normalizeRepoPattern lowercases a pattern and drops a scheme, userinfo, a
// port, a ".git" suffix and slashes at either end.
func normalizeRepoPattern(raw string) string {
	p := strings.ToLower(strings.TrimSpace(raw))
	if _, rest, ok := strings.Cut(p, "://"); ok {
		p = rest
	}
	p = strings.Trim(p, "/")
	host, rest, _ := strings.Cut(p, "/")
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	if strings.HasPrefix(host, "[") {
		// An IPv6 address, "[fd00::1]" or "[fd00::1]:3000": match the
		// address alone, as url.Hostname reports it; the brackets would be
		// a character class to path.Match.
		if end := strings.Index(host, "]"); end > 0 {
			host = host[1:end]
		}
	} else if h, port, ok := strings.Cut(host, ":"); ok && port != "" && !strings.ContainsAny(port, "*?[") {
		host = h
	}
	if rest == "" {
		return host
	}
	return host + "/" + strings.Trim(strings.TrimSuffix(rest, ".git"), "/")
}

// Patterns returns the normalized patterns, for logs and messages.
func (a *RepositoryAllowlist) Patterns() []string {
	if a == nil {
		return nil
	}
	out := make([]string, len(a.patterns))
	for i, p := range a.patterns {
		out[i] = p.String()
	}
	return out
}

// AllowsRepo reports whether repo (as the SCM API names it) on the SCM host
// matches a pattern. A nil allowlist allows everything. An empty host or
// repository is never allowed, and neither is one with a segment that is
// not validRepoSegment: "", "." and "..", and any segment with a percent
// sign, backslash, ?, #, whitespace or a control character, so an escaped
// or smuggled path ("acme/%2e%2e%2fvictim%2frepo") cannot pass for an
// allowed one.
func (a *RepositoryAllowlist) AllowsRepo(host, repo string) bool {
	if a == nil {
		return true
	}
	if a.canon != nil {
		repo = a.canon(repo)
	}
	host, repo = strings.ToLower(host), strings.ToLower(strings.Trim(repo, "/"))
	if host == "" || repo == "" {
		return false
	}
	segs := strings.Split(repo, "/")
	for i := range segs {
		if !validRepoSegment(host, segs, i) {
			return false
		}
	}
	for _, p := range a.patterns {
		if m, _ := path.Match(p.host, host); !m {
			continue
		}
		if p.repo == "**" {
			return true
		}
		if prefix, ok := strings.CutSuffix(p.repo, "/**"); ok {
			// The prefix matches the leading segments; at least one more follows.
			n := strings.Count(prefix, "/") + 1
			if len(segs) > n {
				if m, _ := path.Match(prefix, strings.Join(segs[:n], "/")); m {
					return true
				}
			}
			continue
		}
		if m, _ := path.Match(p.repo, repo); m {
			return true
		}
	}
	return false
}

// repoSegment is a segment of a repository name every SCM accepts.
var repoSegment = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// azureNameSegment is an Azure DevOps project or repository name: words of
// repoSegment characters separated by single spaces.
var azureNameSegment = regexp.MustCompile(`^[A-Za-z0-9._-]+( [A-Za-z0-9._-]+)*$`)

// validRepoSegment reports whether segs[i] may be a segment of a repository
// on host: repoSegment and not "." or "..". Only the project and repository
// names of an Azure DevOps repository (dev.azure.com, organization/project/repo)
// may hold single spaces; its organization may not.
func validRepoSegment(host string, segs []string, i int) bool {
	s := segs[i]
	if s == "." || s == ".." {
		return false
	}
	if host == "dev.azure.com" && len(segs) == 3 && i >= 1 {
		return azureNameSegment.MatchString(s)
	}
	return repoSegment.MatchString(s)
}

// Allows reports whether the repository of the git remote gitURL is allowed:
// its host (dev.azure.com for Azure DevOps) and its repository as
// RepoFromURL parses it. A URL that does not parse is never allowed.
func (a *RepositoryAllowlist) Allows(gitURL string) bool {
	if a == nil {
		return true
	}
	host, repo, err := RepoIdentity(gitURL)
	if err != nil {
		return false
	}
	return a.AllowsRepo(host, repo)
}

// RepoIdentity returns the SCM host and the repository (RepoFromURL) of a git
// remote URL. Azure DevOps remotes ({org}.visualstudio.com, ssh.dev.azure.com,
// vs-ssh.visualstudio.com) are reported on dev.azure.com, the host of the
// organization/project/repo the API acts on.
func RepoIdentity(gitURL string) (host, repo string, err error) {
	host, _, err = splitRemoteURL(gitURL)
	if err != nil {
		return "", "", err
	}
	if host == "" {
		return "", "", fmt.Errorf("repository URL %q has no host", RedactURL(gitURL))
	}
	repo, err = RepoFromURL(gitURL)
	if err != nil {
		return "", "", err
	}
	if host == "ssh.dev.azure.com" || strings.HasSuffix(host, ".visualstudio.com") {
		host = "dev.azure.com"
	}
	return host, repo, nil
}

// WebHost returns the SCM host the allowlist matches the shared token's API
// calls on, for the controller's --scm-provider and --scm-api-url: the API
// URL's host without an "api." prefix (api.github.com is github.com), or the
// provider's public host when no URL is set.
func WebHost(providerType, apiURL string) (string, error) {
	defaults := map[string]string{"": "github.com", "github": "github.com", "gitlab": "gitlab.com",
		"forgejo": "codeberg.org", "gitea": "codeberg.org", "bitbucket": "bitbucket.org", "azuredevops": "dev.azure.com",
		// Data Center has no public host: the API URL names it.
		"bitbucket-datacenter": ""}
	def, ok := defaults[providerType]
	if !ok {
		return "", fmt.Errorf("unknown SCM provider type %q", providerType)
	}
	if def == "" && strings.TrimSpace(apiURL) == "" {
		return "", fmt.Errorf("SCM provider %s needs an API URL to name its host", providerType)
	}
	if strings.TrimSpace(apiURL) == "" {
		return def, nil
	}
	u, err := url.Parse(strings.TrimSpace(apiURL))
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("SCM API URL %q has no host", RedactURL(apiURL))
	}
	host := strings.ToLower(u.Hostname())
	if providerType != "azuredevops" {
		host = strings.TrimPrefix(host, "api.")
	}
	return host, nil
}

// CheckPipeline returns an error wrapping ErrRepositoryNotAllowed when the
// Pipeline would have the controller's shared token act on a repository
// that is not allowed. That is every Pipeline whose spec.git.url is not
// allowed, except one that never needs the shared token: ownSecret (its
// git.secretRef names a Secret that exists, so git clone and push use its
// token) and no environment with approval: pr-review (whose PR the shared
// token opens, polls and closes). A nil allowlist allows every Pipeline.
func (a *RepositoryAllowlist) CheckPipeline(p *v1alpha1.Pipeline, ownSecret bool) error {
	if a == nil || a.Allows(p.Spec.Git.URL) {
		return nil
	}
	why := "the Pipeline has no git.secretRef to a Secret that exists, so nothing but the controller's token can reach it"
	if ownSecret {
		env := prReviewEnv(p)
		if env == "" {
			return nil
		}
		why = fmt.Sprintf("environment %q uses approval: pr-review, and the controller's token opens and tracks its PRs", env)
	}
	return fmt.Errorf("%w: spec.git.url %q is not in the controller's allowed repositories (%s): %s; "+
		"ask the cluster admin to add it to scm.allowedRepositories", ErrRepositoryNotAllowed,
		RedactURL(p.Spec.Git.URL), strings.Join(a.Patterns(), ", "), why)
}

// prReviewEnv returns the first environment with approval: pr-review, or "".
func prReviewEnv(p *v1alpha1.Pipeline) string {
	for _, e := range p.Spec.Environments {
		if e.Approval == "pr-review" {
			return e.Name
		}
	}
	return ""
}

// NotAllowedMessage returns err's text without the ErrRepositoryNotAllowed
// prefix, for a condition or step message.
func NotAllowedMessage(err error) string {
	return strings.TrimPrefix(err.Error(), ErrRepositoryNotAllowed.Error()+": ")
}

// PipelineSecretExists reports whether the Pipeline's git.secretRef names a
// Secret that exists in the Pipeline's namespace, the token git clone and
// push use instead of none (CheckPipeline's ownSecret). A secretRef in another
// namespace does not count. Secrets are read uncached (get only).
func PipelineSecretExists(ctx context.Context, reader client.Reader, p *v1alpha1.Pipeline) (bool, error) {
	ref := p.Spec.Git.SecretRef
	if ref == nil || ref.Name == "" || (ref.Namespace != "" && ref.Namespace != p.Namespace) {
		return false, nil
	}
	var s corev1.Secret
	err := reader.Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: ref.Name}, &s)
	switch {
	case err == nil:
		return true, nil
	case apierrors.IsNotFound(err):
		return false, nil
	default:
		return false, fmt.Errorf("get git.secretRef Secret %s/%s: %w", p.Namespace, ref.Name, err)
	}
}
