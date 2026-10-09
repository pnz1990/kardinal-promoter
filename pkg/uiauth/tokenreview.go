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

// Package uiauth provides Kubernetes TokenReview authentication and
// SubjectAccessReview authorization for the kardinal UI API server. Middleware
// validates caller tokens with the authenticationv1.TokenReview API, so cluster
// users reach the UI with their existing credentials, and AuthorizingClient
// checks every object the UI API reads or writes against the caller's RBAC.
// Both reviews are cached briefly (NewCachedTokenReviewer,
// NewCachedAccessReviewer).
//
// Design reference: docs/design/15-production-readiness.md §Lens 4
package uiauth

import (
	"context"
	"errors"
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

// DefaultAudience is the token audience kardinal accepts by default: mint
// tokens for it with kubectl create token <sa> --audience kardinal-promoter.
const DefaultAudience = "kardinal-promoter"

// KubeTokenReviewer implements TokenReviewer using the Kubernetes authenticationv1 API.
type KubeTokenReviewer struct {
	clientset kubernetes.Interface
	// audiences are the token audiences accepted. A token is accepted when
	// the API server returns one of them in status.audiences.
	audiences []string
	// acceptAPIServer also accepts tokens for the API server's own audience
	// (any kubeconfig token): an explicit opt-in, since such a token is
	// meant for the API server and a service that receives one can replay it.
	acceptAPIServer bool
}

// NewKubeTokenReviewer creates a KubeTokenReviewer from a rest.Config. It
// accepts tokens whose audience is one of audiences, and, when
// acceptAPIServer is set, tokens for the API server's audience too. With no
// audiences acceptAPIServer must be set.
func NewKubeTokenReviewer(cfg *rest.Config, audiences []string, acceptAPIServer bool) (*KubeTokenReviewer, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("uiauth: creating Kubernetes clientset for TokenReviewer: %w", err)
	}
	return newKubeTokenReviewer(cs, audiences, acceptAPIServer)
}

func newKubeTokenReviewer(cs kubernetes.Interface, audiences []string, acceptAPIServer bool) (*KubeTokenReviewer, error) {
	if len(audiences) == 0 && !acceptAPIServer {
		return nil, fmt.Errorf("uiauth: no token audience: set an audience or accept the API server audience")
	}
	return &KubeTokenReviewer{clientset: cs, audiences: audiences, acceptAPIServer: acceptAPIServer}, nil
}

// Review submits a TokenReview to the Kubernetes API server, first for the
// configured audiences and then, when allowed, for the API server's own.
// Timeout is capped at 5 seconds to satisfy O6: fail-closed on slow API servers.
func (r *KubeTokenReviewer) Review(ctx context.Context, token string) (*authv1.TokenReviewStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if len(r.audiences) > 0 {
		st, err := r.review(ctx, token, r.audiences)
		if err != nil {
			return nil, err
		}
		// An authenticator that is not audience-aware authenticates the
		// token but returns no audience: the token is for the API server.
		if st.Authenticated && hasAudience(st.Audiences, r.audiences) {
			return st, nil
		}
		if !r.acceptAPIServer {
			return &authv1.TokenReviewStatus{Authenticated: false,
				Error: fmt.Sprintf("token audience is not %s", strings.Join(r.audiences, " or "))}, nil
		}
	}
	return r.review(ctx, token, nil)
}

func (r *KubeTokenReviewer) review(ctx context.Context, token string, audiences []string) (*authv1.TokenReviewStatus, error) {
	review := &authv1.TokenReview{Spec: authv1.TokenReviewSpec{Token: token, Audiences: audiences}}
	result, err := r.clientset.AuthenticationV1().TokenReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("uiauth: TokenReview API call failed: %w", err)
	}
	return &result.Status, nil
}

func hasAudience(got, want []string) bool {
	for _, g := range got {
		for _, w := range want {
			if g == w {
				return true
			}
		}
	}
	return false
}

// Middleware wraps an http.Handler with Kubernetes TokenReview authentication.
//
// Behavior (per spec O2–O6):
//   - Requests to /api/v1/ui/* without an Authorization: Bearer header → 401
//   - Requests with a token → TokenReview → 401 if Authenticated=false
//   - TokenReview API failure → 503 (fail-closed, not 200)
//   - Requests to /ui/* (static assets) → pass through unchanged (O5)
//
// The authenticated user is stored in the request context (see UserFrom) for
// AuthorizingClient, which authorizes each read and write with a
// SubjectAccessReview. If any of those checks is denied while the request is
// served, the response is replaced with 403 (or 503 when the review API
// failed), whatever the handler tried to write.
//
// Only applied when --ui-tokenreview-auth=true AND --ui-auth-token is not set.
// When both flags are set, the static token middleware takes precedence (O4).
func Middleware(next http.Handler, reviewer TokenReviewer) http.Handler {
	return MiddlewareFor(next, reviewer, "/api/v1/ui/", "kardinal-ui")
}

// MiddlewareFor is Middleware for the paths under prefix, answering 401 with
// the Bearer realm realm. The Bundle API uses it with "/api/v1/bundles".
func MiddlewareFor(next http.Handler, reviewer TokenReviewer, prefix, realm string) http.Handler {
	challenge := `Bearer realm="` + realm + `"`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// O5: Static assets at /ui/* are never gated.
		if !strings.HasPrefix(r.URL.Path, prefix) {
			next.ServeHTTP(w, r)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			// O2: Missing or malformed Authorization header.
			w.Header().Set("Www-Authenticate", challenge)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(authHeader, "Bearer ")

		// O3: Call Kubernetes TokenReview.
		status, err := reviewer.Review(withClientAddr(r.Context(), r.RemoteAddr), token)
		if errors.Is(err, ErrRateLimited) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		if err != nil {
			// O6: API failure → fail-closed with 503.
			http.Error(w, "auth unavailable", http.StatusServiceUnavailable)
			return
		}
		if !status.Authenticated {
			// O3: TokenReview returned Authenticated=false.
			w.Header().Set("Www-Authenticate", challenge)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		ctx := WithUser(r.Context(), status.User)
		ctx, state := withRequestAuth(ctx)
		gw := &guardedWriter{ResponseWriter: w, state: state}
		next.ServeHTTP(gw, r.WithContext(ctx))
		gw.finish()
	})
}

// guardedWriter replaces the handler's response with the recorded
// authorization failure, if there is one, the first time the handler writes.
type guardedWriter struct {
	http.ResponseWriter
	state    *requestAuth
	wrote    bool
	replaced bool
}

func (g *guardedWriter) check() {
	if g.wrote {
		return
	}
	g.wrote = true
	if d := g.state.get(); d != nil {
		g.replaced = true
		if d.code == http.StatusUnauthorized {
			g.ResponseWriter.Header().Set("Www-Authenticate", `Bearer realm="kardinal-ui"`)
		}
		http.Error(g.ResponseWriter, d.msg, d.code)
	}
}

func (g *guardedWriter) WriteHeader(code int) {
	g.check()
	if !g.replaced {
		g.ResponseWriter.WriteHeader(code)
	}
}

func (g *guardedWriter) Write(b []byte) (int, error) {
	g.check()
	if g.replaced {
		return len(b), nil
	}
	return g.ResponseWriter.Write(b)
}

// finish writes the recorded failure when the handler returned without
// writing anything.
func (g *guardedWriter) finish() { g.check() }
