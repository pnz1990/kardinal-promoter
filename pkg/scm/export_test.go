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
