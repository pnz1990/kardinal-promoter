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

// Package uiauth provides Kubernetes TokenReview-based authentication middleware
// for the kardinal UI API server. It validates caller tokens against the Kubernetes
// API server using the authenticationv1.TokenReview API, allowing cluster users to
// access the UI with their existing kubeconfig credentials.
//
// Design reference: docs/design/15-production-readiness.md §Lens 4
package uiauth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// TokenReviewer is an interface for validating bearer tokens via Kubernetes TokenReview.
// The interface allows mocking in unit tests without requiring a live Kubernetes cluster.
type TokenReviewer interface {
	// Review validates a bearer token and returns the authentication status.
	// Returns an error only on transport/API failures — an unauthenticated token
	// returns (status, nil) with Status.Authenticated == false.
	Review(ctx context.Context, token string) (*authv1.TokenReviewStatus, error)
}

// KubeTokenReviewer implements TokenReviewer using the Kubernetes authenticationv1 API.
type KubeTokenReviewer struct {
	clientset kubernetes.Interface
}

// NewKubeTokenReviewer creates a KubeTokenReviewer from a rest.Config.
// The controller's existing rest.Config (from ctrl.GetConfigOrDie()) can be passed directly.
func NewKubeTokenReviewer(cfg *rest.Config) (*KubeTokenReviewer, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("uiauth: creating Kubernetes clientset for TokenReviewer: %w", err)
	}
	return &KubeTokenReviewer{clientset: cs}, nil
}

// Review submits a TokenReview to the Kubernetes API server.
// Timeout is capped at 5 seconds to satisfy O6: fail-closed on slow API servers.
func (r *KubeTokenReviewer) Review(ctx context.Context, token string) (*authv1.TokenReviewStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	review := &authv1.TokenReview{
		Spec: authv1.TokenReviewSpec{
			Token: token,
		},
	}

	result, err := r.clientset.AuthenticationV1().TokenReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("uiauth: TokenReview API call failed: %w", err)
	}
	return &result.Status, nil
}

// Middleware wraps an http.Handler with Kubernetes TokenReview authentication.
//
// Behavior (per spec O2–O6):
//   - Requests to /api/v1/ui/* without an Authorization: Bearer header → 401
//   - Requests with a token → TokenReview → 401 if Authenticated=false
//   - TokenReview API failure → 503 (fail-closed, not 200)
//   - Requests to /ui/* (static assets) → pass through unchanged (O5)
//
// Only applied when --ui-tokenreview-auth=true AND --ui-auth-token is not set.
// When both flags are set, the static token middleware takes precedence (O4).
func Middleware(next http.Handler, reviewer TokenReviewer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// O5: Static assets at /ui/* are never gated.
		if !strings.HasPrefix(r.URL.Path, "/api/v1/ui/") {
			next.ServeHTTP(w, r)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			// O2: Missing or malformed Authorization header.
			w.Header().Set("Www-Authenticate", `Bearer realm="kardinal-ui"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(authHeader, "Bearer ")

		// O3: Call Kubernetes TokenReview.
		status, err := reviewer.Review(r.Context(), token)
		if err != nil {
			// O6: API failure → fail-closed with 503.
			http.Error(w, "auth unavailable", http.StatusServiceUnavailable)
			return
		}
		if !status.Authenticated {
			// O3: TokenReview returned Authenticated=false.
			w.Header().Set("Www-Authenticate", `Bearer realm="kardinal-ui"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}
