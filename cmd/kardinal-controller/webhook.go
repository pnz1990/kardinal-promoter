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

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

const (
	// maxWebhookBody is the maximum webhook payload size (1 MB).
	maxWebhookBody = 1 << 20
)

// webhookEventsTotal counts the number of webhook events processed since startup.
// Uses sync/atomic for lock-free increment; full Prometheus metrics are in Stage 19.
var webhookEventsTotal int64

// webhookServer is an HTTP server that handles incoming SCM webhook events.
type webhookServer struct {
	scm               scm.SCMProvider
	client            client.Client
	log               zerolog.Logger
	webhookConfigured bool
}

// newWebhookServer constructs a webhookServer with the given SCM provider and k8s client.
func newWebhookServer(scmProvider scm.SCMProvider, k8s client.Client, log zerolog.Logger) *webhookServer {
	return &webhookServer{
		scm:    scmProvider,
		client: k8s,
		log:    log,
	}
}

// newWebhookServerWithConfig constructs a webhookServer and records whether a webhook
// secret is configured (for the /webhook/scm/health response).
func newWebhookServerWithConfig(scmProvider scm.SCMProvider, k8s client.Client, log zerolog.Logger, webhookConfigured bool) *webhookServer {
	return &webhookServer{
		scm:               scmProvider,
		client:            k8s,
		log:               log,
		webhookConfigured: webhookConfigured,
	}
}

// Handler returns an http.HandlerFunc that handles SCM webhook events.
// Mount at POST /webhook/scm.
func (s *webhookServer) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
		if err != nil {
			s.log.Error().Err(err).Msg("failed to read webhook body")
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		signature := r.Header.Get("X-Hub-Signature-256")
		event, err := s.scm.ParseWebhookEvent(body, signature)
		if err != nil {
			s.log.Warn().Err(err).Msg("webhook signature invalid or parse error")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// Count every successfully parsed event.
		atomic.AddInt64(&webhookEventsTotal, 1)

		s.log.Info().
			Str("event_type", event.EventType).
			Str("action", event.Action).
			Bool("merged", event.Merged).
			Int("pr", event.PRNumber).
			Msg("webhook received")

		// Only act on merged pull_request events.
		if event.EventType != "pull_request" || event.Action != "closed" || !event.Merged {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		// Graph-purity: the webhook only writes PRStatus CRD status.
		// Business logic (advancing PromotionStep to HealthChecking) lives in the
		// PromotionStep reconciler, which watches PRStatus. This eliminates WH-1.
		if err := s.markPRStatusMerged(ctx, event); err != nil {
			s.log.Error().Err(err).Msg("failed to mark PRStatus as merged")
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

// HealthHandler returns an http.HandlerFunc for GET /webhook/scm/health.
// Responds with 200 OK and a JSON body indicating webhook configuration status
// and the number of webhook events processed since startup.
func (s *webhookServer) HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		resp := map[string]interface{}{
			"status":            "ok",
			"webhookConfigured": s.webhookConfigured,
			"eventsProcessed":   atomic.LoadInt64(&webhookEventsTotal),
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			s.log.Error().Err(err).Msg("failed to encode health response")
		}
	}
}

// markPRStatusMerged finds PRStatus CRDs matching the merged PR and sets
// status.merged = true. The PromotionStep reconciler will detect the change on
// its next reconcile and advance to HealthChecking.
//
// This is the pure version of the old reconcileMergedPR — the webhook now only
// writes to its own CRD (PRStatus) and does not touch PromotionStep status.
func (s *webhookServer) markPRStatusMerged(ctx context.Context, event scm.WebhookEvent) error {
	var prsList v1alpha1.PRStatusList
	if err := s.client.List(ctx, &prsList); err != nil {
		return fmt.Errorf("list prstatuses: %w", err)
	}

	now := metav1.NewTime(time.Now().UTC())
	for i := range prsList.Items {
		prs := &prsList.Items[i]

		// Match by PR number.
		if prs.Spec.PRNumber != event.PRNumber {
			continue
		}

		// Match by repo if available.
		if event.RepoFullName != "" && prs.Spec.Repo != "" && prs.Spec.Repo != event.RepoFullName {
			continue
		}

		// Already marked merged — idempotent skip.
		if prs.Status.Merged {
			continue
		}

		patch := client.MergeFrom(prs.DeepCopy())
		prs.Status.Merged = true
		prs.Status.Open = false
		prs.Status.LastCheckedAt = &now

		if patchErr := s.client.Status().Patch(ctx, prs, patch); patchErr != nil {
			s.log.Error().Err(patchErr).
				Str("prstatus", prs.Name).
				Msg("failed to mark PRStatus as merged via webhook")
			return fmt.Errorf("patch prstatus %s: %w", prs.Name, patchErr)
		}
		s.log.Info().
			Str("prstatus", prs.Name).
			Int("pr", event.PRNumber).
			Msg("PRStatus marked merged via webhook")
	}
	return nil
}

// extractPRNumberFromURL parses the PR number from a GitHub PR URL.
func extractPRNumberFromURL(prURL string) string {
	if prURL == "" {
		return ""
	}
	parts := strings.Split(strings.TrimRight(prURL, "/"), "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

// extractRepoFromURL extracts "owner/repo" from a GitHub PR URL.
func extractRepoFromURL(prURL string) string {
	s := strings.TrimPrefix(prURL, "https://")
	s = strings.TrimPrefix(s, "http://")
	idx := strings.Index(s, "/")
	if idx < 0 {
		return ""
	}
	s = s[idx+1:]
	parts := strings.SplitN(s, "/", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "/" + parts[1]
}
