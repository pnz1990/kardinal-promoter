// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"testing"
	"time"

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

var arT0 = time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

func arBundle(name, tag string, minute int) *v1alpha1.Bundle {
	return &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "default",
			CreationTimestamp: metav1.NewTime(arT0.Add(time.Duration(minute) * time.Minute)),
		},
		Spec: v1alpha1.BundleSpec{
			Type: "image", Pipeline: "app",
			Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: tag}},
		},
	}
}

func arStep(bundle, state string, minute int) *v1alpha1.PromotionStep {
	at := metav1.NewTime(arT0.Add(time.Duration(minute) * time.Minute))
	s := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name: bundle + "-prod", Namespace: "default", CreationTimestamp: at,
			Labels: map[string]string{"kardinal.io/pipeline": "app", "kardinal.io/bundle": bundle},
		},
		Spec:   v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: bundle, Environment: "prod"},
		Status: v1alpha1.PromotionStepStatus{State: state},
	}
	if state == StateVerified {
		s.Status.Conditions = []metav1.Condition{{Type: "Verified", Status: metav1.ConditionTrue, Reason: "Verified", LastTransitionTime: at}}
	}
	return s
}

// TestOnHealthFailureRollback_RestoresPreviousVerifiedBundle covers
// C03-promotionstep-09: onHealthFailure=rollback creates a Bundle with the
// images of the Bundle verified before the failing one (the planner the CLI
// and UI use), never re-promotes the failing image, is idempotent, and stops
// the step for a human when there is nothing safe to roll back to or when the
// failing Bundle is itself a rollback. The rollback Bundle is stamped with
// kardinal.io/created-at like the CLI's and UI's.
func TestOnHealthFailureRollback_RestoresPreviousVerifiedBundle(t *testing.T) {
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{
			{Name: "test"}, {Name: "prod", OnHealthFailure: "rollback"},
		}},
	}
	tests := []struct {
		name      string
		earlier   []client.Object
		wantState string
		wantTag   string
		wantTgt   string
		// failingIsRollback makes app-v2 a rollback Bundle (from app-v3).
		failingIsRollback bool
	}{
		{name: "rolls back to the bundle verified before the failing one",
			earlier:   []client.Object{arBundle("app-v1", "1", 0), arStep("app-v1", StateVerified, 5)},
			wantState: StateRollingBack, wantTag: "1", wantTgt: "app-v1"},
		{name: "skips an earlier bundle with the failing image",
			earlier: []client.Object{
				arBundle("app-v1", "1", 0), arStep("app-v1", StateVerified, 5),
				arBundle("app-v2b", "2", 6), arStep("app-v2b", StateVerified, 8),
			},
			wantState: StateRollingBack, wantTag: "1", wantTgt: "app-v1"},
		{name: "nothing verified before: abort for a human, create nothing",
			wantState: StateAbortedByAlarm},
		{name: "only the failing image was verified before: abort, never re-promote it",
			earlier:   []client.Object{arBundle("app-v2b", "2", 6), arStep("app-v2b", StateVerified, 8)},
			wantState: StateAbortedByAlarm},
		{name: "the failing bundle is itself a rollback: abort, never roll back a rollback",
			earlier:           []client.Object{arBundle("app-v1", "1", 0), arStep("app-v1", StateVerified, 5)},
			failingIsRollback: true,
			wantState:         StateAbortedByAlarm},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			failing := arStep("app-v2", StateHealthChecking, 10)
			failingBundle := arBundle("app-v2", "2", 9)
			if tc.failingIsRollback {
				failingBundle.Labels = map[string]string{lifecycle.LabelRollback: "true", lifecycle.LabelPipeline: "app"}
				failingBundle.Annotations = map[string]string{lifecycle.AnnotationRollbackFrom: "app-v3"}
				failingBundle.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: "prod"}
			}
			objs := append([]client.Object{pipeline.DeepCopy(), failingBundle, failing}, tc.earlier...)
			c := fakeclient.NewClientBuilder().WithScheme(newTestScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
				WithObjects(objs...).Build()
			r := &Reconciler{Client: c}
			key := types.NamespacedName{Namespace: "default", Name: failing.Name}
			env := findEnv(pipeline, "prod")

			for range 2 { // the second run checks idempotency
				var ps v1alpha1.PromotionStep
				require.NoError(t, c.Get(ctx, key, &ps))
				ps.Status.State = StateHealthChecking
				_, err := r.applyHealthFailurePolicy(ctx, zerolog.Nop(), &ps, pipeline, env,
					"resource", "deployment unavailable", client.MergeFrom(ps.DeepCopy()))
				require.NoError(t, err)
			}

			var got v1alpha1.PromotionStep
			require.NoError(t, c.Get(ctx, key, &got))
			assert.Equal(t, tc.wantState, got.Status.State)

			var list v1alpha1.BundleList
			require.NoError(t, c.List(ctx, &list))
			var rollbacks []v1alpha1.Bundle
			for _, b := range list.Items {
				if b.Labels[lifecycle.LabelRollback] == "true" && b.Name != "app-v2" {
					rollbacks = append(rollbacks, b)
				}
			}
			if tc.wantState == StateAbortedByAlarm {
				assert.Empty(t, rollbacks, "no rollback Bundle when there is nothing safe to roll back to")
				assert.Contains(t, got.Status.Message, "human intervention required")
				return
			}
			require.Len(t, rollbacks, 1, "one rollback Bundle, however often the step is reconciled")
			rb := rollbacks[0]
			assert.Equal(t, "app-v2-rollback-alarm", rb.Name)
			require.Len(t, rb.Spec.Images, 1, "the rollback Bundle carries the target's images")
			assert.Equal(t, tc.wantTag, rb.Spec.Images[0].Tag)
			assert.Equal(t, tc.wantTgt, rb.Spec.Provenance.RollbackOf)
			assert.Equal(t, "app-v2", rb.Annotations[lifecycle.AnnotationRollbackFrom])
			assert.Equal(t, "prod", rb.Spec.Intent.TargetEnvironment)
			assert.Equal(t, "AutoRollback", rb.Labels[lifecycle.LabelReason])
			assert.NotEmpty(t, rb.Annotations[lifecycle.AnnotationCreatedAt],
				"the rollback Bundle is stamped with kardinal.io/created-at")
			assert.Contains(t, got.Status.Message, rb.Name)
		})
	}
}
