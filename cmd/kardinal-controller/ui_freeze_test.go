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
	authzv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	// The freeze audit finalizer keeps the window after the unfreeze: the
	// banner no longer lists it (#2034).
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "kardinal-freeze-everyone-payments"}, &cw))
	cw.Finalizers = []string{"kardinal.io/freeze-audit"}
	require.NoError(t, c.Update(context.Background(), &cw))
	require.Equal(t, http.StatusOK, call("POST", "/api/v1/ui/unfreeze", `{"name":"kardinal-freeze-everyone-payments"}`, "bob").Code)
	w = call("GET", "/api/v1/ui/freezes", "", "bob")
	require.Equal(t, http.StatusOK, w.Code)
	list = uiFreezesResponse{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	assert.Empty(t, list.Freezes, "a freeze being lifted is not shown")
	var all v1alpha1.ChangeWindowList
	require.NoError(t, c.List(context.Background(), &all))
	require.Len(t, all.Items, 2, "its audit finalizer keeps it until FreezeLifted is written")
}

// freezeAccess is an access reviewer that grants, per user, verbs on
// resource[/subresource] in a namespace ("" is cluster scope, "*" any).
type freezeAccess struct {
	rules map[string]map[string][]string // user -> namespace -> "verb resource[/sub]"
}

func (a *freezeAccess) Allowed(_ context.Context, u authv1.UserInfo, attrs authzv1.ResourceAttributes) (bool, string, error) {
	want := attrs.Verb + " " + attrs.Resource
	if attrs.Subresource != "" {
		want += "/" + attrs.Subresource
	}
	for _, ns := range []string{attrs.Namespace, "*"} {
		for _, r := range a.rules[u.Username][ns] {
			if r == want || r == attrs.Verb+" *" {
				return true, "", nil
			}
		}
	}
	return false, "", nil
}

// TestUIFreeze_TokenReviewAuth runs the freeze routes behind the real UI
// handler in TokenReview mode: no or a bad token is 401; a viewer, a
// namespace freezer acting cluster-wide or in another namespace, and anyone
// while ui.freezeControls is off get 403; and with --watch-namespace a
// freeze beyond the namespace is 403. Every refusal writes nothing.
func TestUIFreeze_TokenReviewAuth(t *testing.T) {
	tokens := &uiTestTokens{users: map[string]string{
		"viewer": "viewer", "team-a": "alice", "cluster": "ic",
	}}
	access := &freezeAccess{rules: map[string]map[string][]string{
		"viewer": {"*": {"get *", "list *"}},
		"alice":  {"*": {"get *", "list *"}, "team-a": {"update changewindows/freeze"}},
		"ic":     {"*": {"get *", "list *"}, "": {"update changewindows/freeze"}},
	}}
	cases := []struct {
		name, token, path, body string
		controls                bool
		scope                   string
		want                    int
	}{
		{"no token", "", "/api/v1/ui/freeze", `{"actors":"agents","reason":"r"}`, true, "", http.StatusUnauthorized},
		{"bad token", "Bearer nope", "/api/v1/ui/freeze", `{"actors":"agents","reason":"r"}`, true, "", http.StatusUnauthorized},
		{"no token reads nothing", "", "/api/v1/ui/freezes", "", true, "", http.StatusUnauthorized},
		{"a viewer reads the freezes", "Bearer viewer", "/api/v1/ui/freezes", "", true, "", http.StatusOK},
		{"a viewer cannot freeze", "Bearer viewer", "/api/v1/ui/freeze", `{"actors":"agents","reason":"r"}`, true, "", http.StatusForbidden},
		{"a viewer cannot unfreeze", "Bearer viewer", "/api/v1/ui/unfreeze", `{"name":"kardinal-freeze-everyone"}`, true, "", http.StatusForbidden},
		{"a namespace freezer cannot freeze the cluster", "Bearer team-a", "/api/v1/ui/freeze", `{"actors":"agents","reason":"r"}`, true, "", http.StatusForbidden},
		{"a namespace freezer cannot freeze another namespace", "Bearer team-a", "/api/v1/ui/freeze",
			`{"actors":"agents","namespace":"team-b","reason":"r"}`, true, "", http.StatusForbidden},
		{"a namespace freezer cannot lift a cluster freeze", "Bearer team-a", "/api/v1/ui/unfreeze", `{"name":"kardinal-freeze-everyone"}`, true, "", http.StatusForbidden},
		{"a namespace freezer freezes its namespace", "Bearer team-a", "/api/v1/ui/freeze",
			`{"actors":"agents","namespace":"team-a","reason":"r"}`, true, "", http.StatusOK},
		{"a cluster freezer freezes the cluster", "Bearer cluster", "/api/v1/ui/freeze", `{"actors":"agents","reason":"r"}`, true, "", http.StatusOK},
		{"controls off", "Bearer cluster", "/api/v1/ui/freeze", `{"actors":"agents","reason":"r"}`, false, "", http.StatusForbidden},
		{"beyond the watched namespace", "Bearer cluster", "/api/v1/ui/freeze", `{"actors":"agents","reason":"r"}`, true, "team-a", http.StatusForbidden},
		{"another namespace than the watched one", "Bearer cluster", "/api/v1/ui/freeze",
			`{"actors":"agents","namespace":"team-b","reason":"r"}`, true, "team-a", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			existing := &v1alpha1.ChangeWindow{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-freeze-everyone"},
				Spec: v1alpha1.ChangeWindowSpec{Type: "blackout", Start: metav1.Now(), Reason: "r",
					Freeze: &v1alpha1.ChangeWindowFreeze{Actors: "everyone", CreatedBy: "ic"}}}
			c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(existing).Build()
			h := newUIHandler(c, nil, nil, uiAuthConfig{tokens: tokens, access: access, freezeControls: tc.controls,
				scopeNamespace: tc.scope}, "", nil, zerolog.Nop())
			method := http.MethodPost
			if tc.body == "" {
				method = http.MethodGet
			}
			rec := uiAuthDo(t, h, method, tc.path, tc.token, tc.body)
			require.Equal(t, tc.want, rec.Code, rec.Body.String())
			var list v1alpha1.ChangeWindowList
			require.NoError(t, c.List(context.Background(), &list))
			if tc.want != http.StatusOK || method == http.MethodGet {
				require.Len(t, list.Items, 1, "a refused or read-only call writes nothing")
				assert.Equal(t, "kardinal-freeze-everyone", list.Items[0].Name)
			}
		})
	}
}

// TestUIFreeze_NamespaceRightsChangeOnlyOwnFreezes covers the QA finding on
// #1869 (F1): a caller whose freeze right is a namespace's (the chart's
// admin role through a RoleBinding) may freeze that namespace, and change or
// lift only the freezes it made there. Someone else's freeze of the
// namespace, an incident commander's stop-the-line, needs the cluster-wide
// right: lifting it or re-freezing it over (a shorter end, another reason)
// is 403 and writes nothing (docs/design/18-actor-policy.md section 9.5).
func TestUIFreeze_NamespaceRightsChangeOnlyOwnFreezes(t *testing.T) {
	tokens := &uiTestTokens{users: map[string]string{"team-a": "alice", "cluster": "ic"}}
	access := &freezeAccess{rules: map[string]map[string][]string{
		"alice": {"*": {"get *", "list *"}, "team-a": {"update changewindows/freeze"}},
		"ic":    {"*": {"get *", "list *", "update changewindows/freeze"}, "": {"update changewindows/freeze"}},
	}}
	const name = "kardinal-freeze-everyone-team-a"
	cases := []struct {
		name, token, path, body, createdBy string
		want                               int
		wantGone                           bool
	}{
		{"a namespace admin cannot lift someone else's freeze", "Bearer team-a", "/api/v1/ui/unfreeze",
			`{"name":"` + name + `"}`, "ic", http.StatusForbidden, false},
		{"a namespace admin cannot re-freeze over someone else's freeze", "Bearer team-a", "/api/v1/ui/freeze",
			`{"actors":"everyone","namespace":"team-a","reason":"mine","minutes":1}`, "ic", http.StatusForbidden, false},
		{"a namespace admin lifts its own freeze", "Bearer team-a", "/api/v1/ui/unfreeze",
			`{"name":"` + name + `"}`, "alice", http.StatusOK, true},
		{"a namespace admin changes its own freeze", "Bearer team-a", "/api/v1/ui/freeze",
			`{"actors":"everyone","namespace":"team-a","reason":"mine","minutes":1}`, "alice", http.StatusOK, false},
		{"the cluster-wide right lifts anyone's", "Bearer cluster", "/api/v1/ui/unfreeze",
			`{"name":"` + name + `"}`, "alice", http.StatusOK, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			existing := &v1alpha1.ChangeWindow{ObjectMeta: metav1.ObjectMeta{Name: name},
				Spec: v1alpha1.ChangeWindowSpec{Type: "blackout", Start: metav1.Now(), Reason: "INC-9",
					Freeze: &v1alpha1.ChangeWindowFreeze{Actors: "everyone", Namespaces: []string{"team-a"}, CreatedBy: tc.createdBy}}}
			c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(existing).Build()
			h := newUIHandler(c, nil, nil, uiAuthConfig{tokens: tokens, access: access, freezeControls: true},
				"", nil, zerolog.Nop())
			rec := uiAuthDo(t, h, http.MethodPost, tc.path, tc.token, tc.body)
			require.Equal(t, tc.want, rec.Code, rec.Body.String())
			var got v1alpha1.ChangeWindow
			err := c.Get(context.Background(), types.NamespacedName{Name: name}, &got)
			if tc.wantGone {
				assert.True(t, apierrors.IsNotFound(err), "lifted")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.createdBy, got.Spec.Freeze.CreatedBy, "who froze stays")
			if tc.want == http.StatusForbidden {
				assert.Contains(t, rec.Body.String(), "made by "+tc.createdBy)
				assert.Equal(t, "INC-9", got.Spec.Reason, "unchanged")
				assert.True(t, got.Spec.End.IsZero(), "unchanged")
			} else {
				assert.Equal(t, "mine", got.Spec.Reason)
			}
		})
	}
}
