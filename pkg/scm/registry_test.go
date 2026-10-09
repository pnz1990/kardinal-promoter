// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

func registryScheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))
	return s
}

func refSecret(ns, name string, data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name,
		Labels: map[string]string{scm.LabelReferenceable: "true"}}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

func TestRepositoryAllowed(t *testing.T) {
	spec := func(globs ...string) scm.ProviderSpec {
		return scm.ProviderSpec{Identity: v1alpha1.ScmProviderIdentity{Kind: "ScmProvider", Name: "p"},
			Spec: v1alpha1.ScmProviderSpec{Type: "gitlab", APIURL: "https://gitlab.example.com", AllowedRepositories: globs}}
	}
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
		{[]string{"acme/*"}, "acme/%2e%2e", false},
	}
	for _, tt := range tests {
		err := scm.RepositoryAllowed(spec(tt.globs...), tt.repo)
		assert.Equal(t, tt.want, err == nil, "%v %s: %v", tt.globs, tt.repo, err)
		if err != nil {
			assert.ErrorIs(t, err, scm.ErrRepositoryNotAllowed)
		}
	}
	_, _, err := scm.ProviderAllowlist(spec("acme/**/x"))
	assert.ErrorIs(t, err, scm.ErrProviderConfig, "** only at the end")
}

type builtProvider struct {
	scm.SCMProvider
	token, apiURL, webhookSecret string
	labeled                      int
}

func (b *builtProvider) AddLabelsToPR(context.Context, string, int, []string) error {
	b.labeled++
	return nil
}

// countingReader counts Gets per object type.
type countingReader struct {
	client.Reader
	secrets, namespaces atomic.Int32
}

func (c *countingReader) Get(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
	switch obj.(type) {
	case *corev1.Secret:
		c.secrets.Add(1)
	case *corev1.Namespace:
		c.namespaces.Add(1)
	}
	return c.Reader.Get(ctx, key, obj, opts...)
}

// TestRegistry_ForIdentity: the client is built from the provider's
// Secret, reused until the Secret or the spec changes, and refused for a
// recreated provider (another UID), a namespace or repository the provider
// does not allow, a Secret that is not labeled kardinal.io/referenceable, and
// an http:// apiURL without AllowHTTP. Secret reads are reused for
// SecretTTL; every call the client makes is checked against
// allowedRepositories again (Guard), and calls that pass reach the client.
func TestRegistry_ForIdentity(t *testing.T) {
	p := &v1alpha1.ScmProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "gl", UID: "u1", Generation: 1},
		Spec: v1alpha1.ScmProviderSpec{Type: "gitlab", APIURL: "https://gl.example", AllowedRepositories: []string{"acme/*"},
			SecretRef:        v1alpha1.ScmSecretKeyRef{Name: "tok"},
			WebhookSecretRef: &v1alpha1.ScmSecretKeyRef{Name: "hook", Key: "k"}}}
	tok := refSecret("team-a", "tok", map[string]string{"token": " t1\n"})
	hook := refSecret("team-a", "hook", map[string]string{"k": "h"})
	c := fake.NewClientBuilder().WithScheme(registryScheme(t)).WithObjects(p, tok, hook).Build()
	api := &countingReader{Reader: c}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	builds := 0
	var last *builtProvider
	r := &scm.Registry{Client: c, APIReader: api, Now: func() time.Time { return now },
		New: func(typ, token, apiURL, webhookSecret string) (scm.SCMProvider, error) {
			builds++
			assert.Equal(t, "gitlab", typ)
			last = &builtProvider{token: token, apiURL: apiURL, webhookSecret: webhookSecret}
			return last, nil
		}}
	ctx := context.Background()
	id := v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindScmProvider, Name: "gl", UID: "u1"}

	res, err := r.ForIdentity(ctx, "team-a", id, "acme/app")
	require.NoError(t, err)
	assert.Equal(t, "t1", last.token)
	assert.Equal(t, "https://gl.example", last.apiURL)
	assert.Equal(t, "h", last.webhookSecret)

	// The Guard: labels on an allowed repository reach the client; on
	// another repository they are refused before any request.
	require.NoError(t, res.Provider.AddLabelsToPR(ctx, "acme/app", 1, []string{"x"}))
	assert.Equal(t, 1, last.labeled, "an allowed call passes through the Guard")
	err = res.Provider.AddLabelsToPR(ctx, "evil/app", 1, []string{"x"})
	assert.ErrorIs(t, err, scm.ErrRepositoryNotAllowed)
	assert.Equal(t, 1, last.labeled)

	reads := api.secrets.Load()
	_, err = r.ForIdentity(ctx, "team-a", id, "acme/other")
	require.NoError(t, err)
	assert.Equal(t, 1, builds, "the cached client is reused")
	assert.Equal(t, reads, api.secrets.Load(), "Secrets are read again only after SecretTTL")

	tok.Data["token"] = []byte("t2")
	require.NoError(t, c.Update(ctx, tok))
	_, err = r.ForIdentity(ctx, "team-a", id, "acme/app")
	require.NoError(t, err)
	assert.Equal(t, "t1", last.token, "within SecretTTL the old token is used")
	now = now.Add(scm.DefaultSecretTTL + time.Second)
	_, err = r.ForIdentity(ctx, "team-a", id, "acme/app")
	require.NoError(t, err)
	assert.Equal(t, "t2", last.token, "a rotated token is used after SecretTTL")
	assert.Equal(t, 2, builds)

	_, err = r.ForIdentity(ctx, "team-a", id, "evil/app")
	assert.ErrorIs(t, err, scm.ErrRepositoryNotAllowed)
	_, err = r.ForIdentity(ctx, "team-a", v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindScmProvider, Name: "gl", UID: "old"}, "acme/app")
	assert.ErrorIs(t, err, scm.ErrProviderGone, "a recreated provider is not the one the PR was opened on")
	_, err = r.ForIdentity(ctx, "team-b", id, "acme/app")
	assert.ErrorIs(t, err, scm.ErrProviderGone, "a ScmProvider serves its own namespace only")

	// The referenceable label is required.
	delete(tok.Labels, scm.LabelReferenceable)
	require.NoError(t, c.Update(ctx, tok))
	now = now.Add(scm.DefaultSecretTTL + time.Second)
	_, err = r.ForIdentity(ctx, "team-a", id, "acme/app")
	require.ErrorIs(t, err, scm.ErrProviderConfig)
	assert.Contains(t, err.Error(), "not labeled kardinal.io/referenceable=true")
	assert.NotContains(t, err.Error(), "t2", "errors never carry the token")

	// http:// only with AllowHTTP.
	tok.Labels = map[string]string{scm.LabelReferenceable: "true"}
	require.NoError(t, c.Update(ctx, tok))
	p.Spec.APIURL = "http://gl.example"
	require.NoError(t, c.Update(ctx, p))
	now = now.Add(scm.DefaultSecretTTL + time.Second)
	_, err = r.ForIdentity(ctx, "team-a", id, "acme/app")
	require.ErrorIs(t, err, scm.ErrProviderConfig)
	assert.Contains(t, err.Error(), "clear text")
	r.AllowHTTP = true
	_, err = r.ForIdentity(ctx, "team-a", id, "acme/app")
	require.NoError(t, err)

	// Evict drops the client: the next use builds it again.
	before := builds
	r.Evict(v1alpha1.KindScmProvider, "team-a", "gl")
	_, err = r.ForIdentity(ctx, "team-a", id, "acme/app")
	require.NoError(t, err)
	assert.Equal(t, before+1, builds)
}

// TestRegistry_ClusterProviderNamespaces: a ClusterScmProvider's
// allowedNamespaces is checked on every use against the namespace's labels,
// cached for NamespaceTTL: a namespace whose label is removed loses the
// provider within that time.
func TestRegistry_ClusterProviderNamespaces(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a", Labels: map[string]string{"scm": "ghe"}}}
	p := &v1alpha1.ClusterScmProvider{ObjectMeta: metav1.ObjectMeta{Name: "ghe", UID: "c1"},
		Spec: v1alpha1.ClusterScmProviderSpec{
			ScmProviderSpec:   v1alpha1.ScmProviderSpec{Type: "github", SecretRef: v1alpha1.ScmSecretKeyRef{Name: "tok", Namespace: "scm"}},
			AllowedNamespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"scm": "ghe"}}}}
	c := fake.NewClientBuilder().WithScheme(registryScheme(t)).WithObjects(ns, p, refSecret("scm", "tok", map[string]string{"token": "x"})).Build()
	api := &countingReader{Reader: c}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	r := &scm.Registry{Client: c, APIReader: api, Now: func() time.Time { return now },
		New: func(string, string, string, string) (scm.SCMProvider, error) { return &builtProvider{}, nil }}
	ctx := context.Background()
	id := v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindClusterScmProvider, Name: "ghe", UID: "c1"}

	_, err := r.ForIdentity(ctx, "team-a", id, "acme/app")
	require.NoError(t, err)
	_, err = r.ForIdentity(ctx, "team-a", id, "acme/app")
	require.NoError(t, err)
	assert.Equal(t, int32(1), api.namespaces.Load(), "the Namespace is read once per NamespaceTTL")

	ns.Labels = nil
	require.NoError(t, c.Update(ctx, ns))
	_, err = r.ForIdentity(ctx, "team-a", id, "acme/app")
	require.NoError(t, err, "within NamespaceTTL")
	now = now.Add(scm.DefaultNamespaceTTL + time.Second)
	_, err = r.ForIdentity(ctx, "team-a", id, "acme/app")
	assert.ErrorIs(t, err, scm.ErrNamespaceNotAllowed, "the revoked namespace loses the provider after NamespaceTTL")
}

// TestRegistry_WebhookSecret reads only the webhook Secret, and caches a
// NotFound too, so unauthenticated deliveries for a provider without its
// Secret read the API once per SecretTTL.
func TestRegistry_WebhookSecret(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(registryScheme(t)).Build()
	api := &countingReader{Reader: c}
	r := &scm.Registry{Client: c, APIReader: api}
	spec := scm.ProviderSpec{Identity: v1alpha1.ScmProviderIdentity{Kind: "ScmProvider", Name: "p"}, SecretNamespace: "team-a",
		Spec: v1alpha1.ScmProviderSpec{Type: "github", SecretRef: v1alpha1.ScmSecretKeyRef{Name: "tok"},
			WebhookSecretRef: &v1alpha1.ScmSecretKeyRef{Name: "hook"}}}
	for range 3 {
		_, err := r.WebhookSecret(context.Background(), spec)
		require.ErrorIs(t, err, scm.ErrProviderConfig)
	}
	assert.Equal(t, int32(1), api.secrets.Load(), "a missing Secret is cached too, and the token Secret is not read")
	require.NoError(t, c.Create(context.Background(), refSecret("team-a", "hook", map[string]string{"secret": "s"})))
	r2 := &scm.Registry{Client: c, APIReader: api}
	got, err := r2.WebhookSecret(context.Background(), spec)
	require.NoError(t, err)
	assert.Equal(t, "s", got)
}

// TestRegistry_Transport: the providers' API requests go through the
// Registry's Transport (main sets egress.NewTransport).
func TestRegistry_Transport(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"state":"open","merged":false}`))
	}))
	defer srv.Close()
	p := &v1alpha1.ScmProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "fj", UID: "u"},
		Spec: v1alpha1.ScmProviderSpec{Type: "forgejo", APIURL: srv.URL, SecretRef: v1alpha1.ScmSecretKeyRef{Name: "tok"}}}
	c := fake.NewClientBuilder().WithScheme(registryScheme(t)).WithObjects(p, refSecret("team-a", "tok", map[string]string{"token": "x"})).Build()
	var through atomic.Int32
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		through.Add(1)
		return srv.Client().Transport.RoundTrip(req)
	})
	r := &scm.Registry{Client: c, Transport: rt}
	res, err := r.ForIdentity(context.Background(), "team-a", v1alpha1.ScmProviderIdentity{Kind: "ScmProvider", Name: "fj", UID: "u"}, "o/r")
	require.NoError(t, err)
	_, _, _ = res.Provider.GetPRStatus(context.Background(), "o/r", 1)
	assert.Positive(t, through.Load(), "the request used the Registry's Transport")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestRegistry_SecretReadErrorNotCached: an API error reading a Secret is
// not cached; the next call reads again.
func TestRegistry_SecretReadErrorNotCached(t *testing.T) {
	base := fake.NewClientBuilder().WithScheme(registryScheme(t)).WithObjects(refSecret("team-a", "hook", map[string]string{"secret": "s"})).Build()
	fail := true
	c := interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, cl client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
		if _, ok := o.(*corev1.Secret); ok && fail {
			return errors.New("apiserver unavailable")
		}
		return cl.Get(ctx, k, o, opts...)
	}})
	r := &scm.Registry{Client: c}
	spec := scm.ProviderSpec{Identity: v1alpha1.ScmProviderIdentity{Kind: "ScmProvider", Name: "p"}, SecretNamespace: "team-a",
		Spec: v1alpha1.ScmProviderSpec{WebhookSecretRef: &v1alpha1.ScmSecretKeyRef{Name: "hook"}}}
	_, err := r.WebhookSecret(context.Background(), spec)
	require.Error(t, err)
	fail = false
	got, err := r.WebhookSecret(context.Background(), spec)
	require.NoError(t, err)
	assert.Equal(t, "s", got)
}

// TestRegistry_EvictForgetsSecrets: Evict (the provider was deleted) drops
// the cached token and webhook Secrets with the client, so they are not kept
// in memory and the next use reads them again, within SecretTTL too.
//
// Covers SCM-PROVIDERCRD-06.
func TestRegistry_EvictForgetsSecrets(t *testing.T) {
	p := &v1alpha1.ScmProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "gh", UID: "u1"},
		Spec: v1alpha1.ScmProviderSpec{Type: "github", SecretRef: v1alpha1.ScmSecretKeyRef{Name: "tok"},
			WebhookSecretRef: &v1alpha1.ScmSecretKeyRef{Name: "hook"}}}
	c := fake.NewClientBuilder().WithScheme(registryScheme(t)).WithObjects(p,
		refSecret("team-a", "tok", map[string]string{"token": "t"}), refSecret("team-a", "hook", map[string]string{"secret": "h"})).Build()
	api := &countingReader{Reader: c}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	r := &scm.Registry{Client: c, APIReader: api, Now: func() time.Time { return now },
		New: func(string, string, string, string) (scm.SCMProvider, error) { return &builtProvider{}, nil }}
	ctx := context.Background()
	id := v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindScmProvider, Name: "gh", UID: "u1"}
	_, err := r.ForIdentity(ctx, "team-a", id, "")
	require.NoError(t, err)
	assert.Equal(t, int32(2), api.secrets.Load())
	secrets, _ := r.CacheSizesForTest()
	assert.Equal(t, 2, secrets)

	r.Evict(v1alpha1.KindScmProvider, "team-a", "gh")
	secrets, _ = r.CacheSizesForTest()
	assert.Zero(t, secrets, "the provider's Secrets are forgotten with its client")
	_, err = r.ForIdentity(ctx, "team-a", id, "")
	require.NoError(t, err)
	assert.Equal(t, int32(4), api.secrets.Load(), "the next use reads the Secrets again")

	// A webhook delivery only reads the webhook Secret; Evict forgets it too.
	r2 := &scm.Registry{Client: c, APIReader: api, Now: func() time.Time { return now }}
	spec, err := scm.GetProvider(ctx, c, "team-a", v1alpha1.KindScmProvider, "gh")
	require.NoError(t, err)
	_, err = r2.WebhookSecret(ctx, spec)
	require.NoError(t, err)
	r2.Evict(v1alpha1.KindScmProvider, "team-a", "gh")
	secrets, _ = r2.CacheSizesForTest()
	assert.Zero(t, secrets)
}

// TestRegistry_ExpiredEntriesPruned: a cache write drops the expired
// Secrets and Namespaces, so entries of Secrets and namespaces no longer in
// use do not stay until the cache is full.
//
// Covers SCM-PROVIDERCRD-06.
func TestRegistry_ExpiredEntriesPruned(t *testing.T) {
	var objs []client.Object
	for _, ns := range []string{"n1", "n2", "n3"} {
		objs = append(objs, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: map[string]string{"scm": "ok"}}})
	}
	c := fake.NewClientBuilder().WithScheme(registryScheme(t)).WithObjects(objs...).Build()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	r := &scm.Registry{Client: c, Now: func() time.Time { return now }}
	ctx := context.Background()
	webhook := func(name string) {
		spec := scm.ProviderSpec{Identity: v1alpha1.ScmProviderIdentity{Kind: "ScmProvider", Name: name}, SecretNamespace: "team-a",
			Spec: v1alpha1.ScmProviderSpec{WebhookSecretRef: &v1alpha1.ScmSecretKeyRef{Name: name}}}
		_, _ = r.WebhookSecret(ctx, spec)
	}
	cluster := scm.ProviderSpec{Identity: v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindClusterScmProvider, Name: "c"},
		AllowedNamespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"scm": "ok"}}}
	webhook("a")
	webhook("b")
	require.NoError(t, r.Check(ctx, cluster, "n1", ""))
	require.NoError(t, r.Check(ctx, cluster, "n2", ""))
	secrets, namespaces := r.CacheSizesForTest()
	assert.Equal(t, []int{2, 2}, []int{secrets, namespaces})

	now = now.Add(time.Hour)
	webhook("c")
	require.NoError(t, r.Check(ctx, cluster, "n3", ""))
	secrets, namespaces = r.CacheSizesForTest()
	assert.Equal(t, []int{1, 1}, []int{secrets, namespaces}, "the expired entries were dropped on write")
}

// TestRegistry_ClientCacheBounded: the client cache keeps the most recently
// used clients; past its bound the least recently used one is rebuilt at
// its next use.
//
// Covers SCM-PROVIDERCRD-06.
func TestRegistry_ClientCacheBounded(t *testing.T) {
	defer scm.SetMaxRegistryClientsForTest(2)()
	objs := []client.Object{refSecret("team-a", "tok", map[string]string{"token": "t"})}
	for _, n := range []string{"p1", "p2", "p3"} {
		objs = append(objs, &v1alpha1.ScmProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: n, UID: types.UID(n)},
			Spec: v1alpha1.ScmProviderSpec{Type: "github", SecretRef: v1alpha1.ScmSecretKeyRef{Name: "tok"}}})
	}
	c := fake.NewClientBuilder().WithScheme(registryScheme(t)).WithObjects(objs...).Build()
	builds := map[string]int{}
	var current string
	r := &scm.Registry{Client: c, New: func(string, string, string, string) (scm.SCMProvider, error) {
		builds[current]++
		return &builtProvider{}, nil
	}}
	use := func(n string) {
		current = n
		_, err := r.ForIdentity(context.Background(), "team-a", v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindScmProvider, Name: n, UID: n}, "")
		require.NoError(t, err)
	}
	use("p1")
	use("p2")
	use("p1")
	use("p3") // p2 is the least recently used
	use("p1")
	assert.Equal(t, map[string]int{"p1": 1, "p2": 1, "p3": 1}, builds, "p1 stays cached")
	use("p2")
	assert.Equal(t, 2, builds["p2"], "p2 was dropped past the bound and is built again")
}
