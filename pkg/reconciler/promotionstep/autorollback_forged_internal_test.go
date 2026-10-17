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

// QA-3 on #2184 (High): a Bundle that someone who may only create Bundles
// made, with every rollback marker they can write (the rollback and
// pipeline labels, kardinal.io/rollback-from the failing Bundle, intent
// prod, their own creator and actor stamp) and an attacker's image, is
// never adopted as the automatic rollback, at its name or at any other:
// the controller creates its own rollback, of the version Verified before.
//
// Covers RB-AUTO-FORGED-01.
func TestAutoRollback_ForgedMarkersAreNotAdopted(t *testing.T) {
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{
			{Name: "test"}, {Name: "prod", OnHealthFailure: "rollback"},
		}},
	}
	forged := func(name string) *v1alpha1.Bundle {
		b := arBundle(name, "666-attacker", 1)
		b.Labels = map[string]string{lifecycle.LabelRollback: "true", lifecycle.LabelPipeline: "app"}
		b.Annotations = map[string]string{lifecycle.AnnotationRollbackFrom: "app-v2", lifecycle.AnnotationCreatedBy: "mallory"}
		b.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: "prod"}
		b.Spec.Actor = &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "mallory"}
		return b
	}
	for _, name := range []string{"app-v2-rollback-alarm", "totally-unrelated-name"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			failing := arStep("app-v2", StateHealthChecking, 10)
			objs := []client.Object{pipeline.DeepCopy(), arBundle("app-v2", "2", 9), failing,
				arBundle("app-v1", "1", 0), arStep("app-v1", StateVerified, 5), forged(name)}
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
			assert.Equal(t, StateRollingBack, got.Status.State, got.Status.Message)
			assert.NotContains(t, got.Status.Message, "rollback Bundle "+name+" created", "the forged Bundle is not the rollback")
			var list v1alpha1.BundleList
			require.NoError(t, c.List(ctx, &list))
			var own []v1alpha1.Bundle
			for _, b := range list.Items {
				if lifecycle.IsAutoRollback(&b) {
					own = append(own, b)
				}
			}
			require.Len(t, own, 1, "the controller created its own rollback")
			assert.Equal(t, "1", own[0].Spec.Images[0].Tag, "of the version Verified before, not the attacker's image")
			assert.Contains(t, got.Status.Message, "rollback Bundle "+own[0].Name+" created")
		})
	}
}
