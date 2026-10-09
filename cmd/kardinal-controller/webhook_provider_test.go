// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestWebhook_ProviderRoutes (D3): each ScmProvider and ClusterScmProvider has
// its own endpoint, checked with its own webhook secret, and a delivery there
// marks only the PRStatuses of PRs opened on that provider. The controller's
// /webhook/scm marks only PRStatuses without spec.scmProvider. A provider that
// does not exist or has no webhook secret gets the same 401.
func TestWebhook_ProviderRoutes(t *testing.T) {
	teamA := v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindScmProvider, Name: "gh", UID: "uid-a"}
	shared := v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindClusterScmProvider, Name: "shared", UID: "uid-c"}
	prsOf := func(ns, name string, id *v1alpha1.ScmProviderIdentity) *v1alpha1.PRStatus {
		return &v1alpha1.PRStatus{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			Spec:   v1alpha1.PRStatusSpec{PRNumber: 7, Repo: "acme/app", PRURL: "https://x/acme/app/pull/7", ScmProvider: id},
			Status: v1alpha1.PRStatusStatus{Open: true}}
	}
	secret := func(ns, name, key string) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Data: map[string][]byte{key: []byte("v")}}
	}
	newObjs := func() []client.Object {
		return []client.Object{
			&v1alpha1.ScmProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "gh", UID: "uid-a"},
				Spec: v1alpha1.ScmProviderSpec{Type: "github", SecretRef: v1alpha1.ScmSecretKeyRef{Name: "tok"},
					WebhookSecretRef: &v1alpha1.ScmSecretKeyRef{Name: "hook"}}},
			&v1alpha1.ScmProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "nohook", UID: "uid-n"},
				Spec: v1alpha1.ScmProviderSpec{Type: "github", SecretRef: v1alpha1.ScmSecretKeyRef{Name: "tok"}}},
			&v1alpha1.ClusterScmProvider{ObjectMeta: metav1.ObjectMeta{Name: "shared", UID: "uid-c"},
				Spec: v1alpha1.ClusterScmProviderSpec{ScmProviderSpec: v1alpha1.ScmProviderSpec{Type: "github",
					SecretRef:        v1alpha1.ScmSecretKeyRef{Name: "tok", Namespace: "scm"},
					WebhookSecretRef: &v1alpha1.ScmSecretKeyRef{Name: "hook", Namespace: "scm"}}}},
			secret("team-a", "tok", "token"), secret("team-a", "hook", "secret"),
			secret("scm", "tok", "token"), secret("scm", "hook", "secret"),
			prsOf("team-a", "of-team-a", &teamA),
			prsOf("team-b", "of-shared", &shared),
			prsOf("team-a", "of-controller", nil),
			prsOf("team-a", "of-recreated", &v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindScmProvider, Name: "gh", UID: "uid-old"}),
		}
	}
	tests := []struct {
		name, path string
		wantCode   int
		wantMerged []string
	}{
		{name: "ScmProvider", path: "/webhook/scm/namespaces/team-a/gh", wantCode: http.StatusNoContent, wantMerged: []string{"team-a/of-team-a"}},
		{name: "ClusterScmProvider", path: "/webhook/scm/cluster/shared", wantCode: http.StatusNoContent, wantMerged: []string{"team-b/of-shared"}},
		{name: "controller", path: "/webhook/scm", wantCode: http.StatusNoContent, wantMerged: []string{"team-a/of-controller"}},
		{name: "no webhook secret", path: "/webhook/scm/namespaces/team-a/nohook", wantCode: http.StatusUnauthorized},
		{name: "unknown provider", path: "/webhook/scm/namespaces/team-a/nope", wantCode: http.StatusUnauthorized},
		{name: "a ScmProvider under another namespace", path: "/webhook/scm/namespaces/team-b/gh", wantCode: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := webhookScheme()
			require.NoError(t, corev1.AddToScheme(s))
			objs := newObjs()
			b := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...)
			for _, o := range objs {
				if p, ok := o.(*v1alpha1.PRStatus); ok {
					b = b.WithStatusSubresource(p)
				}
			}
			c := b.Build()
			event := scm.WebhookEvent{EventType: "pull_request", Action: "closed", Merged: true, PRNumber: 7, RepoFullName: "acme/app"}
			srv := newWebhookServerWithConfig(&mockSCMProvider{event: event}, c, zerolog.Nop(), true)
			registry := &scm.Registry{Client: c, New: func(_, _, _, _ string) (scm.SCMProvider, error) {
				return &mockSCMProvider{event: event}, nil
			}}
			mux := http.NewServeMux()
			mux.HandleFunc("/webhook/scm", srv.Handler())
			mux.HandleFunc("POST /webhook/scm/namespaces/{namespace}/{name}", srv.ProviderHandler(registry))
			mux.HandleFunc("POST /webhook/scm/cluster/{name}", srv.ProviderHandler(registry))

			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, tt.path, bytes.NewReader([]byte(`{}`))))
			assert.Equal(t, tt.wantCode, w.Code, w.Body.String())

			var list v1alpha1.PRStatusList
			require.NoError(t, c.List(context.Background(), &list))
			var merged []string
			for _, p := range list.Items {
				if p.Status.Merged {
					merged = append(merged, p.Namespace+"/"+p.Name)
				}
			}
			assert.ElementsMatch(t, tt.wantMerged, merged)
		})
	}
}
