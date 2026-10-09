// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

func TestRepositoryAllowed(t *testing.T) {
	tests := []struct {
		globs []string
		repo  string
		want  bool
	}{
		{nil, "anything/at/all", true},
		{[]string{"acme/*"}, "acme/app", true},
		{[]string{"acme/*"}, "ACME/App", true},
		{[]string{"acme/*"}, "acme/sub/app", false},
		{[]string{"acme/*"}, "other/app", false},
		{[]string{"group/**"}, "group/sub/deep/app", true},
		{[]string{"group/**"}, "group", false},
		{[]string{"group/**"}, "groupie/app", false},
		{[]string{"a/b", "c/*"}, "c/d", true},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, scm.RepositoryAllowed(tt.globs, tt.repo), "%v %s", tt.globs, tt.repo)
	}
}

type builtProvider struct {
	scm.SCMProvider
	token, apiURL, webhookSecret string
}

// TestRegistry_ForIdentity: the client is built from the provider's Secret,
// reused until the Secret or the spec changes, and refused for a recreated
// provider (another UID) or a repository outside allowedRepositories.
func TestRegistry_ForIdentity(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))
	p := &v1alpha1.ScmProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "gl", UID: "u1", Generation: 1},
		Spec: v1alpha1.ScmProviderSpec{Type: "gitlab", APIURL: "https://gl.example", AllowedRepositories: []string{"acme/*"},
			SecretRef:        v1alpha1.ScmSecretKeyRef{Name: "tok"},
			WebhookSecretRef: &v1alpha1.ScmSecretKeyRef{Name: "hook", Key: "k"}}}
	tok := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "tok"}, Data: map[string][]byte{"token": []byte(" t1\n")}}
	hook := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "hook"}, Data: map[string][]byte{"k": []byte("h")}}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(p, tok, hook).Build()
	builds := 0
	r := &scm.Registry{Client: c, New: func(typ, token, apiURL, webhookSecret string) (scm.SCMProvider, error) {
		builds++
		assert.Equal(t, "gitlab", typ)
		return &builtProvider{token: token, apiURL: apiURL, webhookSecret: webhookSecret}, nil
	}}
	ctx := context.Background()
	id := v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindScmProvider, Name: "gl", UID: "u1"}

	res, err := r.ForIdentity(ctx, "team-a", id, "acme/app")
	require.NoError(t, err)
	got := res.Provider.(*builtProvider)
	assert.Equal(t, builtProvider{token: "t1", apiURL: "https://gl.example", webhookSecret: "h"}, *got)

	_, err = r.ForIdentity(ctx, "team-a", id, "acme/other")
	require.NoError(t, err)
	assert.Equal(t, 1, builds, "the cached client is reused")

	tok.Data["token"] = []byte("t2")
	require.NoError(t, c.Update(ctx, tok))
	res, err = r.ForIdentity(ctx, "team-a", id, "acme/app")
	require.NoError(t, err)
	assert.Equal(t, "t2", res.Provider.(*builtProvider).token, "a rotated token is used at the next call")
	assert.Equal(t, 2, builds)

	_, err = r.ForIdentity(ctx, "team-a", id, "evil/app")
	assert.True(t, errors.Is(err, scm.ErrRepositoryNotAllowed), err)

	_, err = r.ForIdentity(ctx, "team-a", v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindScmProvider, Name: "gl", UID: "old"}, "acme/app")
	assert.True(t, errors.Is(err, scm.ErrProviderGone), "a recreated provider is not the one the PR was opened on")

	_, err = r.ForIdentity(ctx, "team-b", id, "acme/app")
	assert.True(t, errors.Is(err, scm.ErrProviderGone), "a ScmProvider serves its own namespace only")

	tok.Data = map[string][]byte{}
	require.NoError(t, c.Update(ctx, tok))
	_, err = r.ForIdentity(ctx, "team-a", id, "acme/app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no token key")
	assert.NotContains(t, err.Error(), "t2", "errors never carry the token")
}
