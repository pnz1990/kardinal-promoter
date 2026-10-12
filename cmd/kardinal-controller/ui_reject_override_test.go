// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestUIHandler_Reject: POST /api/v1/ui/reject sets spec.rejected with the
// reason and the requester the TokenReview verified. In TokenReview mode it
// needs update on the virtual subresource bundles/reject (granted to the
// promoter and approver roles), not update on bundles: the controller
// writes. A caller without it is refused and nothing is written; a Bundle
// already rejected is a conflict.
func TestUIHandler_Reject(t *testing.T) {
	tokens := &uiTestTokens{users: map[string]string{"p": "promoter", "a": "approver", "e": "editor", "v": "viewer"}}
	access := exactAccess{
		"promoter update bundles/reject team-a": true,
		"approver update bundles/reject team-a": true,
		"editor update bundles team-a":          true,
		"editor patch bundles team-a":           true,
		"viewer get bundles team-a":             true,
	}
	rejected := func() *v1alpha1.Bundle {
		at := metav1.Now()
		return &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "old", Namespace: "team-a"},
			Spec: v1alpha1.BundleSpec{Pipeline: "app", Rejected: &v1alpha1.BundleRejection{By: "bob", Reason: "bad", At: &at}}}
	}
	tests := []struct {
		name, token, body string
		want              int
		wantBy            string
	}{
		{"promoter rejects", "p", `{"bundle":"app-v2","namespace":"team-a","reason":"CVE-2026-1"}`, http.StatusOK, "promoter"},
		{"approver rejects", "a", `{"bundle":"app-v2","namespace":"team-a","reason":"CVE-2026-1"}`, http.StatusOK, "approver"},
		{"update bundles without bundles/reject", "e", `{"bundle":"app-v2","namespace":"team-a","reason":"x"}`, http.StatusForbidden, ""},
		{"viewer", "v", `{"bundle":"app-v2","namespace":"team-a","reason":"x"}`, http.StatusForbidden, ""},
		{"another namespace", "p", `{"bundle":"app-v2","namespace":"team-b","reason":"x"}`, http.StatusForbidden, ""},
		{"no reason", "p", `{"bundle":"app-v2","namespace":"team-a","reason":"  "}`, http.StatusBadRequest, ""},
		{"no bundle", "p", `{"namespace":"team-a","reason":"x"}`, http.StatusBadRequest, ""},
		{"unknown bundle", "p", `{"bundle":"nope","namespace":"team-a","reason":"x"}`, http.StatusNotFound, ""},
		{"already rejected", "p", `{"bundle":"old","namespace":"team-a","reason":"x"}`, http.StatusConflict, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
				&v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v2", Namespace: "team-a"}, Spec: v1alpha1.BundleSpec{Pipeline: "app"}},
				&v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v2", Namespace: "team-b"}, Spec: v1alpha1.BundleSpec{Pipeline: "app"}},
				rejected(),
			).Build()
			h := newUIHandler(c, nil, uiAuthConfig{tokens: tokens, access: access}, "", nil, zerolog.Nop())
			rec := uiAuthDo(t, h, http.MethodPost, "/api/v1/ui/reject", "Bearer "+tt.token, tt.body)
			require.Equal(t, tt.want, rec.Code, rec.Body.String())
			for _, ns := range []string{"team-a", "team-b"} {
				var b v1alpha1.Bundle
				require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "app-v2"}, &b))
				if tt.want != http.StatusOK || ns != "team-a" {
					assert.Nil(t, b.Spec.Rejected, "%s/app-v2: nothing written", ns)
					continue
				}
				require.NotNil(t, b.Spec.Rejected)
				assert.Equal(t, tt.wantBy, b.Spec.Rejected.By, "the verified requester")
				assert.Equal(t, "CVE-2026-1", b.Spec.Rejected.Reason)
				assert.NotNil(t, b.Spec.Rejected.At)
			}
			var old v1alpha1.Bundle
			require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: "old"}, &old))
			assert.Equal(t, "bob", old.Spec.Rejected.By, "an existing rejection is never replaced")
		})
	}
}

// TestUIHandler_PipelineOverride: POST /api/v1/ui/override overrides a gate
// template in one environment of a Pipeline, as kardinal override --stage
// does: on the instances its in-progress Bundles have there, not those of a
// finished Bundle or another environment. The caller needs
// policygates/override on each instance, checked before any is written,
// and is recorded as createdBy; the duration is capped.
func TestUIHandler_PipelineOverride(t *testing.T) {
	tokens := &uiTestTokens{users: map[string]string{"a": "approver", "e": "editor"}}
	read := func(user string) exactAccess {
		return exactAccess{
			user + " get pipelines team-a": true, user + " get policygates team-a": true,
			user + " list policygates team-a": true, user + " list bundles team-a": true,
		}
	}
	access := exactAccess{"approver update policygates/override team-a": true, "editor update policygates team-a": true}
	for _, u := range []string{"approver", "editor"} {
		for k, v := range read(u) {
			access[k] = v
		}
	}
	instance := func(bundle, env string) *v1alpha1.PolicyGate {
		return &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "no-weekend--" + bundle + "-" + env, Namespace: "team-a",
			Labels: map[string]string{"kardinal.io/pipeline": "app", "kardinal.io/gate-template": "no-weekend",
				"kardinal.io/environment": env, "kardinal.io/bundle": bundle}}}
	}
	bundle := func(name, phase string) *v1alpha1.Bundle {
		return &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team-a"},
			Spec: v1alpha1.BundleSpec{Pipeline: "app"}, Status: v1alpha1.BundleStatus{Phase: phase}}
	}
	body := func(extra string) string {
		return `{"pipeline":"app","namespace":"team-a","gate":"no-weekend","environment":"prod","reason":"P0 hotfix"` + extra + `}`
	}
	tests := []struct {
		name, token, body string
		want              int
	}{
		{"approver overrides", "a", body(`,"expiresInMinutes":30`), http.StatusOK},
		{"update policygates without policygates/override", "e", body(""), http.StatusForbidden},
		{"over the cap", "a", body(`,"expiresInMinutes":100000`), http.StatusBadRequest},
		{"no environment", "a", `{"pipeline":"app","namespace":"team-a","gate":"no-weekend","reason":"r"}`, http.StatusBadRequest},
		{"no reason", "a", `{"pipeline":"app","namespace":"team-a","gate":"no-weekend","environment":"prod"}`, http.StatusBadRequest},
		{"unknown pipeline", "a", `{"pipeline":"nope","namespace":"team-a","gate":"no-weekend","environment":"prod","reason":"r"}`, http.StatusNotFound},
		{"no in-progress Bundle in the environment", "a",
			`{"pipeline":"app","namespace":"team-a","gate":"no-weekend","environment":"uat","reason":"r"}`, http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
				&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a"}},
				bundle("app-v2", "Promoting"), bundle("app-v1", "Verified"),
				instance("app-v2", "prod"), instance("app-v2", "test"), instance("app-v1", "prod"), instance("app-v1", "uat"),
			).Build()
			h := newUIHandler(c, nil, uiAuthConfig{tokens: tokens, access: access}, "", nil, zerolog.Nop())
			rec := uiAuthDo(t, h, http.MethodPost, "/api/v1/ui/override", "Bearer "+tt.token, tt.body)
			require.Equal(t, tt.want, rec.Code, rec.Body.String())
			overridden := map[string]v1alpha1.PolicyGateOverride{}
			var gates v1alpha1.PolicyGateList
			require.NoError(t, c.List(context.Background(), &gates))
			for _, g := range gates.Items {
				for _, o := range g.Spec.Overrides {
					overridden[g.Name] = o
				}
			}
			if tt.want == http.StatusForbidden {
				assert.Contains(t, rec.Body.String(), "approver or admin role grants it", "a refusal names the role")
			}
			if tt.want != http.StatusOK {
				assert.Empty(t, overridden, "nothing written")
				return
			}
			require.Len(t, overridden, 1, "only the in-progress Bundle's prod instance")
			o, ok := overridden["no-weekend--app-v2-prod"]
			require.True(t, ok)
			assert.Equal(t, "approver", o.CreatedBy)
			assert.Equal(t, "prod", o.Stage)
			assert.Equal(t, "P0 hotfix", o.Reason)
			require.NotNil(t, o.CreatedAt)
			assert.InDelta(t, 30*60, o.ExpiresAt.Sub(o.CreatedAt.Time).Seconds(), 1)
			var resp uiPipelineOverrideResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.Equal(t, []string{"no-weekend--app-v2-prod"}, resp.Gates)
			assert.Equal(t, "gate no-weekend overridden in prod for 1 version in progress until "+resp.ExpiresAt, resp.Message)
		})
	}
}

// TestVersionsInProgress: the override reply counts versions in words, not
// the kind name with "(s)".
func TestVersionsInProgress(t *testing.T) {
	for n, want := range map[int]string{0: "0 versions in progress", 1: "1 version in progress", 2: "2 versions in progress"} {
		assert.Equal(t, want, versionsInProgress(n))
	}
}

// TestUIHandler_PipelineOverride_PartialFailure: when one instance cannot be
// written, the others still are, and the answer names each gate that was and
// was not overridden.
func TestUIHandler_PipelineOverride_PartialFailure(t *testing.T) {
	instance := func(bundle string) *v1alpha1.PolicyGate {
		return &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "no-weekend--" + bundle + "-prod", Namespace: "team-a",
			Labels: map[string]string{"kardinal.io/pipeline": "app", "kardinal.io/gate-template": "no-weekend",
				"kardinal.io/environment": "prod", "kardinal.io/bundle": bundle}}}
	}
	bundle := func(name string) *v1alpha1.Bundle {
		return &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team-a"},
			Spec: v1alpha1.BundleSpec{Pipeline: "app"}, Status: v1alpha1.BundleStatus{Phase: "Promoting"}}
	}
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a"}},
		bundle("app-v2"), bundle("app-v3"), instance("app-v2"), instance("app-v3"),
	).WithInterceptorFuncs(interceptor.Funcs{
		Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if obj.GetName() == "no-weekend--app-v3-prod" {
				return errors.New("etcd unavailable")
			}
			return cl.Patch(ctx, obj, patch, opts...)
		},
	}).Build()
	h := newUIHandler(c, nil, uiAuthConfig{}, "", nil, zerolog.Nop())
	rec := uiAuthDo(t, h, http.MethodPost, "/api/v1/ui/override", "",
		`{"pipeline":"app","namespace":"team-a","gate":"no-weekend","environment":"prod","reason":"P0 hotfix"}`)
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "overridden on no-weekend--app-v2-prod")
	assert.Contains(t, rec.Body.String(), "not overridden on no-weekend--app-v3-prod (failed to override gate no-weekend--app-v3-prod)")
	var g v1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: "no-weekend--app-v2-prod"}, &g))
	assert.Len(t, g.Spec.Overrides, 1, "the gate that could be written was")
}
