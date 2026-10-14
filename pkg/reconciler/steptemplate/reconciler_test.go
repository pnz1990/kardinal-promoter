// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steptemplate_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/steptemplate"
)

// TestStepTemplate validates templates into status.ready and status.reason,
// and a second reconcile writes nothing (idempotent).
func TestStepTemplate(t *testing.T) {
	cases := []struct {
		name          string
		spec          v1alpha1.StepTemplateSpec
		requireDigest bool
		ready         bool
		reason        string
	}{
		{name: "valid", spec: v1alpha1.StepTemplateSpec{Image: "x@sha256:1", ServiceAccountName: "qa"}, ready: true},
		{name: "tag allowed by default", spec: v1alpha1.StepTemplateSpec{Image: "x:1", ServiceAccountName: "qa"}, ready: true},
		{name: "tag refused with requireDigest", spec: v1alpha1.StepTemplateSpec{Image: "x:1", ServiceAccountName: "qa"},
			requireDigest: true, reason: "has no digest"},
		{name: "timeout too long", spec: v1alpha1.StepTemplateSpec{Image: "x", ServiceAccountName: "qa", Timeout: "25h"},
			reason: "longer than 24h"},
		{name: "reserved env", spec: v1alpha1.StepTemplateSpec{Image: "x", ServiceAccountName: "qa",
			Env: []v1alpha1.StepTemplateEnv{{Name: "KARDINAL_RUN", Value: "1"}}}, reason: "KARDINAL_"},
		{name: "bad egress", spec: v1alpha1.StepTemplateSpec{Image: "x", ServiceAccountName: "qa",
			Egress: []v1alpha1.StepTemplateEgress{{CIDR: "nope"}}}, reason: "egress nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := runtime.NewScheme()
			require.NoError(t, v1alpha1.AddToScheme(s))
			tmpl := &v1alpha1.StepTemplate{ObjectMeta: metav1.ObjectMeta{Name: "qa", Generation: 2}, Spec: tc.spec}
			c := fake.NewClientBuilder().WithScheme(s).WithObjects(tmpl).WithStatusSubresource(tmpl).Build()
			r := &steptemplate.Reconciler{Client: c, RequireDigest: tc.requireDigest}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "qa"}}
			var rv string
			for i := range 2 {
				_, err := r.Reconcile(context.Background(), req)
				require.NoError(t, err)
				var got v1alpha1.StepTemplate
				require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
				assert.Equal(t, tc.ready, got.Status.Ready)
				assert.Contains(t, got.Status.Reason, tc.reason)
				assert.Equal(t, int64(2), got.Status.ObservedGeneration)
				if i == 1 {
					assert.Equal(t, rv, got.ResourceVersion, "a second reconcile writes nothing")
				}
				rv = got.ResourceVersion
			}
		})
	}
}
