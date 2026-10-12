// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
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
