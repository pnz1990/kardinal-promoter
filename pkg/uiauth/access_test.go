// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package uiauth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/uiauth"
)

// fakeAccess records every SubjectAccessReview and answers from a rule func.
type fakeAccess struct {
	mu    sync.Mutex
	calls []authzv1.ResourceAttributes
	users []string
	allow func(user string, a authzv1.ResourceAttributes) bool
	err   error
}

func (f *fakeAccess) Allowed(_ context.Context, u authv1.UserInfo, a authzv1.ResourceAttributes) (bool, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, a)
	f.users = append(f.users, u.Username)
	if f.err != nil {
		return false, "", f.err
	}
	if f.allow != nil && f.allow(u.Username, a) {
		return true, "", nil
	}
	return false, "no RBAC policy matched", nil
}

func uiauthScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

func uiauthPipeline(ns, name string) *v1alpha1.Pipeline {
	return &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
}

// TestAuthorizingClient_ChecksEachCall verifies that every client call is
// checked with a SubjectAccessReview for the right verb, group, resource,
// namespace and name, and that a denial or review failure stops the call.
func TestAuthorizingClient_ChecksEachCall(t *testing.T) {
	alice := authv1.UserInfo{Username: "alice", Groups: []string{"devs"}}
	allowAll := func(string, authzv1.ResourceAttributes) bool { return true }

	tests := []struct {
		name      string
		scope     string
		user      *authv1.UserInfo
		allow     func(string, authzv1.ResourceAttributes) bool
		reviewErr error
		op        func(ctx context.Context, c client.Client) error
		wantAttrs *authzv1.ResourceAttributes
		wantErr   func(error) bool
		wantPause *bool // expected spec.paused of team-a/app afterwards
	}{
		{
			name: "get is checked with name and namespace", user: &alice, allow: allowAll,
			op: func(ctx context.Context, c client.Client) error {
				return c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "app"}, &v1alpha1.Pipeline{})
			},
			wantAttrs: &authzv1.ResourceAttributes{Verb: "get", Group: "kardinal.io", Resource: "pipelines", Namespace: "team-a", Name: "app"},
		},
		{
			name: "list without namespace is cluster-wide", user: &alice, allow: allowAll,
			op: func(ctx context.Context, c client.Client) error {
				return c.List(ctx, &v1alpha1.BundleList{})
			},
			wantAttrs: &authzv1.ResourceAttributes{Verb: "list", Group: "kardinal.io", Resource: "bundles"},
		},
		{
			name: "list without namespace uses the scope namespace", scope: "team-a", user: &alice, allow: allowAll,
			op: func(ctx context.Context, c client.Client) error {
				return c.List(ctx, &v1alpha1.PolicyGateList{})
			},
			wantAttrs: &authzv1.ResourceAttributes{Verb: "list", Group: "kardinal.io", Resource: "policygates", Namespace: "team-a"},
		},
		{
			name: "list in a namespace", user: &alice, allow: allowAll,
			op: func(ctx context.Context, c client.Client) error {
				return c.List(ctx, &v1alpha1.PromotionStepList{}, client.InNamespace("team-b"))
			},
			wantAttrs: &authzv1.ResourceAttributes{Verb: "list", Group: "kardinal.io", Resource: "promotionsteps", Namespace: "team-b"},
		},
		{
			name: "create is checked in the object's namespace", user: &alice, allow: allowAll,
			op: func(ctx context.Context, c client.Client) error {
				return c.Create(ctx, &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "b1"},
					Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "app"}})
			},
			wantAttrs: &authzv1.ResourceAttributes{Verb: "create", Group: "kardinal.io", Resource: "bundles", Namespace: "team-a", Name: "b1"},
		},
		{
			name: "update allowed changes the object", user: &alice, allow: allowAll,
			op: func(ctx context.Context, c client.Client) error {
				var p v1alpha1.Pipeline
				if err := c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "app"}, &p); err != nil {
					return err
				}
				p.Spec.Paused = true
				return c.Update(ctx, &p)
			},
			wantAttrs: &authzv1.ResourceAttributes{Verb: "update", Group: "kardinal.io", Resource: "pipelines", Namespace: "team-a", Name: "app"},
			wantPause: ptrBool(true),
		},
		{
			name: "update denied leaves the object unchanged", user: &alice,
			allow: func(_ string, a authzv1.ResourceAttributes) bool { return a.Verb == "get" },
			op: func(ctx context.Context, c client.Client) error {
				var p v1alpha1.Pipeline
				if err := c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "app"}, &p); err != nil {
					return err
				}
				p.Spec.Paused = true
				return c.Update(ctx, &p)
			},
			wantAttrs: &authzv1.ResourceAttributes{Verb: "update", Group: "kardinal.io", Resource: "pipelines", Namespace: "team-a", Name: "app"},
			wantErr:   apierrors.IsForbidden,
			wantPause: ptrBool(false),
		},
		{
			name: "delete is checked", user: &alice, allow: allowAll,
			op: func(ctx context.Context, c client.Client) error {
				return c.Delete(ctx, uiauthPipeline("team-a", "app"))
			},
			wantAttrs: &authzv1.ResourceAttributes{Verb: "delete", Group: "kardinal.io", Resource: "pipelines", Namespace: "team-a", Name: "app"},
		},
		{
			name: "no user in context is refused without a review",
			op: func(ctx context.Context, c client.Client) error {
				return c.List(ctx, &v1alpha1.PipelineList{})
			},
			wantErr: apierrors.IsUnauthorized,
		},
		{
			name: "review API failure is refused", user: &alice, reviewErr: errors.New("apiserver down"),
			op: func(ctx context.Context, c client.Client) error {
				return c.List(ctx, &v1alpha1.PipelineList{})
			},
			wantAttrs: &authzv1.ResourceAttributes{Verb: "list", Group: "kardinal.io", Resource: "pipelines"},
			wantErr:   apierrors.IsServiceUnavailable,
		},
		{
			name: "status writes are refused", user: &alice, allow: allowAll,
			op: func(ctx context.Context, c client.Client) error {
				return c.Status().Update(ctx, uiauthPipeline("team-a", "app"))
			},
			wantErr: apierrors.IsMethodNotSupported,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := fake.NewClientBuilder().WithScheme(uiauthScheme(t)).
				WithObjects(uiauthPipeline("team-a", "app")).Build()
			access := &fakeAccess{allow: tt.allow, err: tt.reviewErr}
			c := uiauth.NewAuthorizingClient(base, access, tt.scope)

			ctx := context.Background()
			if tt.user != nil {
				ctx = uiauth.WithUser(ctx, *tt.user)
			}
			err := tt.op(ctx, c)
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.True(t, tt.wantErr(err), "unexpected error: %v", err)
			} else {
				require.NoError(t, err)
			}

			if tt.wantAttrs == nil {
				assert.Empty(t, access.calls)
			} else {
				require.NotEmpty(t, access.calls)
				assert.Equal(t, *tt.wantAttrs, access.calls[len(access.calls)-1])
				assert.Equal(t, "alice", access.users[len(access.users)-1])
			}
			if tt.wantPause != nil {
				var p v1alpha1.Pipeline
				require.NoError(t, base.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: "app"}, &p))
				assert.Equal(t, *tt.wantPause, p.Spec.Paused)
			}
		})
	}
}

func ptrBool(b bool) *bool { return &b }

// TestMiddleware_AuthorizesHandlerWrites runs a handler that writes through
// the AuthorizingClient behind the TokenReview middleware. Regression test for
// C07-controller-06/07: TokenReview mode authenticated but never authorized, so
// any ServiceAccount token could pause a pipeline.
func TestMiddleware_AuthorizesHandlerWrites(t *testing.T) {
	// pauseHandler mimics the UI handlers: it maps any client error to 500,
	// or ignores it entirely when ignoreErr is set.
	pauseHandler := func(c client.Client, ignoreErr bool) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var p v1alpha1.Pipeline
			if err := c.Get(r.Context(), client.ObjectKey{Namespace: "team-a", Name: "app"}, &p); err != nil && !ignoreErr {
				http.Error(w, "get failed: "+err.Error(), http.StatusInternalServerError)
				return
			}
			p.Spec.Paused = true
			if err := c.Update(r.Context(), &p); err != nil && !ignoreErr {
				http.Error(w, "update failed: "+err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"paused":true}`))
		})
	}
	operator := func(user string, a authzv1.ResourceAttributes) bool {
		return user == "system:serviceaccount:team-a:deployer" && a.Namespace == "team-a"
	}
	readOnly := func(user string, a authzv1.ResourceAttributes) bool {
		return a.Verb == "get" || a.Verb == "list"
	}

	tests := []struct {
		name       string
		user       string
		allow      func(string, authzv1.ResourceAttributes) bool
		reviewErr  error
		ignoreErr  bool
		wantCode   int
		wantPaused bool
	}{
		{name: "allowed user pauses", user: "system:serviceaccount:team-a:deployer", allow: operator,
			wantCode: http.StatusOK, wantPaused: true},
		{name: "unrelated service account is forbidden", user: "system:serviceaccount:random-ns:default", allow: operator,
			wantCode: http.StatusForbidden},
		{name: "read-only user cannot update", user: "viewer", allow: readOnly,
			wantCode: http.StatusForbidden},
		{name: "denial wins even if the handler ignores errors", user: "viewer", allow: readOnly, ignoreErr: true,
			wantCode: http.StatusForbidden},
		{name: "review failure is 503", user: "system:serviceaccount:team-a:deployer", reviewErr: errors.New("down"),
			wantCode: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := fake.NewClientBuilder().WithScheme(uiauthScheme(t)).
				WithObjects(uiauthPipeline("team-a", "app")).Build()
			access := &fakeAccess{allow: tt.allow, err: tt.reviewErr}
			authz := uiauth.NewAuthorizingClient(base, access, "")
			tr := &userReviewer{user: authv1.UserInfo{Username: tt.user}}
			h := uiauth.Middleware(pauseHandler(authz, tt.ignoreErr), tr)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/ui/pause", nil)
			req.Header.Set("Authorization", "Bearer sa-token")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantCode, rec.Code, rec.Body.String())
			if tt.wantCode != http.StatusOK {
				assert.NotContains(t, rec.Body.String(), `"paused":true`)
			}
			var p v1alpha1.Pipeline
			require.NoError(t, base.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: "app"}, &p))
			assert.Equal(t, tt.wantPaused, p.Spec.Paused)
		})
	}
}

// userReviewer authenticates every token as a fixed user.
type userReviewer struct {
	user  authv1.UserInfo
	calls int
}

func (r *userReviewer) Review(_ context.Context, _ string) (*authv1.TokenReviewStatus, error) {
	r.calls++
	return &authv1.TokenReviewStatus{Authenticated: true, User: r.user}, nil
}

// TestMiddleware_StoresUser verifies the reviewed identity reaches handlers.
func TestMiddleware_StoresUser(t *testing.T) {
	tr := &userReviewer{user: authv1.UserInfo{Username: "alice", Groups: []string{"devs"}}}
	var got authv1.UserInfo
	var ok bool
	h := uiauth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = uiauth.UserFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}), tr)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/pipelines", nil)
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	require.True(t, ok)
	assert.Equal(t, "alice", got.Username)
	assert.Equal(t, []string{"devs"}, got.Groups)
}

// TestAuthorizingClient_ListFallsBackToNamespaceRBAC: an all-namespaces
// list by a user who may not list cluster-wide returns the items of the
// namespaces where the user may list, without recording a denial; a user
// who may list nowhere is denied (403); one who may list cluster-wide gets
// everything with one review.
func TestAuthorizingClient_ListFallsBackToNamespaceRBAC(t *testing.T) {
	objs := []client.Object{uiauthPipeline("team-a", "a1"), uiauthPipeline("team-a", "a2"),
		uiauthPipeline("team-b", "b1"), uiauthPipeline("team-c", "c1")}
	tests := []struct {
		name      string
		allow     func(string, authzv1.ResourceAttributes) bool
		want      []string
		wantForb  bool
		wantCalls int
	}{
		{name: "cluster-wide viewer", allow: func(_ string, a authzv1.ResourceAttributes) bool { return true },
			want: []string{"team-a/a1", "team-a/a2", "team-b/b1", "team-c/c1"}, wantCalls: 1},
		{name: "viewer in team-a and team-c", allow: func(_ string, a authzv1.ResourceAttributes) bool {
			return a.Namespace == "team-a" || a.Namespace == "team-c"
		}, want: []string{"team-a/a1", "team-a/a2", "team-c/c1"}, wantCalls: 4},
		{name: "viewer nowhere", allow: func(string, authzv1.ResourceAttributes) bool { return false }, wantForb: true},
		{name: "may list other kinds only", allow: func(_ string, a authzv1.ResourceAttributes) bool {
			return a.Resource == "bundles"
		}, wantForb: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(uiauthScheme(t)).WithObjects(objs...).Build()
			access := &fakeAccess{allow: tt.allow}
			ac := uiauth.NewAuthorizingClient(c, access, "")
			ctx := uiauth.WithUser(context.Background(), authv1.UserInfo{Username: "alice"})
			var list v1alpha1.PipelineList
			err := ac.List(ctx, &list)
			if tt.wantForb {
				require.Error(t, err)
				assert.True(t, apierrors.IsForbidden(err), "%v", err)
				return
			}
			require.NoError(t, err)
			var got []string
			for _, p := range list.Items {
				got = append(got, p.Namespace+"/"+p.Name)
			}
			assert.ElementsMatch(t, tt.want, got)
			if tt.wantCalls > 0 {
				assert.Len(t, access.calls, tt.wantCalls, "%+v", access.calls)
			}
			for _, a := range access.calls {
				assert.Equal(t, "list", a.Verb)
				assert.Equal(t, "pipelines", a.Resource)
			}
		})
	}
}

// TestMiddlewareFor_GuardsItsPrefixOnly: MiddlewareFor checks tokens on its
// prefix with its realm and lets other paths through.
func TestMiddlewareFor_GuardsItsPrefixOnly(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := uiauth.UserFrom(r.Context())
		if ok {
			_, _ = w.Write([]byte(u.Username))
		}
	})
	h := uiauth.MiddlewareFor(next, &userReviewer{user: authv1.UserInfo{Username: "ci-bot"}}, "/api/v1/bundles", "kardinal-bundle-api")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/bundles", nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, `Bearer realm="kardinal-bundle-api"`, w.Header().Get("Www-Authenticate"))
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bundles", nil)
	req.Header.Set("Authorization", "Bearer x")
	h.ServeHTTP(w, req)
	assert.Equal(t, "ci-bot", w.Body.String())
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/webhook/scm/health", nil))
	assert.Equal(t, http.StatusOK, w.Code, "other paths are not guarded")
}
