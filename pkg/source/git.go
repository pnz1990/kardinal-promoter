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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// GitWatcher watches a Git repository for new commits on a branch.
//
// Over HTTP(S) it uses the Git Smart HTTP protocol v0 reference advertisement
// (GET info/refs?service=git-upload-pack) to read the current HEAD of a branch
// without cloning. This works for GitHub, GitLab, Gitea/Forgejo, and any
// standard HTTPS git server. Over SSH (ssh://user@host/path or user@host:path)
// it reads the same advertisement from git-upload-pack. Credentials come from
// Credentials: a token or username and password for HTTP(S), a private key
// and known_hosts for SSH.
//
// With PathGlob set, a poll that sees a new head fetches the branch's last
// DiscoveryLimit commits (shallow, without file contents when the server
// supports partial clone) and reports the newest commit that changed a
// matching path.
type GitWatcher struct {
	// RepoURL is the Git repository URL (e.g. "https://github.com/myorg/myapp",
	// "ssh://git@github.com/myorg/myapp.git", "git@github.com:myorg/myapp.git").
	RepoURL string
	// Branch is the branch to watch. Defaults to "main".
	Branch string
	// PathGlob is spec.git.pathGlob: only commits that change a matching path
	// count. Empty means every commit.
	PathGlob string
	// DiscoveryLimit is the most commits a pathGlob poll reads (default 20).
	DiscoveryLimit int
	// LastRevision is status.lastSeenRevision: the head the previous pathGlob
	// poll read up to. The walk stops there.
	LastRevision string
	// Credentials authenticate to the repository; the zero value is anonymous.
	Credentials Credentials
	// httpClient is the HTTP client used for requests. NewGitWatcher sets a
	// client with a timeout and the egress guard.
	httpClient *http.Client
}

// NewGitWatcher creates a GitWatcher with an HTTP client that times out and
// refuses loopback, link-local and cloud metadata addresses (pkg/egress).
func NewGitWatcher(repoURL, branch, pathGlob string) *GitWatcher {
	b := branch
	if b == "" {
		b = "main"
	}
	return &GitWatcher{
		RepoURL:    repoURL,
		Branch:     b,
		PathGlob:   pathGlob,
		httpClient: newHTTPClient(),
	}
}

// WithHTTPClient makes w send its requests with c instead of the
// egress-guarded default, and returns w. It is for tests: an httptest server
// listens on loopback, which the guard refuses. The controller keeps the
// default.
func (w *GitWatcher) WithHTTPClient(c *http.Client) *GitWatcher {
	w.httpClient = c
	return w
}

// Watch polls the Git repository for the latest commit SHA on the watched
// branch (with PathGlob, the latest commit that changed a matching path).
//
// Over HTTP(S) it uses the Git Smart HTTP protocol endpoint:
//
//	GET <repoURL>/info/refs?service=git-upload-pack
//
// The response body contains pkt-line encoded reference advertisements.
// We parse the response to find the SHA for refs/heads/<branch>. A branch that
// is not advertised is an error.
//
// Returns Changed=true when the latest SHA differs from lastDigest.
// First-run (lastDigest=="") returns Changed=false to avoid creating a Bundle
// on every Subscription creation at controller startup. The caller records the
// returned Digest as the baseline.
func (w *GitWatcher) Watch(ctx context.Context, lastDigest string) (*WatchResult, error) {
	if w.RepoURL == "" {
		return nil, fmt.Errorf("GitWatcher: repoURL must not be empty")
	}
	if w.PathGlob != "" && !doublestar.ValidatePattern(w.PathGlob) {
		return nil, fmt.Errorf("GitWatcher: pathGlob %q is not a valid glob", w.PathGlob)
	}

	branch := w.Branch
	if branch == "" {
		branch = "main"
	}

	var sha string
	var err error
	if isSSHURL(w.RepoURL) {
		sha, err = w.sshHead(ctx, branch)
	} else {
		sha, err = w.fetchLatestSHA(ctx, branch)
	}
	if err != nil {
		return nil, fmt.Errorf("GitWatcher: fetch latest SHA for %s@%s: %w", redactURL(w.RepoURL), branch, err)
	}
	if w.PathGlob != "" {
		res, err := w.watchPath(ctx, branch, sha, lastDigest)
		if err != nil {
			return nil, fmt.Errorf("GitWatcher: pathGlob %q on %s@%s: %w", w.PathGlob, redactURL(w.RepoURL), branch, err)
		}
		return res, nil
	}

	// First-run (lastDigest=="") is not considered a change to avoid creating
	// a Bundle for every Subscription on controller startup.
	changed := lastDigest != "" && sha != lastDigest

	return &WatchResult{
		Digest:  sha,
		Tag:     shortSHA(sha),
		Changed: changed,
	}, nil
}

// shortSHA returns the first 7 characters of a commit SHA.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// setHTTPAuth adds the HTTP(S) credentials to req: a token as the password
// of user "git" (or Credentials.Username), or a username and password.
func (w *GitWatcher) setHTTPAuth(req *http.Request) {
	if user, pass, ok := w.httpBasic(); ok {
		req.SetBasicAuth(user, pass)
	}
}

// httpBasic returns the HTTP(S) basic credentials, if any.
func (w *GitWatcher) httpBasic() (string, string, bool) {
	c := w.Credentials
	switch {
	case c.Token != "":
		user := c.Username
		if user == "" {
			user = "git"
		}
		return user, c.Token, true
	case c.Username != "" || c.Password != "":
		return c.Username, c.Password, true
	}
	return "", "", false
}

// fetchLatestSHA fetches the current HEAD SHA for the given branch using
// the Git Smart HTTP protocol.
//
// It deliberately sends no "Git-Protocol: version=2" header: with it, servers
// answer with a v2 capability advertisement that lists no refs.
func (w *GitWatcher) fetchLatestSHA(ctx context.Context, branch string) (string, error) {
	refsURL := strings.TrimRight(w.RepoURL, "/") + "/info/refs?service=git-upload-pack"
	repo := redactURL(w.RepoURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, refsURL, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	w.setHTTPAuth(req)

	httpClient := w.httpClient
	if httpClient == nil {
		httpClient = newHTTPClient()
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET info/refs: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	switch resp.StatusCode {
	case http.StatusNotFound:
		return "", fmt.Errorf("repository not found: %s (HTTP 404)", repo)
	case http.StatusUnauthorized, http.StatusForbidden:
		if _, _, ok := w.httpBasic(); ok {
			return "", fmt.Errorf("access denied to %s (HTTP %d) with the credentials from secretRef; "+
				"check that the token can read the repository", repo, resp.StatusCode)
		}
		return "", fmt.Errorf("authentication required for %s (HTTP %d); the repository may be private: "+
			"set secretRef to a Secret with a token for it", repo, resp.StatusCode)
	case http.StatusOK:
		// continue
	default:
		return "", fmt.Errorf("unexpected HTTP %d from %s", resp.StatusCode, repo)
	}

	refName := "refs/heads/" + branch
	sha, refs, err := parsePktLineRefs(bufio.NewReader(io.LimitReader(resp.Body, maxRefsBytes)), refName)
	if err != nil {
		return "", fmt.Errorf("parse ref advertisement: %w", err)
	}
	if sha != "" {
		return sha, nil
	}
	if refs == 0 {
		return "", fmt.Errorf("repository %s advertises no refs (empty repository or not a git smart HTTP endpoint)", repo)
	}
	return "", fmt.Errorf("branch %q not found in %s (%d refs advertised)", branch, repo, refs)
}

// redactURL returns raw with any password replaced, for errors and status
// messages. A URL that does not parse is not echoed.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid URL>"
	}
	return u.Redacted()
}

// parsePktLineRefs parses the Git pkt-line format used in Smart HTTP responses
// and returns the SHA for the given refName ("" if the ref is not found) and
// the number of refs seen.
//
// Pkt-line format: each line is prefixed with a 4-hex-digit length (including
// the 4-byte prefix itself). A line starting with "0000" is a flush packet.
// The git Smart HTTP response has two sections separated by a flush packet:
//
//  1. Service announcement: "# service=git-upload-pack" + 0000 (flush)
//  2. Ref advertisement: each ref + 0000 (flush at end)
//
// Each ref line is: "<sha> <refname>[NUL capabilities]"
//
// Reads use io.ReadFull, so a body delivered in small chunks parses the same as
// one written at once. A truncated packet is an error; EOF at a packet
// boundary ends the advertisement.
func parsePktLineRefs(r *bufio.Reader, refName string) (string, int, error) {
	flushCount := 0
	refs := 0
	lenHex := make([]byte, 4)
	for {
		if _, err := io.ReadFull(r, lenHex); err != nil {
			if errors.Is(err, io.EOF) {
				return "", refs, nil
			}
			return "", refs, fmt.Errorf("read pkt-line length: %w", err)
		}
		lenBytes, err := hex.DecodeString(string(lenHex))
		if err != nil {
			if refs == 0 && flushCount == 0 {
				// Not a pkt-line stream — may be plain text (dumb HTTP servers).
				sha, n := parsePlainRefs(r, refName, string(lenHex))
				return sha, n, nil
			}
			return "", refs, fmt.Errorf("invalid pkt-line length %q", lenHex)
		}
		lineLen := int(lenBytes[0])<<8 | int(lenBytes[1])
		if lineLen == 0 {
			// Flush packet — end of current section.
			flushCount++
			if flushCount >= 2 {
				// Second flush ends the ref advertisement section.
				return "", refs, nil
			}
			// First flush separates service announcement from refs — continue.
			continue
		}
		if lineLen < 4 {
			return "", refs, fmt.Errorf("invalid pkt-line length %d", lineLen)
		}
		payload := make([]byte, lineLen-4)
		if _, err := io.ReadFull(r, payload); err != nil {
			return "", refs, fmt.Errorf("read pkt-line payload: %w", err)
		}
		line := strings.TrimRight(string(payload), "\n\x00")
		// Strip capability advertisement (after NUL byte on the first ref line).
		if idx := strings.IndexByte(line, '\x00'); idx >= 0 {
			line = line[:idx]
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 || !looksLikeSHA(parts[0]) {
			continue
		}
		refs++
		if parts[1] == refName {
			return parts[0], refs, nil
		}
	}
}

// parsePlainRefs is a fallback parser for git servers that respond with
// plain text (non-pkt-line format). Line format: "<sha>\t<refname>".
// It returns the SHA for refName ("" if absent) and the number of lines read.
func parsePlainRefs(r *bufio.Reader, refName, alreadyRead string) (string, int) {
	// Reconstruct the first line from alreadyRead + rest of current line.
	rest, _ := r.ReadString('\n')
	firstLine := alreadyRead + rest
	lines := 1
	if sha := extractSHAFromLine(firstLine, refName); sha != "" {
		return sha, lines
	}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		lines++
		if sha := extractSHAFromLine(scanner.Text(), refName); sha != "" {
			return sha, lines
		}
	}
	return "", lines
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
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
