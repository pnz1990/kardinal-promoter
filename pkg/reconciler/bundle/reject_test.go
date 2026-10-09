// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
)

func rejection() *kardinalv1alpha1.BundleRejection {
	at := metav1.NewTime(time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC))
	return &kardinalv1alpha1.BundleRejection{By: "alice", Reason: "CVE-2026-1 in the base image", At: &at}
}

// TestBundleReconciler_Rejected moves a Bundle whose spec.rejected is set to
// Rejected from every phase, Verified and Superseded included, with the
// Ready and Rejected conditions naming who rejected it and why, and one
// Warning Event. The Bundle is not translated, and a second reconcile
// changes nothing and emits nothing (idempotent).
func TestBundleReconciler_Rejected(t *testing.T) {
	for _, phase := range []string{"", "Available", "Promoting", "Failed", "Verified", "Superseded"} {
		t.Run("from "+phase, func(t *testing.T) {
			pipeline := &kardinalv1alpha1.Pipeline{
				ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
				Spec:       kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "prod"}}},
			}
			b := &kardinalv1alpha1.Bundle{
				ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default"},
				Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app", Rejected: rejection()},
				Status:     kardinalv1alpha1.BundleStatus{Phase: phase, GraphRef: "app-app-v1"},
			}
			c := indexedBuilder(newScheme()).WithObjects(pipeline, b).WithStatusSubresource(b).Build()
			tr := &mockTranslator{graphName: "app-app-v1"}
			rec := events.NewFakeRecorder(10)
			r := &bundle.Reconciler{Client: c, Translator: tr, GraphChecker: &mockGraphChecker{exists: false}, Recorder: rec}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "app-v1", Namespace: "default"}}

			_, err := r.Reconcile(context.Background(), req)
			require.NoError(t, err)
			var got kardinalv1alpha1.Bundle
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Equal(t, "Rejected", got.Status.Phase)
			ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
			require.NotNil(t, ready)
			assert.Equal(t, metav1.ConditionFalse, ready.Status)
			assert.Equal(t, "Rejected", ready.Reason)
			cond := meta.FindStatusCondition(got.Status.Conditions, "Rejected")
			require.NotNil(t, cond)
			assert.Equal(t, metav1.ConditionTrue, cond.Status)
			assert.Equal(t, "rejected by alice: CVE-2026-1 in the base image; it is never promoted again", cond.Message)
			require.Len(t, rec.Events, 1)
			assert.Contains(t, <-rec.Events, "Warning Rejected")

			// Again: settled, no translation (the missing Graph is not
			// recreated), no write and no Event.
			before := got.ResourceVersion
			for range 2 {
				_, err = r.Reconcile(context.Background(), req)
				require.NoError(t, err)
			}
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Equal(t, "Rejected", got.Status.Phase)
			assert.Equal(t, before, got.ResourceVersion, "a Rejected Bundle's status is not rewritten")
			assert.False(t, tr.called, "a rejected Bundle is never translated")
			assert.Empty(t, rec.Events)
		})
	}
}

// TestBundleReconciler_RejectedSiblingSupersedesNothing: a newer Bundle that
// is rejected, before or after its phase says so, never supersedes an older
// one in flight.
func TestBundleReconciler_RejectedSiblingSupersedesNothing(t *testing.T) {
	for _, newerPhase := range []string{"Promoting", "Rejected"} {
		t.Run(newerPhase, func(t *testing.T) {
			now := time.Now()
			pipeline := &kardinalv1alpha1.Pipeline{
				ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
				Spec:       kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "prod"}}},
			}
			older := &kardinalv1alpha1.Bundle{
				ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-time.Minute))},
				Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
				Status:     kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
			}
			newer := &kardinalv1alpha1.Bundle{
				ObjectMeta: metav1.ObjectMeta{Name: "app-v2", Namespace: "default", CreationTimestamp: metav1.NewTime(now)},
				Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app", Rejected: rejection()},
				Status:     kardinalv1alpha1.BundleStatus{Phase: newerPhase},
			}
			c := indexedBuilder(newScheme()).WithObjects(pipeline, older, newer).WithStatusSubresource(older, newer).Build()
			r := &bundle.Reconciler{Client: c}
			_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "app-v1", Namespace: "default"}})
			require.NoError(t, err)
			var got kardinalv1alpha1.Bundle
			require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "app-v1", Namespace: "default"}, &got))
			assert.Equal(t, "Promoting", got.Status.Phase)
		})
	}
}

// TestBundleReconciler_HistoryGCKeepsRejected: historyLimit never deletes a
// rejected Bundle (spec.rejected), whatever its phase (also one not marked
// Rejected yet), which records that its artifacts must not be promoted
// again, and does not count it against the limit. A Bundle the controller
// rejected only because it carries a rejected artifact (phase Rejected, no
// spec.rejected) is history and may be deleted (QA #1489).
//
// Covers BUNDLE-REJECT-08.
func TestBundleReconciler_HistoryGCKeepsRejected(t *testing.T) {
	now := time.Now()
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{HistoryLimit: 1, Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "prod"}}},
	}
	oldest := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Minute))},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app", Rejected: rejection()},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Rejected"},
	}
	gone := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "app-v0", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-3 * time.Minute))},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Verified"},
	}
	kept := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "app-v2", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-time.Minute))},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Verified"},
	}
	fresh := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "app-v3", Namespace: "default", CreationTimestamp: metav1.NewTime(now)},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
	}
	rejectedVerified := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "app-v0r", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-5 * time.Minute))},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app", Rejected: rejection()},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Verified"},
	}
	carrier := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "app-v0c", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-4 * time.Minute))},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Rejected"},
	}
	// A carrier whose change is live in prod (its step there is Verified):
	// what prod runs, shown with the roll-back hint, so GC keeps it.
	liveCarrier := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "app-v0l", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-6 * time.Minute))},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Rejected"},
	}
	liveStep := &kardinalv1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "app-v0l-prod", Namespace: "default", Labels: map[string]string{
			"kardinal.io/pipeline": "app", "kardinal.io/bundle": "app-v0l", "kardinal.io/environment": "prod"}},
		Spec:   kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "app-v0l", Environment: "prod"},
		Status: kardinalv1alpha1.PromotionStepStatus{State: "Verified"},
	}
	c := indexedBuilder(newScheme()).WithObjects(pipeline, gone, oldest, kept, fresh, rejectedVerified, carrier, liveCarrier, liveStep).
		WithStatusSubresource(gone, oldest, kept, fresh, rejectedVerified, carrier, liveCarrier, liveStep).Build()
	r := &bundle.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "app-v3", Namespace: "default"}})
	require.NoError(t, err)
	var list kardinalv1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &list))
	var names []string
	for _, b := range list.Items {
		names = append(names, b.Name)
	}
	assert.ElementsMatch(t, []string{"app-v0r", "app-v0l", "app-v1", "app-v2", "app-v3"}, names)
}

// TestBundleReconciler_RejectedArtifact: a new or promoting Bundle that
// carries the image of a rejected Bundle (same repository and digest) is
// rejected too, with reason RejectedArtifact naming the rejected Bundle, and
// supersedes nothing; a Verified one is left as history. Reconciling again
// writes nothing (idempotent).
func TestBundleReconciler_RejectedArtifact(t *testing.T) {
	img := []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/x/app", Tag: "1.2", Digest: "sha256:bad"}}
	for _, phase := range []string{"", "Promoting", "Verified"} {
		t.Run("phase "+phase, func(t *testing.T) {
			now := time.Now()
			pipeline := &kardinalv1alpha1.Pipeline{
				ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
				Spec:       kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "prod"}}},
			}
			older := &kardinalv1alpha1.Bundle{
				ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))},
				Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
				Status:     kardinalv1alpha1.BundleStatus{Phase: "Promoting"},
			}
			bad := &kardinalv1alpha1.Bundle{
				ObjectMeta: metav1.ObjectMeta{Name: "app-v2", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-time.Minute))},
				Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app", Images: img, Rejected: rejection()},
				Status:     kardinalv1alpha1.BundleStatus{Phase: "Rejected"},
			}
			retry := &kardinalv1alpha1.Bundle{
				ObjectMeta: metav1.ObjectMeta{Name: "app-v3", Namespace: "default", CreationTimestamp: metav1.NewTime(now)},
				Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app",
					Images: []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/x/app", Tag: "retag", Digest: "sha256:bad"}}},
				Status: kardinalv1alpha1.BundleStatus{Phase: phase},
			}
			c := indexedBuilder(newScheme()).WithObjects(pipeline, older, bad, retry).WithStatusSubresource(older, bad, retry).Build()
			r := &bundle.Reconciler{Client: c}
			// app-v2 first records what its rejection rejects (an
			// unrecorded rejection marks no carrier: that is final).
			for _, name := range []string{"app-v2", "app-v3", "app-v3", "app-v1"} {
				_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: "default"}})
				require.NoError(t, err)
			}
			var got kardinalv1alpha1.Bundle
			require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "app-v3", Namespace: "default"}, &got))
			if phase == "Verified" {
				assert.Equal(t, "Verified", got.Status.Phase, "a finished Bundle is history")
				return
			}
			assert.Equal(t, "Rejected", got.Status.Phase)
			assert.Nil(t, got.Spec.Rejected, "nobody rejected it: spec.rejected stays unset")
			cond := meta.FindStatusCondition(got.Status.Conditions, "Rejected")
			require.NotNil(t, cond)
			assert.Equal(t, "RejectedArtifact", cond.Reason)
			assert.Equal(t, "carries an image or config commit of the rejected bundle app-v2; it is never promoted", cond.Message)
			require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "app-v1", Namespace: "default"}, &got))
			assert.Equal(t, "Promoting", got.Status.Phase, "a Bundle carrying a rejected artifact supersedes nothing")
		})
	}
}

// TestBundleReconciler_RejectedArtifactSet (QA #1489): marking a Bundle
// Rejected records what its rejection rejects, the artifacts not Verified
// before it where it went (status.rejectedArtifacts): here the app image it
// changed, not the sidecar it kept. A Bundle rejected before the set existed
// gets it on the next reconcile, without a second Rejected event.
//
// Covers BUNDLE-REJECT-09.
func TestBundleReconciler_RejectedArtifactSet(t *testing.T) {
	now := time.Now()
	pipeline := &kardinalv1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "prod"}}}}
	side := kardinalv1alpha1.ImageRef{Repository: "r/side", Tag: "s1"}
	v1 := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))},
		Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app",
			Images: []kardinalv1alpha1.ImageRef{{Repository: "r/app", Tag: "1"}, side}},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Verified",
			Environments: []kardinalv1alpha1.EnvironmentStatus{{Name: "prod", Phase: "Verified"}}},
	}
	for _, phase := range []string{"Verified", "Rejected"} {
		t.Run("from "+phase, func(t *testing.T) {
			v2 := &kardinalv1alpha1.Bundle{
				ObjectMeta: metav1.ObjectMeta{Name: "app-v2", Namespace: "default", CreationTimestamp: metav1.NewTime(now)},
				Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app", Rejected: rejection(),
					Images: []kardinalv1alpha1.ImageRef{{Repository: "r/app", Tag: "2"}, side}},
				Status: kardinalv1alpha1.BundleStatus{Phase: phase,
					Environments: []kardinalv1alpha1.EnvironmentStatus{{Name: "prod", Phase: "Verified"}}},
			}
			if phase == "Verified" {
				// Retired (#1492): the rejection still applies.
				at := metav1.NewTime(now)
				v2.Status.RetiredAt = &at
			}
			c := indexedBuilder(newScheme()).WithObjects(pipeline, v1, v2).WithStatusSubresource(v1, v2).Build()
			r := &bundle.Reconciler{Client: c}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "app-v2", Namespace: "default"}}
			for range 2 {
				_, err := r.Reconcile(context.Background(), req)
				require.NoError(t, err)
			}
			var got kardinalv1alpha1.Bundle
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Equal(t, "Rejected", got.Status.Phase)
			require.NotNil(t, got.Status.RejectedArtifacts)
			assert.Equal(t, []kardinalv1alpha1.ImageRef{{Repository: "r/app", Tag: "2"}}, got.Status.RejectedArtifacts.Images)
			assert.Equal(t, []string{"prod=app-v1"}, got.Status.RejectedArtifacts.ComparedWith)
		})
	}
}
