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
//	  - It writes only to its own CRD status (processedEventKeys, lastSentAt, ...).
//	  - Each qualifying event is delivered once: its key is recorded in
//	    status.processedEventKeys after a successful POST.
//	  - time.Now() is only called inside a CRD status write — no logic leak.
//	  - No cross-CRD status mutations, no exec.Command, no in-memory state.
//
// The reconciler lists all qualifying Bundles, PolicyGates, and PromotionSteps
// on each reconcile and delivers every event whose key is not yet in
// status.processedEventKeys, oldest first. A failed delivery is retried with
// exponential backoff; after maxDeliveryAttempts the event is given up on.
package notificationhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/rs/zerolog"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/eventfilter"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/shard"
)

const (
	// webhookTimeout is the HTTP client timeout for webhook delivery.
	webhookTimeout = 10 * time.Second

	// maxTrackedEvents bounds the events considered per reconcile (the newest
	// ones) and therefore the size of status.processedEventKeys.
	maxTrackedEvents = 100

	// maxDeliveriesPerReconcile bounds the time one reconcile spends posting;
	// the rest are delivered on an immediate requeue.
	maxDeliveriesPerReconcile = 10

	// maxDeliveryAttempts is how many times one event is tried before the
	// controller gives up on it and moves to the next event.
	maxDeliveryAttempts = 10

	// retryBaseDelay and retryMaxDelay bound the exponential retry backoff.
	retryBaseDelay = 30 * time.Second
	retryMaxDelay  = 10 * time.Minute

	// gateReadyCondition is the PolicyGate condition whose transition time
	// identifies one blocking episode.
	gateReadyCondition = "Ready"

	labelBundle      = "kardinal.io/bundle"
	labelPipeline    = "kardinal.io/pipeline"
	labelEnvironment = "kardinal.io/environment"
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

// pendingEvent describes a qualifying event.
type pendingEvent struct {
	eventType v1alpha1.NotificationHookEventType
	// eventKey is deterministic: "<EventType>/<resource-name>", plus the
	// block's transition time for PolicyGate.Blocked, so a gate that is
	// allowed and then blocked again is a new event.
	eventKey string
	// legacyKey is the key format used before per-transition gate keys; an
	// event whose legacyKey equals status.lastEventKey was already delivered.
	legacyKey string
	at        time.Time
	payload   notificationPayload
}

// Reconciler handles NotificationHook objects and delivers webhooks on promotion events.
// It is idempotent and safe to re-run after a crash.
type Reconciler struct {
	client.Client
	// APIReader reads straight from the API server (mgr.GetAPIReader()). The
	// hook is read through it, so a reconcile sees the status the previous
	// reconcile of the same hook wrote. The informer cache can lag that write
	// by a few milliseconds, and a Bundle, PolicyGate or PromotionStep change
	// in that window reconciles the hook again: from the cache it has no
	// nextRetryAt and no record of the delivery, and the event is POSTed a
	// second time. When nil, Client is used (tests).
	APIReader client.Reader
	// HTTPClient is the HTTP client used for webhook delivery. When nil, a
	// client with the egress address guard is used (no loopback, link-local
	// or cloud metadata destinations). Overridable for testing. Redirects are
	// never followed, whichever client is used.
	HTTPClient *http.Client
	// NowFn returns the current time. Overridable for testing.
	NowFn func() time.Time
}

// Reconcile processes a single NotificationHook and delivers any pending events.
//
// State machine:
//  1. Not found → deleted, skip.
//  2. List qualifying events from Bundles, PolicyGates and PromotionSteps.
//  3. On the first reconcile of a hook, record all but the newest existing
//     event as processed (no backfill).
//  4. Deliver every event not in status.processedEventKeys, oldest first,
//     recording each key after its successful POST.
//  5. On a failed POST, record the attempt and status.nextRetryAt and requeue
//     with backoff; after maxDeliveryAttempts give up on that event. No POST
//     is made before status.nextRetryAt, however often the hook is reconciled.
//
// A NotificationHook deleted while it is reconciled ends the reconcile (objectgone).
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return objectgone.Reconcile(ctx, req, notificationHooksResource, r.reconcile)
}

// notificationHooksResource is the resource objectgone matches a NotFound against.
var notificationHooksResource = v1alpha1.GroupVersion.WithResource("notificationhooks").GroupResource()

func (r *Reconciler) reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().
		Str("notificationhook", req.Name).
		Str("namespace", req.Namespace).
		Logger()

	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	var hook v1alpha1.NotificationHook
	if err := reader.Get(ctx, req.NamespacedName, &hook); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get notificationhook: %w", err)
	}

	events, err := r.qualifyingEvents(ctx, &hook)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("scan events: %w", err)
	}

	base := hook.DeepCopy()
	processed := make(map[string]bool, len(hook.Status.ProcessedEventKeys))
	for _, k := range hook.Status.ProcessedEventKeys {
		processed[k] = true
	}
	delivered := func(ev pendingEvent) bool {
		return processed[ev.eventKey] || ev.eventKey == hook.Status.LastEventKey ||
			(ev.legacyKey != "" && ev.legacyKey == hook.Status.LastEventKey)
	}

	observed := hook.Generation
	if observed < 1 {
		observed = 1
	}
	switch {
	case hook.Status.ObservedGeneration == 0:
		// First reconcile: do not backfill history. Only the newest existing
		// event is delivered, as before per-event tracking existed.
		for i := 0; i+1 < len(events); i++ {
			processed[events[i].eventKey] = true
		}
	case hook.Status.ObservedGeneration != observed:
		// The spec changed (for example a corrected URL): retry at once.
		hook.Status.FailedAttempts = 0
		hook.Status.NextRetryAt = ""
	}

	writeStatus := func() error {
		keys := make([]string, 0, len(events))
		for _, ev := range events {
			if processed[ev.eventKey] {
				keys = append(keys, ev.eventKey)
			}
		}
		hook.Status.ProcessedEventKeys = keys
		hook.Status.ObservedGeneration = observed
		if apiequality.Semantic.DeepEqual(base.Status, hook.Status) {
			return nil
		}
		if err := r.Status().Patch(ctx, &hook, client.MergeFrom(base)); err != nil {
			return fmt.Errorf("update status: %w", err)
		}
		base = hook.DeepCopy()
		return nil
	}

	var result ctrl.Result
	sent := 0
	for i := range events {
		ev := events[i]
		if delivered(ev) {
			processed[ev.eventKey] = true
			continue
		}
		if sent == maxDeliveriesPerReconcile {
			result.RequeueAfter = time.Second
			break
		}
		// Watches on Bundles, PolicyGates and PromotionSteps reconcile the
		// hook far more often than the backoff, so the retry time is kept in
		// status and checked here, not only returned as RequeueAfter.
		if wait := r.retryWait(&hook); wait > 0 {
			result.RequeueAfter = wait
			break
		}
		if deliveryErr := r.deliver(ctx, &hook, &ev); deliveryErr != nil {
			hook.Status.FailedAttempts++
			log.Warn().Err(deliveryErr).Str("eventKey", ev.eventKey).Str("host", urlHost(hook.Spec.Webhook.URL)).
				Int32("attempt", hook.Status.FailedAttempts).Msg("notificationhook: webhook delivery failed")
			if hook.Status.FailedAttempts >= maxDeliveryAttempts {
				processed[ev.eventKey] = true
				hook.Status.FailureMessage = fmt.Sprintf("gave up on %s after %d attempts: %v",
					ev.eventKey, maxDeliveryAttempts, deliveryErr)
				hook.Status.FailedAttempts = 0
				hook.Status.NextRetryAt = ""
				result.RequeueAfter = retryBaseDelay
			} else {
				hook.Status.FailureMessage = fmt.Sprintf("delivery of %s failed (attempt %d of %d): %v",
					ev.eventKey, hook.Status.FailedAttempts, maxDeliveryAttempts, deliveryErr)
				result.RequeueAfter = retryDelay(hook.Status.FailedAttempts)
				// time.Now() is called here, inside the status write.
				hook.Status.NextRetryAt = r.now().Add(result.RequeueAfter).UTC().Format(time.RFC3339)
			}
			break
		}
		sent++
		processed[ev.eventKey] = true
		// time.Now() is called here, inside the status write.
		hook.Status.LastSentAt = r.now().UTC().Format(time.RFC3339)
		hook.Status.LastEvent = string(ev.eventType)
		hook.Status.LastEventKey = ev.eventKey
		hook.Status.FailedAttempts = 0
		hook.Status.NextRetryAt = ""
		hook.Status.FailureMessage = ""
		log.Info().Str("eventKey", ev.eventKey).Str("host", urlHost(hook.Spec.Webhook.URL)).
			Msg("notificationhook: webhook delivered")
		// Record each delivery before the next POST, so a crash re-sends at most one.
		if err := writeStatus(); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := writeStatus(); err != nil {
		return ctrl.Result{}, err
	}
	return result, nil
}

// retryWait returns how long to wait before the next delivery attempt after a
// failed one: the time left until status.nextRetryAt, or 0 when an attempt is
// due. Like the health-check spacing in the PromotionStep reconciler, it reads
// the clock only to compare with a time this reconciler wrote to its own
// status.
func (r *Reconciler) retryWait(hook *v1alpha1.NotificationHook) time.Duration {
	if hook.Status.NextRetryAt == "" {
		return 0
	}
	next, err := time.Parse(time.RFC3339, hook.Status.NextRetryAt)
	if err != nil {
		return 0
	}
	return next.Sub(r.now())
}

// retryDelay is the backoff after the given number of consecutive failures.
func retryDelay(attempts int32) time.Duration {
	d := retryBaseDelay
	for i := int32(1); i < attempts && d < retryMaxDelay; i++ {
		d *= 2
	}
	if d > retryMaxDelay {
		d = retryMaxDelay
	}
	return d
}

// qualifyingEvents returns the events that match hook, oldest first, limited
// to the newest maxTrackedEvents.
func (r *Reconciler) qualifyingEvents(ctx context.Context, hook *v1alpha1.NotificationHook) ([]pendingEvent, error) {
	eventSet := make(map[v1alpha1.NotificationHookEventType]bool, len(hook.Spec.Events))
	for _, e := range hook.Spec.Events {
		eventSet[e] = true
	}
	selector := hook.Spec.PipelineSelector
	var events []pendingEvent

	// Bundle.Verified and Bundle.Failed. The pipeline is matched on
	// spec.pipeline: Bundles created by `kardinal create bundle` or kubectl do
	// not carry the kardinal.io/pipeline label.
	if eventSet[v1alpha1.NotificationEventBundleVerified] || eventSet[v1alpha1.NotificationEventBundleFailed] {
		var bundles v1alpha1.BundleList
		if err := r.List(ctx, &bundles, client.InNamespace(hook.Namespace)); err != nil {
			return nil, fmt.Errorf("list bundles: %w", err)
		}
		for _, b := range bundles.Items {
			if selector != "" && b.Spec.Pipeline != selector {
				continue
			}
			var evType v1alpha1.NotificationHookEventType
			switch b.Status.Phase {
			case "Verified":
				evType = v1alpha1.NotificationEventBundleVerified
			case "Failed":
				evType = v1alpha1.NotificationEventBundleFailed
			}
			if evType == "" || !eventSet[evType] {
				continue
			}
			events = append(events, pendingEvent{
				eventType: evType,
				eventKey:  string(evType) + "/" + b.Name,
				at:        b.CreationTimestamp.Time,
				payload: notificationPayload{
					Event:    string(evType),
					Pipeline: b.Spec.Pipeline,
					Bundle:   b.Name,
					Message:  fmt.Sprintf("Bundle %s is %s", b.Name, b.Status.Phase),
				},
			})
		}
	}

	// PolicyGate.Blocked: one event per blocking episode of a gate instance.
	// Templates (no kardinal.io/bundle label) are never evaluated, so they are
	// not blocking anything. The episode is identified by the Ready
	// condition's lastTransitionTime, which only moves when the gate flips;
	// status.lastEvaluatedAt moves on every re-evaluation.
	if eventSet[v1alpha1.NotificationEventPolicyGateBlocked] {
		var gates v1alpha1.PolicyGateList
		if err := r.List(ctx, &gates, client.InNamespace(hook.Namespace)); err != nil {
			return nil, fmt.Errorf("list policygates: %w", err)
		}
		for _, g := range gates.Items {
			if g.Labels[labelBundle] == "" || g.Status.Ready {
				continue
			}
			if selector != "" && g.Labels[labelPipeline] != selector {
				continue
			}
			cond := meta.FindStatusCondition(g.Status.Conditions, gateReadyCondition)
			if cond == nil || cond.Status != metav1.ConditionFalse {
				continue
			}
			evType := v1alpha1.NotificationEventPolicyGateBlocked
			events = append(events, pendingEvent{
				eventType: evType,
				eventKey:  string(evType) + "/" + g.Name + "/" + cond.LastTransitionTime.UTC().Format(time.RFC3339),
				legacyKey: string(evType) + "/" + g.Name,
				at:        cond.LastTransitionTime.Time,
				payload: notificationPayload{
					Event:       string(evType),
					Pipeline:    g.Labels[labelPipeline],
					Bundle:      g.Labels[labelBundle],
					Environment: g.Labels[labelEnvironment],
					Message:     fmt.Sprintf("PolicyGate %s is blocking: %s", g.Name, g.Status.Reason),
				},
			})
		}
	}

	// PromotionStep.Failed.
	if eventSet[v1alpha1.NotificationEventPromotionStepFailed] {
		var steps v1alpha1.PromotionStepList
		if err := r.List(ctx, &steps, client.InNamespace(hook.Namespace)); err != nil {
			return nil, fmt.Errorf("list promotionsteps: %w", err)
		}
		for _, ps := range steps.Items {
			if ps.Status.State != "Failed" {
				continue
			}
			if selector != "" && ps.Spec.PipelineName != selector {
				continue
			}
			evType := v1alpha1.NotificationEventPromotionStepFailed
			events = append(events, pendingEvent{
				eventType: evType,
				eventKey:  string(evType) + "/" + ps.Name,
				at:        ps.CreationTimestamp.Time,
				payload: notificationPayload{
					Event:       string(evType),
					Pipeline:    ps.Spec.PipelineName,
					Bundle:      ps.Spec.BundleName,
					Environment: ps.Spec.Environment,
					Message:     fmt.Sprintf("PromotionStep %s failed: %s", ps.Name, ps.Status.Message),
				},
			})
		}
	}

	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].at.Equal(events[j].at) {
			return events[i].at.Before(events[j].at)
		}
		return events[i].eventKey < events[j].eventKey
	})
	if len(events) > maxTrackedEvents {
		events = events[len(events)-maxTrackedEvents:]
	}
	return events, nil
}

// errRedirect is returned for a 3xx response: redirects are not followed, so a
// hook cannot bounce the controller's POST to another address.
var errRedirect = errors.New("webhook redirects are not followed")

// guardedTransport refuses loopback, link-local and cloud metadata
// destinations at dial time, so a hook URL (or a name it resolves to) cannot
// reach the controller's own UI API or the node's credential endpoints. It
// honours HTTP(S)_PROXY, as the default transport did.
var guardedTransport = egress.NewTransport(http.ProxyFromEnvironment)

// deliver sends the webhook payload to the configured URL.
// The returned error never contains the URL, which may embed a token (Slack,
// Teams); it is written to status and logs.
func (r *Reconciler) deliver(ctx context.Context, hook *v1alpha1.NotificationHook, ev *pendingEvent) error {
	ev.payload.Timestamp = r.now().UTC().Format(time.RFC3339)

	body, err := json.Marshal(ev.payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	httpClient := http.Client{Timeout: webhookTimeout, Transport: guardedTransport}
	if r.HTTPClient != nil {
		httpClient = *r.HTTPClient
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	reqCtx, cancel := context.WithTimeout(ctx, webhookTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, hook.Spec.Webhook.URL, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid webhook URL")
	}
	req.Header.Set("Content-Type", "application/json")
	if hook.Spec.Webhook.AuthorizationHeader != "" {
		req.Header.Set("Authorization", hook.Spec.Webhook.AuthorizationHeader)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		// *url.Error repeats the full URL; keep only the underlying cause.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			if uerr.Timeout() {
				return errors.New("webhook request timed out")
			}
			return fmt.Errorf("webhook request failed: %w", uerr.Err)
		}
		return fmt.Errorf("webhook request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return fmt.Errorf("webhook returned HTTP %d: %w", resp.StatusCode, errRedirect)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// urlHost returns the host of a webhook URL for logging. The path and query
// are dropped because incoming-webhook URLs embed their token there.
func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<invalid URL>"
	}
	return u.Host
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
			zerolog.Ctx(ctx).Error().Err(err).Str("namespace", obj.GetNamespace()).
				Msg("notificationhook: failed to list hooks for watch event; the event is delivered on the next change")
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

	b := ctrl.NewControllerManagedBy(mgr).
		// Status writes of the hook itself must not re-trigger it; retries use RequeueAfter.
		For(&v1alpha1.NotificationHook{}, builder.WithPredicates(eventfilter.SpecOrAnnotationChanged)).
		Watches(&v1alpha1.Bundle{}, handler.EnqueueRequestsFromMapFunc(mapToAllHooks)).
		Watches(&v1alpha1.PolicyGate{}, handler.EnqueueRequestsFromMapFunc(mapToAllHooks)).
		Watches(&v1alpha1.PromotionStep{}, handler.EnqueueRequestsFromMapFunc(mapToAllHooks)).
		Named("notificationhook")
	return shard.Active().Complete(b, r, &v1alpha1.NotificationHookList{})
}

// now returns the current time, using NowFn if set (for testing).
func (r *Reconciler) now() time.Time {
	if r.NowFn != nil {
		return r.NowFn()
	}
	return time.Now().UTC()
}
