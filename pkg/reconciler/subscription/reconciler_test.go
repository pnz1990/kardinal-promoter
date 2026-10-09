// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package subscription_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone/objectgonetest"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/subscription"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/source"
)

// newScheme returns a scheme with all kardinal types registered.
func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = kardinalv1alpha1.AddToScheme(s)
	return s
}

// makeImageSub creates a test Subscription of type image.
func makeImageSub(name, ns, pipeline, registry string) *kardinalv1alpha1.Subscription {
	return &kardinalv1alpha1.Subscription{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: kardinalv1alpha1.SubscriptionSpec{
			Type:     kardinalv1alpha1.SubscriptionTypeImage,
			Pipeline: pipeline,
			Image: &kardinalv1alpha1.ImageSubscriptionSpec{
				Registry:     registry,
				TagSelection: kardinalv1alpha1.TagSelection{TagFilter: "^sha-"},
				Interval:     "5m",
			},
		},
	}
}

// makeGitSub creates a test Subscription of type git.
func makeGitSub(name, ns, pipeline, repoURL string) *kardinalv1alpha1.Subscription {
	return &kardinalv1alpha1.Subscription{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: kardinalv1alpha1.SubscriptionSpec{
			Type:     kardinalv1alpha1.SubscriptionTypeGit,
			Pipeline: pipeline,
			Git: &kardinalv1alpha1.GitSubscriptionSpec{
				RepoURL:  repoURL,
				Branch:   "main",
				Interval: "5m",
			},
		},
	}
}

// changedWatcher follows the production watcher contract: the first poll
// (empty lastDigest) is a baseline and reports Changed=false.
type changedWatcher struct{ digest, tag string }

func (w *changedWatcher) Watch(_ context.Context, lastDigest string) (*source.WatchResult, error) {
	return &source.WatchResult{Digest: w.digest, Tag: w.tag, Changed: lastDigest != "" && w.digest != lastDigest}, nil
}

// forcedChangeWatcher always reports Changed=true, as two HA replicas reading a
// stale lastSeenDigest would both see.
type forcedChangeWatcher struct{ digest, tag string }

func (w *forcedChangeWatcher) Watch(_ context.Context, _ string) (*source.WatchResult, error) {
	return &source.WatchResult{Digest: w.digest, Tag: w.tag, Changed: true}, nil
}

// unchangedWatcher reports that nothing has changed.
type unchangedWatcher struct{ digest string }

func (w *unchangedWatcher) Watch(_ context.Context, _ string) (*source.WatchResult, error) {
	return &source.WatchResult{Digest: w.digest, Tag: "", Changed: false}, nil
}

// errWatcher always returns an error.
type errWatcher struct{}

func (w *errWatcher) Watch(_ context.Context, _ string) (*source.WatchResult, error) {
	return nil, fmt.Errorf("simulated watcher error")
}

// TestSubscriptionReconciler_ImageType_NoChange verifies that when the watcher
// reports no change, no Bundle is created and phase=Watching is set.
func TestSubscriptionReconciler_ImageType_NoChange(t *testing.T) {
	sub := makeImageSub("sub-nochange", "default", "my-pipeline", "ghcr.io/test/app")
	sub.Status.LastSeenDigest = "sha256:existing"

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sub).WithStatusSubresource(sub).Build()

	r := &subscription.Reconciler{
		Client: c,
		WatcherFn: func(_ *kardinalv1alpha1.Subscription, _ source.Credentials) (source.Watcher, error) {
			return &unchangedWatcher{"sha256:existing"}, nil
		},
		NowFn: func() time.Time { return time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC) },
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace}}

	result, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter.Seconds(), float64(0), "should requeue after interval")

	var got kardinalv1alpha1.Subscription
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.Equal(t, "Watching", got.Status.Phase)
	assert.Empty(t, got.Status.LastBundleCreated, "no bundle on no change")
}

// TestSubscriptionReconciler_ImageType_Changed verifies that when the watcher
// reports a new digest, a Bundle is created and status is updated.
func TestSubscriptionReconciler_ImageType_Changed(t *testing.T) {
	sub := makeImageSub("sub-changed", "default", "my-pipeline", "ghcr.io/test/app")
	sub.Status.LastSeenDigest = "sha256:old"

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sub).WithStatusSubresource(sub).Build()

	r := &subscription.Reconciler{
		Client: c,
		WatcherFn: func(_ *kardinalv1alpha1.Subscription, _ source.Credentials) (source.Watcher, error) {
			return &changedWatcher{digest: "sha256:new", tag: "sha-abc1234"}, nil
		},
		NowFn: func() time.Time { return time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC) },
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace}}

	result, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter.Seconds(), float64(0))

	var got kardinalv1alpha1.Subscription
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.Equal(t, "Watching", got.Status.Phase)
	assert.Equal(t, "sha256:new", got.Status.LastSeenDigest)
	assert.NotEmpty(t, got.Status.LastBundleCreated, "a Bundle must be created when digest changes")

	var bundleList kardinalv1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundleList))
	require.Len(t, bundleList.Items, 1, "exactly one Bundle must be created")
	assert.Equal(t, "my-pipeline", bundleList.Items[0].Labels["kardinal.io/pipeline"])
}

// TestSubscriptionReconciler_Deduplication verifies that the same digest does not
// create a second Bundle (idempotency — no change means Changed=false).
func TestSubscriptionReconciler_Deduplication(t *testing.T) {
	sub := makeImageSub("sub-dedup", "default", "my-pipeline", "ghcr.io/test/app")
	sub.Status.LastSeenDigest = "sha256:same"

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sub).WithStatusSubresource(sub).Build()

	r := &subscription.Reconciler{
		Client: c,
		WatcherFn: func(_ *kardinalv1alpha1.Subscription, _ source.Credentials) (source.Watcher, error) {
			// same digest as LastSeenDigest → Changed=false
			return &unchangedWatcher{"sha256:same"}, nil
		},
		NowFn: func() time.Time { return time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC) },
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace}}

	for i := 0; i < 3; i++ {
		_, err := r.Reconcile(context.Background(), req)
		require.NoErrorf(t, err, "iteration %d", i)
	}

	var bundleList kardinalv1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundleList))
	assert.Empty(t, bundleList.Items, "no Bundles should be created for unchanged digest")
}

// TestSubscriptionReconciler_WatcherError verifies that watcher errors set phase=Error.
func TestSubscriptionReconciler_WatcherError(t *testing.T) {
	sub := makeImageSub("sub-error", "default", "my-pipeline", "ghcr.io/test/app")

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sub).WithStatusSubresource(sub).Build()

	r := &subscription.Reconciler{
		Client: c,
		WatcherFn: func(_ *kardinalv1alpha1.Subscription, _ source.Credentials) (source.Watcher, error) {
			return &errWatcher{}, nil
		},
		NowFn: func() time.Time { return time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC) },
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace}}

	result, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err, "reconcile must not error — write Error phase to status instead")
	assert.Greater(t, result.RequeueAfter.Seconds(), float64(0))

	var got kardinalv1alpha1.Subscription
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.Equal(t, "Error", got.Status.Phase)
	assert.Contains(t, got.Status.Message, "simulated watcher error")
}

// TestSubscriptionReconciler_GitType_Changed verifies git subscriptions create config Bundles.
func TestSubscriptionReconciler_GitType_Changed(t *testing.T) {
	sub := makeGitSub("sub-git", "default", "my-pipeline", "https://github.com/myorg/myapp")
	sub.Status.LastSeenDigest = "0000000old"

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sub).WithStatusSubresource(sub).Build()

	r := &subscription.Reconciler{
		Client: c,
		WatcherFn: func(_ *kardinalv1alpha1.Subscription, _ source.Credentials) (source.Watcher, error) {
			return &changedWatcher{digest: "abc1234def5678", tag: "abc1234"}, nil
		},
		NowFn: func() time.Time { return time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC) },
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace}}

	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var bundleList kardinalv1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundleList))
	require.Len(t, bundleList.Items, 1)
	assert.Equal(t, "config", bundleList.Items[0].Spec.Type, "git → config Bundle")
}

// TestSubscriptionReconciler_Idempotent verifies safe re-run after crash.
func TestSubscriptionReconciler_Idempotent(t *testing.T) {
	sub := makeImageSub("sub-idempotent", "default", "my-pipeline", "ghcr.io/test/app")
	sub.Status.LastSeenDigest = "sha256:stable"

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sub).WithStatusSubresource(sub).Build()

	r := &subscription.Reconciler{
		Client: c,
		WatcherFn: func(_ *kardinalv1alpha1.Subscription, _ source.Credentials) (source.Watcher, error) {
			return &unchangedWatcher{"sha256:stable"}, nil
		},
		NowFn: func() time.Time { return time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC) },
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace}}

	for i := 0; i < 3; i++ {
		result, err := r.Reconcile(context.Background(), req)
		require.NoErrorf(t, err, "iter %d", i)
		assert.Greater(t, result.RequeueAfter.Seconds(), float64(0), "iter %d", i)
	}

	var bundleList kardinalv1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundleList))
	assert.Empty(t, bundleList.Items, "no Bundles for unchanged digest")
}

// TestSubscriptionReconciler_LabelSelectorDedup verifies that the label-selector
// deduplication prevents duplicate Bundle creation under concurrent reconciles (#620).
//
// This simulates the HA race: two reconcile calls see Changed=true because the
// status.lastSeenDigest hasn't been updated yet. The label-selector check must
// prevent the second call from creating a duplicate Bundle.
func TestSubscriptionReconciler_LabelSelectorDedup(t *testing.T) {
	digest := "sha256:abc123def456abc123def456abc123def456abc123def456abc123def456abc1"
	sub := makeImageSub("sub-ha", "default", "my-pipeline", "ghcr.io/test/app")
	// LastSeenDigest is EMPTY — simulating first run or HA split-brain
	sub.Status.LastSeenDigest = ""

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sub).WithStatusSubresource(sub).Build()

	r := &subscription.Reconciler{
		Client: c,
		WatcherFn: func(_ *kardinalv1alpha1.Subscription, _ source.Credentials) (source.Watcher, error) {
			return &forcedChangeWatcher{digest: digest, tag: "v2.0.0"}, nil
		},
		NowFn: func() time.Time { return time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC) },
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace}}

	// First reconcile — creates the Bundle
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	// Second reconcile with the SAME digest (simulating concurrent/restart scenario)
	// — the status may not yet reflect the new digest on the second call.
	// The label-selector check must prevent a duplicate.
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var bundleList kardinalv1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundleList,
		client.InNamespace("default"),
	))
	assert.Len(t, bundleList.Items, 1,
		"exactly 1 Bundle should exist — label-selector dedup must prevent duplicates")

	// Verify the source-digest label is set (sanitized: sha256: prefix stripped, truncated to 63)
	assert.NotEmpty(t, bundleList.Items[0].Labels["kardinal.io/source-digest"],
		"source-digest label must be set")
}

// e2eDigest is the digest from the live repro of E2E-R13 (gaps-sub.log:59).
const e2eDigest = "sha256:34f9009520f4faa9bf1fcfc1d63a8d178e964e29d64019900abe41e4b613d04e"

// TestSubscriptionReconciler_SourceDigestLabelIsDigestPrefix covers E2E-R13:
// the kardinal.io/source-digest label kept the last 63 hex characters of a
// sha256 digest, so it dropped the first one ("4f90..." for "34f90...") and
// matched neither the digest nor the short form in the Bundle name.
func TestSubscriptionReconciler_SourceDigestLabelIsDigestPrefix(t *testing.T) {
	hex := strings.TrimPrefix(e2eDigest, "sha256:")
	sub := makeImageSub("gaps-img2", "default", "gaps-sub-img", "ghcr.io/test/app")
	sub.Status.LastSeenDigest = digestOf('0')
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(sub).WithStatusSubresource(sub).Build()
	r := &subscription.Reconciler{
		Client: c,
		WatcherFn: func(_ *kardinalv1alpha1.Subscription, _ source.Credentials) (source.Watcher, error) {
			return &changedWatcher{digest: e2eDigest, tag: "1.1.0"}, nil
		},
		NowFn: func() time.Time { return time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC) },
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace}}
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	bundles := listBundles(t, c)
	require.Len(t, bundles, 1)
	assert.Equal(t, "gaps-img2-1-1-0-34f90095", bundles[0].Name)
	label := bundles[0].Labels["kardinal.io/source-digest"]
	assert.Equal(t, hex[:63], label, "the label is the first 63 hex characters of the digest")
	assert.True(t, strings.HasSuffix(bundles[0].Name, label[:8]), "the Bundle name short form is a prefix of the label")

	var found kardinalv1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &found,
		client.MatchingLabels{"kardinal.io/source-digest": hex[:63]}))
	assert.Len(t, found.Items, 1, "a selector on the digest prefix finds the Bundle")
}

// TestSubscriptionReconciler_LegacyDigestLabelStillDedups verifies that a
// Bundle labelled by a controller from before the E2E-R13 fix (the last 63 hex
// characters of the digest) still dedups after an upgrade: the reconcile
// reuses it instead of creating a duplicate Bundle or going to phase Error.
func TestSubscriptionReconciler_LegacyDigestLabelStillDedups(t *testing.T) {
	hex := strings.TrimPrefix(e2eDigest, "sha256:")
	legacy := func(name string) *kardinalv1alpha1.Bundle {
		return &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline":      "gaps-sub-img",
				"kardinal.io/subscription":  "gaps-img2",
				"kardinal.io/source-digest": hex[1:],
			},
		}}
	}
	// emptyBundleList makes every Bundle List return nothing, as a cache that
	// has not seen the Bundle yet would, so the Create hits AlreadyExists.
	emptyBundleList := interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*kardinalv1alpha1.BundleList); ok {
				return nil
			}
			return c.List(ctx, list, opts...)
		},
	}
	tests := []struct {
		name      string
		existing  string
		staleList bool
	}{
		{name: "same generated name, found by the label lookup", existing: "gaps-img2-1-1-0-34f90095"},
		{name: "other name, found by the label lookup", existing: "gaps-img2-20260413-095900"},
		{name: "same generated name, lookup misses and Create hits AlreadyExists", existing: "gaps-img2-1-1-0-34f90095", staleList: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := makeImageSub("gaps-img2", "default", "gaps-sub-img", "ghcr.io/test/app")
			base := fake.NewClientBuilder().WithScheme(newScheme()).
				WithObjects(sub, legacy(tt.existing)).WithStatusSubresource(sub).Build()
			var c client.Client = base
			if tt.staleList {
				c = interceptor.NewClient(base, emptyBundleList)
			}
			r := &subscription.Reconciler{
				Client: c,
				WatcherFn: func(_ *kardinalv1alpha1.Subscription, _ source.Credentials) (source.Watcher, error) {
					return &forcedChangeWatcher{digest: e2eDigest, tag: "1.1.0"}, nil
				},
				NowFn: func() time.Time { return time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC) },
			}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace}}
			for i := 0; i < 2; i++ {
				_, err := r.Reconcile(context.Background(), req)
				require.NoErrorf(t, err, "reconcile %d", i)
			}

			var got kardinalv1alpha1.Subscription
			require.NoError(t, base.Get(context.Background(), req.NamespacedName, &got))
			assert.Equal(t, "Watching", got.Status.Phase, got.Status.Message)
			assert.Equal(t, tt.existing, got.Status.LastBundleCreated)
			bundles := listBundles(t, base)
			require.Len(t, bundles, 1, "no duplicate Bundle for a digest labelled the old way")
			assert.Equal(t, tt.existing, bundles[0].Name)
		})
	}
}

// newReconcilerWithRealWatchers wires the production watchers as
// cmd/kardinal-controller/main.go does, except that they send their requests
// with httpClient: the egress guard of the default client refuses the
// loopback address an httptest server listens on.
func newReconcilerWithRealWatchers(c client.Client, now func() time.Time, httpClient *http.Client) *subscription.Reconciler {
	return &subscription.Reconciler{
		Client: c,
		WatcherFn: func(sub *kardinalv1alpha1.Subscription, creds source.Credentials) (source.Watcher, error) {
			w, err := subscription.NewWatcher(sub, creds)
			switch w := w.(type) {
			case *source.OCIWatcher:
				w.WithHTTPClient(httpClient)
			case *source.GitWatcher:
				w.WithHTTPClient(httpClient)
			case *source.HelmWatcher:
				w.WithHTTPClient(httpClient)
			}
			return w, err
		},
		NowFn: now,
	}
}

// fakeRegistry is a minimal OCI distribution API: a tag list and HEAD manifests.
type fakeRegistry struct {
	mu   sync.Mutex
	name string
	tags map[string]string // tag -> digest
}

func (f *fakeRegistry) set(tag, digest string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tags[tag] = digest
}

func (f *fakeRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := "/v2/" + f.name + "/"
	switch {
	case r.URL.Path == prefix+"tags/list":
		names := make([]string, 0, len(f.tags))
		for tag := range f.tags {
			names = append(names, `"`+tag+`"`)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"name":%q,"tags":[%s]}`, f.name, strings.Join(names, ","))
	case strings.HasPrefix(r.URL.Path, prefix+"manifests/"):
		digest, ok := f.tags[strings.TrimPrefix(r.URL.Path, prefix+"manifests/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Docker-Content-Digest", digest)
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func digestOf(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }

func listBundles(t *testing.T, c client.Client) []kardinalv1alpha1.Bundle {
	t.Helper()
	var list kardinalv1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &list, client.InNamespace("default")))
	return list.Items
}

// TestSubscriptionReconciler_RealOCIWatcher_NewPushCreatesBundle runs the
// production OCIWatcher against an httptest registry: the first poll records a
// baseline, and a later push creates exactly one Bundle (C04-gates-04, C05-steps-04).
func TestSubscriptionReconciler_RealOCIWatcher_NewPushCreatesBundle(t *testing.T) {
	reg := &fakeRegistry{name: "org/app", tags: map[string]string{"v1.0.0": digestOf('1')}}
	srv := httptest.NewServer(reg)
	defer srv.Close()

	sub := makeImageSub("app-sub", "default", "my-pipeline", srv.URL+"/org/app")
	sub.Spec.Image.TagFilter = `^v\d+\.\d+\.\d+$`
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(sub).WithStatusSubresource(sub).Build()
	r := newReconcilerWithRealWatchers(c, func() time.Time { return time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC) }, srv.Client())
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace}}

	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	var got kardinalv1alpha1.Subscription
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	require.Equal(t, "Watching", got.Status.Phase, got.Status.Message)
	assert.Equal(t, digestOf('1'), got.Status.LastSeenDigest, "first poll records the baseline")
	assert.Empty(t, listBundles(t, c), "the baseline does not create a Bundle")

	reg.set("v1.1.0", digestOf('2'))
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.Equal(t, digestOf('2'), got.Status.LastSeenDigest)

	bundles := listBundles(t, c)
	require.Len(t, bundles, 1, "a new push creates a Bundle")
	assert.Equal(t, got.Status.LastBundleCreated, bundles[0].Name)
	require.Len(t, bundles[0].Spec.Images, 1)
	assert.Equal(t, "v1.1.0", bundles[0].Spec.Images[0].Tag)
	assert.Equal(t, digestOf('2'), bundles[0].Spec.Images[0].Digest)
	assert.Equal(t, strings.TrimPrefix(srv.URL, "http://")+"/org/app", bundles[0].Spec.Images[0].Repository,
		"the Bundle image repository carries no URL scheme")

	// A further poll with no push changes nothing.
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Len(t, listBundles(t, c), 1)
}

// TestSubscriptionReconciler_MutableTagRepush verifies that re-pushing the same
// tag with a new digest creates a second Bundle instead of colliding on the
// name (C04-gates-19).
func TestSubscriptionReconciler_MutableTagRepush(t *testing.T) {
	reg := &fakeRegistry{name: "org/app", tags: map[string]string{"latest": digestOf('1')}}
	srv := httptest.NewServer(reg)
	defer srv.Close()

	sub := makeImageSub("app-sub", "default", "my-pipeline", srv.URL+"/org/app")
	sub.Spec.Image.TagFilter = "^latest$"
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(sub).WithStatusSubresource(sub).Build()
	r := newReconcilerWithRealWatchers(c, func() time.Time { return time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC) }, srv.Client())
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace}}

	for _, d := range []byte{'1', '2', '3'} {
		reg.set("latest", digestOf(d))
		_, err := r.Reconcile(context.Background(), req)
		require.NoError(t, err)
	}
	bundles := listBundles(t, c)
	require.Len(t, bundles, 2, "each re-push after the baseline creates a Bundle")
	names := []string{bundles[0].Name, bundles[1].Name}
	assert.ElementsMatch(t, []string{"app-sub-latest-22222222", "app-sub-latest-33333333"}, names)
}

// TestSubscriptionReconciler_ReturningDigest verifies that a moving tag pushed
// back to an earlier image creates a new Bundle, named after the earlier one
// with a -<n> suffix, also after the oldest Bundle was pruned, and that a
// reconcile that sees the same change again (a stale lastSeenDigest) finds the
// newest Bundle and creates nothing.
func TestSubscriptionReconciler_ReturningDigest(t *testing.T) {
	reg := &fakeRegistry{name: "org/app", tags: map[string]string{"main": digestOf('1')}}
	srv := httptest.NewServer(reg)
	defer srv.Close()

	sub := makeImageSub("app-sub", "default", "my-pipeline", srv.URL+"/org/app")
	sub.Spec.Image.TagFilter = "^main$"
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(sub).WithStatusSubresource(sub).Build()
	now := time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC)
	r := newReconcilerWithRealWatchers(c, func() time.Time { now = now.Add(time.Second); return now }, srv.Client())
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace}}
	ctx := context.Background()

	poll := func(d byte) string {
		t.Helper()
		reg.set("main", digestOf(d))
		_, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
		var got kardinalv1alpha1.Subscription
		require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
		require.Equal(t, "Watching", got.Status.Phase, got.Status.Message)
		return got.Status.LastBundleCreated
	}
	var created []string
	for _, d := range []byte{'1', '2', '3', '2', '3', '2'} {
		created = append(created, poll(d))
	}
	assert.Equal(t, []string{"", "app-sub-main-22222222", "app-sub-main-33333333", "app-sub-main-22222222-2",
		"app-sub-main-33333333-2", "app-sub-main-22222222-3"}, created)
	assert.Len(t, listBundles(t, c), 5)

	// The same change seen again creates nothing.
	var got kardinalv1alpha1.Subscription
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	orig := got.DeepCopy()
	got.Status.LastSeenDigest = digestOf('3')
	got.Status.LastBundleCreated = ""
	require.NoError(t, c.Status().Patch(ctx, &got, client.MergeFrom(orig)))
	assert.Equal(t, "app-sub-main-22222222-3", poll('2'))
	assert.Len(t, listBundles(t, c), 5)

	// historyLimit pruned the first Bundle for the digest: the suffix still grows.
	require.NoError(t, c.Delete(ctx, &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{
		Name: "app-sub-main-22222222", Namespace: "default"}}))
	assert.Equal(t, "app-sub-main-33333333-3", poll('3'))
	assert.Equal(t, "app-sub-main-22222222-4", poll('2'))
	assert.Len(t, listBundles(t, c), 6)
}

// TestSubscriptionReconciler_NameCollisionWithOtherDigestIsAnError verifies that
// AlreadyExists is only treated as crash recovery when the existing Bundle is
// for the same digest (C04-gates-19).
func TestSubscriptionReconciler_NameCollisionWithOtherDigestIsAnError(t *testing.T) {
	sub := makeImageSub("app-sub", "default", "my-pipeline", "ghcr.io/org/app")
	sub.Status.LastSeenDigest = digestOf('0')
	squatter := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name: "app-sub-latest-11111111", Namespace: "default",
			Labels: map[string]string{"kardinal.io/subscription": "other", "kardinal.io/source-digest": "x"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(sub, squatter).WithStatusSubresource(sub).Build()
	r := &subscription.Reconciler{
		Client: c,
		WatcherFn: func(_ *kardinalv1alpha1.Subscription, _ source.Credentials) (source.Watcher, error) {
			return &changedWatcher{digest: digestOf('1'), tag: "latest"}, nil
		},
		NowFn: func() time.Time { return time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC) },
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace}}

	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	var got kardinalv1alpha1.Subscription
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.Equal(t, "Error", got.Status.Phase)
	assert.Contains(t, got.Status.Message, "already exists for another artifact")
	assert.Equal(t, digestOf('0'), got.Status.LastSeenDigest, "the digest is not consumed")
}

// TestSubscriptionReconciler_SpecNamespace verifies that a Subscription creates
// Bundles only in its own namespace (C08-api-config-02, C04-gates-18).
func TestSubscriptionReconciler_SpecNamespace(t *testing.T) {
	tests := []struct {
		name      string
		specNS    string
		wantPhase string
		wantNS    string
	}{
		{name: "other namespace is rejected", specNS: "team-b", wantPhase: "Error"},
		{name: "own namespace is allowed", specNS: "default", wantPhase: "Watching", wantNS: "default"},
		{name: "empty uses own namespace", specNS: "", wantPhase: "Watching", wantNS: "default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := makeImageSub("app-sub", "default", "my-pipeline", "ghcr.io/org/app")
			sub.Spec.Namespace = tt.specNS
			sub.Status.LastSeenDigest = digestOf('0')
			c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(sub).WithStatusSubresource(sub).Build()
			r := &subscription.Reconciler{
				Client: c,
				WatcherFn: func(_ *kardinalv1alpha1.Subscription, _ source.Credentials) (source.Watcher, error) {
					return &changedWatcher{digest: digestOf('1'), tag: "v1.0.0"}, nil
				},
				NowFn: func() time.Time { return time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC) },
			}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace}}
			_, err := r.Reconcile(context.Background(), req)
			require.NoError(t, err)

			var got kardinalv1alpha1.Subscription
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Equal(t, tt.wantPhase, got.Status.Phase, got.Status.Message)

			var all kardinalv1alpha1.BundleList
			require.NoError(t, c.List(context.Background(), &all))
			if tt.wantNS == "" {
				assert.Empty(t, all.Items, "no Bundle in any namespace")
				assert.Contains(t, got.Status.Message, `spec.namespace "team-b" is not allowed`)
				return
			}
			require.Len(t, all.Items, 1)
			assert.Equal(t, tt.wantNS, all.Items[0].Namespace)
		})
	}
}

// TestSubscriptionReconciler_DeletedBeforeStatusWrite: a Subscription deleted
// between its read and its status write, after a poll or a failed poll, ends
// the reconcile with no error, requeue or warn or error log.
func TestSubscriptionReconciler_DeletedBeforeStatusWrite(t *testing.T) {
	tests := []struct {
		name    string
		watcher source.Watcher
	}{
		{name: "no change", watcher: &unchangedWatcher{"sha256:existing"}},
		{name: "watch error", watcher: &errWatcher{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := makeImageSub("sub-deleted", "default", "my-pipeline", "ghcr.io/test/app")
			sub.Status.LastSeenDigest = "sha256:existing"
			c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(sub).WithStatusSubresource(sub).
				WithInterceptorFuncs(objectgonetest.DeleteOnWrite(t, nil)).Build()
			r := &subscription.Reconciler{
				Client: c,
				WatcherFn: func(*kardinalv1alpha1.Subscription, source.Credentials) (source.Watcher, error) {
					return tt.watcher, nil
				},
				NowFn: func() time.Time { return time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC) },
			}
			var logs bytes.Buffer
			res, err := r.Reconcile(objectgonetest.Context(&logs), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace},
			})
			objectgonetest.AssertQuiet(t, res, err, &logs)
		})
	}
}
