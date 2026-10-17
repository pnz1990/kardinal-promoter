// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestNewerPushed_FleetTargetRollback (#2153 QA): a newer rollback of one
// fleet target counts, for a step in an environment on its way there, only
// when its step there completed git-push and the caller found the branch
// moved (landed). A push intent alone does not supersede: the step waits.
// Before a push (the push guard) the rollback supersedes nothing outside
// its target.
func TestNewerPushed_FleetTargetRollback(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	pl := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "app"},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "dev"},
			{Name: "prod", DependsOn: []string{"dev"},
				Fleet: &v1alpha1.FleetSpec{Targets: []v1alpha1.FleetTarget{{Name: "eu"}, {Name: "us"}}}}}}}
	bundle := func(name string, at time.Time) *v1alpha1.Bundle {
		return &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name, CreationTimestamp: metav1.NewTime(at)},
			Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "app"}, Status: v1alpha1.BundleStatus{Phase: "Promoting"}}
	}
	step := func(name, b string) *v1alpha1.PromotionStep {
		return &v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name,
			Labels: map[string]string{"kardinal.io/pipeline": "app", "kardinal.io/environment": "dev"}},
			Spec: v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: b, Environment: "dev"}}
	}
	old := bundle("app-v1", t0)
	rb := bundle("app-rollback-eu", t0.Add(time.Minute))
	rb.Labels = map[string]string{"kardinal.io/rollback": "true"}
	rb.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: "prod-eu"}
	ps := step("app-v1-dev", old.Name)

	intentOnly := step("app-rollback-eu-dev", rb.Name)
	intentOnly.Status.State = "Promoting"
	intentOnly.Status.Outputs = map[string]string{outputPushIntent: t0.Add(time.Minute).Format(time.RFC3339Nano)}
	pushed := intentOnly.DeepCopy()
	pushed.Status.Steps = []v1alpha1.StepStatus{{Name: "git-push", State: v1alpha1.StepExecutionCompleted}}

	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	for _, tc := range []struct {
		name   string
		rbStep *v1alpha1.PromotionStep
		landed bool
		want   string
	}{
		{"the rollback only recorded its push intent: the step waits", intentOnly, true, ""},
		{"the rollback pushed here and the branch moved: superseded", pushed, true, rb.Name},
		{"before this step's push the rollback supersedes nothing outside its target", pushed, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objs := []client.Object{pl.DeepCopy(), old.DeepCopy(), rb.DeepCopy(), ps.DeepCopy(), tc.rbStep.DeepCopy()}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(objs...).Build()
			r := &Reconciler{Client: c, APIReader: c}
			got, err := r.newerPushed(context.Background(), c, ps, old, tc.landed)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
