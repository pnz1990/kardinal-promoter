// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

var uiLcT0 = time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

func uiLcPipeline() *v1alpha1.Pipeline {
	return &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default", UID: "uid-app"},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{
			{Name: "test"}, {Name: "uat"}, {Name: "prod"},
		}},
	}
}

func uiLcBundle(name, tag string, minute int) *v1alpha1.Bundle {
	return &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "default",
			CreationTimestamp: metav1.NewTime(uiLcT0.Add(time.Duration(minute) * time.Minute)),
		},
		Spec: v1alpha1.BundleSpec{
			Type: "image", Pipeline: "app",
			Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: tag}},
		},
		Status: v1alpha1.BundleStatus{Phase: "Superseded"},
	}
}

func uiLcStep(bundle, env, state string, minute int) *v1alpha1.PromotionStep {
	at := metav1.NewTime(uiLcT0.Add(time.Duration(minute) * time.Minute))
	s := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name: bundle + "-" + env, Namespace: "default", CreationTimestamp: at,
			Labels: map[string]string{
				"kardinal.io/pipeline": "app", "kardinal.io/bundle": bundle, "kardinal.io/environment": env,
			},
		},
		Spec:   v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: bundle, Environment: env},
		Status: v1alpha1.PromotionStepStatus{State: state},
	}
	if state == "Verified" {
		s.Status.Conditions = []metav1.Condition{{Type: "Verified", Status: metav1.ConditionTrue, Reason: "Verified", LastTransitionTime: at}}
	}
	return s
}

func uiLcPost(t *testing.T, c client.Client, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	newUIAPIServer(c, zerolog.Nop()).RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func uiLcCreated(t *testing.T, c client.Client, fixture ...string) []v1alpha1.Bundle {
	t.Helper()
	var list v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &list))
	known := map[string]bool{}
	for _, n := range fixture {
		known[n] = true
	}
	var out []v1alpha1.Bundle
	for _, b := range list.Items {
		if !known[b.Name] {
			out = append(out, b)
		}
	}
	return out
}

// TestUIAPI_Promote_CopiesBundleVerifiedUpstream covers C07-controller-08 and
// E2E-18: UI promote copies the images of the Bundle verified upstream and
// refuses, creating nothing, when there is nothing to promote or a newer Bundle
// is still promoting.
func TestUIAPI_Promote_CopiesBundleVerifiedUpstream(t *testing.T) {
	tests := []struct {
		name     string
		objs     []client.Object
		wantCode int
		wantTag  string
	}{
		{name: "copies the bundle verified in uat",
			objs:     []client.Object{uiLcPipeline(), uiLcBundle("app-v1", "1", 0), uiLcStep("app-v1", "uat", "Verified", 5)},
			wantCode: http.StatusCreated, wantTag: "1"},
		{name: "nothing verified upstream",
			objs:     []client.Object{uiLcPipeline(), uiLcBundle("app-v1", "1", 0), uiLcStep("app-v1", "uat", "Promoting", 5)},
			wantCode: http.StatusConflict},
		{name: "a newer bundle still promoting is not superseded",
			objs: []client.Object{uiLcPipeline(), uiLcBundle("app-v1", "1", 0), uiLcStep("app-v1", "uat", "Verified", 5),
				func() client.Object { b := uiLcBundle("app-v2", "2", 10); b.Status.Phase = "Promoting"; return b }()},
			wantCode: http.StatusConflict},
		{name: "the first environment has nothing upstream",
			objs:     []client.Object{uiLcPipeline()},
			wantCode: http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(tc.objs...).Build()
			env := "prod"
			if tc.wantCode == http.StatusBadRequest {
				env = "test"
			}
			w := uiLcPost(t, c, "/api/v1/ui/promote", `{"pipeline":"app","environment":"`+env+`"}`)
			require.Equal(t, tc.wantCode, w.Code, w.Body.String())
			created := uiLcCreated(t, c, "app-v1", "app-v2")
			if tc.wantCode != http.StatusCreated {
				assert.Empty(t, created, "a refused promote creates no Bundle")
				return
			}
			require.Len(t, created, 1)
			b := created[0]
			require.Len(t, b.Spec.Images, 1, "a promote Bundle always carries images")
			assert.Equal(t, tc.wantTag, b.Spec.Images[0].Tag)
			assert.Equal(t, "prod", b.Spec.Intent.TargetEnvironment)
			assert.Equal(t, "kardinal-ui", b.Annotations[lifecycle.AnnotationRequestedBy])
			var resp map[string]string
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, b.Name, resp["bundle"])
			assert.Equal(t, "app-v1", resp["source"])
		})
	}
}

// TestUIAPI_Rollback_RestoresPreviousVerifiedBundle covers C07-controller-09
// and E2E-08: UI rollback targets the Bundle verified before the deployed one
// and copies its images; the environment is required.
func TestUIAPI_Rollback_RestoresPreviousVerifiedBundle(t *testing.T) {
	history := func() []client.Object {
		return []client.Object{
			uiLcPipeline(),
			uiLcBundle("app-v1", "1", 0), uiLcBundle("app-v2", "2", 10),
			uiLcStep("app-v1", "prod", "Verified", 5), uiLcStep("app-v2", "prod", "Verified", 15),
		}
	}
	tests := []struct {
		name       string
		objs       []client.Object
		body       string
		wantCode   int
		wantTarget string
	}{
		{name: "default target is the previous verified bundle",
			objs: history(), body: `{"pipeline":"app","environment":"prod"}`,
			wantCode: http.StatusCreated, wantTarget: "app-v1"},
		{name: "toBundle is the deployed bundle",
			objs: history(), body: `{"pipeline":"app","environment":"prod","toBundle":"app-v2"}`,
			wantCode: http.StatusConflict},
		{name: "toBundle does not exist",
			objs: history(), body: `{"pipeline":"app","environment":"prod","toBundle":"app-v0"}`,
			wantCode: http.StatusNotFound},
		{name: "environment is required",
			objs: history(), body: `{"pipeline":"app"}`,
			wantCode: http.StatusBadRequest},
		{name: "only one verified bundle",
			objs:     []client.Object{uiLcPipeline(), uiLcBundle("app-v1", "1", 0), uiLcStep("app-v1", "prod", "Verified", 5)},
			body:     `{"pipeline":"app","environment":"prod"}`,
			wantCode: http.StatusConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(tc.objs...).Build()
			w := uiLcPost(t, c, "/api/v1/ui/rollback", tc.body)
			require.Equal(t, tc.wantCode, w.Code, w.Body.String())
			created := uiLcCreated(t, c, "app-v1", "app-v2")
			if tc.wantCode != http.StatusCreated {
				assert.Empty(t, created, "a refused rollback creates no Bundle")
				return
			}
			require.Len(t, created, 1)
			rb := created[0]
			assert.Equal(t, tc.wantTarget, rb.Spec.Provenance.RollbackOf)
			require.Len(t, rb.Spec.Images, 1, "the rollback Bundle carries the target's images")
			assert.Equal(t, "1", rb.Spec.Images[0].Tag)
			assert.Equal(t, "prod", rb.Spec.Intent.TargetEnvironment)
			assert.Equal(t, "app-v2", rb.Annotations[lifecycle.AnnotationRollbackFrom])
		})
	}
}

// TestUIAPI_PauseResume_FreezeGate covers C10a-web-01 (server half) and the
// pause half of C07-controller-24: UI pause creates the freeze gate the
// PromotionStep reconciler holds on, resume deletes it, and a concurrent write
// to the Pipeline does not fail the request (merge patch, not Update).
func TestUIAPI_PauseResume_FreezeGate(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(uiLcPipeline()).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if _, ok := obj.(*v1alpha1.Pipeline); ok {
					return apierrors.NewConflict(schema.GroupResource{Group: "kardinal.io", Resource: "pipelines"}, obj.GetName(), nil)
				}
				return cl.Update(ctx, obj, opts...)
			},
		}).Build()
	key := types.NamespacedName{Namespace: "default", Name: "app"}
	gateKey := types.NamespacedName{Namespace: "default", Name: lifecycle.FreezeGateName("app")}

	w := uiLcPost(t, c, "/api/v1/ui/pause", `{"pipeline":"app"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var p v1alpha1.Pipeline
	require.NoError(t, c.Get(ctx, key, &p))
	assert.True(t, p.Spec.Paused)
	var gate v1alpha1.PolicyGate
	require.NoError(t, c.Get(ctx, gateKey, &gate), "UI pause creates the freeze gate")
	assert.Equal(t, "true", gate.Labels[lifecycle.LabelFreeze])

	w = uiLcPost(t, c, "/api/v1/ui/resume", `{"pipeline":"app"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, c.Get(ctx, key, &p))
	assert.False(t, p.Spec.Paused)
	assert.True(t, apierrors.IsNotFound(c.Get(ctx, gateKey, &gate)), "UI resume deletes the freeze gate")

	w = uiLcPost(t, c, "/api/v1/ui/pause", `{"pipeline":"missing"}`)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestUIAPI_CreateBundle_StampsCreatedAt covers the UI half of C02-bundle-04:
// Bundles created from the UI carry sub-second creation order.
func TestUIAPI_CreateBundle_StampsCreatedAt(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(uiScheme()).Build()
	w := uiLcPost(t, c, "/api/v1/ui/bundles", `{"pipeline":"app","image":"ghcr.io/org/app:1"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	created := uiLcCreated(t, c)
	require.Len(t, created, 1)
	_, err := time.Parse(time.RFC3339Nano, created[0].Annotations[lifecycle.AnnotationCreatedAt])
	assert.NoError(t, err)
}
