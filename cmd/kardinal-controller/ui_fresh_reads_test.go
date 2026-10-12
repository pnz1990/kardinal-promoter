// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/uiauth"
)

// TestUIAPI_ResumeRightAfterPauseReadsTheAPIServer: the UI's client reads
// an informer cache that can lag a write. A resume sent right after a pause
// read the Pipeline still unpaused there, found nothing to change and left it
// paused (TestUI_APIConformsToOpenAPI timed out). The lifecycle actions read
// the object they write through the API reader (freshGets), so the resume
// writes. The control case shows the stale cache alone loses the resume.
func TestUIAPI_ResumeRightAfterPauseReadsTheAPIServer(t *testing.T) {
	for _, fresh := range []bool{true, false} {
		t.Run(map[bool]string{true: "API reader", false: "cache only"}[fresh], func(t *testing.T) {
			p := uiLcPipeline()
			p.Spec.Paused = true // the pause was written
			apiServer := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(p).
				WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
			// The cache has not seen the pause yet.
			cached := interceptor.NewClient(apiServer, interceptor.Funcs{
				Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if err := cl.Get(ctx, key, obj, opts...); err != nil {
						return err
					}
					if pl, ok := obj.(*v1alpha1.Pipeline); ok {
						pl.Spec.Paused = false
					}
					return nil
				}})
			s := newUIAPIServer(cached, zerolog.Nop())
			if fresh {
				s.apiReader = apiServer
			}
			mux := http.NewServeMux()
			s.RegisterRoutes(mux)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/ui/resume", strings.NewReader(`{"pipeline":"app"}`))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())

			var got v1alpha1.Pipeline
			require.NoError(t, apiServer.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "app"}, &got))
			assert.Equal(t, !fresh, got.Spec.Paused, "resumed only when the action reads the API server")
		})
	}
}

// countingReader counts the Gets that reach the API server.
type countingReader struct {
	client.Reader
	gets int
}

func (r *countingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	r.gets++
	return r.Reader.Get(ctx, key, obj, opts...)
}

// TestUIAPI_ActionsStayInTheWatchNamespace: with --watch-namespace a
// lifecycle action on another namespace is refused (400) before any read,
// so the uncached, controller-privileged reader never reaches outside the
// namespace the controller watches. The watched namespace works.
func TestUIAPI_ActionsStayInTheWatchNamespace(t *testing.T) {
	p := uiLcPipeline()
	p.Spec.Paused = true
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(p).
		WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	reader := &countingReader{Reader: c}
	s := newUIAPIServer(c, zerolog.Nop())
	s.apiReader, s.scopeNamespace = reader, "default"
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/ui/resume", strings.NewReader(body)))
		return w
	}
	for _, path := range []string{"/api/v1/ui/pause", "/api/v1/ui/resume"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"pipeline":"app","namespace":"kube-system"}`)))
		assert.Equal(t, http.StatusBadRequest, w.Code, path)
		assert.Contains(t, w.Body.String(), "the controller watches namespace default only")
	}
	assert.Zero(t, reader.gets, "nothing outside the watch namespace is read")

	w := post(`{"pipeline":"app","namespace":"default"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Positive(t, reader.gets)
}

// TestUIAPI_TokenReviewActionsReadTheAPIServer: in TokenReview mode the
// action runs on the controller's privileged client (actionClient), and it
// too reads the object it writes through the API reader newUIHandler is
// given: a resume right after a pause, against a cache that has not seen the
// pause, resumes. main gives newUIHandler the manager's API reader.
func TestUIAPI_TokenReviewActionsReadTheAPIServer(t *testing.T) {
	cfg, _ := fakeKubeAuthAPI(t)
	review := reviewOptions{audiences: []string{uiauth.DefaultAudience}, shared: &sharedReviewers{}}
	auth, err := buildUIAuth(cfg, uiAuthFlags{tokenReview: true, review: review})
	require.NoError(t, err)
	p := uiLcPipeline()
	p.Spec.Paused = true
	apiServer := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(p).
		WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	cached := interceptor.NewClient(apiServer, interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if err := cl.Get(ctx, key, obj, opts...); err != nil {
				return err
			}
			if pl, ok := obj.(*v1alpha1.Pipeline); ok {
				pl.Spec.Paused = false
			}
			return nil
		}})
	ui := newUIHandler(cached, apiServer, nil, auth, "", nil, zerolog.Nop())
	rec := uiAuthDo(t, ui, http.MethodPost, "/api/v1/ui/resume", "Bearer tok-alice", `{"pipeline":"app"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got v1alpha1.Pipeline
	require.NoError(t, apiServer.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "app"}, &got))
	assert.False(t, got.Spec.Paused, "the privileged action read the pause through the API reader")

	src, err := os.ReadFile("main.go")
	require.NoError(t, err)
	assert.Contains(t, string(src), "newUIHandler(mgr.GetClient(), mgr.GetAPIReader(),",
		"main gives the UI handler the manager's API reader")
}
