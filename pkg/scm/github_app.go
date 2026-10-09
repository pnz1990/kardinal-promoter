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
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The keys of a Secret that holds GitHub App credentials instead of a token,
// the names Argo CD uses for its repository Secrets.
const (
	SecretKeyGitHubAppID             = "githubAppID"
	SecretKeyGitHubAppInstallationID = "githubAppInstallationID"
	SecretKeyGitHubAppPrivateKey     = "githubAppPrivateKey"
)

// appJWTLifetime is how long the JWT that asks for an installation token is
// valid. GitHub accepts at most 10 minutes.
const appJWTLifetime = 9 * time.Minute

// appJWTBackdate backdates the JWT's iat to allow for clock drift, as GitHub
// recommends.
const appJWTBackdate = 60 * time.Second

// appTokenRefreshBefore is how long before its expiry an installation token
// is replaced. GitHub issues tokens valid for one hour; a token is never sent
// with less than this left, so a request that is slow to arrive or a git push
// that runs for minutes does not see it expire.
const appTokenRefreshBefore = 10 * time.Minute

// GitHubAppCredentials authenticate as a GitHub App installation.
type GitHubAppCredentials struct {
	// AppID is the App's ID (Settings > Developer settings > GitHub Apps).
	AppID int64
	// InstallationID is the ID of the App's installation on the account
	// that owns the GitOps repositories.
	InstallationID int64
	// PrivateKey is the App's private key, PEM (PKCS#1 as GitHub downloads
	// it, or PKCS#8).
	PrivateKey []byte
}

// GitHubAppCredentialsFromData reads GitHub App credentials from the data of
// a Secret: githubAppID, githubAppInstallationID and githubAppPrivateKey.
// ok is false when the data has no githubAppPrivateKey, which means the
// Secret holds a token. An error names the key that is missing or wrong,
// never a value.
func GitHubAppCredentialsFromData(data map[string][]byte) (creds GitHubAppCredentials, ok bool, err error) {
	key := data[SecretKeyGitHubAppPrivateKey]
	if len(strings.TrimSpace(string(key))) == 0 {
		return GitHubAppCredentials{}, false, nil
	}
	id := func(k string) (int64, error) {
		raw := strings.TrimSpace(string(data[k]))
		if raw == "" {
			return 0, fmt.Errorf("%s is set but %s is missing", SecretKeyGitHubAppPrivateKey, k)
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("%s must be a positive number", k)
		}
		return n, nil
	}
	creds.PrivateKey = key
	if creds.AppID, err = id(SecretKeyGitHubAppID); err != nil {
		return GitHubAppCredentials{}, true, err
	}
	if creds.InstallationID, err = id(SecretKeyGitHubAppInstallationID); err != nil {
		return GitHubAppCredentials{}, true, err
	}
	return creds, true, nil
}

// Fingerprint identifies the credentials without revealing the key: the App
// and installation IDs and a hash of the key. A SecretWatcher compares
// fingerprints to see a rotation.
func (c GitHubAppCredentials) Fingerprint() string {
	sum := sha256.Sum256(c.PrivateKey)
	return fmt.Sprintf("app:%d:%d:%s", c.AppID, c.InstallationID, hex.EncodeToString(sum[:8]))
}

// parseAppPrivateKey parses a PEM RSA private key, PKCS#1 or PKCS#8.
func parseAppPrivateKey(raw []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("GitHub App private key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("GitHub App private key is not an RSA key in PKCS#1 or PKCS#8 form")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("GitHub App private key is not an RSA key")
	}
	return rk, nil
}

// TokenSource returns the token to send with an SCM API request or a git
// operation. Implementations are safe for concurrent use.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// GitHubAppTokenSource mints GitHub App installation tokens and caches each
// until appTokenRefreshBefore before it expires. Concurrent callers share one
// mint.
type GitHubAppTokenSource struct {
	appID          int64
	installationID int64
	key            *rsa.PrivateKey
	apiURL         string
	client         *http.Client
	// now is the clock; tests replace it.
	now func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
	// refreshAt is when the cached token is replaced: appTokenRefreshBefore
	// before it expires, or for a token that lives less than twice that,
	// a tenth of its life before.
	refreshAt time.Time
	// After a failed mint, lastErr is returned without asking GitHub until
	// retryAt; failures doubles the wait each time, up to appMintBackoffMax.
	lastErr  error
	retryAt  time.Time
	failures int
}

// The backoff after a failed mint: appMintBackoff, doubled per failure, at
// most appMintBackoffMax.
const (
	appMintBackoff    = 5 * time.Second
	appMintBackoffMax = 5 * time.Minute
)

// NewGitHubAppTokenSource returns a token source for creds against the
// GitHub API at apiURL ("" is https://api.github.com; GitHub Enterprise
// Server is https://<host>/api/v3).
func NewGitHubAppTokenSource(creds GitHubAppCredentials, apiURL string) (*GitHubAppTokenSource, error) {
	key, err := parseAppPrivateKey(creds.PrivateKey)
	if err != nil {
		return nil, err
	}
	if creds.AppID <= 0 || creds.InstallationID <= 0 {
		return nil, errors.New("GitHub App ID and installation ID must be positive")
	}
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	return &GitHubAppTokenSource{
		appID: creds.AppID, installationID: creds.InstallationID, key: key,
		apiURL: strings.TrimRight(apiURL, "/"),
		client: &http.Client{Timeout: providerHTTPTimeout},
		now:    time.Now,
	}, nil
}

// Token returns a cached installation token, or mints a new one when the
// cached one expires within appTokenRefreshBefore.
func (s *GitHubAppTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.token != "" && now.Before(s.refreshAt) {
		return s.token, nil
	}
	if s.lastErr != nil && now.Before(s.retryAt) {
		if s.token != "" && now.Before(s.expires) {
			// The cached token is still valid: use it while minting fails.
			return s.token, nil
		}
		return "", fmt.Errorf("%w (not retried before %s)", s.lastErr, s.retryAt.UTC().Format(time.RFC3339))
	}
	tok, exp, err := s.mint(ctx)
	if err != nil {
		s.failures++
		s.lastErr = err
		s.retryAt = now.Add(min(appMintBackoff<<min(s.failures-1, 10), appMintBackoffMax))
		if s.token != "" && now.Before(s.expires) {
			return s.token, nil
		}
		return "", err
	}
	s.failures, s.lastErr = 0, nil
	margin := appTokenRefreshBefore
	if life := exp.Sub(now); life < 2*margin {
		margin = life / 10
	}
	s.token, s.expires, s.refreshAt = tok, exp, exp.Add(-margin)
	return tok, nil
}

// appJWT returns the RS256 JWT that authenticates as the App.
func (s *GitHubAppTokenSource) appJWT() (string, error) {
	now := s.now()
	enc := base64.RawURLEncoding
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", fmt.Errorf("encode JWT header: %w", err)
	}
	claims, err := json.Marshal(map[string]interface{}{
		"iat": now.Add(-appJWTBackdate).Unix(),
		"exp": now.Add(appJWTLifetime).Unix(),
		"iss": strconv.FormatInt(s.appID, 10),
	})
	if err != nil {
		return "", fmt.Errorf("encode JWT claims: %w", err)
	}
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign GitHub App JWT: %w", err)
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// mint asks GitHub for an installation token.
func (s *GitHubAppTokenSource) mint(ctx context.Context) (string, time.Time, error) {
	jwt, err := s.appJWT()
	if err != nil {
		return "", time.Time{}, err
	}
	path := fmt.Sprintf("/app/installations/%d/access_tokens", s.installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL+path, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("create installation token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("mint GitHub App installation token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", time.Time{}, fmt.Errorf("mint GitHub App installation token: %w", newAPIError("GitHub", http.MethodPost, path, resp, raw))
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", time.Time{}, fmt.Errorf("decode installation token response: %w", err)
	}
	if out.Token == "" {
		return "", time.Time{}, errors.New("mint GitHub App installation token: the response has no token")
	}
	if out.ExpiresAt.IsZero() {
		// GitHub always sends it; without it assume the documented hour.
		out.ExpiresAt = s.now().Add(time.Hour)
	}
	return out.Token, out.ExpiresAt, nil
}

// AppTokenCache keeps one GitHubAppTokenSource per set of credentials, so
// the git credentials of many Pipelines that name the same App share their
// installation tokens. It is safe for concurrent use.
type AppTokenCache struct {
	// APIURL is the GitHub API the tokens are minted at ("" is
	// https://api.github.com).
	APIURL string

	mu      sync.Mutex
	sources map[string]*GitHubAppTokenSource
}

// maxAppTokenSources bounds the cache: past it, the cache is emptied and
// rebuilt as Pipelines ask, so a stream of rotated keys cannot grow it.
const maxAppTokenSources = 256

// Token returns an installation token for creds.
func (c *AppTokenCache) Token(ctx context.Context, creds GitHubAppCredentials) (string, error) {
	fp := creds.Fingerprint()
	c.mu.Lock()
	src, ok := c.sources[fp]
	if !ok {
		var err error
		src, err = NewGitHubAppTokenSource(creds, c.APIURL)
		if err != nil {
			c.mu.Unlock()
			return "", err
		}
		if c.sources == nil || len(c.sources) >= maxAppTokenSources {
			c.sources = map[string]*GitHubAppTokenSource{}
		}
		c.sources[fp] = src
	}
	c.mu.Unlock()
	return src.Token(ctx)
}
