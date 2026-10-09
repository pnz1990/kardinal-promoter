// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package subscription_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/subscription"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/source"
)

// TestSubscriptionReconciler_RejectedArtifactNotPromoted: when the new image
// is one a rejected Bundle of the pipeline carries (same digest), no Bundle
// is created; the digest is recorded as seen, with a message naming the
// rejected Bundle, so the next poll does not try again. A different digest
// still gets its Bundle.
func TestSubscriptionReconciler_RejectedArtifactNotPromoted(t *testing.T) {
	sub := makeImageSub("sub-rej", "default", "my-pipeline", "ghcr.io/test/app")
	sub.Status.LastSeenDigest = "sha256:old"
	bad := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pipeline-bad", Namespace: "default"},
		Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "my-pipeline",
			Images:   []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/test/app", Tag: "sha-bad", Digest: "sha256:bad"}},
			Rejected: &kardinalv1alpha1.BundleRejection{By: "alice", Reason: "CVE"}},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Rejected"},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(sub, bad).WithStatusSubresource(sub, bad).Build()
	digest := "sha256:bad"
	r := &subscription.Reconciler{
		Client: c,
		WatcherFn: func(_ *kardinalv1alpha1.Subscription) (source.Watcher, error) {
			return &forcedChangeWatcher{digest: digest, tag: "sha-retag"}, nil
		},
		NowFn: func() time.Time { return time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC) },
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace}}

	for range 2 {
		_, err := r.Reconcile(context.Background(), req)
		require.NoError(t, err)
	}
	var got kardinalv1alpha1.Subscription
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.Equal(t, "Watching", got.Status.Phase)
	assert.Equal(t, "sha256:bad", got.Status.LastSeenDigest)
	assert.Equal(t, "artifact sha256:bad was rejected (bundle my-pipeline-bad): no Bundle is created for it", got.Status.Message)
	var list kardinalv1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &list))
	assert.Len(t, list.Items, 1, "no Bundle for the rejected digest")

	digest = "sha256:good"
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	require.NoError(t, c.List(context.Background(), &list))
	assert.Len(t, list.Items, 2, "a new digest still gets a Bundle")
}

// TestSubscriptionReconciler_RejectedMovingTag (QA #1489): rejecting
// app:latest@sha256:bad does not block a fixed image pushed under the same
// tag (app:latest@sha256:fixed): a rejected image with a digest matches by
// digest only. The rejected digest itself, under the same tag, stays blocked.
//
// Covers BUNDLE-REJECT-06.
func TestSubscriptionReconciler_RejectedMovingTag(t *testing.T) {
	sub := makeImageSub("sub-latest", "default", "my-pipeline", "ghcr.io/test/app")
	sub.Status.LastSeenDigest = "sha256:old"
	bad := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pipeline-bad", Namespace: "default"},
		Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "my-pipeline",
			Images:   []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/test/app", Tag: "latest", Digest: "sha256:bad"}},
			Rejected: &kardinalv1alpha1.BundleRejection{By: "alice", Reason: "CVE"}},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Rejected"},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(sub, bad).WithStatusSubresource(sub, bad).Build()
	digest := "sha256:bad"
	r := &subscription.Reconciler{
		Client: c,
		WatcherFn: func(_ *kardinalv1alpha1.Subscription) (source.Watcher, error) {
			return &forcedChangeWatcher{digest: digest, tag: "latest"}, nil
		},
		NowFn: func() time.Time { return time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC) },
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace}}
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	var list kardinalv1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &list))
	assert.Len(t, list.Items, 1, "the rejected digest is still blocked")

	digest = "sha256:fixed"
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	require.NoError(t, c.List(context.Background(), &list))
	require.Len(t, list.Items, 2, "the fixed image under the same tag gets a Bundle")
	for _, b := range list.Items {
		if b.Name != bad.Name {
			require.Len(t, b.Spec.Images, 1)
			assert.Equal(t, "latest", b.Spec.Images[0].Tag)
			assert.Equal(t, "sha256:fixed", b.Spec.Images[0].Digest)
		}
	}
}
