// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	authv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/uiauth"
)

// invalidReason is the API server's refusal of a field over its CRD limit.
var invalidReason = apierrors.NewInvalid(schema.GroupKind{Group: "kardinal.io", Kind: "PolicyGate"}, "g",
	field.ErrorList{field.TooLong(field.NewPath("spec", "overrides").Index(0).Child("reason"), "", 1024)})

// TestUI_InvalidIsBadRequest: when the API server refuses a field the caller
// sent (over its CRD's maxLength), the UI answers 400 with the API server's
// message, which names the field, not 500: for the lifecycle actions
// (lifecycleStatus) and for a gate override. Covers UI-INVALID-01.
func TestUI_InvalidIsBadRequest(t *testing.T) {
	s := newUIAPIServer(nil, zerolog.Nop())
	code, msg := s.lifecycleStatus("approval", invalidReason)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, msg, "spec.overrides[0].reason")
	code, _ = s.lifecycleStatus("approval", errors.New("etcd is down"))
	assert.Equal(t, http.StatusInternalServerError, code, "anything else stays an internal error")

	gate := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "default"},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true"}}
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(gate).WithInterceptorFuncs(interceptor.Funcs{
		Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
			return invalidReason
		},
	}).Build()
	mux := http.NewServeMux()
	newUIAPIServer(c, zerolog.Nop()).RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/ui/gates/default/g/approve", strings.NewReader(`{"reason":"`+strings.Repeat("a", 2000)+`"}`))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(uiauth.WithUser(r.Context(), authv1.UserInfo{Username: "alice"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "spec.overrides[0].reason")
}
