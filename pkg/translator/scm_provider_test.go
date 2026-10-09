// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package translator

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestResolveProvider covers spec.git.providerRef: the identity carries the
// provider's UID; a missing provider, a namespace a ClusterScmProvider does
// not select and a repository outside allowedRepositories are refused.
func TestResolveProvider(t *testing.T) {
	nsA := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a", Labels: map[string]string{"team": "a"}}}
	nsB := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-b"}}
	local := &kardinalv1alpha1.ScmProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "gl", UID: "uid-local"},
		Spec: kardinalv1alpha1.ScmProviderSpec{Type: "gitlab", AllowedRepositories: []string{"acme/*"}}}
	shared := &kardinalv1alpha1.ClusterScmProvider{ObjectMeta: metav1.ObjectMeta{Name: "gh", UID: "uid-cluster"},
		Spec: kardinalv1alpha1.ClusterScmProviderSpec{
			ScmProviderSpec:   kardinalv1alpha1.ScmProviderSpec{Type: "github"},
			AllowedNamespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "a"}},
		}}
	open := shared.DeepCopy()
	open.Name, open.UID, open.Spec.AllowedNamespaces = "open", "uid-open", nil
	plain := &kardinalv1alpha1.ScmProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "plain", UID: "uid-plain"},
		Spec: kardinalv1alpha1.ScmProviderSpec{Type: "forgejo", APIURL: "http://forgejo.forgejo.svc:3000"}}

	s := translatorTestScheme()
	require.NoError(t, corev1.AddToScheme(s))
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(nsA, nsB, local, shared, open, plain).Build()
	tr := New(nil, graph.NewBuilder(), c, nil, zerolog.Nop()).WithProviders(&scm.Registry{Client: c, APIReader: c})

	tests := []struct {
		name, ns, url string
		ref           *kardinalv1alpha1.ScmProviderRef
		want          *kardinalv1alpha1.ScmProviderIdentity
		wantErr       error
	}{
		{name: "no providerRef", ns: "team-a", url: "https://x/acme/app"},
		{name: "namespaced", ns: "team-a", url: "https://gitlab.example/acme/app.git", ref: &kardinalv1alpha1.ScmProviderRef{Name: "gl"},
			want: &kardinalv1alpha1.ScmProviderIdentity{Kind: "ScmProvider", Name: "gl", UID: "uid-local"}},
		{name: "repository not allowed", ns: "team-a", url: "https://gitlab.example/other/app.git", ref: &kardinalv1alpha1.ScmProviderRef{Name: "gl"},
			wantErr: scm.ErrRepositoryNotAllowed},
		{name: "a ScmProvider of another namespace", ns: "team-b", url: "https://gitlab.example/acme/app.git", ref: &kardinalv1alpha1.ScmProviderRef{Name: "gl"},
			wantErr: scm.ErrProviderGone},
		{name: "cluster, selected namespace", ns: "team-a", url: "https://github.com/acme/app", ref: &kardinalv1alpha1.ScmProviderRef{Kind: "ClusterScmProvider", Name: "gh"},
			want: &kardinalv1alpha1.ScmProviderIdentity{Kind: "ClusterScmProvider", Name: "gh", UID: "uid-cluster"}},
		{name: "cluster, other namespace", ns: "team-b", url: "https://github.com/acme/app", ref: &kardinalv1alpha1.ScmProviderRef{Kind: "ClusterScmProvider", Name: "gh"},
			wantErr: scm.ErrNamespaceNotAllowed},
		{name: "cluster without allowedNamespaces", ns: "team-a", url: "https://github.com/acme/app", ref: &kardinalv1alpha1.ScmProviderRef{Kind: "ClusterScmProvider", Name: "open"},
			wantErr: scm.ErrNamespaceNotAllowed},
		{name: "http apiURL without the opt-in", ns: "team-a", url: "http://forgejo.forgejo.svc:3000/acme/app", ref: &kardinalv1alpha1.ScmProviderRef{Name: "plain"},
			wantErr: scm.ErrProviderConfig},
		{name: "missing", ns: "team-a", url: "https://github.com/acme/app", ref: &kardinalv1alpha1.ScmProviderRef{Kind: "ClusterScmProvider", Name: "nope"},
			wantErr: scm.ErrProviderGone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &kardinalv1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Namespace: tt.ns, Name: "app"},
				Spec: kardinalv1alpha1.PipelineSpec{Git: kardinalv1alpha1.PipelineGit{URL: tt.url, ProviderRef: tt.ref}}}
			got, err := tr.resolveProvider(context.Background(), p)
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErr), err.Error())
				assert.False(t, errors.Is(err, graph.ErrInvalid), "a provider can be fixed later: the translation is retried")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestResolveProvider_NamespaceReadUncached: the Namespace is read with the
// API reader, not the cached client.
func TestResolveProvider_NamespaceReadUncached(t *testing.T) {
	s := translatorTestScheme()
	require.NoError(t, corev1.AddToScheme(s))
	shared := &kardinalv1alpha1.ClusterScmProvider{ObjectMeta: metav1.ObjectMeta{Name: "gh", UID: "u"},
		Spec: kardinalv1alpha1.ClusterScmProviderSpec{ScmProviderSpec: kardinalv1alpha1.ScmProviderSpec{Type: "github"},
			AllowedNamespaces: &metav1.LabelSelector{}}}
	cached := fake.NewClientBuilder().WithScheme(s).WithObjects(shared).Build()
	api := &countingReader{Reader: fake.NewClientBuilder().WithScheme(s).
		WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}}).Build()}
	tr := New(nil, graph.NewBuilder(), cached, nil, zerolog.Nop()).WithProviders(&scm.Registry{Client: cached, APIReader: api})
	p := &kardinalv1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "app"},
		Spec: kardinalv1alpha1.PipelineSpec{Git: kardinalv1alpha1.PipelineGit{URL: "https://github.com/acme/app",
			ProviderRef: &kardinalv1alpha1.ScmProviderRef{Kind: "ClusterScmProvider", Name: "gh"}}}}
	_, err := tr.resolveProvider(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, 1, api.gets)
}

type countingReader struct {
	client.Reader
	gets int
}

func (r *countingReader) Get(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
	r.gets++
	return r.Reader.Get(ctx, key, obj, opts...)
}
