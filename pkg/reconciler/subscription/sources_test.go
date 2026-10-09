// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package subscription_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/subscription"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/source"
)

func schemeWithSecrets() *runtime.Scheme {
	s := newScheme()
	_ = corev1.AddToScheme(s)
	return s
}

func reqFor(sub *kardinalv1alpha1.Subscription) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace}}
}

func getSubscription(t *testing.T, c client.Client, sub *kardinalv1alpha1.Subscription) *kardinalv1alpha1.Subscription {
	t.Helper()
	var got kardinalv1alpha1.Subscription
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(sub), &got))
	return &got
}

// TestSubscriptionReconciler_Credentials reads the secretRef Secret of each
// source type from the Subscription's namespace and hands its keys to the
// watcher; a missing Secret, a Secret with no known key, or a Secret in
// another namespace (not reachable: secretRef has no namespace) give phase
// Error naming the Secret and never a value.
func TestSubscriptionReconciler_Credentials(t *testing.T) {
	secret := func(name string, data map[string]string) *corev1.Secret {
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team"}, Data: map[string][]byte{}}
		for k, v := range data {
			s.Data[k] = []byte(v)
		}
		return s
	}
	elsewhere := secret("other-ns", map[string]string{"token": "t"})
	elsewhere.Namespace = "other"
	tests := []struct {
		name    string
		sub     func() *kardinalv1alpha1.Subscription
		want    source.Credentials
		wantErr string
	}{
		{name: "image dockerconfigjson", sub: func() *kardinalv1alpha1.Subscription {
			s := makeImageSub("img", "team", "p", "ghcr.io/org/app")
			s.Spec.Image.SecretRef = &kardinalv1alpha1.SubscriptionSecretRef{Name: "pull"}
			return s
		}, want: source.Credentials{DockerConfigJSON: []byte(`{"auths":{}}`)}},
		{name: "git token", sub: func() *kardinalv1alpha1.Subscription {
			s := makeGitSub("git", "team", "p", "https://git.example.com/org/repo")
			s.Spec.Git.SecretRef = &kardinalv1alpha1.SubscriptionSecretRef{Name: "git-token"}
			return s
		}, want: source.Credentials{Token: "tok", Username: "bot"}},
		{name: "git ssh", sub: func() *kardinalv1alpha1.Subscription {
			s := makeGitSub("ssh", "team", "p", "git@git.example.com:org/repo.git")
			s.Spec.Git.SecretRef = &kardinalv1alpha1.SubscriptionSecretRef{Name: "git-ssh"}
			return s
		}, want: source.Credentials{SSHPrivateKey: []byte("KEY"), SSHKnownHosts: []byte("HOSTS")}},
		{name: "helm basic", sub: func() *kardinalv1alpha1.Subscription {
			return helmSub("chart", "team", &kardinalv1alpha1.SubscriptionSecretRef{Name: "helm-login"})
		}, want: source.Credentials{Username: "u", Password: "p"}},
		{name: "missing secret", sub: func() *kardinalv1alpha1.Subscription {
			s := makeImageSub("img", "team", "p", "ghcr.io/org/app")
			s.Spec.Image.SecretRef = &kardinalv1alpha1.SubscriptionSecretRef{Name: "other-ns"}
			return s
		}, wantErr: `secretRef: Secret "other-ns" not found in namespace "team"`},
		{name: "no known key", sub: func() *kardinalv1alpha1.Subscription {
			s := makeImageSub("img", "team", "p", "ghcr.io/org/app")
			s.Spec.Image.SecretRef = &kardinalv1alpha1.SubscriptionSecretRef{Name: "junk"}
			return s
		}, wantErr: `secretRef: Secret "junk" has none of the keys .dockerconfigjson`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := tt.sub()
			c := fake.NewClientBuilder().WithScheme(schemeWithSecrets()).WithObjects(sub,
				secret("pull", map[string]string{".dockerconfigjson": `{"auths":{}}`}),
				secret("git-token", map[string]string{"token": "tok", "username": "bot"}),
				secret("git-ssh", map[string]string{"ssh-privatekey": "KEY", "known_hosts": "HOSTS"}),
				secret("helm-login", map[string]string{"username": "u", "password": "p"}),
				secret("junk", map[string]string{"api-key": "do-not-print"}),
				elsewhere,
			).WithStatusSubresource(sub).Build()
			var got *source.Credentials
			r := &subscription.Reconciler{Client: c,
				WatcherFn: func(_ *kardinalv1alpha1.Subscription, creds source.Credentials) (source.Watcher, error) {
					got = &creds
					return &unchangedWatcher{"sha256:x"}, nil
				}}
			_, err := r.Reconcile(context.Background(), reqFor(sub))
			require.NoError(t, err)
			st := getSubscription(t, c, sub).Status
			if tt.wantErr != "" {
				assert.Equal(t, "Error", st.Phase)
				assert.Contains(t, st.Message, tt.wantErr)
				assert.NotContains(t, st.Message, "do-not-print")
				assert.Nil(t, got, "no watcher without credentials")
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.want, *got)
			assert.Equal(t, "Watching", st.Phase)
		})
	}
}

func helmSub(name, ns string, ref *kardinalv1alpha1.SubscriptionSecretRef) *kardinalv1alpha1.Subscription {
	return &kardinalv1alpha1.Subscription{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: kardinalv1alpha1.SubscriptionSpec{
			Type: kardinalv1alpha1.SubscriptionTypeHelm, Pipeline: "my-pipeline",
			Helm: &kardinalv1alpha1.HelmSubscriptionSpec{RepoURL: "https://charts.example.com", Chart: "podinfo",
				SecretRef: ref, Interval: "5m"},
		},
	}
}

// TestSubscriptionReconciler_HelmChartBundle checks that a new chart
// version creates a chart Bundle that carries the chart repository, name,
// version and digest, labelled like the other Bundles; a second reconcile of
// the same version creates nothing (idempotent).
func TestSubscriptionReconciler_HelmChartBundle(t *testing.T) {
	sub := helmSub("podinfo-chart", "default", nil)
	sub.Status.LastSeenDigest = "sha256:old"
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(sub).WithStatusSubresource(sub).Build()
	r := &subscription.Reconciler{Client: c,
		WatcherFn: func(_ *kardinalv1alpha1.Subscription, _ source.Credentials) (source.Watcher, error) {
			return &changedWatcher{digest: "sha256:0123456789abcdef", tag: "6.15.0"}, nil
		},
		NowFn: func() time.Time { return time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC) }}
	for range 2 {
		_, err := r.Reconcile(context.Background(), reqFor(sub))
		require.NoError(t, err)
	}
	var list kardinalv1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &list))
	require.Len(t, list.Items, 1)
	b := list.Items[0]
	assert.Equal(t, "podinfo-chart-6-15-0-01234567", b.Name)
	assert.Equal(t, "chart", b.Spec.Type)
	assert.Equal(t, &kardinalv1alpha1.ChartRef{RepoURL: "https://charts.example.com", Name: "podinfo",
		Version: "6.15.0", Digest: "sha256:0123456789abcdef"}, b.Spec.Chart)
	assert.Empty(t, b.Spec.Images)
	assert.Nil(t, b.Spec.ConfigRef)
	assert.Equal(t, "podinfo-chart", b.Labels["kardinal.io/subscription"])
	st := getSubscription(t, c, sub).Status
	assert.Equal(t, "6.15.0", st.LastSeenTag)
	assert.Equal(t, b.Name, st.LastBundleCreated)
}

// revisionWatcher reports a pathGlob result and records the LastRevision it
// was built with.
type revisionWatcher struct{ digest, revision string }

func (w *revisionWatcher) Watch(_ context.Context, last string) (*source.WatchResult, error) {
	return &source.WatchResult{Digest: w.digest, Tag: w.digest[:7], Revision: w.revision, Changed: last != "" && last != w.digest}, nil
}

// TestSubscriptionReconciler_PathGlobRevision records the head a pathGlob
// poll read up to in status.lastSeenRevision and passes it to the next
// watcher (NewWatcher sets GitWatcher.LastRevision from it).
func TestSubscriptionReconciler_PathGlobRevision(t *testing.T) {
	sub := makeGitSub("glob", "default", "p", "https://git.example.com/org/repo")
	sub.Spec.Git.PathGlob = "apps/**"
	sub.Status.LastSeenRevision = "1111111111111111111111111111111111111111"
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(sub).WithStatusSubresource(sub).Build()
	var seen string
	r := &subscription.Reconciler{Client: c,
		WatcherFn: func(s *kardinalv1alpha1.Subscription, creds source.Credentials) (source.Watcher, error) {
			w, err := subscription.NewWatcher(s, creds)
			require.NoError(t, err)
			seen = w.(*source.GitWatcher).LastRevision
			return &revisionWatcher{digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", revision: "2222222222222222222222222222222222222222"}, nil
		}}
	_, err := r.Reconcile(context.Background(), reqFor(sub))
	require.NoError(t, err)
	assert.Equal(t, "1111111111111111111111111111111111111111", seen)
	st := getSubscription(t, c, sub).Status
	assert.Equal(t, "2222222222222222222222222222222222222222", st.LastSeenRevision)
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", st.LastSeenDigest)
}

// TestSubscriptionReconciler_RefreshSpacing covers the kardinal.io/refresh
// annotation: a request answered by a poll is recorded in
// status.lastRefreshRequest; a new request within 10s of the last poll waits
// for the rest of the spacing without polling; one after it polls at once.
func TestSubscriptionReconciler_RefreshSpacing(t *testing.T) {
	now := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	sub := makeImageSub("refresh", "default", "p", "ghcr.io/org/app")
	sub.Annotations = map[string]string{kardinalv1alpha1.RefreshAnnotation: "2026-10-08T09:59:58Z"}
	sub.Status.LastSeenDigest = "sha256:x"
	sub.Status.LastCheckedAt = now.Add(-3 * time.Second).Format(time.RFC3339)
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(sub).WithStatusSubresource(sub).Build()
	polls := 0
	r := &subscription.Reconciler{Client: c,
		WatcherFn: func(_ *kardinalv1alpha1.Subscription, _ source.Credentials) (source.Watcher, error) {
			polls++
			return &unchangedWatcher{"sha256:x"}, nil
		},
		NowFn: func() time.Time { return now }}

	res, err := r.Reconcile(context.Background(), reqFor(sub))
	require.NoError(t, err)
	assert.Zero(t, polls, "3s after a poll the refresh waits")
	assert.Equal(t, 8*time.Second, res.RequeueAfter, "until 10s after the end of the last poll's second")

	now = now.Add(8 * time.Second)
	res, err = r.Reconcile(context.Background(), reqFor(sub))
	require.NoError(t, err)
	assert.Equal(t, 1, polls)
	assert.Equal(t, 5*time.Minute, res.RequeueAfter)
	st := getSubscription(t, c, sub).Status
	assert.Equal(t, "2026-10-08T09:59:58Z", st.LastRefreshRequest)

	// Answered: the same annotation no longer delays a poll (interval
	// requeues and other annotation changes poll as before).
	now = now.Add(time.Second)
	_, err = r.Reconcile(context.Background(), reqFor(sub))
	require.NoError(t, err)
	assert.Equal(t, 2, polls)
}

// TestNewWatcher maps every spec field to its watcher.
func TestNewWatcher(t *testing.T) {
	img := makeImageSub("i", "ns", "p", "ghcr.io/org/app")
	img.Spec.Image.TagSelection = kardinalv1alpha1.TagSelection{TagFilter: "^v", ExcludeTagFilter: "rc",
		SemverConstraint: "^1", AllowTags: []string{"a"}, IgnoreTags: []string{"b"}}
	img.Spec.Image.Strategy = kardinalv1alpha1.TagStrategyLexical
	img.Spec.Image.DiscoveryLimit = 7
	w, err := subscription.NewWatcher(img, source.Credentials{Username: "u"})
	require.NoError(t, err)
	oci := w.(*source.OCIWatcher)
	assert.Equal(t, source.TagFilters{Include: "^v", Exclude: "rc", SemverConstraint: "^1", Allow: []string{"a"}, Ignore: []string{"b"}}, oci.Filters)
	assert.Equal(t, "Lexical", oci.Strategy)
	assert.Equal(t, 7, oci.DiscoveryLimit)
	assert.Equal(t, "u", oci.Credentials.Username)

	g := makeGitSub("g", "ns", "p", "https://x/y")
	g.Spec.Git.DiscoveryLimit = 9
	w, err = subscription.NewWatcher(g, source.Credentials{Token: "t"})
	require.NoError(t, err)
	gw := w.(*source.GitWatcher)
	assert.Equal(t, 9, gw.DiscoveryLimit)
	assert.Equal(t, "t", gw.Credentials.Token)

	h := helmSub("h", "ns", nil)
	h.Spec.Helm.SemverConstraint = ">=6"
	w, err = subscription.NewWatcher(h, source.Credentials{})
	require.NoError(t, err)
	hw := w.(*source.HelmWatcher)
	assert.Equal(t, "podinfo", hw.Chart)
	assert.Equal(t, ">=6", hw.Filters.SemverConstraint)

	_, err = subscription.NewWatcher(&kardinalv1alpha1.Subscription{Spec: kardinalv1alpha1.SubscriptionSpec{Type: "helm"}}, source.Credentials{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing spec.helm")
}

// TestSubscriptionReconciler_PrivateRegistryEndToEnd runs the real OCI
// watcher against a registry that requires a login, with the credentials
// read from a dockerconfigjson Secret.
func TestSubscriptionReconciler_PrivateRegistryEndToEnd(t *testing.T) {
	const user, pass = "ci", "pw"
	digests := map[string]string{"1.0.0": "sha256:aaaa1111", "1.1.0": "sha256:bbbb2222"}
	tags := []string{"1.0.0"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != user || p != pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="r"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/v2/org/app/tags/list":
			_, _ = w.Write([]byte(`{"tags":["` + strings.Join(tags, `","`) + `"]}`))
		case strings.HasPrefix(r.URL.Path, "/v2/org/app/manifests/"):
			w.Header().Set("Docker-Content-Digest", digests[strings.TrimPrefix(r.URL.Path, "/v2/org/app/manifests/")])
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	sub := makeImageSub("private", "default", "p", srv.URL+"/org/app")
	sub.Spec.Image.TagFilter = ""
	sub.Spec.Image.SecretRef = &kardinalv1alpha1.SubscriptionSecretRef{Name: "pull"}
	pull := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "pull", Namespace: "default"},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{".dockerconfigjson": []byte(`{"auths":{"` + host + `":{"username":"ci","password":"pw"}}}`)}}
	c := fake.NewClientBuilder().WithScheme(schemeWithSecrets()).WithObjects(sub, pull).WithStatusSubresource(sub).Build()
	r := newReconcilerWithRealWatchers(c, func() time.Time { return time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC) }, srv.Client())

	_, err := r.Reconcile(context.Background(), reqFor(sub))
	require.NoError(t, err)
	st := getSubscription(t, c, sub).Status
	require.Equal(t, "Watching", st.Phase, st.Message)
	assert.Equal(t, "sha256:aaaa1111", st.LastSeenDigest)

	tags = append(tags, "1.1.0")
	_, err = r.Reconcile(context.Background(), reqFor(sub))
	require.NoError(t, err)
	st = getSubscription(t, c, sub).Status
	assert.Equal(t, "private-1-1-0-bbbb2222", st.LastBundleCreated)
}
