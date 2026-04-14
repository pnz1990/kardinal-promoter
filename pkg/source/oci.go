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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// OCIWatcher watches an OCI registry for new image tags.
// It uses the OCI Distribution Specification API (no external dependencies):
//   - GET /v2/<name>/tags/list to list tags
//   - HEAD /v2/<name>/manifests/<tag> to read the digest
//
// The watcher is safe for concurrent use.
type OCIWatcher struct {
	// Registry is the OCI image repository (e.g. "ghcr.io/myorg/myapp").
	// For a local/test registry include the scheme: "http://localhost:5000/myapp".
	Registry string
	// TagFilter is an optional regex that tags must match. When empty, all tags match.
	TagFilter string
	// httpClient is the HTTP client used for requests. Defaults to http.DefaultClient.
	// Set to a custom client for testing or for private registries requiring authentication.
	httpClient *http.Client
}

// NewOCIWatcher creates an OCIWatcher with the default HTTP client.
func NewOCIWatcher(registry, tagFilter string) *OCIWatcher {
	return &OCIWatcher{
		Registry:   registry,
		TagFilter:  tagFilter,
		httpClient: http.DefaultClient,
	}
}

// ociTagsListResponse is the JSON response from /v2/<name>/tags/list.
type ociTagsListResponse struct {
	Tags []string `json:"tags"`
}

// Watch polls the OCI registry for the latest tag matching TagFilter.
//
// Algorithm:
//  1. GET /v2/<name>/tags/list to list all tags
//  2. Filter tags by TagFilter regex (if set)
//  3. Sort tags lexicographically; pick the last (lexicographically "newest")
//  4. HEAD /v2/<name>/manifests/<tag> to get the Docker-Content-Digest header
//  5. Return Changed=true if the digest differs from lastDigest
//
// First-run (lastDigest=="") returns Changed=false to avoid creating a Bundle
// on every Subscription creation at controller startup.
func (w *OCIWatcher) Watch(ctx context.Context, lastDigest string) (*WatchResult, error) {
	if w.Registry == "" {
		return nil, fmt.Errorf("OCIWatcher: registry must not be empty")
	}

	host, name, err := parseRegistryRef(w.Registry)
	if err != nil {
		return nil, fmt.Errorf("OCIWatcher: parse registry ref %q: %w", w.Registry, err)
	}

	// List all tags.
	tags, err := w.listTags(ctx, host, name)
	if err != nil {
		return nil, fmt.Errorf("OCIWatcher: list tags for %q: %w", w.Registry, err)
	}

	// Filter by TagFilter regex.
	filtered, err := filterTags(tags, w.TagFilter)
	if err != nil {
		return nil, fmt.Errorf("OCIWatcher: filter tags: %w", err)
	}
	if len(filtered) == 0 {
		return &WatchResult{Digest: lastDigest, Tag: "", Changed: false}, nil
	}

	// Sort lexicographically descending: pick "newest" tag.
	sort.Strings(filtered)
	newestTag := filtered[len(filtered)-1]

	// HEAD the manifest to get the digest.
	digest, err := w.headManifest(ctx, host, name, newestTag)
	if err != nil {
		return nil, fmt.Errorf("OCIWatcher: head manifest for %q:%q: %w", w.Registry, newestTag, err)
	}

	// First-run is not considered a change.
	changed := lastDigest != "" && digest != lastDigest

	return &WatchResult{
		Digest:  digest,
		Tag:     newestTag,
		Changed: changed,
	}, nil
}

// listTags fetches the tag list from the OCI Distribution API.
func (w *OCIWatcher) listTags(ctx context.Context, host, name string) ([]string, error) {
	url := host + "/v2/" + name + "/tags/list"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := w.doRequest(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var result ociTagsListResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode tags/list response: %w", err)
	}
	return result.Tags, nil
}

// headManifest fetches the content digest of the given tag.
func (w *OCIWatcher) headManifest(ctx context.Context, host, name, tag string) (string, error) {
	url := host + "/v2/" + name + "/manifests/" + tag
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	// Accept both OCI and Docker manifest schemas for broad registry compatibility.
	req.Header.Set("Accept",
		"application/vnd.docker.distribution.manifest.v2+json,"+
			"application/vnd.oci.image.manifest.v1+json")

	resp, err := w.doRequest(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close() //nolint:errcheck

	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		// Some registries use OCI header name.
		digest = resp.Header.Get("Content-Digest")
	}
	if digest == "" {
		return "", fmt.Errorf("registry did not return a content digest for %s:%s", name, tag)
	}
	return digest, nil
}

// doRequest executes an HTTP request and validates the status code.
func (w *OCIWatcher) doRequest(req *http.Request) (*http.Response, error) {
	client := w.httpClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP %s %s: %w", req.Method, req.URL, err)
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return resp, nil
	case http.StatusNotFound:
		resp.Body.Close() //nolint:errcheck
		return nil, fmt.Errorf("not found: %s (HTTP 404)", req.URL)
	case http.StatusUnauthorized, http.StatusForbidden:
		resp.Body.Close() //nolint:errcheck
		return nil, fmt.Errorf("authentication required for %s (HTTP %d)", req.URL, resp.StatusCode)
	default:
		resp.Body.Close() //nolint:errcheck
		return nil, fmt.Errorf("unexpected HTTP %d from %s", resp.StatusCode, req.URL)
	}
}

// parseRegistryRef splits a registry reference into (scheme+host, name).
// Examples:
//
//	"ghcr.io/myorg/myapp"          → ("https://ghcr.io", "myorg/myapp")
//	"http://localhost:5000/myapp"   → ("http://localhost:5000", "myapp")
//	"my.registry:5000/ns/app"      → ("https://my.registry:5000", "ns/app")
func parseRegistryRef(ref string) (host, imageName string, err error) {
	if ref == "" {
		return "", "", fmt.Errorf("empty registry reference")
	}

	// If ref has an explicit scheme, use it as-is.
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		// Find the first "/" after the scheme.
		withoutScheme := ref[strings.Index(ref, "//")+2:]
		slashIdx := strings.Index(withoutScheme, "/")
		if slashIdx < 0 {
			return "", "", fmt.Errorf("registry reference %q must include image name", ref)
		}
		scheme := ref[:strings.Index(ref, "//")+2]
		return scheme + withoutScheme[:slashIdx], withoutScheme[slashIdx+1:], nil
	}

	// No scheme — assume HTTPS.
	slashIdx := strings.Index(ref, "/")
	if slashIdx < 0 {
		return "", "", fmt.Errorf("registry reference %q must include image name (e.g. ghcr.io/myorg/myapp)", ref)
	}
	return "https://" + ref[:slashIdx], ref[slashIdx+1:], nil
}

// filterTags returns tags that match the given regex pattern.
// If pattern is empty, all tags are returned.
func filterTags(tags []string, pattern string) ([]string, error) {
	if pattern == "" {
		return tags, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid tagFilter regex %q: %w", pattern, err)
	}
	result := make([]string, 0, len(tags))
	for _, t := range tags {
		if re.MatchString(t) {
			result = append(result, t)
		}
	}
	return result, nil
}
