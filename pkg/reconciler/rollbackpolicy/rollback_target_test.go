// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package rollbackpolicy_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/rollbackpolicy"
)

func rtPipeline() *v1alpha1.Pipeline {
	return &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{
			{Name: "test"}, {Name: "prod"},
		}},
	}
}

func rtBundle(name, tag string, minute int) *v1alpha1.Bundle {
	b := makeBundle(name, "nginx-demo")
	b.CreationTimestamp = metav1.NewTime(fixedNow.Add(time.Duration(minute-60) * time.Minute))
	b.Spec.Images = []v1alpha1.ImageRef{{Repository: "nginx", Tag: tag}}
	return b
}

// rtVerifiedStep is a prod step of bundle Verified at minute. Its name sorts
// after the failing step's, which the reconciler reads first.
func rtVerifiedStep(bundle string, minute int) *v1alpha1.PromotionStep {
	at := metav1.NewTime(fixedNow.Add(time.Duration(minute-60) * time.Minute))
	s := makePromotionStep("z-"+bundle+"-prod", "nginx-demo", "prod", 0)
	s.Labels["kardinal.io/bundle"] = bundle
	s.Spec.BundleName = bundle
	s.CreationTimestamp = at
	s.Status.State = "Verified"
	s.Status.Conditions = []metav1.Condition{{Type: "Verified", Status: metav1.ConditionTrue, Reason: "Verified", LastTransitionTime: at}}
	return s
}

// TestRollbackPolicy_RollsBackToPreviousVerifiedBundle covers C04-gates-06: the
// RollbackPolicy rollback Bundle carries the images of the Bundle Verified
// before spec.bundleRef in the environment, never the failing image; nothing is
// created when there is no such Bundle or when spec.bundleRef is itself a
// rollback; an existing automatic rollback of the Bundle is reused; and a
// second reconcile creates nothing more.
func TestRollbackPolicy_RollsBackToPreviousVerifiedBundle(t *testing.T) {
	alarm := func() client.Object {
		b := rtBundle(lifecycle.AutoRollbackName("bundle-1", "alarm"), "1.24.0", 55)
		b.Labels[lifecycle.LabelRollback] = "true"
		b.Annotations = map[string]string{lifecycle.AnnotationRollbackFrom: "bundle-1"}
		b.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: "prod"}
		return b
	}
	tests := []struct {
		name     string
		history  []client.Object
		wantName string // "" = no rollback Bundle
		wantTag  string
		wantTo   string
		// failingIsRollback makes bundle-1 a rollback Bundle (from bundle-2).
		failingIsRollback bool
	}{
		{name: "restores the bundle verified before the failing one",
			history:  []client.Object{rtBundle("bundle-0", "1.24.0", 0), rtVerifiedStep("bundle-0", 5)},
			wantName: lifecycle.AutoRollbackName("bundle-1", "policy"), wantTag: "1.24.0", wantTo: "bundle-0"},
		{name: "skips an earlier bundle with the failing image",
			history: []client.Object{
				rtBundle("bundle-0", "1.24.0", 0), rtVerifiedStep("bundle-0", 5),
				rtBundle("bundle-0b", "1.25.0", 10), rtVerifiedStep("bundle-0b", 15),
			},
			wantName: lifecycle.AutoRollbackName("bundle-1", "policy"), wantTag: "1.24.0", wantTo: "bundle-0"},
		{name: "nothing verified before: no rollback bundle"},
		{name: "the failing bundle is itself a rollback: no rollback bundle",
			history:           []client.Object{rtBundle("bundle-0", "1.24.0", 0), rtVerifiedStep("bundle-0", 5)},
			failingIsRollback: true},
		{name: "reuses the onHealthFailure rollback of the same bundle",
			history:  []client.Object{rtBundle("bundle-0", "1.24.0", 0), rtVerifiedStep("bundle-0", 5), alarm()},
			wantName: lifecycle.AutoRollbackName("bundle-1", "alarm")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			rp := makeRollbackPolicy("rp-1", "nginx-demo", "prod", "bundle-1", 3)
			failing := makePromotionStep("a-bundle-1-prod", "nginx-demo", "prod", 3)
			failing.Labels["kardinal.io/bundle"] = "bundle-1"
			failingBundle := rtBundle("bundle-1", "1.25.0", 30)
			if tc.failingIsRollback {
				failingBundle.Labels[lifecycle.LabelRollback] = "true"
				failingBundle.Annotations = map[string]string{lifecycle.AnnotationRollbackFrom: "bundle-2"}
				failingBundle.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: "prod"}
			}
			objs := append([]client.Object{rp, failing, rtPipeline(), failingBundle}, tc.history...)
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(objs...).
				WithStatusSubresource(&v1alpha1.RollbackPolicy{}, &v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).Build()
			r := &rollbackpolicy.Reconciler{Client: c, NowFn: func() time.Time { return fixedNow }}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "rp-1", Namespace: "default"}}

			for range 2 {
				_, err := r.Reconcile(ctx, req)
				require.NoError(t, err)
			}

			var updated v1alpha1.RollbackPolicy
			require.NoError(t, c.Get(ctx, req.NamespacedName, &updated))
			assert.True(t, updated.Status.ShouldRollback)

			var list v1alpha1.BundleList
			require.NoError(t, c.List(ctx, &list))
			var rollbacks []v1alpha1.Bundle
			for _, b := range list.Items {
				if b.Labels[lifecycle.LabelRollback] == "true" && b.Name != "bundle-1" {
					rollbacks = append(rollbacks, b)
				}
			}
			if tc.wantName == "" {
				assert.Nil(t, updated.Status.RollbackBundleName)
				assert.Empty(t, rollbacks, "the failing image is never re-promoted")
				return
			}
			require.NotNil(t, updated.Status.RollbackBundleName)
			assert.Equal(t, tc.wantName, *updated.Status.RollbackBundleName)
			require.Len(t, rollbacks, 1, "one rollback Bundle per failing Bundle and environment")
			rb := rollbacks[0]
			assert.Equal(t, tc.wantName, rb.Name)
			if tc.wantTag == "" {
				return
			}
			require.Len(t, rb.Spec.Images, 1)
			assert.Equal(t, tc.wantTag, rb.Spec.Images[0].Tag, "the rollback restores the earlier good image")
			assert.Equal(t, tc.wantTo, rb.Spec.Provenance.RollbackOf)
			assert.Equal(t, "bundle-1", rb.Annotations[lifecycle.AnnotationRollbackFrom])
			require.NotNil(t, rb.Spec.Intent)
			assert.Equal(t, "prod", rb.Spec.Intent.TargetEnvironment)
			assert.Equal(t, "AutoRollback", rb.Labels[lifecycle.LabelReason])
		})
	}
}
