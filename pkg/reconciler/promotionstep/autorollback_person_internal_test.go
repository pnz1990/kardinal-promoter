// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// personsRollback is a rollback Bundle as a person's `kardinal rollback`
// (or kubectl) makes it: the rollback labels, rollback-from from,
// rollbackOf of and intent prod, with a human stamp and no via. Nothing on
// it is a stamp only the controller can write.
func personsRollback(name, from, of, tag string, minute int) *v1alpha1.Bundle {
	b := arBundle(name, tag, minute)
	b.Labels = map[string]string{lifecycle.LabelRollback: "true", lifecycle.LabelPipeline: "app"}
	b.Annotations = map[string]string{lifecycle.AnnotationRollbackFrom: from, lifecycle.AnnotationCreatedBy: "alice"}
	b.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: "prod"}
	b.Spec.Provenance = &v1alpha1.BundleProvenance{RollbackOf: of}
	b.Spec.Actor = &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "alice"}
	return b
}

// landedFailed makes s a step that failed after its change landed: its
// health check started.
func landedFailed(s *v1alpha1.PromotionStep) *v1alpha1.PromotionStep {
	at := metav1.NewTime(arT0)
	s.Status.HealthCheckExpiry = &at
	return s
}

// QA-3 on #2187: v1 Verified; v2 Verified and then found bad; a person's
// kardinal rollback r1 (to v1, from v2) is deployed. When r1 fails, or a
// later release fails after r1, the automatic rollback never brings v2
// back: r1 deploys exactly what was Verified in prod, so it is a rollback
// (a failing one stops for a human) and it marks v2 rolled back from,
// because it ran in prod. A label on new artifacts is no such fact: that
// release is rolled back like any other, and a Bundle that never ran in
// prod marks nothing.
//
// Covers RB-AUTO-FORGED-05.
func TestAutoRollback_PersonsRollbackNeverRestoresWhatItLeft(t *testing.T) {
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{
			{Name: "test"}, {Name: "prod", OnHealthFailure: "rollback"},
		}},
	}
	history := func() []client.Object {
		return []client.Object{pipeline.DeepCopy(),
			arBundle("app-v0", "0", 0), arStep("app-v0", StateVerified, 1),
			arBundle("app-v1", "1", 2), arStep("app-v1", StateVerified, 5),
			arBundle("app-v2", "2", 10), arStep("app-v2", StateVerified, 15)}
	}
	forged := func(name, tag string, minute int) *v1alpha1.Bundle {
		b := personsRollback(name, "app-v2", "app-v1", tag, minute)
		b.Annotations[lifecycle.AnnotationCreatedBy] = "mallory"
		b.Spec.Actor.Principal = "mallory"
		return b
	}
	tests := []struct {
		name    string
		objs    []client.Object
		failing string
		// wantTag is the image the automatic rollback restores; "" when it
		// stops for a human instead.
		wantTag string
	}{
		{name: "a person's rollback that fails stops for a human",
			objs:    []client.Object{personsRollback("r1", "app-v2", "app-v1", "1", 20)},
			failing: "r1"},
		{name: "a release after a person's rollback goes back to what the rollback restored",
			objs:    []client.Object{personsRollback("r1", "app-v2", "app-v1", "1", 20), arStep("r1", StateVerified, 25), arBundle("app-v3", "3", 30)},
			failing: "app-v3", wantTag: "1"},
		{name: "a release labelled a rollback with new artifacts is rolled back",
			objs:    []client.Object{forged("evil", "666", 20)},
			failing: "evil", wantTag: "2"},
		{name: "a labelled release with new artifacts marks nothing rolled back from",
			objs:    []client.Object{forged("evil", "666", 20), landedFailed(arStep("evil", StateFailed, 25)), arBundle("app-v3", "3", 30)},
			failing: "app-v3", wantTag: "2"},
		{name: "a rollback that never ran in prod marks nothing rolled back from",
			objs:    []client.Object{forged("never-ran", "1", 20), arBundle("app-v3", "3", 30)},
			failing: "app-v3", wantTag: "2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			failing := arStep(tt.failing, StateHealthChecking, 40)
			objs := append(append(history(), tt.objs...), failing)
			c := fakeclient.NewClientBuilder().WithScheme(newTestScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
				WithObjects(objs...).WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
			r := &Reconciler{Client: c}
			key := types.NamespacedName{Namespace: "default", Name: failing.Name}
			var ps v1alpha1.PromotionStep
			require.NoError(t, c.Get(ctx, key, &ps))

			// The planner path on its own: createAutoRollback.
			name, refusal, err := r.createAutoRollback(ctx, ps.DeepCopy())
			require.NoError(t, err)
			if tt.wantTag == "" {
				require.ErrorIs(t, refusal, lifecycle.ErrConflict)
			} else {
				require.NoError(t, refusal)
				var b v1alpha1.Bundle
				require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: name}, &b))
				assert.Equal(t, tt.wantTag, b.Spec.Images[0].Tag)
				require.NoError(t, c.Delete(ctx, &b))
			}

			// The step's health-failure path.
			ps.Status.State = StateHealthChecking
			_, err = r.applyHealthFailurePolicy(ctx, zerolog.Nop(), ps.DeepCopy(), &ps, findEnv(pipeline, "prod"), "resource", "unavailable")
			require.NoError(t, err)
			var got v1alpha1.PromotionStep
			require.NoError(t, c.Get(ctx, key, &got))
			var own []v1alpha1.Bundle
			var list v1alpha1.BundleList
			require.NoError(t, c.List(ctx, &list))
			for _, b := range list.Items {
				if lifecycle.IsAutoRollback(&b) {
					own = append(own, b)
				}
			}
			if tt.wantTag == "" {
				assert.Equal(t, StateAbortedByAlarm, got.Status.State, got.Status.Message)
				assert.Contains(t, got.Status.Message, "is a rollback and is not rolled back again")
				assert.Empty(t, own, "no rollback is created, so v2 is not restored")
				return
			}
			assert.Equal(t, StateRollingBack, got.Status.State, got.Status.Message)
			require.Len(t, own, 1)
			assert.Equal(t, tt.wantTag, own[0].Spec.Images[0].Tag)
		})
	}
}

// QA-3 on #2187: a person's rollback that names a digest-pinned Verified
// version's image by tag alone (or by digest alone) is that version's
// artifact: when it fails it stops for a human, never brings the bad v2
// back. One image of a Verified version with one never Verified is a new
// release, rolled back as such. Each Bundle runs app and a sidecar.
//
// Covers RB-AUTO-FORGED-09.
func TestAutoRollback_PersonsRollbackMatchesByDigestOrTag(t *testing.T) {
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{
			{Name: "test"}, {Name: "prod", OnHealthFailure: "rollback"},
		}},
	}
	images := func(b *v1alpha1.Bundle, app, appDigest, side string) *v1alpha1.Bundle {
		b.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: app, Digest: appDigest},
			{Repository: "ghcr.io/org/sidecar", Tag: side}}
		return b
	}
	tests := []struct {
		name               string
		app, digest, side  string
		wantAbort          bool
		wantApp, wantAppDg string
	}{
		{name: "by tag alone, where v1 pinned a digest", app: "1", side: "1", wantAbort: true},
		{name: "by digest alone", digest: "sha256:a1", side: "1", wantAbort: true},
		{name: "the exact copy", app: "1", digest: "sha256:a1", side: "1", wantAbort: true},
		{name: "one image Verified, one not", app: "1", side: "9", wantApp: "2", wantAppDg: "sha256:a2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			r1 := images(personsRollback("r1", "app-v2", "app-v1", "", 20), tt.app, tt.digest, tt.side)
			failing := arStep("r1", StateHealthChecking, 40)
			objs := []client.Object{pipeline.DeepCopy(),
				images(arBundle("app-v0", "", 0), "0", "sha256:a0", "1"), arStep("app-v0", StateVerified, 1),
				images(arBundle("app-v1", "", 2), "1", "sha256:a1", "1"), arStep("app-v1", StateVerified, 5),
				images(arBundle("app-v2", "", 10), "2", "sha256:a2", "1"), arStep("app-v2", StateVerified, 15),
				r1, failing}
			c := fakeclient.NewClientBuilder().WithScheme(newTestScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
				WithObjects(objs...).WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
			r := &Reconciler{Client: c}
			key := types.NamespacedName{Namespace: "default", Name: failing.Name}
			var ps v1alpha1.PromotionStep
			require.NoError(t, c.Get(ctx, key, &ps))
			ps.Status.State = StateHealthChecking
			_, err := r.applyHealthFailurePolicy(ctx, zerolog.Nop(), ps.DeepCopy(), &ps, findEnv(pipeline, "prod"), "resource", "unavailable")
			require.NoError(t, err)
			var got v1alpha1.PromotionStep
			require.NoError(t, c.Get(ctx, key, &got))
			var list v1alpha1.BundleList
			require.NoError(t, c.List(ctx, &list))
			var own []v1alpha1.Bundle
			for _, b := range list.Items {
				if lifecycle.IsAutoRollback(&b) {
					own = append(own, b)
				}
			}
			if tt.wantAbort {
				assert.Equal(t, StateAbortedByAlarm, got.Status.State, got.Status.Message)
				assert.Empty(t, own, "no rollback, so v2 is not restored")
				return
			}
			assert.Equal(t, StateRollingBack, got.Status.State, got.Status.Message)
			require.Len(t, own, 1)
			assert.Equal(t, tt.wantApp, own[0].Spec.Images[0].Tag)
			assert.Equal(t, tt.wantAppDg, own[0].Spec.Images[0].Digest)
		})
	}
}
