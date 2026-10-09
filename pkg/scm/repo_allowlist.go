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
	"errors"
	"fmt"
	"path"
	"strings"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// ReasonRepositoryNotAllowed is the Pipeline Ready=False reason, and the
// cause in a PromotionStep's failure, for a Pipeline that would have the
// controller's shared SCM token act on a repository outside
// --scm-allowed-repositories (#1332).
const ReasonRepositoryNotAllowed = "RepositoryNotAllowed"

// RepositoryAllowlist is the controller's --scm-allowed-repositories (Helm
// value scm.allowedRepositories): the repositories a Pipeline without its own
// git.secretRef may promote into. Without a secretRef the controller's own
// SCM token opens, labels, comments on and closes the PRs and deletes
// kardinal/ branches, so anyone who can create a Pipeline could otherwise
// reach every repository that token can write to.
//
// A pattern is host/path, matched case-insensitively against the host and
// path of spec.git.url without scheme, userinfo, port and ".git":
// "github.com/acme/*" matches every repository of acme, "*" one path
// segment as in path.Match, and a pattern ending in "/**" everything under
// it, GitLab subgroups included ("gitlab.example.com/platform/**"). A
// pattern may carry a scheme or ".git"; both are dropped.
//
// A nil *RepositoryAllowlist allows every repository (the flag is unset,
// today's behaviour).
type RepositoryAllowlist struct {
	patterns []string
}

// ParseRepositoryAllowlist returns the allowlist for patterns, or nil when
// there is none (empty strings are skipped). A malformed pattern is an error.
func ParseRepositoryAllowlist(patterns []string) (*RepositoryAllowlist, error) {
	var out []string
	for _, raw := range patterns {
		p := normalizeRepoPattern(raw)
		if p == "" {
			continue
		}
		if _, err := path.Match(strings.TrimSuffix(p, "/**"), ""); err != nil {
			return nil, fmt.Errorf("allowed repository pattern %q: %w", raw, err)
		}
		if strings.Contains(strings.TrimSuffix(p, "/**"), "**") {
			return nil, fmt.Errorf("allowed repository pattern %q: ** is allowed only as the last path segment", raw)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &RepositoryAllowlist{patterns: out}, nil
}

// normalizeRepoPattern lowercases a pattern and drops a scheme, a ".git"
// suffix and slashes at either end.
func normalizeRepoPattern(raw string) string {
	p := strings.ToLower(strings.TrimSpace(raw))
	if _, rest, ok := strings.Cut(p, "://"); ok {
		p = rest
	}
	p = strings.Trim(p, "/")
	return strings.Trim(strings.TrimSuffix(p, ".git"), "/")
}

// Patterns returns the normalized patterns, for logs and messages.
func (a *RepositoryAllowlist) Patterns() []string {
	if a == nil {
		return nil
	}
	return append([]string(nil), a.patterns...)
}

// Allows reports whether gitURL matches a pattern. A nil allowlist allows
// every URL; an allowlist never allows a URL it cannot parse.
func (a *RepositoryAllowlist) Allows(gitURL string) bool {
	if a == nil {
		return true
	}
	host, p, err := splitRemoteURL(gitURL)
	if err != nil || host == "" {
		return false
	}
	repo := strings.Trim(strings.TrimSuffix(strings.ToLower(host+"/"+strings.Trim(p, "/")), ".git"), "/")
	for _, pat := range a.patterns {
		if prefix, ok := strings.CutSuffix(pat, "/**"); ok {
			if m, _ := path.Match(prefix, repo); m {
				return true
			}
			// Match the pattern's segments against the leading segments of repo.
			n := strings.Count(prefix, "/") + 1
			segs := strings.Split(repo, "/")
			if len(segs) > n {
				if m, _ := path.Match(prefix, strings.Join(segs[:n], "/")); m {
					return true
				}
			}
			continue
		}
		if m, _ := path.Match(pat, repo); m {
			return true
		}
	}
	return false
}

// ErrRepositoryNotAllowed is wrapped by CheckPipeline's error.
var ErrRepositoryNotAllowed = errors.New("repository not allowed")

// CheckPipeline returns an error wrapping ErrRepositoryNotAllowed when p has
// no git.secretRef and its spec.git.url is not allowed, so the controller's
// shared token would act on it. A Pipeline with its own secretRef pushes
// with its namespace's token, which proves its author's access, and is
// always allowed. A nil allowlist allows every Pipeline.
func (a *RepositoryAllowlist) CheckPipeline(p *v1alpha1.Pipeline) error {
	if a == nil || (p.Spec.Git.SecretRef != nil && p.Spec.Git.SecretRef.Name != "") {
		return nil
	}
	if a.Allows(p.Spec.Git.URL) {
		return nil
	}
	return fmt.Errorf("%w: spec.git.url %q is not in the controller's allowed repositories (%s), "+
		"so the controller's SCM token may not open PRs there; set git.secretRef to a Secret in this "+
		"namespace with a token for the repository, or ask the cluster admin to add it to "+
		"scm.allowedRepositories", ErrRepositoryNotAllowed, RedactURL(p.Spec.Git.URL), strings.Join(a.patterns, ", "))
}
