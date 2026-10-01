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
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const (
	// maxTagPages bounds tags/list pagination.
	maxTagPages = 100
	// maxNewestBuildTags bounds how many tags newest-build ordering inspects.
	// Newest-build reads each tag's manifest and image config, so it costs two or
	// three registry requests per tag on every poll.
	maxNewestBuildTags = 50
	// manifestAccept lists every manifest media type the watcher understands.
	// The index and manifest-list types are required for multi-arch tags: without
	// them registries return 404 or down-convert to a single-arch digest.
	manifestAccept = "application/vnd.oci.image.index.v1+json," +
		"application/vnd.docker.distribution.manifest.list.v2+json," +
		"application/vnd.oci.image.manifest.v1+json," +
		"application/vnd.docker.distribution.manifest.v2+json"
	dockerHubRegistry = "registry-1.docker.io"
)

// semverTag matches a full semantic version, with an optional "v" prefix.
var semverTag = regexp.MustCompile(`^v?\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// OCIWatcher watches an OCI registry for new image tags.
// It uses the OCI Distribution Specification API (no external dependencies):
//   - GET /v2/<name>/tags/list (following Link rel="next" pagination) to list tags
//   - HEAD or GET /v2/<name>/manifests/<tag> to read the digest
//   - GET /v2/<name>/blobs/<config> to read the build time (newest-build ordering)
//
// Public registries that require the anonymous Bearer token flow (ghcr.io,
// Docker Hub, quay.io) are supported. Registries that need credentials (ECR,
// private repositories) are not: the watcher returns an error saying so.
//
// The watcher is safe for concurrent use: Watch keeps no state between calls.
type OCIWatcher struct {
	// Registry is the OCI image repository (e.g. "ghcr.io/myorg/myapp").
	// A reference without a registry host ("nginx", "myorg/app") is a Docker Hub
	// repository. For a plain-HTTP in-cluster registry include the scheme:
	// "http://registry.registry.svc.cluster.local:5000/myapp".
	Registry string
	// TagFilter is an optional regex that tags must match. When empty, all tags match.
	TagFilter string
	// httpClient is the HTTP client used for requests. NewOCIWatcher sets a
	// client with a timeout and the egress guard.
	httpClient *http.Client
}

// NewOCIWatcher creates an OCIWatcher with an HTTP client that times out and
// refuses loopback, link-local and cloud metadata addresses (pkg/egress).
func NewOCIWatcher(registry, tagFilter string) *OCIWatcher {
	return &OCIWatcher{
		Registry:   registry,
		TagFilter:  tagFilter,
		httpClient: newHTTPClient(),
	}
}

// WithHTTPClient makes w send every request, token fetches included, with c
// instead of the egress-guarded default, and returns w. It is for tests: an
// httptest server listens on loopback, which the guard refuses. The
// controller keeps the default.
func (w *OCIWatcher) WithHTTPClient(c *http.Client) *OCIWatcher {
	w.httpClient = c
	return w
}

// ociTagsListResponse is the JSON response from /v2/<name>/tags/list.
type ociTagsListResponse struct {
	Tags []string `json:"tags"`
}

// Watch polls the OCI registry for the newest tag matching TagFilter.
//
// Selection of the newest tag among the tags that match TagFilter:
//   - exactly one tag (a moving tag such as "latest" or "main"): that tag. A new
//     push is detected as a digest change.
//   - every tag is a full semantic version: the highest version.
//   - otherwise: the tag whose image was built most recently (image config
//     "created"). At most maxNewestBuildTags tags are inspected; more is an error.
//
// No matching tag is an error, so a wrong tagFilter is visible in status.
//
// First-run (lastDigest=="") returns Changed=false to avoid creating a Bundle
// on every Subscription creation at controller startup. The caller records the
// returned Digest as the baseline.
func (w *OCIWatcher) Watch(ctx context.Context, lastDigest string) (*WatchResult, error) {
	if w.Registry == "" {
		return nil, fmt.Errorf("OCIWatcher: registry must not be empty")
	}
	base, name, err := parseRegistryRef(w.Registry)
	if err != nil {
		return nil, fmt.Errorf("OCIWatcher: parse registry ref: %w", err)
	}
	var filter *regexp.Regexp
	if w.TagFilter != "" {
		if filter, err = regexp.Compile(w.TagFilter); err != nil {
			return nil, fmt.Errorf("OCIWatcher: invalid tagFilter regex %q: %w", w.TagFilter, err)
		}
	}

	s := &registrySession{client: w.client(), base: base, name: name}
	tags, err := s.listTags(ctx)
	if err != nil {
		return nil, fmt.Errorf("OCIWatcher: list tags for %q: %w", w.Registry, err)
	}
	filtered := filterTags(tags, filter)
	if len(filtered) == 0 {
		return nil, fmt.Errorf("OCIWatcher: no tag of %q matches tagFilter %q (%d tags listed)",
			w.Registry, w.TagFilter, len(tags))
	}

	tag, digest, err := s.selectNewest(ctx, filtered)
	if err != nil {
		return nil, fmt.Errorf("OCIWatcher: %s: %w", w.Registry, err)
	}

	// First-run is not considered a change.
	changed := lastDigest != "" && digest != lastDigest
	return &WatchResult{Digest: digest, Tag: tag, Changed: changed}, nil
}

func (w *OCIWatcher) client() *http.Client {
	if w.httpClient != nil {
		return w.httpClient
	}
	return newHTTPClient()
}

// registrySession holds the per-Watch connection details, including the
// anonymous Bearer token. It lives for one Watch call only.
type registrySession struct {
	client *http.Client
	base   *url.URL // scheme://host
	name   string
	token  string
}

// selectNewest picks the newest tag (see Watch) and returns it with its digest.
func (s *registrySession) selectNewest(ctx context.Context, tags []string) (string, string, error) {
	if len(tags) == 1 {
		digest, err := s.manifestDigest(ctx, tags[0])
		return tags[0], digest, err
	}
	if allSemver(tags) {
		tag := maxSemver(tags)
		digest, err := s.manifestDigest(ctx, tag)
		return tag, digest, err
	}
	if len(tags) > maxNewestBuildTags {
		return "", "", fmt.Errorf("%d tags match tagFilter and they are not all semantic versions; "+
			"newest-build ordering reads each image's build time and is limited to %d tags. "+
			"Narrow tagFilter, publish semantic version tags, or watch a single moving tag (for example tagFilter \"^main$\")",
			len(tags), maxNewestBuildTags)
	}
	return s.newestBuild(ctx, tags)
}

// newestBuild returns the tag whose image config has the latest "created" time.
// Equal build times (for example reproducible builds that pin created to the
// epoch) cannot be ordered and are an error.
func (s *registrySession) newestBuild(ctx context.Context, tags []string) (string, string, error) {
	var bestTag, bestDigest string
	var bestCreated time.Time
	tie := false
	for _, tag := range tags {
		digest, created, err := s.imageCreated(ctx, tag)
		if err != nil {
			return "", "", fmt.Errorf("read build time of tag %q: %w", tag, err)
		}
		switch {
		case bestTag == "" || created.After(bestCreated):
			bestTag, bestDigest, bestCreated, tie = tag, digest, created, false
		case created.Equal(bestCreated):
			tie = true
		}
	}
	if tie {
		return "", "", fmt.Errorf("several matching tags have the same build time %s, so the newest cannot be chosen; "+
			"publish semantic version tags or watch a single moving tag", bestCreated.Format(time.RFC3339))
	}
	return bestTag, bestDigest, nil
}

// listTags fetches every tag, following Link rel="next" pagination.
func (s *registrySession) listTags(ctx context.Context) ([]string, error) {
	next := s.base.JoinPath("v2", s.name, "tags", "list")
	var tags []string
	for page := 0; page < maxTagPages; page++ {
		body, header, err := s.get(ctx, next, "application/json")
		if err != nil {
			return nil, err
		}
		var result ociTagsListResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("decode tags/list response: %w", err)
		}
		tags = append(tags, result.Tags...)

		ref := nextLink(header.Get("Link"))
		if ref == "" {
			return tags, nil
		}
		u, err := next.Parse(ref)
		if err != nil {
			return nil, fmt.Errorf("parse tags/list pagination link: %w", err)
		}
		if u.Scheme != s.base.Scheme || u.Host != s.base.Host {
			return nil, fmt.Errorf("tags/list pagination link points to another host %q", u.Host)
		}
		next = u
	}
	return nil, fmt.Errorf("tags/list has more than %d pages", maxTagPages)
}

// manifestDigest returns the content digest of the tag's manifest (the index
// digest for a multi-arch tag). It tries HEAD first and falls back to GET when
// the registry does not send Docker-Content-Digest.
func (s *registrySession) manifestDigest(ctx context.Context, tag string) (string, error) {
	u := s.base.JoinPath("v2", s.name, "manifests", tag)
	resp, err := s.do(ctx, http.MethodHead, u, manifestAccept)
	if err != nil {
		return "", err
	}
	drainClose(resp)
	if digest := resp.Header.Get("Docker-Content-Digest"); digest != "" {
		return digest, nil
	}
	_, digest, err := s.getManifest(ctx, tag)
	return digest, err
}

// ociManifest is the subset of an image manifest or index the watcher reads.
type ociManifest struct {
	Config struct {
		Digest string `json:"digest"`
	} `json:"config"`
	Manifests []struct {
		Digest   string `json:"digest"`
		Platform *struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		} `json:"platform"`
	} `json:"manifests"`
}

// getManifest GETs a manifest by tag or digest and returns it with its digest.
func (s *registrySession) getManifest(ctx context.Context, ref string) (*ociManifest, string, error) {
	body, header, err := s.get(ctx, s.base.JoinPath("v2", s.name, "manifests", ref), manifestAccept)
	if err != nil {
		return nil, "", err
	}
	digest := header.Get("Docker-Content-Digest")
	if digest == "" {
		sum := sha256.Sum256(body)
		digest = "sha256:" + hex.EncodeToString(sum[:])
	}
	var m ociManifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, "", fmt.Errorf("decode manifest %s: %w", ref, err)
	}
	return &m, digest, nil
}

// imageCreated returns the tag's manifest digest and its image build time.
// For a multi-arch index the linux/amd64 image (or the first real image) is read.
func (s *registrySession) imageCreated(ctx context.Context, tag string) (string, time.Time, error) {
	m, digest, err := s.getManifest(ctx, tag)
	if err != nil {
		return "", time.Time{}, err
	}
	if len(m.Manifests) > 0 {
		child := pickPlatformManifest(m)
		if child == "" {
			return "", time.Time{}, fmt.Errorf("index %s lists no image manifest", digest)
		}
		if m, _, err = s.getManifest(ctx, child); err != nil {
			return "", time.Time{}, err
		}
	}
	if m.Config.Digest == "" {
		return "", time.Time{}, fmt.Errorf("manifest %s has no config", digest)
	}
	body, _, err := s.get(ctx, s.base.JoinPath("v2", s.name, "blobs", m.Config.Digest), "")
	if err != nil {
		return "", time.Time{}, err
	}
	var cfg struct {
		Created string `json:"created"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return "", time.Time{}, fmt.Errorf("decode image config: %w", err)
	}
	if cfg.Created == "" {
		return "", time.Time{}, fmt.Errorf("image config has no created time")
	}
	created, err := time.Parse(time.RFC3339Nano, cfg.Created)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("parse image created time %q: %w", cfg.Created, err)
	}
	return digest, created, nil
}

// pickPlatformManifest returns the digest of the linux/amd64 entry of an index,
// or of the first entry that is not an attestation (platform unknown/unknown).
func pickPlatformManifest(m *ociManifest) string {
	first := ""
	for _, e := range m.Manifests {
		if e.Platform != nil && e.Platform.OS == "unknown" {
			continue
		}
		if e.Platform != nil && e.Platform.OS == "linux" && e.Platform.Architecture == "amd64" {
			return e.Digest
		}
		if first == "" {
			first = e.Digest
		}
	}
	return first
}

// get performs a GET and returns the size-limited body and the response headers.
func (s *registrySession) get(ctx context.Context, u *url.URL, accept string) ([]byte, http.Header, error) {
	resp, err := s.do(ctx, http.MethodGet, u, accept)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close() //nolint:errcheck
	body, err := readLimited(resp.Body, maxResponseBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", u.Redacted(), err)
	}
	return body, resp.Header, nil
}

// do sends a request to the registry. On the first 401 with a Bearer challenge
// it fetches an anonymous token and retries once. The caller closes the body.
func (s *registrySession) do(ctx context.Context, method string, u *url.URL, accept string) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		req.Header.Set("User-Agent", userAgent)
		if s.token != "" {
			req.Header.Set("Authorization", "Bearer "+s.token)
		}
		resp, err := s.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("HTTP %s %s: %w", method, u.Redacted(), err)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 && s.token == "" {
			challenge := resp.Header.Get("WWW-Authenticate")
			drainClose(resp)
			if err := s.fetchAnonymousToken(ctx, challenge); err != nil {
				return nil, err
			}
			continue
		}
		switch resp.StatusCode {
		case http.StatusOK, http.StatusNoContent:
			return resp, nil
		case http.StatusNotFound:
			drainClose(resp)
			return nil, fmt.Errorf("not found: %s (HTTP 404)", u.Redacted())
		case http.StatusUnauthorized, http.StatusForbidden:
			drainClose(resp)
			return nil, fmt.Errorf("access denied to %s (HTTP %d); only public repositories are supported",
				u.Redacted(), resp.StatusCode)
		case http.StatusTooManyRequests:
			drainClose(resp)
			return nil, fmt.Errorf("registry rate limit reached for %s (HTTP 429)", u.Redacted())
		default:
			drainClose(resp)
			return nil, fmt.Errorf("unexpected HTTP %d from %s", resp.StatusCode, u.Redacted())
		}
	}
}

// fetchAnonymousToken runs the anonymous half of the Docker/OCI token flow:
// it GETs the challenge realm with service and scope, without credentials,
// and keeps the returned token for the rest of this Watch call.
// The token is never logged or included in an error.
func (s *registrySession) fetchAnonymousToken(ctx context.Context, challenge string) error {
	scheme, params := parseChallenge(challenge)
	switch strings.ToLower(scheme) {
	case "bearer":
	case "basic":
		return fmt.Errorf("registry %s requires credentials (HTTP 401, Basic auth); "+
			"Subscriptions only poll public repositories", s.base.Host)
	default:
		return fmt.Errorf("authentication required for %s (HTTP 401 without a Bearer challenge); "+
			"Subscriptions only poll public repositories", s.base.Host)
	}
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Host == "" {
		return fmt.Errorf("registry %s sent an invalid token realm", s.base.Host)
	}
	if realm.Scheme != "https" && (realm.Scheme != "http" || s.base.Scheme != "http") {
		return fmt.Errorf("registry %s sent a token realm that is not https", s.base.Host)
	}
	q := realm.Query()
	if service := params["service"]; service != "" {
		q.Set("service", service)
	}
	scope := params["scope"]
	if scope == "" {
		scope = "repository:" + s.name + ":pull"
	}
	q.Set("scope", scope)
	realm.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("anonymous token request to %s: %w", realm.Host, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("anonymous token request to %s returned HTTP %d; the repository may be private",
			realm.Host, resp.StatusCode)
	}
	body, err := readLimited(resp.Body, maxTokenBytes)
	if err != nil {
		return fmt.Errorf("read anonymous token from %s: %w", realm.Host, err)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return fmt.Errorf("decode anonymous token response from %s: invalid JSON", realm.Host)
	}
	s.token = tok.Token
	if s.token == "" {
		s.token = tok.AccessToken
	}
	if s.token == "" {
		return fmt.Errorf("anonymous token response from %s has no token", realm.Host)
	}
	return nil
}

// parseChallenge parses a WWW-Authenticate header such as
// `Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:a/b:pull"`.
// Quoted values may contain commas.
func parseChallenge(h string) (string, map[string]string) {
	h = strings.TrimSpace(h)
	scheme, rest, _ := strings.Cut(h, " ")
	params := map[string]string{}
	for rest = strings.TrimSpace(rest); rest != ""; {
		key, after, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		key = strings.ToLower(strings.TrimSpace(key))
		var val string
		if strings.HasPrefix(after, `"`) {
			end := strings.Index(after[1:], `"`)
			if end < 0 {
				val, rest = after[1:], ""
			} else {
				val, rest = after[1:end+1], after[end+2:]
			}
		} else {
			val, rest, _ = strings.Cut(after, ",")
			val = strings.TrimSpace(val)
			rest = "," + rest
		}
		params[key] = val
		rest = strings.TrimLeft(strings.TrimSpace(rest), ",")
		rest = strings.TrimSpace(rest)
	}
	return scheme, params
}

// nextLink returns the target of the rel="next" entry of a Link header, or "".
func nextLink(h string) string {
	for _, part := range strings.Split(h, ",") {
		target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok {
			continue
		}
		target = strings.TrimSpace(target)
		if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
			continue
		}
		for _, p := range strings.Split(params, ";") {
			k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
			if strings.EqualFold(k, "rel") && strings.Trim(v, `"`) == "next" {
				return target[1 : len(target)-1]
			}
		}
	}
	return ""
}

// parseRegistryRef splits a registry reference into the registry base URL
// (scheme://host) and the repository name, normalising Docker Hub.
// Examples:
//
//	"ghcr.io/myorg/myapp"          → (https://ghcr.io, "myorg/myapp")
//	"http://localhost:5000/myapp"  → (http://localhost:5000, "myapp")
//	"my.registry:5000/ns/app"      → (https://my.registry:5000, "ns/app")
//	"docker.io/library/nginx"      → (https://registry-1.docker.io, "library/nginx")
//	"nginx"                        → (https://registry-1.docker.io, "library/nginx")
//	"myorg/app"                    → (https://registry-1.docker.io, "myorg/app")
func parseRegistryRef(ref string) (*url.URL, string, error) {
	if ref == "" {
		return nil, "", fmt.Errorf("empty registry reference")
	}
	if strings.Contains(ref, "@") {
		// Not echoed: the reference may carry credentials.
		return nil, "", fmt.Errorf("registry reference must not include credentials or a digest")
	}
	scheme := "https"
	rest := ref
	if s, r, ok := strings.Cut(ref, "://"); ok {
		if s != "http" && s != "https" {
			return nil, "", fmt.Errorf("registry reference %q has unsupported scheme %q", ref, s)
		}
		scheme, rest = s, r
		if !strings.Contains(rest, "/") {
			return nil, "", fmt.Errorf("registry reference %q must include image name", ref)
		}
	}

	if rest == ref && !strings.Contains(rest, "/") && strings.ContainsAny(rest, ".:") {
		return nil, "", fmt.Errorf("registry reference %q must include image name (e.g. ghcr.io/myorg/myapp)", ref)
	}
	host, name := dockerHubRegistry, rest
	if first, remainder, ok := strings.Cut(rest, "/"); ok &&
		(scheme == "http" || rest != ref || strings.ContainsAny(first, ".:") || first == "localhost") {
		host, name = first, remainder
	}
	if host == "docker.io" || host == "index.docker.io" {
		host = dockerHubRegistry
	}
	if host == dockerHubRegistry && !strings.Contains(name, "/") {
		name = "library/" + name
	}
	name = strings.Trim(name, "/")
	if name == "" {
		return nil, "", fmt.Errorf("registry reference %q must include image name", ref)
	}
	if last := name[strings.LastIndex(name, "/")+1:]; strings.Contains(last, ":") {
		return nil, "", fmt.Errorf("registry reference %q must not include a tag; use tagFilter", ref)
	}
	return &url.URL{Scheme: scheme, Host: host}, name, nil
}

// filterTags returns tags that match filter. A nil filter matches every tag.
func filterTags(tags []string, filter *regexp.Regexp) []string {
	if filter == nil {
		return tags
	}
	result := make([]string, 0, len(tags))
	for _, t := range tags {
		if filter.MatchString(t) {
			result = append(result, t)
		}
	}
	return result
}

// allSemver reports whether every tag is a full semantic version.
func allSemver(tags []string) bool {
	for _, t := range tags {
		if !semverTag.MatchString(t) {
			return false
		}
	}
	return true
}

// maxSemver returns the highest semantic version. Versions that compare equal
// ("1.2.3" and "v1.2.3", or differing build metadata) are ordered by name so
// the result is deterministic.
func maxSemver(tags []string) string {
	sorted := append([]string(nil), tags...)
	sort.Slice(sorted, func(i, j int) bool {
		if c := semver.Compare(canonicalSemver(sorted[i]), canonicalSemver(sorted[j])); c != 0 {
			return c < 0
		}
		return sorted[i] < sorted[j]
	})
	return sorted[len(sorted)-1]
}

func canonicalSemver(tag string) string {
	if strings.HasPrefix(tag, "v") {
		return tag
	}
	return "v" + tag
}
