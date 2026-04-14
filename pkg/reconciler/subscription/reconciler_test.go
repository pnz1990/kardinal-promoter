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
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
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
				Registry:  registry,
				TagFilter: "^sha-",
				Interval:  "5m",
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

// changedWatcher reports that a new digest is available.
type changedWatcher struct{ digest, tag string }

func (w *changedWatcher) Watch(_ context.Context, lastDigest string) (*source.WatchResult, error) {
	return &source.WatchResult{Digest: w.digest, Tag: w.tag, Changed: w.digest != lastDigest}, nil
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
		WatcherFn: func(_ *kardinalv1alpha1.Subscription) (source.Watcher, error) {
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
		WatcherFn: func(_ *kardinalv1alpha1.Subscription) (source.Watcher, error) {
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
		WatcherFn: func(_ *kardinalv1alpha1.Subscription) (source.Watcher, error) {
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
		Client:    c,
		WatcherFn: func(_ *kardinalv1alpha1.Subscription) (source.Watcher, error) { return &errWatcher{}, nil },
		NowFn:     func() time.Time { return time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC) },
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

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sub).WithStatusSubresource(sub).Build()

	r := &subscription.Reconciler{
		Client: c,
		WatcherFn: func(_ *kardinalv1alpha1.Subscription) (source.Watcher, error) {
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
		WatcherFn: func(_ *kardinalv1alpha1.Subscription) (source.Watcher, error) {
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
