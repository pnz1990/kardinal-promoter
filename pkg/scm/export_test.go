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
