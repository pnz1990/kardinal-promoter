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
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
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

		if err := s.reconcileMergedPR(ctx, event); err != nil {
			s.log.Error().Err(err).Msg("failed to reconcile merged PR")
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

// reconcileMergedPR finds PromotionSteps waiting on the merged PR and advances their status.
func (s *webhookServer) reconcileMergedPR(ctx context.Context, event scm.WebhookEvent) error {
	var psList v1alpha1.PromotionStepList
	// List all PromotionSteps in WaitingForMerge across all namespaces.
	if err := s.client.List(ctx, &psList); err != nil {
		return fmt.Errorf("list promotionsteps: %w", err)
	}

	for i := range psList.Items {
		ps := &psList.Items[i]
		if ps.Status.State != "WaitingForMerge" {
			continue
		}
		// Match by PR number and repo.
		prNumStr, ok := ps.Status.Outputs["prNumber"]
		if !ok {
			prNumStr = extractPRNumberFromURL(ps.Status.PRURL)
		}
		if prNumStr == "" {
			continue
		}
		prNum, err := strconv.Atoi(prNumStr)
		if err != nil || prNum != event.PRNumber {
			continue
		}
		// Check repo match if available.
		if event.RepoFullName != "" {
			prRepo := extractRepoFromURL(ps.Status.PRURL)
			if prRepo != "" && prRepo != event.RepoFullName {
				continue
			}
		}

		// Advance the PromotionStep to HealthChecking.
		patch := client.MergeFrom(ps.DeepCopy())
		ps.Status.State = "HealthChecking"
		ps.Status.Message = fmt.Sprintf("PR #%d merged via webhook", event.PRNumber)
		if patchErr := s.client.Status().Patch(ctx, ps, patch); patchErr != nil {
			s.log.Error().Err(patchErr).
				Str("promotionstep", ps.Name).
				Msg("failed to patch promotionstep after webhook merge")
			return fmt.Errorf("patch promotionstep %s: %w", ps.Name, patchErr)
		}
		s.log.Info().
			Str("promotionstep", ps.Name).
			Int("pr", event.PRNumber).
			Msg("advanced promotionstep to HealthChecking via webhook")
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
