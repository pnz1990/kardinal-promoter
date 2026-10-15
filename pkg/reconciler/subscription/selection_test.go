// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package subscription_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/subscription"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/source"
)

// TestSubscriptionReconciler_SelectionChangeIsBaseline (#1943): editing what
// selects the tag (or chart version) records a new baseline and creates no
// Bundle, although the new selection's digest differs from lastSeenDigest: it
// may be an older artifact, and promoting it would roll the Pipeline back.
// The next new artifact under the new selection is a Bundle. An edit that
// does not change the selection (the interval, the order of a tag list, a
// default spelled out) is not a new baseline.
func TestSubscriptionReconciler_SelectionChangeIsBaseline(t *testing.T) {
	image := func() *kardinalv1alpha1.Subscription {
		s := makeImageSub("img", "default", "p", "ghcr.io/test/app")
		s.Spec.Image.Strategy = kardinalv1alpha1.TagStrategyNewestBuild
		s.Spec.Image.AllowTags = []string{"sha-1", "sha-2"}
		return s
	}
	helm := func() *kardinalv1alpha1.Subscription { return helmSub("chart", "default", nil) }
	cases := []struct {
		name     string
		sub      func() *kardinalv1alpha1.Subscription
		edit     func(*kardinalv1alpha1.Subscription)
		baseline bool // the edit is a new baseline
	}{
		{"image strategy", image, func(s *kardinalv1alpha1.Subscription) { s.Spec.Image.Strategy = kardinalv1alpha1.TagStrategyLexical }, true},
		{"image tagFilter", image, func(s *kardinalv1alpha1.Subscription) { s.Spec.Image.TagFilter = "^sha-1" }, true},
		{"image excludeTagFilter", image, func(s *kardinalv1alpha1.Subscription) { s.Spec.Image.ExcludeTagFilter = "-rc" }, true},
		{"image allowTags", image, func(s *kardinalv1alpha1.Subscription) { s.Spec.Image.AllowTags = []string{"sha-1"} }, true},
		{"image ignoreTags", image, func(s *kardinalv1alpha1.Subscription) { s.Spec.Image.IgnoreTags = []string{"sha-2"} }, true},
		{"image semverConstraint", image, func(s *kardinalv1alpha1.Subscription) { s.Spec.Image.SemverConstraint = "^1" }, true},
		{"image discoveryLimit", image, func(s *kardinalv1alpha1.Subscription) { s.Spec.Image.DiscoveryLimit = 10 }, true},
		{"helm tagFilter", helm, func(s *kardinalv1alpha1.Subscription) { s.Spec.Helm.TagFilter = "^6\\." }, true},
		{"helm semverConstraint", helm, func(s *kardinalv1alpha1.Subscription) { s.Spec.Helm.SemverConstraint = "<6.0.0" }, true},
		{"helm ignoreTags", helm, func(s *kardinalv1alpha1.Subscription) { s.Spec.Helm.IgnoreTags = []string{"6.15.0"} }, true},
		{"image interval", image, func(s *kardinalv1alpha1.Subscription) { s.Spec.Image.Interval = "10m" }, false},
		{"image allowTags reordered", image, func(s *kardinalv1alpha1.Subscription) { s.Spec.Image.AllowTags = []string{"sha-2", "sha-1"} }, false},
		{"image default discoveryLimit spelled out", image, func(s *kardinalv1alpha1.Subscription) { s.Spec.Image.DiscoveryLimit = 50 }, false},
		{"helm interval", helm, func(s *kardinalv1alpha1.Subscription) { s.Spec.Helm.Interval = "10m" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := tc.sub()
			c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(sub).WithStatusSubresource(sub).
				WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
			w := &changedWatcher{}
			r := &subscription.Reconciler{Client: c,
				WatcherFn: func(*kardinalv1alpha1.Subscription, source.Credentials) (source.Watcher, error) { return w, nil },
				NowFn:     func() time.Time { return time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC) }}
			poll := func(digest, tag string) kardinalv1alpha1.SubscriptionStatus {
				t.Helper()
				w.digest, w.tag = digest, tag
				_, err := r.Reconcile(context.Background(), reqFor(sub))
				require.NoError(t, err)
				return getSubscription(t, c, sub).Status
			}

			st := poll(digestOf('a'), "sha-a")
			require.Empty(t, st.LastBundleCreated, "the first poll is a baseline")
			before := st.ObservedSelection
			require.NotEmpty(t, before, "the baseline records its selection")
			st = poll(digestOf('b'), "sha-b")
			require.NotEmpty(t, st.LastBundleCreated, "a new artifact under the same selection is a Bundle")
			first := st.LastBundleCreated

			cur := getSubscription(t, c, sub)
			tc.edit(cur)
			require.NoError(t, c.Update(context.Background(), cur))
			st = poll(digestOf('c'), "sha-c") // what the edited selection picks, perhaps an older build
			assert.Equal(t, digestOf('c'), st.LastSeenDigest)
			if !tc.baseline {
				assert.NotEqual(t, first, st.LastBundleCreated, "an edit that keeps the selection is no baseline")
				assert.Equal(t, before, st.ObservedSelection)
				assert.Len(t, listBundles(t, c), 2)
				return
			}
			assert.Equal(t, first, st.LastBundleCreated, "a new selection is a baseline, not a version")
			assert.Len(t, listBundles(t, c), 1)
			assert.NotEqual(t, before, st.ObservedSelection)
			after := st.ObservedSelection

			st = poll(digestOf('c'), "sha-c")
			assert.Equal(t, first, st.LastBundleCreated, "the new baseline is not a version on the next poll either")
			st = poll(digestOf('d'), "sha-d")
			assert.NotEqual(t, first, st.LastBundleCreated, "a new artifact under the new selection is a Bundle")
			assert.Len(t, listBundles(t, c), 2)
			assert.Equal(t, after, st.ObservedSelection)
		})
	}
}

// TestSubscriptionReconciler_SelectionRecordedOnUpgrade: a Subscription whose
// baseline predates status.observedSelection (an upgrade) keeps it: the next
// new artifact is a Bundle, as before, and the selection is recorded.
func TestSubscriptionReconciler_SelectionRecordedOnUpgrade(t *testing.T) {
	sub := makeImageSub("img", "default", "p", "ghcr.io/test/app")
	sub.Status.LastSeenDigest = digestOf('a')
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(sub).WithStatusSubresource(sub).
		WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	r := &subscription.Reconciler{Client: c,
		WatcherFn: func(*kardinalv1alpha1.Subscription, source.Credentials) (source.Watcher, error) {
			return &changedWatcher{digest: digestOf('b'), tag: "sha-b"}, nil
		},
		NowFn: func() time.Time { return time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC) }}
	_, err := r.Reconcile(context.Background(), reqFor(sub))
	require.NoError(t, err)
	st := getSubscription(t, c, sub).Status
	assert.NotEmpty(t, st.LastBundleCreated)
	assert.NotEmpty(t, st.ObservedSelection)
}
