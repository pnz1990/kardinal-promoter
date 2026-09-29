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

// Package main (bundle_api.go) implements the POST /api/v1/bundles endpoint
// that allows CI systems to create Bundle CRDs via HTTP.
package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

const (
	// bundleAPIMaxBody is the maximum request body size for the bundle API (1 MB).
	bundleAPIMaxBody = 1 << 20
	// bundleRateLimit is the maximum number of Bundle creation requests per minute.
	// There is one bundle API token, so this cap is shared by every CI caller.
	bundleRateLimit = 60
)

// bundleCreateRequest is the JSON body accepted by POST /api/v1/bundles.
type bundleCreateRequest struct {
	// Pipeline is the target Pipeline name.
	Pipeline string `json:"pipeline"`
	// Type is the bundle type: "image", "config", or "mixed".
	Type string `json:"type"`
	// Namespace is the target namespace. Defaults to the server's default namespace.
	Namespace string `json:"namespace,omitempty"`
	// Images lists the container images in this Bundle.
	Images []v1alpha1.ImageRef `json:"images,omitempty"`
	// ConfigRef is the GitOps commit for "config" and "mixed" bundles.
	ConfigRef *v1alpha1.ConfigRef `json:"configRef,omitempty"`
	// Provenance carries build metadata.
	Provenance *v1alpha1.BundleProvenance `json:"provenance,omitempty"`
	// Intent limits or shapes the promotion (targetEnvironment, skipEnvironments).
	Intent *v1alpha1.BundleIntent `json:"intent,omitempty"`
}

// bundleCreateResponse is the JSON response for POST /api/v1/bundles.
type bundleCreateResponse struct {
	// Name is the name of the created Bundle CRD.
	Name string `json:"name"`
	// Namespace is the namespace of the created Bundle CRD.
	Namespace string `json:"namespace"`
}

// tokenRateLimiter tracks per-token request counts for the current minute window.
type tokenRateLimiter struct {
	mu      sync.Mutex
	counts  map[string]int
	resetAt time.Time
	limit   int
}

func newTokenRateLimiter(limit int) *tokenRateLimiter {
	return &tokenRateLimiter{
		counts:  make(map[string]int),
		resetAt: time.Now().Add(time.Minute),
		limit:   limit,
	}
}

// Allow returns true if the token is within the rate limit for the current minute.
func (r *tokenRateLimiter) Allow(token string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if now.After(r.resetAt) {
		r.counts = make(map[string]int)
		r.resetAt = now.Add(time.Minute)
	}
	r.counts[token]++
	return r.counts[token] <= r.limit
}

// bundleAPIServer handles POST /api/v1/bundles requests.
type bundleAPIServer struct {
	client    client.Client
	token     string
	namespace string
	// onlyNamespace, when set (--watch-namespace), is the only namespace the API
	// may create Bundles in; the controller does not reconcile any other.
	onlyNamespace string
	limiter       *tokenRateLimiter
	log           zerolog.Logger
}

// newBundleAPIServer constructs a bundleAPIServer.
func newBundleAPIServer(k8s client.Client, token, namespace string) *bundleAPIServer {
	return &bundleAPIServer{
		client:    k8s,
		token:     token,
		namespace: namespace,
		limiter:   newTokenRateLimiter(bundleRateLimit),
		log:       zerolog.Nop(),
	}
}

// newBundleAPIServerWithLogger constructs a bundleAPIServer with a logger.
func newBundleAPIServerWithLogger(k8s client.Client, token, namespace string, log zerolog.Logger) *bundleAPIServer {
	s := newBundleAPIServer(k8s, token, namespace)
	s.log = log
	return s
}

// Handler returns an http.HandlerFunc for POST /api/v1/bundles.
func (s *bundleAPIServer) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Authenticate via Bearer token.
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		providedToken := strings.TrimPrefix(authHeader, "Bearer ")
		if s.token == "" || subtle.ConstantTimeCompare([]byte(providedToken), []byte(s.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// Rate limit (one token, so one shared window for all callers).
		if !s.limiter.Allow(providedToken) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		// Read body with size limit.
		body, err := io.ReadAll(io.LimitReader(r.Body, bundleAPIMaxBody+1))
		if err != nil || len(body) > bundleAPIMaxBody {
			http.Error(w, "request body too large or unreadable", http.StatusBadRequest)
			return
		}

		// Reject unknown fields so a misspelt or unsupported key (for example
		// "intent" before it was supported) fails loudly instead of being dropped.
		var req bundleCreateRequest
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)
			return
		}
		if msg := validateBundleCreateRequest(&req); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}

		// Determine namespace.
		ns := req.Namespace
		if ns == "" {
			ns = s.namespace
		}
		if s.onlyNamespace != "" && ns != s.onlyNamespace {
			http.Error(w, fmt.Sprintf("namespace %q is not watched by this controller", ns), http.StatusForbidden)
			return
		}

		// The Pipeline must exist in the target namespace. This also stops the
		// token from creating Bundles in namespaces that have no such Pipeline.
		var pipeline v1alpha1.Pipeline
		if err := s.client.Get(r.Context(), client.ObjectKey{Namespace: ns, Name: req.Pipeline}, &pipeline); err != nil {
			if apierrors.IsNotFound(err) {
				http.Error(w, fmt.Sprintf("pipeline %s/%s not found", ns, req.Pipeline), http.StatusNotFound)
				return
			}
			s.log.Error().Err(err).Str("namespace", ns).Str("pipeline", req.Pipeline).Msg("bundle API: failed to get pipeline")
			http.Error(w, "failed to look up pipeline", http.StatusInternalServerError)
			return
		}

		// Set timestamp on provenance if not provided.
		now := time.Now().UTC()
		if req.Provenance == nil {
			req.Provenance = &v1alpha1.BundleProvenance{}
		}
		if req.Provenance.Timestamp.IsZero() {
			req.Provenance.Timestamp = metav1.NewTime(now)
		}

		// Name: pipeline-YYYYMMDDHHMMSS-<random>. GenerateName makes the API
		// server pick a unique suffix, so concurrent requests cannot collide.
		bundle := &v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: fmt.Sprintf("%s-%s-", sanitizeName(req.Pipeline), now.Format("20060102150405")),
				Namespace:    ns,
				Labels: map[string]string{
					"kardinal.io/pipeline": req.Pipeline,
				},
			},
			Spec: v1alpha1.BundleSpec{
				Type:       req.Type,
				Pipeline:   req.Pipeline,
				Images:     req.Images,
				ConfigRef:  req.ConfigRef,
				Provenance: req.Provenance,
				Intent:     req.Intent,
			},
		}
		lifecycle.StampCreatedAt(bundle, now) // sub-second creation order for supersession

		if err := s.client.Create(r.Context(), bundle); err != nil {
			s.log.Error().Err(err).Str("namespace", ns).Str("pipeline", req.Pipeline).Msg("failed to create bundle")
			switch {
			case apierrors.IsInvalid(err):
				http.Error(w, "bundle rejected by validation", http.StatusBadRequest)
			case apierrors.IsAlreadyExists(err):
				http.Error(w, "bundle already exists", http.StatusConflict)
			default:
				http.Error(w, "failed to create bundle", http.StatusInternalServerError)
			}
			return
		}
		name := bundle.Name

		s.log.Info().
			Str("name", name).
			Str("namespace", ns).
			Str("pipeline", req.Pipeline).
			Msg("bundle created via API")

		resp := bundleCreateResponse{Name: name, Namespace: ns}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if encErr := json.NewEncoder(w).Encode(resp); encErr != nil {
			s.log.Error().Err(encErr).Msg("failed to encode bundle create response")
		}
	}
}

// validateBundleCreateRequest checks the request and fills in defaults. It
// returns a client-facing error message, or "" when the request is valid.
func validateBundleCreateRequest(req *bundleCreateRequest) string {
	if req.Pipeline == "" {
		return "pipeline is required"
	}
	if len(validation.IsDNS1123Subdomain(req.Pipeline)) > 0 || len(validation.IsValidLabelValue(req.Pipeline)) > 0 {
		return "pipeline must be a valid Kubernetes object name of at most 63 characters"
	}
	if req.Type == "" {
		req.Type = "image"
	}
	needImages, needConfig := false, false
	switch req.Type {
	case "image":
		needImages = true
	case "config":
		needConfig = true
	case "mixed":
		needImages, needConfig = true, true
	default:
		return fmt.Sprintf("type must be one of image, config, mixed (got %q)", req.Type)
	}
	if needImages && len(req.Images) == 0 {
		return fmt.Sprintf("type %q requires at least one entry in images", req.Type)
	}
	if needConfig && (req.ConfigRef == nil || req.ConfigRef.CommitSHA == "") {
		return fmt.Sprintf("type %q requires configRef.commitSHA", req.Type)
	}
	return ""
}

// sanitizeName replaces characters not valid in Kubernetes names with hyphens.
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
