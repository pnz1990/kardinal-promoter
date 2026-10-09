// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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

package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// maxIndexBytes bounds a Helm repository index.yaml.
const maxIndexBytes = 32 << 20

// HelmWatcher watches a Helm chart repository for new versions of one chart.
//
//   - HTTP(S) repository ("https://charts.example.com"): GET <repo>/index.yaml,
//     with basic auth when Credentials has a username and password. The
//     version's digest is the index entry's digest.
//   - OCI repository ("oci://ghcr.io/org/charts", or "oci+http://host:port/path"
//     for a plain-HTTP registry): the chart is the repository <path>/<chart>
//     and its tags are the versions (Helm writes "+" as "_" in a tag). The
//     digest is the manifest digest. Registry credentials work as for images.
//
// The highest semantic version that passes Filters wins; versions that are
// not semantic versions are ignored. The first poll (lastDigest "") is the
// baseline and reports no change.
type HelmWatcher struct {
	// RepoURL is the repository URL.
	RepoURL string
	// Chart is the chart name.
	Chart string
	// Filters select the versions.
	Filters TagFilters
	// Credentials authenticate to the repository; the zero value is anonymous.
	Credentials Credentials
	httpClient  *http.Client
}

// NewHelmWatcher creates a HelmWatcher with the egress-guarded HTTP client.
func NewHelmWatcher(repoURL, chart string) *HelmWatcher {
	return &HelmWatcher{RepoURL: repoURL, Chart: chart, httpClient: newHTTPClient()}
}

// WithHTTPClient makes w send its requests with c instead of the
// egress-guarded default, and returns w. It is for tests.
func (w *HelmWatcher) WithHTTPClient(c *http.Client) *HelmWatcher {
	w.httpClient = c
	return w
}

func (w *HelmWatcher) client() *http.Client {
	if w.httpClient != nil {
		return w.httpClient
	}
	return newHTTPClient()
}

// Watch returns the highest chart version that passes the filters.
func (w *HelmWatcher) Watch(ctx context.Context, lastDigest string) (*WatchResult, error) {
	if w.RepoURL == "" || w.Chart == "" {
		return nil, fmt.Errorf("HelmWatcher: repoURL and chart must not be empty")
	}
	if strings.ContainsAny(w.Chart, "/@: ") {
		return nil, fmt.Errorf("HelmWatcher: chart %q must be a chart name, not a path or reference", w.Chart)
	}
	compiled, err := w.Filters.compile()
	if err != nil {
		return nil, fmt.Errorf("HelmWatcher: %w", err)
	}
	var version, digest string
	if rest, ok := strings.CutPrefix(w.RepoURL, "oci://"); ok {
		version, digest, err = w.watchOCI(ctx, "https://"+rest, compiled)
	} else if rest, ok := strings.CutPrefix(w.RepoURL, "oci+http://"); ok {
		version, digest, err = w.watchOCI(ctx, "http://"+rest, compiled)
	} else {
		version, digest, err = w.watchIndex(ctx, compiled)
	}
	if err != nil {
		return nil, fmt.Errorf("HelmWatcher: chart %s in %s: %w", w.Chart, redactURL(w.RepoURL), err)
	}
	return &WatchResult{Digest: digest, Tag: version, Changed: lastDigest != "" && digest != lastDigest}, nil
}

// noVersion is the error for a chart with no version left after the filters.
func (w *HelmWatcher) noVersion(listed int) error {
	return fmt.Errorf("no version passes the filters (%s; %d versions listed)", w.Filters.describe(), listed)
}

// highestSemver returns the highest semantic version of versions, ignoring
// the rest; "" when there is none.
func highestSemver(versions []string) string {
	vs := semverOnly(versions)
	if len(vs) == 0 {
		return ""
	}
	return maxSemver(vs)
}

// watchOCI lists the chart repository's tags.
func (w *HelmWatcher) watchOCI(ctx context.Context, repoURL string, filters *compiledFilters) (string, string, error) {
	base, name, err := parseRegistryRef(strings.TrimRight(repoURL, "/") + "/" + w.Chart)
	if err != nil {
		return "", "", fmt.Errorf("parse OCI repository: %w", err)
	}
	auth, err := w.Credentials.registryAuthFor(base.Host)
	if err != nil {
		return "", "", err
	}
	s := &registrySession{client: w.client(), base: base, name: name, auth: auth}
	tags, err := s.listTags(ctx)
	if err != nil {
		return "", "", fmt.Errorf("list tags: %w", err)
	}
	byVersion := make(map[string]string, len(tags))
	versions := make([]string, 0, len(tags))
	for _, tag := range tags {
		v := strings.ReplaceAll(tag, "_", "+")
		byVersion[v] = tag
		versions = append(versions, v)
	}
	version := highestSemver(filters.apply(versions))
	if version == "" {
		return "", "", w.noVersion(len(tags))
	}
	digest, err := s.manifestDigest(ctx, byVersion[version])
	if err != nil {
		return "", "", err
	}
	return version, digest, nil
}

// helmIndex is the subset of index.yaml the watcher reads.
type helmIndex struct {
	Entries map[string][]struct {
		Version string `json:"version"`
		Digest  string `json:"digest"`
	} `json:"entries"`
}

// watchIndex reads <repoURL>/index.yaml.
func (w *HelmWatcher) watchIndex(ctx context.Context, filters *compiledFilters) (string, string, error) {
	u, err := url.Parse(w.RepoURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", "", fmt.Errorf("repoURL must be an https://, http://, oci:// or oci+http:// URL")
	}
	if u.User != nil {
		return "", "", fmt.Errorf("repoURL must not include credentials; use secretRef")
	}
	u = u.JoinPath("index.yaml")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	basic := w.Credentials.Username != "" || w.Credentials.Password != ""
	if basic {
		req.SetBasicAuth(w.Credentials.Username, w.Credentials.Password)
	}
	resp, err := w.client().Do(req)
	if err != nil {
		return "", "", fmt.Errorf("GET index.yaml: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	switch {
	case resp.StatusCode == http.StatusOK:
	case (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && basic:
		return "", "", fmt.Errorf("access denied to %s (HTTP %d) with the credentials from secretRef", u.Redacted(), resp.StatusCode)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", "", fmt.Errorf("authentication required for %s (HTTP %d); set secretRef to a Secret with keys username and password",
			u.Redacted(), resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound:
		// Some servers (Gitea, Forgejo, GitHub) answer 404 for a private
		// repository without credentials.
		if basic {
			return "", "", fmt.Errorf("not found: %s (HTTP 404); check repoURL", u.Redacted())
		}
		return "", "", fmt.Errorf("not found: %s (HTTP 404); check repoURL, and set secretRef if the repository is private",
			u.Redacted())
	default:
		return "", "", fmt.Errorf("unexpected HTTP %d from %s", resp.StatusCode, u.Redacted())
	}
	body, err := readLimited(resp.Body, maxIndexBytes)
	if err != nil {
		return "", "", fmt.Errorf("read index.yaml: %w", err)
	}
	var idx helmIndex
	if err := yaml.Unmarshal(body, &idx); err != nil {
		return "", "", fmt.Errorf("parse index.yaml: %w", err)
	}
	entries, ok := idx.Entries[w.Chart]
	if !ok {
		names := make([]string, 0, len(idx.Entries))
		for n := range idx.Entries {
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) > 10 {
			names = append(names[:10], "...")
		}
		return "", "", fmt.Errorf("the index has no chart %q (charts: %s)", w.Chart, strings.Join(names, ", "))
	}
	digests := make(map[string]string, len(entries))
	versions := make([]string, 0, len(entries))
	for _, e := range entries {
		if _, dup := digests[e.Version]; dup {
			continue
		}
		digests[e.Version] = e.Digest
		versions = append(versions, e.Version)
	}
	version := highestSemver(filters.apply(versions))
	if version == "" {
		return "", "", w.noVersion(len(versions))
	}
	return version, chartDigest(w.Chart, version, digests[version]), nil
}

// chartDigest returns "sha256:<hex>" for an index digest, or a digest of
// chart@version when the index has none (so a version is still identified).
func chartDigest(chart, version, indexDigest string) string {
	d := strings.ToLower(strings.TrimSpace(indexDigest))
	if d != "" {
		if strings.Contains(d, ":") {
			return d
		}
		return "sha256:" + d
	}
	sum := sha256.Sum256([]byte(chart + "@" + version))
	return "sha256:" + hex.EncodeToString(sum[:])
}
