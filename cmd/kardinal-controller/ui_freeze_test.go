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

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/uiauth"
)

// TestUIFreeze: the UI freezes and unfreezes for a verified caller only,
// records the caller as who froze, validates its input and never deletes a
// ChangeWindow that is not a freeze; GET lists the freezes.
func TestUIFreeze(t *testing.T) {
	plain := &v1alpha1.ChangeWindow{ObjectMeta: metav1.ObjectMeta{Name: "holiday"},
		Spec: v1alpha1.ChangeWindowSpec{Type: "blackout", Start: metav1.Now(), End: metav1.Now()}}
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(plain).Build()
	mux := http.NewServeMux()
	srv := newUIAPIServer(c, zerolog.Nop())
	srv.RegisterRoutes(mux)
	call := func(method, path, body, user string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if user != "" {
			r = r.WithContext(uiauth.WithUser(r.Context(), authv1.UserInfo{Username: user}))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	off := call("POST", "/api/v1/ui/freeze", `{"actors":"agents","reason":"r"}`, "alice")
	assert.Equal(t, http.StatusForbidden, off.Code)
	assert.Contains(t, off.Body.String(), "ui.freezeControls")
	srv.freezeControls = true
	assert.Equal(t, http.StatusForbidden, call("POST", "/api/v1/ui/freeze", `{"actors":"agents","reason":"r"}`, "").Code,
		"a freeze names who froze")
	assert.Equal(t, http.StatusBadRequest, call("POST", "/api/v1/ui/freeze", `{"actors":"robots","reason":"r"}`, "alice").Code)
	assert.Equal(t, http.StatusBadRequest, call("POST", "/api/v1/ui/freeze", `{"actors":"agents"}`, "alice").Code)
	assert.Equal(t, http.StatusBadRequest, call("POST", "/api/v1/ui/freeze", `{"actors":"agents","reason":"r","minutes":-1}`, "alice").Code)

	w := call("POST", "/api/v1/ui/freeze", `{"actors":"everyone","namespace":"payments","reason":"INC-4211","minutes":60}`, "alice")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "kardinal-freeze-everyone-payments")
	var cw v1alpha1.ChangeWindow
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "kardinal-freeze-everyone-payments"}, &cw))
	assert.Equal(t, "alice", cw.Spec.Freeze.CreatedBy)
	assert.Equal(t, []string{"payments"}, cw.Spec.Freeze.Namespaces)
	assert.False(t, cw.Spec.End.IsZero())

	w = call("GET", "/api/v1/ui/freezes", "", "bob")
	require.Equal(t, http.StatusOK, w.Code)
	var list uiFreezesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.Len(t, list.Freezes, 1, "the plain window is not a freeze")
	assert.Equal(t, "everyone, namespaces payments", list.Freezes[0].Scope)
	assert.Equal(t, "INC-4211", list.Freezes[0].Reason)
	assert.True(t, list.Freezes[0].Active, "a status behind the spec counts as active")

	assert.Equal(t, http.StatusForbidden, call("POST", "/api/v1/ui/unfreeze", `{"name":"kardinal-freeze-everyone-payments"}`, "").Code)
	assert.Equal(t, http.StatusConflict, call("POST", "/api/v1/ui/unfreeze", `{"name":"holiday"}`, "bob").Code)
	assert.Equal(t, http.StatusNotFound, call("POST", "/api/v1/ui/unfreeze", `{"name":"nope"}`, "bob").Code)
	require.Equal(t, http.StatusOK, call("POST", "/api/v1/ui/unfreeze", `{"name":"kardinal-freeze-everyone-payments"}`, "bob").Code)
	var all v1alpha1.ChangeWindowList
	require.NoError(t, c.List(context.Background(), &all))
	require.Len(t, all.Items, 1)
	assert.Equal(t, "holiday", all.Items[0].Name)
}
