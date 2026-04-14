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
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"path"
	"strings"
)

// GitWatcher watches a Git repository for new commits on a branch.
//
// It uses the Git Smart HTTP protocol (info/refs?service=git-upload-pack)
// to read the current HEAD of a branch without cloning. This works for
// GitHub, GitLab, Gitea/Forgejo, and any standard HTTPS git server.
//
// PathGlob filtering is not applied at the reference-discovery level:
// the watcher returns Changed=true on any new commit to the branch.
// Path-level filtering is a client-side concern handled by the Subscription
// reconciler when deciding whether to create a Bundle.
type GitWatcher struct {
	// RepoURL is the HTTPS Git repository URL (e.g. "https://github.com/myorg/myapp").
	RepoURL string
	// Branch is the branch to watch. Defaults to "main".
	Branch string
	// PathGlob is the optional file glob to filter commits.
	// Currently recorded in the Watcher for future use when go-git is available.
	// Today the watcher returns Changed=true on any new commit (#495 tracks go-git migration).
	PathGlob string
	// httpClient is the HTTP client used for requests. Defaults to http.DefaultClient.
	httpClient *http.Client
}

// NewGitWatcher creates a GitWatcher with the default HTTP client.
func NewGitWatcher(repoURL, branch, pathGlob string) *GitWatcher {
	b := branch
	if b == "" {
		b = "main"
	}
	return &GitWatcher{
		RepoURL:    repoURL,
		Branch:     b,
		PathGlob:   pathGlob,
		httpClient: http.DefaultClient,
	}
}

// Watch polls the Git repository for the latest commit SHA on the watched branch.
//
// Uses the Git Smart HTTP protocol endpoint:
//
//	GET <repoURL>/info/refs?service=git-upload-pack
//
// The response body contains pkt-line encoded reference advertisements.
// We parse the response to find the SHA for refs/heads/<branch>.
//
// Returns Changed=true when the latest SHA differs from lastDigest.
// First-run (lastDigest=="") returns Changed=false to avoid creating a Bundle
// on every Subscription creation at controller startup.
func (w *GitWatcher) Watch(ctx context.Context, lastDigest string) (*WatchResult, error) {
	if w.RepoURL == "" {
		return nil, fmt.Errorf("GitWatcher: repoURL must not be empty")
	}

	branch := w.Branch
	if branch == "" {
		branch = "main"
	}

	sha, err := w.fetchLatestSHA(ctx, branch)
	if err != nil {
		return nil, fmt.Errorf("GitWatcher: fetch latest SHA for %s@%s: %w", w.RepoURL, branch, err)
	}

	if sha == "" {
		// Branch not found in advertisement — return not-changed.
		return &WatchResult{Digest: lastDigest, Tag: "", Changed: false}, nil
	}

	// First-run (lastDigest=="") is not considered a change to avoid creating
	// a Bundle for every Subscription on controller startup.
	changed := lastDigest != "" && sha != lastDigest

	shortSHA := sha
	if len(shortSHA) > 7 {
		shortSHA = shortSHA[:7]
	}

	return &WatchResult{
		Digest:  sha,
		Tag:     shortSHA,
		Changed: changed,
	}, nil
}

// fetchLatestSHA fetches the current HEAD SHA for the given branch using
// the Git Smart HTTP protocol.
func (w *GitWatcher) fetchLatestSHA(ctx context.Context, branch string) (string, error) {
	refsURL := strings.TrimRight(w.RepoURL, "/") + "/info/refs?service=git-upload-pack"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, refsURL, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Git-Protocol", "version=2")
	req.Header.Set("User-Agent", "kardinal-promoter/subscription-watcher")

	httpClient := w.httpClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", refsURL, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	switch resp.StatusCode {
	case http.StatusNotFound:
		return "", fmt.Errorf("repository not found: %s (HTTP 404)", w.RepoURL)
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", fmt.Errorf("authentication required for %s (HTTP %d)", w.RepoURL, resp.StatusCode)
	case http.StatusOK:
		// continue
	default:
		return "", fmt.Errorf("unexpected HTTP %d from %s", resp.StatusCode, refsURL)
	}

	return parsePktLineRefs(bufio.NewReader(resp.Body), "refs/heads/"+branch)
}

// parsePktLineRefs parses the Git pkt-line format used in Smart HTTP responses
// and returns the SHA for the given refName. Returns "" if the ref is not found.
//
// Pkt-line format: each line is prefixed with a 4-hex-digit length (including
// the 4-byte prefix itself). A line starting with "0000" is a flush packet.
// The git Smart HTTP response has two sections separated by a flush packet:
//
//  1. Service announcement: "# service=git-upload-pack" + 0000 (flush)
//  2. Ref advertisement: each ref + 0000 (flush at end)
//
// Each ref line is: "<sha> <refname>[NUL capabilities]"
func parsePktLineRefs(r *bufio.Reader, refName string) (string, error) {
	flushCount := 0
	for {
		// Read 4-byte hex length prefix.
		lenHex := make([]byte, 4)
		if _, err := r.Read(lenHex); err != nil {
			break
		}
		lenBytes, err := hex.DecodeString(string(lenHex))
		if err != nil {
			// Not a valid pkt-line — may be plain text (older git servers).
			// Fall back to line-by-line parsing.
			return parsePlainRefs(r, refName, string(lenHex))
		}
		lineLen := int(lenBytes[0])<<8 | int(lenBytes[1])
		if lineLen == 0 {
			// Flush packet — end of current section.
			flushCount++
			if flushCount >= 2 {
				// Second flush ends the ref advertisement section.
				break
			}
			// First flush separates service announcement from refs — continue.
			continue
		}
		if lineLen < 4 {
			break
		}
		payload := make([]byte, lineLen-4)
		if _, err := r.Read(payload); err != nil {
			break
		}
		line := strings.TrimRight(string(payload), "\n\x00")
		// Strip capability advertisement (after NUL byte on the first ref line).
		if idx := strings.IndexByte(line, '\x00'); idx >= 0 {
			line = line[:idx]
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) == 2 && parts[1] == refName {
			return parts[0], nil
		}
	}
	return "", nil
}

// parsePlainRefs is a fallback parser for git servers that respond with
// plain text (non-pkt-line format). Line format: "<sha>\t<refname>"
func parsePlainRefs(r *bufio.Reader, refName, alreadyRead string) (string, error) {
	// Reconstruct the first line from alreadyRead + rest of current line.
	rest, _ := r.ReadString('\n')
	firstLine := alreadyRead + rest
	if sha := extractSHAFromLine(firstLine, refName); sha != "" {
		return sha, nil
	}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if sha := extractSHAFromLine(scanner.Text(), refName); sha != "" {
			return sha, nil
		}
	}
	return "", nil
}

// extractSHAFromLine extracts the SHA from a line if it references refName.
// Handles both "sha ref\n" (pkt-line) and "sha\tref" (plain text) formats.
func extractSHAFromLine(line, refName string) string {
	line = strings.TrimRight(line, "\n\r\x00")
	// Try space separator (pkt-line body).
	parts := strings.SplitN(line, " ", 2)
	if len(parts) == 2 && parts[1] == refName && looksLikeSHA(parts[0]) {
		return parts[0]
	}
	// Try tab separator (ls-remote plain output).
	parts = strings.SplitN(line, "\t", 2)
	if len(parts) == 2 && parts[1] == refName && looksLikeSHA(parts[0]) {
		return parts[0]
	}
	// Try trailing full ref match (some server variants include full path).
	if !strings.HasSuffix(line, "\t"+path.Base(refName)) {
		return ""
	}
	if len(line) < 40 {
		return ""
	}
	sha := line[:40]
	if looksLikeSHA(sha) {
		return sha
	}
	return ""
}

// looksLikeSHA returns true if s looks like a 40-character hex string (full SHA).
func looksLikeSHA(s string) bool {
	if len(s) < 40 {
		return false
	}
	for _, c := range s[:40] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
