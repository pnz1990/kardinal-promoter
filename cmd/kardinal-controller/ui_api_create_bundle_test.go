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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestUIAPI_CreateBundle_Errors checks that POST /api/v1/ui/bundles applies
// the rules of POST /api/v1/bundles: an invalid spec or a Bundle the API
// server refuses is a 400 with the reason, a missing Pipeline a 404, and
// only an unexpected error a 500. Before, every one of them created the
// Bundle or answered 500 "failed to create bundle".
func TestUIAPI_CreateBundle_Errors(t *testing.T) {
	demo := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "team-a"}}
	invalid := apierrors.NewInvalid(schema.GroupKind{Group: "kardinal.io", Kind: "Bundle"}, "nginx-demo-",
		field.ErrorList{field.Invalid(field.NewPath("spec", "images").Index(0).Child("repository"), "x", "bad repository")})
	tests := []struct {
		name      string
		body      string
		createErr error
		wantCode  int
		wantBody  string
	}{
		{name: "pipeline name that is not an object name",
			body:     `{"pipeline":"Nginx Demo","image":"ghcr.io/example/app:1.0","namespace":"team-a"}`,
			wantCode: http.StatusBadRequest, wantBody: "pipeline must be a valid Kubernetes object name"},
		{name: "pipeline that does not exist",
			body:     `{"pipeline":"ghost","image":"ghcr.io/example/app:1.0","namespace":"team-a"}`,
			wantCode: http.StatusNotFound, wantBody: "pipeline team-a/ghost not found"},
		{name: "pipeline in another namespace",
			body:     `{"pipeline":"nginx-demo","image":"ghcr.io/example/app:1.0"}`,
			wantCode: http.StatusNotFound, wantBody: "pipeline default/nginx-demo not found"},
		{name: "bundle the API server refuses",
			body:      `{"pipeline":"nginx-demo","image":"ghcr.io/example/app:1.0","namespace":"team-a"}`,
			createErr: invalid, wantCode: http.StatusBadRequest, wantBody: "bundle rejected by validation: " + invalid.Error()},
		{name: "create forbidden",
			body:      `{"pipeline":"nginx-demo","image":"ghcr.io/example/app:1.0","namespace":"team-a"}`,
			createErr: apierrors.NewForbidden(schema.GroupResource{Group: "kardinal.io", Resource: "bundles"}, "", nil),
			wantCode:  http.StatusForbidden, wantBody: "forbidden"},
		{name: "unexpected error",
			body:      `{"pipeline":"nginx-demo","image":"ghcr.io/example/app:1.0","namespace":"team-a"}`,
			createErr: apierrors.NewServiceUnavailable("etcd down"),
			wantCode:  http.StatusInternalServerError, wantBody: "failed to create bundle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(demo.DeepCopy()).
				WithInterceptorFuncs(interceptor.Funcs{
					Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						if tt.createErr != nil {
							return tt.createErr
						}
						return c.Create(ctx, obj, opts...)
					},
				}).Build()
			mux := http.NewServeMux()
			newUIAPIServer(c, zerolog.Nop()).RegisterRoutes(mux)

			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/ui/bundles", strings.NewReader(tt.body)))

			assert.Equal(t, tt.wantCode, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), tt.wantBody)
			var bundles v1alpha1.BundleList
			require.NoError(t, c.List(context.Background(), &bundles))
			assert.Empty(t, bundles.Items, "no Bundle is created")
		})
	}
}

// TestUIAPI_CreateBundle_InPipelineNamespace checks the Bundle lands in the
// requested namespace, next to its Pipeline, with the pipeline label.
func TestUIAPI_CreateBundle_InPipelineNamespace(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(&v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "team-a"},
	}).Build()
	mux := http.NewServeMux()
	newUIAPIServer(c, zerolog.Nop()).RegisterRoutes(mux)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/ui/bundles",
		strings.NewReader(`{"pipeline":"nginx-demo","image":"ghcr.io/example/app:1.0","namespace":"team-a"}`)))

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundles, client.InNamespace("team-a")))
	require.Len(t, bundles.Items, 1)
	assert.Equal(t, "nginx-demo", bundles.Items[0].Labels["kardinal.io/pipeline"])
	assert.Equal(t, "image", bundles.Items[0].Spec.Type)
}
