// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/labelvalue"
	subscriptionrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/subscription"
)

// changeRequestWebhookPrefix is where the change request trigger is
// mounted: /webhook/changerequests/<namespace>/<name>[/<token>].
const changeRequestWebhookPrefix = "/webhook/changerequests/"

// changeRequestTokenHeader carries the trigger's token; it is preferred to
// the last path segment, which proxies and access logs record.
const changeRequestTokenHeader = "X-Kardinal-Token"

// The receiver's coarse limit across every template and sender, checked
// before anything is read: each request costs a ChangeRequest and a Secret
// read. The per-template limit applies only to requests with a valid token,
// so a sender without it cannot use up a template's budget.
const changeRequestGlobalRate, changeRequestGlobalBurst = 50, 100

// changeRequestWebhook receives "a ticket changed" triggers from a
// ticketing system (a ServiceNow business rule or flow, a Jira webhook) for
// a per-promotion ChangeRequest template (docs/design/23-change-requests.md
// section 7.1). An authenticated request sets the kardinal.io/refresh
// annotation of the template's instances, so the ChangeRequest reconciler
// reads their tickets at once instead of at the next poll. The body is read
// (and bounded) but never used: the state always comes from the provider's
// API. The receiver writes nothing but that annotation.
//
// Authentication: the template's spec.webhook.secretRef names a referenceable
// Secret whose key token (at least 16 characters) is the last path segment,
// compared in constant time. Every failure, a missing template or one
// without a webhook included, is the same 401 after the same Secret read.
// Idempotent: an instance whose refresh is still pending (the annotation is
// not yet status.observedRefresh) is not written again, so a replayed
// trigger costs nothing.
type changeRequestWebhook struct {
	client client.Client
	log    zerolog.Logger
	global *rate.Limiter
	perCR  *keyedLimiter
	now    func() time.Time
}

func newChangeRequestWebhook(c client.Client, log zerolog.Logger) *changeRequestWebhook {
	return &changeRequestWebhook{client: c, log: log,
		global: rate.NewLimiter(changeRequestGlobalRate, changeRequestGlobalBurst),
		perCR:  newKeyedLimiter(webhookPerSubRate, webhookPerSubBurst, webhookLimiterEntries),
		now:    time.Now}
}

// parseChangeRequestWebhookPath splits
// /webhook/changerequests/<namespace>/<name>[/<token>].
func parseChangeRequestWebhookPath(p string) (ns, name, token string, ok bool) {
	rest, found := strings.CutPrefix(p, changeRequestWebhookPrefix)
	if !found {
		return "", "", "", false
	}
	parts := strings.Split(strings.TrimSuffix(rest, "/"), "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" || (len(parts) == 3 && parts[2] == "") {
		return "", "", "", false
	}
	if len(parts) == 3 {
		token = parts[2]
	}
	return parts[0], parts[1], token, true
}

// Handler serves POST /webhook/changerequests/<namespace>/<name>[/<token>],
// the token in the X-Kardinal-Token header (preferred) or the last path
// segment. In order: the coarse global limit; the token, checked in
// constant time; the per-template limit, for valid tokens only; the
// refresh. Nothing that is logged carries the token.
func (h *changeRequestWebhook) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeWebhookJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		ns, name, pathToken, ok := parseChangeRequestWebhookPath(r.URL.Path)
		if !ok {
			writeWebhookJSON(w, http.StatusNotFound, "not found: use /webhook/changerequests/<namespace>/<name> with the X-Kardinal-Token header")
			return
		}
		now := h.now()
		if !h.global.AllowN(now, 1) {
			w.Header().Set("Retry-After", "1")
			writeWebhookJSON(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		// The body is never trusted for state; it is only bounded.
		if _, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, maxWebhookBody)); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeWebhookJSON(w, http.StatusRequestEntityTooLarge, "payload too large")
				return
			}
			writeWebhookJSON(w, http.StatusBadRequest, "bad request")
			return
		}
		token := r.Header.Get(changeRequestTokenHeader)
		if token == "" {
			token = pathToken
		}
		log := h.log.With().Str("changerequest", name).Str("namespace", ns).Logger()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		tmpl, reason := h.authenticate(ctx, ns, name, token)
		if tmpl == nil {
			log.Warn().Str("reason", reason).Str("remoteAddr", r.RemoteAddr).Msg("change request webhook refused")
			writeWebhookJSON(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !h.perCR.allow(ns+"/"+name, now) {
			w.Header().Set("Retry-After", "1")
			writeWebhookJSON(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		n, err := h.refresh(ctx, tmpl, now)
		if err != nil {
			log.Error().Err(err).Msg("change request webhook: request refresh")
			writeWebhookJSON(w, http.StatusInternalServerError, "internal error")
			return
		}
		log.Info().Int("instances", n).Msg("change request webhook received")
		writeWebhookJSON(w, http.StatusAccepted, fmt.Sprintf("refresh requested for %d instances", n))
	}
}

// authenticate returns the template when token is its webhook token, or
// nil and why not (for the log, never the answer).
func (h *changeRequestWebhook) authenticate(ctx context.Context, ns, name, token string) (*v1alpha1.ChangeRequest, string) {
	var cr v1alpha1.ChangeRequest
	miss := ""
	switch err := h.client.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &cr); {
	case err != nil:
		miss = "get changerequest: " + err.Error()
	case !cr.Spec.PerPromotion:
		miss = "not a per-promotion template"
	case cr.Spec.Webhook == nil || cr.Spec.Webhook.SecretRef.Name == "":
		miss = "the ChangeRequest has no spec.webhook"
	}
	if token == "" && miss == "" {
		miss = "no token (X-Kardinal-Token header or last path segment)"
	}
	if miss != "" {
		var pad corev1.Secret
		_ = h.client.Get(ctx, client.ObjectKey{Namespace: ns, Name: webhookTimingPadSecret}, &pad)
		_ = subtle.ConstantTimeCompare(padToken[:], []byte(token))
		return nil, miss
	}
	secret, err := subscriptionrecon.ReferenceableSecret(ctx, h.client, ns, cr.Spec.Webhook.SecretRef.Name, "spec.webhook.secretRef")
	if err != nil {
		return nil, err.Error()
	}
	want := secret.Data["token"]
	if len(want) < minWebhookTokenLen {
		return nil, fmt.Sprintf("webhook Secret %q has no key token of at least %d characters", secret.Name, minWebhookTokenLen)
	}
	if subtle.ConstantTimeCompare(want, []byte(token)) != 1 {
		return nil, "wrong token"
	}
	return &cr, ""
}

// refresh sets kardinal.io/refresh on the template's instances that are not
// suspended and have no refresh pending, and returns how many it set.
func (h *changeRequestWebhook) refresh(ctx context.Context, tmpl *v1alpha1.ChangeRequest, now time.Time) (int, error) {
	var list v1alpha1.ChangeRequestList
	if err := h.client.List(ctx, &list, client.InNamespace(tmpl.Namespace),
		client.MatchingLabels{graph.LabelChangeRequestTemplate: labelvalue.Fit(tmpl.Name)}); err != nil {
		return 0, fmt.Errorf("list instances: %w", err)
	}
	n := 0
	for i := range list.Items {
		inst := &list.Items[i]
		pending := inst.Annotations[v1alpha1.RefreshAnnotation]
		if inst.Spec.Suspend || (pending != "" && pending != inst.Status.ObservedRefresh) {
			continue
		}
		patch := client.MergeFrom(inst.DeepCopy())
		if inst.Annotations == nil {
			inst.Annotations = map[string]string{}
		}
		inst.Annotations[v1alpha1.RefreshAnnotation] = now.UTC().Format(time.RFC3339Nano)
		if err := h.client.Patch(ctx, inst, patch); err != nil {
			return n, fmt.Errorf("patch %s: %w", inst.Name, err)
		}
		n++
	}
	return n, nil
}
