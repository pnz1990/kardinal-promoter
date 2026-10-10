// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package subscription_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/subscription"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/source"
)

// TestSubscription_StampsCreatedAt covers the Subscription half of
// C02-bundle-04: Bundles a Subscription creates carry the sub-second creation
// stamp the Bundle reconciler orders same-second Bundles by.
func TestSubscription_StampsCreatedAt(t *testing.T) {
	now := time.Date(2026, 4, 13, 10, 0, 0, 123456789, time.UTC)
	sub := makeImageSub("sub-stamp", "default", "my-pipeline", "ghcr.io/test/app")
	sub.Status.LastSeenDigest = "sha256:old"
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(sub).WithStatusSubresource(sub).WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	r := &subscription.Reconciler{
		Client: c,
		WatcherFn: func(_ *kardinalv1alpha1.Subscription, _ source.Credentials) (source.Watcher, error) {
			return &changedWatcher{digest: "sha256:new", tag: "sha-abc1234"}, nil
		},
		NowFn: func() time.Time { return now },
	}
	_, err := r.Reconcile(context.Background(),
		ctrl.Request{NamespacedName: types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace}})
	require.NoError(t, err)

	var list kardinalv1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &list))
	require.Len(t, list.Items, 1)
	assert.Equal(t, now.Format(time.RFC3339Nano), list.Items[0].Annotations[lifecycle.AnnotationCreatedAt])
}
