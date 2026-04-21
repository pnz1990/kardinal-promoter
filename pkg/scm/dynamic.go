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
	"fmt"
	"sync/atomic"
)

// DynamicProvider wraps an SCMProvider and supports zero-downtime credential
// rotation. All SCMProvider method calls are delegated to the current inner
// provider. Calling Reload atomically swaps the inner provider so that
// subsequent calls use the new credentials without restarting the controller.
//
// DynamicProvider is safe for concurrent use: multiple reconciler goroutines
// may call SCMProvider methods while a Secret watcher calls Reload.
type DynamicProvider struct {
	// inner holds the current SCMProvider. atomic.Pointer guarantees that
	// concurrent Load and Store calls see a consistent snapshot without a mutex.
	inner atomic.Pointer[SCMProvider]

	// providerType, apiURL, and webhookSecret are fixed at construction time
	// (they change only on Helm upgrade, not on Secret rotation).
	providerType  string
	apiURL        string
	webhookSecret string
}

// NewDynamicProvider creates a DynamicProvider initialised with the given token.
// providerType, apiURL, and webhookSecret follow the same semantics as NewProvider.
func NewDynamicProvider(providerType, token, apiURL, webhookSecret string) (*DynamicProvider, error) {
	d := &DynamicProvider{
		providerType:  providerType,
		apiURL:        apiURL,
		webhookSecret: webhookSecret,
	}
	if err := d.reload(token); err != nil {
		return nil, fmt.Errorf("initialising dynamic SCM provider: %w", err)
	}
	return d, nil
}

// Reload constructs a new inner SCMProvider with the given token and atomically
// replaces the current provider. Subsequent calls to SCMProvider methods will
// use the new credentials. Reload is safe to call from multiple goroutines;
// only one new provider is created per call.
//
// Reload is a no-op if token is empty.
func (d *DynamicProvider) Reload(token string) error {
	if token == "" {
		return nil
	}
	return d.reload(token)
}

func (d *DynamicProvider) reload(token string) error {
	p, err := NewProvider(d.providerType, token, d.apiURL, d.webhookSecret)
	if err != nil {
		return fmt.Errorf("creating SCM provider during reload: %w", err)
	}
	d.inner.Store(&p)
	return nil
}

// current returns the active inner SCMProvider. It panics if no provider has
// been initialised — this should never happen after NewDynamicProvider.
func (d *DynamicProvider) current() SCMProvider {
	p := d.inner.Load()
	if p == nil {
		panic("scm: DynamicProvider used before initialisation")
	}
	return *p
}

// OpenPR implements SCMProvider.
func (d *DynamicProvider) OpenPR(ctx context.Context, repo, title, body, head, base string) (string, int, error) {
	return d.current().OpenPR(ctx, repo, title, body, head, base)
}

// ClosePR implements SCMProvider.
func (d *DynamicProvider) ClosePR(ctx context.Context, repo string, prNumber int) error {
	return d.current().ClosePR(ctx, repo, prNumber)
}

// CommentOnPR implements SCMProvider.
func (d *DynamicProvider) CommentOnPR(ctx context.Context, repo string, prNumber int, body string) error {
	return d.current().CommentOnPR(ctx, repo, prNumber, body)
}

// GetPRStatus implements SCMProvider.
func (d *DynamicProvider) GetPRStatus(ctx context.Context, repo string, prNumber int) (bool, bool, error) {
	return d.current().GetPRStatus(ctx, repo, prNumber)
}

// GetPRReviewStatus implements SCMProvider.
func (d *DynamicProvider) GetPRReviewStatus(ctx context.Context, repo string, prNumber int) (bool, int, error) {
	return d.current().GetPRReviewStatus(ctx, repo, prNumber)
}

// ParseWebhookEvent implements SCMProvider.
func (d *DynamicProvider) ParseWebhookEvent(payload []byte, signature string) (WebhookEvent, error) {
	return d.current().ParseWebhookEvent(payload, signature)
}

// AddLabelsToPR implements SCMProvider.
func (d *DynamicProvider) AddLabelsToPR(ctx context.Context, repo string, prNumber int, labels []string) error {
	return d.current().AddLabelsToPR(ctx, repo, prNumber, labels)
}
