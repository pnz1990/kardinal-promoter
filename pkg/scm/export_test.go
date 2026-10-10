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
	"net"
	"sync/atomic"
	"time"

	gogithttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// HTTPAuthUsernameForTest exposes the basic-auth username httpAuth picks, or
// "" when httpAuth sends no credentials.
func HTTPAuthUsernameForTest(remoteURL, token string) string {
	a, ok := httpAuth(remoteURL, token).(*gogithttp.BasicAuth)
	if !ok || a == nil {
		return ""
	}
	return a.Username
}

// HTTPClientTimeoutForTest returns the timeout of a provider's HTTP client.
func HTTPClientTimeoutForTest(p SCMProvider) time.Duration {
	switch v := p.(type) {
	case *GitHubProvider:
		return v.client.Timeout
	case *GitLabProvider:
		return v.client.Timeout
	case *ForgejoProvider:
		return v.client.Timeout
	case *BitbucketProvider:
		return v.client.Timeout
	case *AzureDevOpsProvider:
		return v.client.Timeout
	}
	return -1
}

// CheckAndReloadForTest runs one poll of the Secret watcher.
func (w *SecretWatcher) CheckAndReloadForTest(ctx context.Context) {
	log := w.Log.With().
		Str("secret", w.SecretNamespace+"/"+w.SecretName).
		Str("key", w.SecretKey).
		Logger()
	w.checkAndReload(ctx, log)
}

// SetAppTokenClockForTest replaces the clock of a GitHubAppTokenSource.
func SetAppTokenClockForTest(s *GitHubAppTokenSource, now func() time.Time) { s.now = now }

// SetBeforeFlightForTest runs f between Token's cache check and the mint
// flight. f may call SetAppTokenStateForTest to play a flight that ended in
// that window.
func SetBeforeFlightForTest(s *GitHubAppTokenSource, f func()) { s.beforeFlight = f }

// SetAppTokenStateForTest sets the cache as a finished flight leaves it: a
// token (with its refresh and expiry times), or a failure and its retry time.
func SetAppTokenStateForTest(s *GitHubAppTokenSource, token string, refreshAt, expires time.Time, lastErr error, retryAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token, s.refreshAt, s.expires, s.lastErr, s.retryAt = token, refreshAt, expires, lastErr, retryAt
}

// AuthMethodForTest exposes authMethod.
var AuthMethodForTest = authMethod

// SetSSHTimeoutsForTest replaces the ssh connect and receive-pack wait
// limits and returns a function that restores them.
func SetSSHTimeoutsForTest(dial, wait time.Duration) (restore func()) {
	oldDial, oldWait := sshDialTimeout, receivePackWait
	sshDialTimeout, receivePackWait = dial, wait
	return func() { sshDialTimeout, receivePackWait = oldDial, oldWait }
}

// SetGitIdleTimeoutForTest shortens the git connection idle bound.
func SetGitIdleTimeoutForTest(d time.Duration) (restore func()) {
	old := gitIdleTimeout
	gitIdleTimeout = d
	return func() { gitIdleTimeout = old }
}

// NewIdleConnForTest wraps c in the git idle bound.
func NewIdleConnForTest(c net.Conn, idle time.Duration) net.Conn { return newIdleConn(c, idle) }

// SetDialTCPForTest replaces the git TCP dial.
func SetDialTCPForTest(f func(ctx context.Context, network, addr string) (net.Conn, error)) (restore func()) {
	old := dialTCP
	dialTCP = f
	return func() { dialTCP = old }
}

// CountEvidenceRendersForTest counts the evidence renders of the PR template
// functions (the whole body, and each section) until restore is called.
func CountEvidenceRendersForTest() (counts func() (body, sections int), restore func()) {
	var b, sec atomic.Int32
	prevBody, prevSection := renderPRBodyFn, renderSectionFn
	renderPRBodyFn = func(body PRBody) (string, error) { b.Add(1); return prevBody(body) }
	renderSectionFn = func(name string, body PRBody) (string, error) { sec.Add(1); return prevSection(name, body) }
	return func() (int, int) { return int(b.Load()), int(sec.Load()) },
		func() { renderPRBodyFn, renderSectionFn = prevBody, prevSection }
}

// SetMaxRegistryClientsForTest bounds the client cache of the Registries
// created after it, and returns a function that restores it.
func SetMaxRegistryClientsForTest(n int) func() {
	prev := maxRegistryClients
	maxRegistryClients = n
	return func() { maxRegistryClients = prev }
}

// CacheSizesForTest returns the number of cached Secrets and Namespaces.
func (r *Registry) CacheSizesForTest() (secrets, namespaces int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.secrets), len(r.namespaces)
}

// NewerRVForTest exposes newerRV.
var NewerRVForTest = newerRV
