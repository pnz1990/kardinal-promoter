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

// #2090: the automatic rollback adopts only its own Bundle at its name. A
// Bundle someone else put at the plain name (no intent, or prod's intent
// but not a rollback of app-v2) is left alone, and the rollback takes prod's
// own name; when that one is someone else's too, the step stops for a human
// at once, naming the Bundle in the way, instead of retrying forever.
//
// Covers RB-AUTO-NAME-01.
func TestAutoRollback_ForeignBundleAtTheName(t *testing.T) {
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{
			{Name: "test"}, {Name: "prod", OnHealthFailure: "rollback"},
		}},
	}
	foreign := func(name string, intent *v1alpha1.BundleIntent) *v1alpha1.Bundle {
		b := arBundle(name, "9", 1)
		b.Spec.Intent = intent
		return b
	}
	prodIntent := &v1alpha1.BundleIntent{TargetEnvironment: "prod"}
	usIntent := &v1alpha1.BundleIntent{TargetEnvironment: "prod-us"}
	for _, tc := range []struct {
		name     string
		squat    []client.Object
		wantName string // "" for AbortedByAlarm
		wantMsg  string
	}{
		{name: "a Bundle with no intent at the plain name", squat: []client.Object{foreign("app-v2-rollback-alarm", nil)},
			wantName: "app-v2-rollback-alarm-prod"},
		{name: "prod's intent, but not a rollback of app-v2", squat: []client.Object{foreign("app-v2-rollback-alarm", prodIntent)},
			wantName: "app-v2-rollback-alarm-prod"},
		{name: "both names held by others", squat: []client.Object{
			foreign("app-v2-rollback-alarm", usIntent), foreign("app-v2-rollback-alarm-prod", nil)},
			wantMsg: "bundle app-v2-rollback-alarm-prod is at the automatic rollback's name and is not a rollback of app-v2 in prod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			failing := arStep("app-v2", StateHealthChecking, 10)
			objs := append([]client.Object{pipeline.DeepCopy(), arBundle("app-v2", "2", 9), failing,
				arBundle("app-v1", "1", 0), arStep("app-v1", StateVerified, 5)}, tc.squat...)
			c := fakeclient.NewClientBuilder().WithScheme(newTestScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
				WithObjects(objs...).WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
			r := &Reconciler{Client: c}
			key := types.NamespacedName{Namespace: "default", Name: failing.Name}
			env := findEnv(pipeline, "prod")
			for range 2 { // the second run: idempotent, and no error to retry on
				var ps v1alpha1.PromotionStep
				require.NoError(t, c.Get(ctx, key, &ps))
				if ps.Status.State != StateHealthChecking && ps.Status.State != "" {
					break
				}
				ps.Status.State = StateHealthChecking
				_, err := r.applyHealthFailurePolicy(ctx, zerolog.Nop(), ps.DeepCopy(), &ps, env, "resource", "deployment unavailable")
				require.NoError(t, err, "a blocked name is not an error to retry forever")
			}
			var got v1alpha1.PromotionStep
			require.NoError(t, c.Get(ctx, key, &got))
			for _, s := range tc.squat {
				var b v1alpha1.Bundle
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(s), &b))
				assert.Empty(t, b.Annotations[lifecycle.AnnotationRollbackFrom], "%s is left as it was", b.Name)
			}
			if tc.wantName == "" {
				assert.Equal(t, StateAbortedByAlarm, got.Status.State)
				assert.Contains(t, got.Status.Message, tc.wantMsg)
				assert.Contains(t, got.Status.Message, "human intervention required")
				return
			}
			assert.Equal(t, StateRollingBack, got.Status.State, got.Status.Message)
			assert.Contains(t, got.Status.Message, "rollback Bundle "+tc.wantName+" created")
			var rb v1alpha1.Bundle
			require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: tc.wantName}, &rb))
			assert.Equal(t, "app-v2", rb.Annotations[lifecycle.AnnotationRollbackFrom])
			assert.Equal(t, "prod", rb.Spec.Intent.TargetEnvironment)
		})
	}
}
