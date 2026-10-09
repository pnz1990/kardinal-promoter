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
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	subscriptionrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/subscription"
)

const (
	// subscriptionWebhookPrefix is where the receiver is mounted:
	// /webhook/subscriptions/<namespace>/<name>/<provider>[/<token>].
	subscriptionWebhookPrefix = "/webhook/subscriptions/"
	// minWebhookTokenLen is the shortest webhook token the receiver accepts.
	minWebhookTokenLen = 16
	// The receiver limits requests per source IP and per Subscription path,
	// before authentication: each request costs a Secret read from the API
	// server. A sender over its limit gets 429 and does not use up the
	// budget of other senders or other Subscriptions.
	webhookPerIPRate, webhookPerIPBurst   = 10, 20
	webhookPerSubRate, webhookPerSubBurst = 5, 10
	// webhookLimiterEntries bounds each limiter table; idle entries are
	// dropped first.
	webhookLimiterEntries = 10000
	// webhookTimingPadSecret is the Secret name the receiver reads when the
	// Subscription does not exist or has no webhook, so that answer costs
	// the same API round trip as a real check.
	webhookTimingPadSecret = "kardinal-webhook-timing-pad"
)

// webhookProviders are the senders the receiver understands.
var webhookProviders = map[string]bool{
	"dockerhub": true, "ghcr": true, "github": true, "harbor": true, "quay": true, "artifactory": true, "generic": true,
}

// subscriptionWebhook receives registry and SCM webhooks for Subscriptions.
// An authenticated delivery that announces a push sets the Subscription's
// kardinal.io/refresh annotation; the Subscription reconciler polls the
// source and creates the Bundle. The receiver never polls a source or
// creates a Bundle itself, and it writes nothing but that annotation.
//
// Authentication is per Subscription: spec.webhook.secretRef names a Secret
// in the Subscription's namespace whose key token is either the last path
// segment (senders that cannot sign: Docker Hub, Quay), a header (Harbor's
// Authorization, Artifactory's X-JFrog-Event-Auth), or an HMAC-SHA256 key
// (GitHub's X-Hub-Signature-256, the generic X-Kardinal-Signature-256). Every
// failure, including a Subscription that does not exist or has no webhook,
// is the same 401, so the receiver does not reveal which Subscriptions exist.
//
// Idempotent: a refresh that is already pending (the annotation is newer
// than the status.lastRefreshRequest the reconciler last answered) is not
// written again, so redelivered and duplicate events cost nothing, and the
// reconciler polls at most once every 10 seconds per Subscription.
type subscriptionWebhook struct {
	client client.Client
	log    zerolog.Logger
	perIP  *keyedLimiter
	perSub *keyedLimiter
	now    func() time.Time

	refreshes atomic.Int64
}

func newSubscriptionWebhook(c client.Client, log zerolog.Logger) *subscriptionWebhook {
	return &subscriptionWebhook{
		client: c,
		log:    log,
		perIP:  newKeyedLimiter(webhookPerIPRate, webhookPerIPBurst, webhookLimiterEntries),
		perSub: newKeyedLimiter(webhookPerSubRate, webhookPerSubBurst, webhookLimiterEntries),
		now:    time.Now,
	}
}

// webhookResponse is the JSON body of every answer.
type webhookResponse struct {
	Status string `json:"status"`
}

func writeWebhookJSON(w http.ResponseWriter, code int, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(webhookResponse{Status: status})
}

// Handler serves POST /webhook/subscriptions/<namespace>/<name>/<provider>[/<token>].
func (s *subscriptionWebhook) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeWebhookJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		ns, name, provider, pathToken, ok := parseSubscriptionWebhookPath(r.URL.Path)
		if !ok {
			writeWebhookJSON(w, http.StatusNotFound,
				"not found: use /webhook/subscriptions/<namespace>/<name>/<dockerhub|ghcr|github|harbor|quay|artifactory|generic>[/<token>]")
			return
		}
		// The connection's address, not X-Forwarded-For, which a sender
		// can set: behind an Ingress every sender shares the Ingress's
		// address, and the per-Subscription limit still separates them.
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		now := s.now()
		if !s.perSub.allow(ns+"/"+name, now) || !s.perIP.allow(ip, now) {
			w.Header().Set("Retry-After", "1")
			writeWebhookJSON(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeWebhookJSON(w, http.StatusRequestEntityTooLarge, "payload too large")
				return
			}
			writeWebhookJSON(w, http.StatusBadRequest, "bad request")
			return
		}
		log := s.log.With().Str("subscription", name).Str("namespace", ns).Str("provider", provider).Logger()

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		sub, reason := s.authenticate(ctx, ns, name, provider, pathToken, body, r.Header)
		if sub == nil {
			// The reason is logged, never answered: the response is the same
			// for every failure. It never contains the token.
			log.Warn().Str("reason", reason).Str("remoteAddr", r.RemoteAddr).Msg("subscription webhook refused")
			writeWebhookJSON(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		refresh, what, err := webhookEvent(provider, body, r.Header)
		if err != nil {
			log.Warn().Err(err).Msg("subscription webhook payload not understood")
			writeWebhookJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		if !refresh {
			log.Debug().Str("event", what).Msg("subscription webhook event ignored")
			writeWebhookJSON(w, http.StatusOK, "ignored: "+what)
			return
		}

		status, err := s.requestRefresh(ctx, sub)
		if err != nil {
			log.Error().Err(err).Msg("subscription webhook: request refresh")
			writeWebhookJSON(w, http.StatusInternalServerError, "internal error")
			return
		}
		log.Info().Str("event", what).Str("result", status).Msg("subscription webhook received")
		writeWebhookJSON(w, http.StatusAccepted, status)
	}
}

// parseSubscriptionWebhookPath splits
// /webhook/subscriptions/<namespace>/<name>/<provider>[/<token>].
func parseSubscriptionWebhookPath(p string) (ns, name, provider, token string, ok bool) {
	rest, found := strings.CutPrefix(p, subscriptionWebhookPrefix)
	if !found {
		return "", "", "", "", false
	}
	parts := strings.Split(strings.TrimSuffix(rest, "/"), "/")
	if len(parts) < 3 || len(parts) > 4 {
		return "", "", "", "", false
	}
	for _, part := range parts {
		if part == "" {
			return "", "", "", "", false
		}
	}
	if !webhookProviders[parts[2]] {
		return "", "", "", "", false
	}
	if len(parts) == 4 {
		token = parts[3]
	}
	return parts[0], parts[1], parts[2], token, true
}

// authenticate returns the Subscription when the delivery carries its
// webhook token, or nil and why not (for the log).
//
// A Subscription that does not exist, or has no spec.webhook, still costs a
// Secret read (webhookTimingPadSecret) and a token check against a random
// key, so its 401 takes about as long as a wrong token's and the timing does
// not tell which Subscriptions exist. The remaining difference is the cache
// read of the Subscription, which both paths do, and the API latency of a
// Secret that exists or not.
func (s *subscriptionWebhook) authenticate(ctx context.Context, ns, name, provider, pathToken string,
	body []byte, h http.Header) (*v1alpha1.Subscription, string) {
	var sub v1alpha1.Subscription
	secretName, miss := "", ""
	switch err := s.client.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &sub); {
	case err != nil:
		miss = "get subscription: " + err.Error()
	case sub.Spec.Webhook == nil || sub.Spec.Webhook.SecretRef.Name == "":
		miss = "the Subscription has no spec.webhook"
	default:
		secretName = sub.Spec.Webhook.SecretRef.Name
	}
	if miss != "" {
		var pad corev1.Secret
		_ = s.client.Get(ctx, client.ObjectKey{Namespace: ns, Name: webhookTimingPadSecret}, &pad)
		_ = verifyWebhook(provider, padToken[:], pathToken, body, h)
		return nil, miss
	}
	secret, err := subscriptionrecon.ReferenceableSecret(ctx, s.client, ns, secretName, "spec.webhook.secretRef")
	if err != nil {
		return nil, err.Error()
	}
	token := secret.Data["token"]
	if len(token) < minWebhookTokenLen {
		return nil, fmt.Sprintf("webhook Secret %q has no key token of at least %d characters", secret.Name, minWebhookTokenLen)
	}
	if verifyWebhook(provider, token, pathToken, body, h) {
		return &sub, ""
	}
	return nil, "no valid token or signature for provider " + provider
}

// padToken is the key the miss path checks against: random, so no delivery
// matches it.
var padToken = func() [32]byte {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return b
}()

// keyedLimiter is a token bucket per key (a source IP, a Subscription), with
// at most max keys: when full, keys idle for 10 minutes are dropped, and if
// none is, the new key is refused (the sender gets 429).
type keyedLimiter struct {
	mu    sync.Mutex
	r     rate.Limit
	burst int
	max   int
	m     map[string]*keyedEntry
}

type keyedEntry struct {
	l    *rate.Limiter
	seen time.Time
}

func newKeyedLimiter(r rate.Limit, burst, max int) *keyedLimiter {
	return &keyedLimiter{r: r, burst: burst, max: max, m: map[string]*keyedEntry{}}
}

func (k *keyedLimiter) allow(key string, now time.Time) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	e, ok := k.m[key]
	if !ok {
		if len(k.m) >= k.max {
			for key, old := range k.m {
				if now.Sub(old.seen) > 10*time.Minute {
					delete(k.m, key)
				}
			}
			if len(k.m) >= k.max {
				return false
			}
		}
		e = &keyedEntry{l: rate.NewLimiter(k.r, k.burst)}
		k.m[key] = e
	}
	e.seen = now
	return e.l.AllowN(now, 1)
}

// verifyWebhook checks the delivery's credential for provider.
func verifyWebhook(provider string, token []byte, pathToken string, body []byte, h http.Header) bool {
	equal := func(got string) bool {
		return got != "" && subtle.ConstantTimeCompare([]byte(got), token) == 1
	}
	mac := hmac.New(sha256.New, token)
	mac.Write(body)
	sum := hex.EncodeToString(mac.Sum(nil))
	signed := func(header string) bool {
		got := strings.TrimPrefix(strings.TrimSpace(h.Get(header)), "sha256=")
		return got != "" && hmac.Equal([]byte(strings.ToLower(got)), []byte(sum))
	}
	switch provider {
	case "ghcr", "github":
		// GitHub always signs a webhook that has a secret; a URL token alone
		// is not accepted.
		return signed("X-Hub-Signature-256")
	case "harbor":
		auth := strings.TrimSpace(h.Get("Authorization"))
		return equal(pathToken) || equal(auth) || equal(strings.TrimPrefix(auth, "Bearer "))
	case "artifactory":
		return equal(pathToken) || equal(h.Get("X-JFrog-Event-Auth")) || signed("X-JFrog-Event-Auth")
	case "generic":
		return equal(pathToken) || signed("X-Kardinal-Signature-256")
	default: // dockerhub, quay: they cannot sign or add headers
		return equal(pathToken)
	}
}

// webhookEvent reports whether the delivery announces a new artifact (a
// push), and names the event for the log and the answer.
func webhookEvent(provider string, body []byte, h http.Header) (bool, string, error) {
	var m map[string]any
	decode := func() error {
		if err := json.Unmarshal(body, &m); err != nil {
			return fmt.Errorf("the %s payload is not a JSON object", provider)
		}
		return nil
	}
	str := func(key string) string {
		v, _ := m[key].(string)
		return v
	}
	switch provider {
	case "ghcr", "github":
		event := h.Get("X-GitHub-Event")
		switch event {
		case "package", "registry_package", "push":
			return true, event, nil
		case "":
			return false, "", fmt.Errorf("the GitHub delivery has no X-GitHub-Event header")
		default:
			return false, event, nil // ping and every other event
		}
	case "dockerhub":
		if err := decode(); err != nil {
			return false, "", err
		}
		if _, ok := m["push_data"]; !ok {
			return false, "", fmt.Errorf("the Docker Hub payload has no push_data")
		}
		return true, "push", nil
	case "quay":
		if err := decode(); err != nil {
			return false, "", err
		}
		if _, ok := m["updated_tags"]; !ok {
			return false, "", fmt.Errorf("the Quay payload has no updated_tags")
		}
		return true, "repo_push", nil
	case "harbor":
		if err := decode(); err != nil {
			return false, "", err
		}
		switch t := str("type"); t {
		case "PUSH_ARTIFACT", "pushImage", "UPLOAD_CHART":
			return true, t, nil
		case "":
			return false, "", fmt.Errorf("the Harbor payload has no type")
		default:
			return false, t, nil
		}
	case "artifactory":
		if err := decode(); err != nil {
			return false, "", err
		}
		switch t := str("event_type"); t {
		case "pushed", "deployed":
			return true, str("domain") + " " + t, nil
		case "":
			return false, "", fmt.Errorf("the Artifactory payload has no event_type")
		default:
			return false, str("domain") + " " + t, nil
		}
	default: // generic: any authenticated delivery is a refresh
		return true, "generic", nil
	}
}

// requestRefresh sets the kardinal.io/refresh annotation unless a refresh is
// already pending. It returns what it did.
func (s *subscriptionWebhook) requestRefresh(ctx context.Context, sub *v1alpha1.Subscription) (string, error) {
	pending := sub.Annotations[v1alpha1.RefreshAnnotation]
	if pending != "" && pending != sub.Status.LastRefreshRequest {
		return "refresh already pending", nil
	}
	patch := client.MergeFrom(sub.DeepCopy())
	if sub.Annotations == nil {
		sub.Annotations = map[string]string{}
	}
	sub.Annotations[v1alpha1.RefreshAnnotation] = s.now().UTC().Format(time.RFC3339Nano)
	if err := s.client.Patch(ctx, sub, patch); err != nil {
		return "", fmt.Errorf("patch subscription %s/%s: %w", sub.Namespace, sub.Name, err)
	}
	s.refreshes.Add(1)
	return "refresh requested", nil
}
