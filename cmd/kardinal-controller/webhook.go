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
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/prstatus"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

const (
	// maxWebhookBody is the maximum webhook payload size (1 MB).
	maxWebhookBody = 1 << 20
	// mergeConfirmTimeout bounds the SCM API call that confirms a merge event
	// (mergeConfirmed). GitHub counts a webhook delivery failed when the
	// endpoint has not answered within 10 seconds, and the handler's 30-second
	// context and the provider's 30-second HTTP timeout would let one slow SCM
	// call turn the delivery into a failure. A confirmation that runs out of
	// time is treated as a failed one: 204, nothing marked, polling records
	// the merge.
	mergeConfirmTimeout = 8 * time.Second
)

// webhookServer is an HTTP server that handles incoming SCM webhook events.
type webhookServer struct {
	scm               scm.SCMProvider
	client            client.Client
	log               zerolog.Logger
	webhookConfigured bool

	// eventsTotal counts every signed event since startup, whatever its type
	// (push, comment, PR). mergedPREventsTotal counts only the merged pull
	// request (merge request) events, the only ones the handler acts on.
	eventsTotal         atomic.Int64
	mergedPREventsTotal atomic.Int64
}

// newWebhookServerWithConfig constructs a webhookServer and records whether a webhook
// secret is configured. Without a secret the server rejects every event (fail closed):
// an HMAC with an empty key proves nothing, and PRStatus polling keeps promotions
// moving without webhooks.
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

		// Fail closed: without a shared secret no request can be authenticated,
		// so every event is rejected. main.go logs once at startup that SCM
		// webhooks are disabled.
		if !s.webhookConfigured {
			http.Error(w, "webhook secret not configured", http.StatusUnauthorized)
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				// Reject instead of truncating: a truncated body fails the HMAC
				// check and would be misreported as a bad signature.
				http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
				return
			}
			s.log.Error().Err(err).Msg("failed to read webhook body")
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// Each provider signs with its own header and names the event in its
		// own header (or the payload); the provider validates and reads them.
		event, err := scm.ParseWebhookRequest(s.scm, body, r.Header)
		if err != nil {
			// The header's name, never its value: GitLab and Azure DevOps send
			// the secret itself.
			header := scm.WebhookSignatureHeader(r.Header)
			if header == "" {
				header = "none"
			}
			s.log.Warn().Err(err).Str("signatureHeader", header).Str("remoteAddr", r.RemoteAddr).
				Msg("webhook signature invalid or parse error")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		s.eventsTotal.Add(1)

		s.log.Info().
			Str("event_type", event.EventType).
			Str("action", event.Action).
			Bool("merged", event.Merged).
			Int("pr", event.PRNumber).
			Str("repo", event.RepoFullName).
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
		s.mergedPREventsTotal.Add(1)

		w.WriteHeader(http.StatusNoContent)
	}
}

// webhookHealthResponse is the body of GET /webhook/scm/health.
type webhookHealthResponse struct {
	// Status is always "ok".
	Status string `json:"status"`
	// WebhookConfigured is true when a webhook secret is set; without one
	// every event is refused.
	WebhookConfigured bool `json:"webhookConfigured"`
	// EventsProcessed counts the signed webhook events of any type since startup.
	EventsProcessed int64 `json:"eventsProcessed"`
	// MergedPREvents counts the merged pull request events among them.
	MergedPREvents int64 `json:"mergedPREvents"`
}

// HealthHandler returns an http.HandlerFunc for GET /webhook/scm/health.
// Responds with 200 OK and a JSON body indicating webhook configuration status,
// the number of signed webhook events of any type since startup
// (eventsProcessed) and the number of merged pull request events among them
// (mergedPREvents).
func (s *webhookServer) HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		resp := webhookHealthResponse{
			Status:            "ok",
			WebhookConfigured: s.webhookConfigured,
			EventsProcessed:   s.eventsTotal.Load(),
			MergedPREvents:    s.mergedPREventsTotal.Load(),
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			s.log.Error().Err(err).Msg("failed to encode health response")
		}
	}
}

// markPRStatusMerged finds PRStatus CRDs matching the merged PR and sets
// status.merged = true, with status.mergeCommitSHA when the event carries it.
// The PromotionStep reconciler will detect the change on its next reconcile
// and advance to HealthChecking.
//
// The event is a hint, not the record: before it marks anything, the webhook
// asks the SCM provider once whether the PR is merged (mergeConfirmed), so an
// event signed with the shared secret cannot advance a PR that is not merged.
//
// This is the pure version of the old reconcileMergedPR — the webhook now only
// writes to its own CRD (PRStatus) and does not touch PromotionStep status.
func (s *webhookServer) markPRStatusMerged(ctx context.Context, event scm.WebhookEvent) error {
	if event.PRNumber <= 0 || event.RepoFullName == "" {
		s.log.Warn().Int("pr", event.PRNumber).Str("repo", event.RepoFullName).
			Msg("merged webhook event has no PR number or repo; ignoring")
		return nil
	}

	var prsList v1alpha1.PRStatusList
	if err := s.client.List(ctx, &prsList); err != nil {
		return fmt.Errorf("list prstatuses: %w", err)
	}

	var toMark []*v1alpha1.PRStatus
	for i := range prsList.Items {
		prs := &prsList.Items[i]

		// Match by PR number and repo. Both are required: a PRStatus without a PR
		// number or repo is a placeholder whose PR is not open yet, and an event
		// without them cannot be scoped to one PR.
		if prs.Spec.PRNumber != event.PRNumber {
			continue
		}
		if prs.Spec.Repo == "" || !scm.SameRepo(s.scm, prs.Spec.Repo, event.RepoFullName) {
			continue
		}
		if prstatus.DescribesSpec(prs) && prs.Status.Merged &&
			(prs.Status.MergeCommitSHA != "" || event.MergeCommitSHA == "") {
			// Already marked merged, and nothing to add — idempotent skip.
			continue
		}
		toMark = append(toMark, prs)
	}
	// An event that names no tracked PR, or only PRs already marked, costs no
	// SCM API call.
	if len(toMark) == 0 || !s.mergeConfirmed(ctx, toMark[0]) {
		return nil
	}

	now := metav1.NewTime(time.Now().UTC())
	for _, prs := range toMark {
		patch := client.MergeFrom(prs.DeepCopy())
		if !prstatus.DescribesSpec(prs) {
			// The status is still the one of the PR the spec named before (a
			// recreated step opened this one, B72): none of it holds.
			prs.Status = v1alpha1.PRStatusStatus{}
		}
		prs.Status.ObservedGeneration = prs.Generation
		if !prs.Status.Merged {
			prs.Status.Merged = true
			prs.Status.Open = false
			prs.Status.LastCheckedAt = &now
		}
		if prs.Status.MergeCommitSHA == "" && event.MergeCommitSHA != "" {
			// Written with merged, so the health check knows the commit from
			// the start (#1307). The status holds the commit or that it is
			// unavailable, not both.
			prs.Status.MergeCommitSHA = event.MergeCommitSHA
			prs.Status.MergeCommitUnavailable = false
		}

		patchErr := s.client.Status().Patch(ctx, prs, patch)
		if apierrors.IsNotFound(patchErr) {
			// Deleted since the list, with its Graph or step: nothing to advance.
			s.log.Debug().Str("prstatus", prs.Name).Msg("PRStatus deleted before it was marked merged; skipped")
			continue
		}
		if patchErr != nil {
			s.log.Error().Err(patchErr).
				Str("prstatus", prs.Name).
				Msg("failed to mark PRStatus as merged via webhook")
			return fmt.Errorf("patch prstatus %s: %w", prs.Name, patchErr)
		}
		s.log.Info().
			Str("prstatus", prs.Name).
			Str("namespace", prs.Namespace).
			Int("pr", event.PRNumber).
			Str("mergeCommit", event.MergeCommitSHA).
			Msg("PRStatus marked merged via webhook")
	}
	return nil
}

// mergeConfirmed asks the SCM provider whether the PR of a merge event, which
// prs tracks, is merged. A valid signature proves only that the sender has
// the webhook secret, and GitLab and Azure DevOps send that secret in plain
// text with every event, so the webhook only marks a merge the SCM API
// reports, as a poll would. When the API says the PR is not merged, or the
// call fails, nothing is marked and the event still gets 204: the PRStatus
// poll records the merge when there is one, and a 5xx would only make the
// SCM retry the delivery or disable the webhook. The call gets
// mergeConfirmTimeout, under GitHub's delivery timeout, so a slow SCM API
// does not make the delivery fail.
func (s *webhookServer) mergeConfirmed(ctx context.Context, prs *v1alpha1.PRStatus) bool {
	ctx, cancel := context.WithTimeout(ctx, mergeConfirmTimeout)
	defer cancel()
	merged, open, err := s.scm.GetPRStatus(ctx, prs.Spec.Repo, prs.Spec.PRNumber)
	if err == nil && merged {
		return true
	}
	log := s.log.Warn().Str("prstatus", prs.Name).Str("namespace", prs.Namespace).
		Str("repo", prs.Spec.Repo).Int("pr", prs.Spec.PRNumber)
	if err != nil {
		log.Err(err).Msg("could not confirm the merge event with the SCM provider; PRStatus not marked merged, polling will record the merge")
		return false
	}
	log.Bool("open", open).Msg("SCM provider reports the PR of the merge event not merged; PRStatus not marked merged")
	return false
}
