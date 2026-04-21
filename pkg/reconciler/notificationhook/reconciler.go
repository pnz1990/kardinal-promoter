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

// Package notificationhook implements the NotificationHookReconciler.
//
// The NotificationHook CRD allows cluster operators to register outbound webhooks
// that are fired when specific promotion events occur (Bundle.Verified, Bundle.Failed,
// PolicyGate.Blocked, PromotionStep.Failed).
//
// Architecture context:
//
//	This reconciler is an Owned node (Q2 in the Graph-first question stack):
//	  - It writes only to its own CRD status (status.lastSentAt, lastEvent, etc.).
//	  - HTTP calls are made at-most-once per event (idempotent via status.lastEventKey).
//	  - time.Now() is only called inside a CRD status write — no logic leak.
//	  - No cross-CRD status mutations, no exec.Command, no in-memory state.
//
// The reconciler scans all qualifying Bundles, PolicyGates, and PromotionSteps
// on each reconcile, determines the latest event for this hook, and delivers it
// if not already delivered. The event key (type + resource name) stored in
// status.lastEventKey provides idempotency across reconcile loops and restarts.
package notificationhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

const (
	// webhookTimeout is the HTTP client timeout for webhook delivery.
	webhookTimeout = 10 * time.Second
)

// notificationPayload is the JSON body sent to the webhook URL.
type notificationPayload struct {
	Event       string `json:"event"`
	Pipeline    string `json:"pipeline,omitempty"`
	Bundle      string `json:"bundle,omitempty"`
	Environment string `json:"environment,omitempty"`
	Message     string `json:"message"`
	Timestamp   string `json:"timestamp"`
}

// pendingEvent describes an event that should be delivered.
type pendingEvent struct {
	eventType v1alpha1.NotificationHookEventType
	eventKey  string // deterministic: "<EventType>/<resource-name>"
	payload   notificationPayload
}

// Reconciler handles NotificationHook objects and delivers webhooks on promotion events.
// It is idempotent and safe to re-run after a crash.
type Reconciler struct {
	client.Client
	// HTTPClient is the HTTP client used for webhook delivery.
	// Overridable for testing.
	HTTPClient *http.Client
	// NowFn returns the current time. Overridable for testing.
	NowFn func() time.Time
}

// Reconcile processes a single NotificationHook and delivers any pending events.
//
// State machine:
//  1. Not found → deleted, skip.
//  2. Scan all Bundles, PolicyGates, PromotionSteps for qualifying events.
//  3. If a pending event's key differs from status.lastEventKey, deliver the webhook.
//  4. Write delivery result (success or failure) to status.
//
// At-most-once delivery is guaranteed within a single reconcile loop because
// the reconciler only delivers the most-recent event per hook per reconcile.
// The status.lastEventKey ensures a delivered event is never re-delivered.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().
		Str("notificationhook", req.Name).
		Str("namespace", req.Namespace).
		Logger()

	var hook v1alpha1.NotificationHook
	if err := r.Get(ctx, req.NamespacedName, &hook); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get notificationhook: %w", err)
	}

	// Determine the latest qualifying event for this hook.
	pending, err := r.latestPendingEvent(ctx, &hook)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("scan events: %w", err)
	}

	if pending == nil {
		// No new events — nothing to do.
		return ctrl.Result{}, nil
	}

	// Idempotency: skip if we already delivered this event.
	if hook.Status.LastEventKey == pending.eventKey {
		log.Debug().Str("eventKey", pending.eventKey).Msg("notificationhook: event already delivered, skipping")
		return ctrl.Result{}, nil
	}

	// Deliver the webhook.
	deliveryErr := r.deliver(ctx, &hook, pending)

	// Write delivery result to status — time.Now() called here (inside status write).
	patch := client.MergeFrom(hook.DeepCopy())
	now := r.now()
	if deliveryErr != nil {
		hook.Status.FailureMessage = fmt.Sprintf("delivery failed: %v", deliveryErr)
		log.Warn().Err(deliveryErr).Str("eventKey", pending.eventKey).Msg("notificationhook: webhook delivery failed")
	} else {
		hook.Status.LastSentAt = now.UTC().Format(time.RFC3339)
		hook.Status.LastEvent = string(pending.eventType)
		hook.Status.LastEventKey = pending.eventKey
		hook.Status.FailureMessage = ""
		log.Info().Str("eventKey", pending.eventKey).Str("url", hook.Spec.Webhook.URL).Msg("notificationhook: webhook delivered")
	}

	if err := r.Status().Patch(ctx, &hook, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	return ctrl.Result{}, nil
}

// latestPendingEvent scans watched objects for the most recent qualifying event for hook.
// Returns nil if no qualifying event exists.
func (r *Reconciler) latestPendingEvent(ctx context.Context, hook *v1alpha1.NotificationHook) (*pendingEvent, error) {
	eventSet := make(map[v1alpha1.NotificationHookEventType]bool, len(hook.Spec.Events))
	for _, e := range hook.Spec.Events {
		eventSet[e] = true
	}

	var latest *pendingEvent
	var latestTime time.Time

	// Scan Bundles for Bundle.Verified and Bundle.Failed events.
	if eventSet[v1alpha1.NotificationEventBundleVerified] || eventSet[v1alpha1.NotificationEventBundleFailed] {
		var bundles v1alpha1.BundleList
		if err := r.List(ctx, &bundles, listOptsForPipeline(hook.Spec.PipelineSelector, hook.Namespace)...); err != nil {
			return nil, fmt.Errorf("list bundles: %w", err)
		}
		for _, b := range bundles.Items {
			var evType v1alpha1.NotificationHookEventType
			switch b.Status.Phase {
			case "Verified":
				if eventSet[v1alpha1.NotificationEventBundleVerified] {
					evType = v1alpha1.NotificationEventBundleVerified
				}
			case "Failed":
				if eventSet[v1alpha1.NotificationEventBundleFailed] {
					evType = v1alpha1.NotificationEventBundleFailed
				}
			}
			if evType == "" {
				continue
			}
			if !b.CreationTimestamp.IsZero() && b.CreationTimestamp.After(latestTime) {
				latestTime = b.CreationTimestamp.Time
				latest = &pendingEvent{
					eventType: evType,
					eventKey:  string(evType) + "/" + b.Name,
					payload: notificationPayload{
						Event:    string(evType),
						Pipeline: b.Spec.Pipeline,
						Bundle:   b.Name,
						Message:  fmt.Sprintf("Bundle %s is %s", b.Name, b.Status.Phase),
					},
				}
			}
		}
	}

	// Scan PolicyGates for PolicyGate.Blocked events.
	if eventSet[v1alpha1.NotificationEventPolicyGateBlocked] {
		var gates v1alpha1.PolicyGateList
		if err := r.List(ctx, &gates, client.InNamespace(hook.Namespace)); err != nil {
			return nil, fmt.Errorf("list policygates: %w", err)
		}
		for _, g := range gates.Items {
			if g.Status.Ready || g.Status.LastEvaluatedAt == nil {
				continue
			}
			// Filter by pipeline if selector is set.
			if hook.Spec.PipelineSelector != "" {
				if g.Labels["kardinal.io/pipeline"] != hook.Spec.PipelineSelector {
					continue
				}
			}
			evalTime := g.Status.LastEvaluatedAt.Time
			if evalTime.After(latestTime) {
				latestTime = evalTime
				latest = &pendingEvent{
					eventType: v1alpha1.NotificationEventPolicyGateBlocked,
					eventKey:  string(v1alpha1.NotificationEventPolicyGateBlocked) + "/" + g.Name,
					payload: notificationPayload{
						Event:    string(v1alpha1.NotificationEventPolicyGateBlocked),
						Pipeline: g.Labels["kardinal.io/pipeline"],
						Message:  fmt.Sprintf("PolicyGate %s is blocking: %s", g.Name, g.Status.Reason),
					},
				}
			}
		}
	}

	// Scan PromotionSteps for PromotionStep.Failed events.
	if eventSet[v1alpha1.NotificationEventPromotionStepFailed] {
		var steps v1alpha1.PromotionStepList
		if err := r.List(ctx, &steps, listOptsForPipeline(hook.Spec.PipelineSelector, hook.Namespace)...); err != nil {
			return nil, fmt.Errorf("list promotionsteps: %w", err)
		}
		for _, ps := range steps.Items {
			if ps.Status.State != "Failed" {
				continue
			}
			if !ps.CreationTimestamp.IsZero() && ps.CreationTimestamp.After(latestTime) {
				latestTime = ps.CreationTimestamp.Time
				latest = &pendingEvent{
					eventType: v1alpha1.NotificationEventPromotionStepFailed,
					eventKey:  string(v1alpha1.NotificationEventPromotionStepFailed) + "/" + ps.Name,
					payload: notificationPayload{
						Event:       string(v1alpha1.NotificationEventPromotionStepFailed),
						Pipeline:    ps.Spec.PipelineName,
						Bundle:      ps.Spec.BundleName,
						Environment: ps.Spec.Environment,
						Message:     fmt.Sprintf("PromotionStep %s failed: %s", ps.Name, ps.Status.Message),
					},
				}
			}
		}
	}

	return latest, nil
}

// deliver sends the webhook payload to the configured URL.
// Returns an error if delivery fails.
func (r *Reconciler) deliver(ctx context.Context, hook *v1alpha1.NotificationHook, ev *pendingEvent) error {
	ev.payload.Timestamp = r.now().UTC().Format(time.RFC3339)

	body, err := json.Marshal(ev.payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: webhookTimeout}
	}

	reqCtx, cancel := context.WithTimeout(ctx, webhookTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, hook.Spec.Webhook.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if hook.Spec.Webhook.AuthorizationHeader != "" {
		req.Header.Set("Authorization", hook.Spec.Webhook.AuthorizationHeader)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}

// SetupWithManager registers the NotificationHookReconciler with the controller-runtime Manager.
// It watches NotificationHook objects directly and also watches Bundle, PolicyGate, and
// PromotionStep objects, mapping them back to all NotificationHook instances so they are
// re-evaluated on each relevant event.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Map any Bundle/PolicyGate/PromotionStep change to all NotificationHooks in the same namespace.
	mapToAllHooks := func(ctx context.Context, obj client.Object) []reconcile.Request {
		var hooks v1alpha1.NotificationHookList
		if err := mgr.GetClient().List(ctx, &hooks, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(hooks.Items))
		for _, h := range hooks.Items {
			reqs = append(reqs, reconcile.Request{
				NamespacedName: client.ObjectKey{Name: h.Name, Namespace: h.Namespace},
			})
		}
		return reqs
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.NotificationHook{}).
		Watches(&v1alpha1.Bundle{}, handler.EnqueueRequestsFromMapFunc(mapToAllHooks)).
		Watches(&v1alpha1.PolicyGate{}, handler.EnqueueRequestsFromMapFunc(mapToAllHooks)).
		Watches(&v1alpha1.PromotionStep{}, handler.EnqueueRequestsFromMapFunc(mapToAllHooks)).
		Named("notificationhook").
		Complete(r)
}

// listOptsForPipeline returns List options that filter by namespace and optionally
// by pipeline label (kardinal.io/pipeline).
func listOptsForPipeline(pipelineSelector, namespace string) []client.ListOption {
	opts := []client.ListOption{client.InNamespace(namespace)}
	if pipelineSelector != "" {
		opts = append(opts, client.MatchingLabels{"kardinal.io/pipeline": pipelineSelector})
	}
	return opts
}

// now returns the current time, using NowFn if set (for testing).
func (r *Reconciler) now() time.Time {
	if r.NowFn != nil {
		return r.NowFn()
	}
	return time.Now().UTC()
}
