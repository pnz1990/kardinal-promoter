// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

// nameRuleClient is a fake client that rejects a status write to a PolicyGate
// whose name is longer than 63 characters and that has no spec.generated, the
// way the CRD's name rule does on the API server (the rule is on the root, so
// it runs on status writes too).
func nameRuleClient(gate *kardinalv1alpha1.PolicyGate, objs ...client.Object) (client.Client, *int) {
	statusWrites := 0
	c := fake.NewClientBuilder().WithScheme(newScheme()).
		WithObjects(append(objs, gate)...).WithStatusSubresource(gate).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object,
				patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if g, ok := obj.(*kardinalv1alpha1.PolicyGate); ok && len(g.Name) > 63 && !g.Spec.Generated {
					return apierrors.NewInvalid(schema.GroupKind{Group: "kardinal.io", Kind: "PolicyGate"}, g.Name,
						field.ErrorList{field.Invalid(field.NewPath(""), g.Name, "PolicyGate names are at most 63 characters")})
				}
				statusWrites++
				return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	return c, &statusWrites
}

// TestPolicyGateReconciler_MarksLegacyGeneratedGates (GATE-REJECT-02): a gate
// kardinal created before spec.generated existed may have a name longer than
// 63 characters. The new CRD rejects its status writes until it has the
// marker, so the reconciler sets spec.generated first, but only on a gate
// kardinal made: a gate instance or a pause freeze gate. A long gate a user
// wrote is left alone: no Graph uses it as a template.
func TestPolicyGateReconciler_MarksLegacyGeneratedGates(t *testing.T) {
	long := strings.Repeat("p", 60)
	freeze := func(labels map[string]string) *kardinalv1alpha1.PolicyGate {
		g := lifecycle.DesiredFreezeGate(&kardinalv1alpha1.Pipeline{})
		g.Name, g.Namespace, g.Labels = lifecycle.FreezeGateName(long), "default", labels
		g.Spec.Generated = false
		return g
	}
	instance := func(name string) *kardinalv1alpha1.PolicyGate {
		g := makeGateInstance(name, "default", "app-v1", "true", "5m")
		g.Labels["kardinal.io/gate-template"] = "t"
		return g
	}
	user := func(name string) *kardinalv1alpha1.PolicyGate {
		g := makeGateInstance(name, "default", "", "true", "5m")
		delete(g.Labels, "kardinal.io/bundle")
		return g
	}
	tests := []struct {
		name       string
		gate       *kardinalv1alpha1.PolicyGate
		wantMarked bool
		wantStatus bool
	}{
		{name: "long gate instance", gate: instance("app-v1-prod-" + long), wantMarked: true, wantStatus: true},
		{
			name: "long freeze gate",
			gate: freeze(map[string]string{
				lifecycle.LabelPipeline: long, lifecycle.LabelScope: "system", lifecycle.LabelFreeze: "true",
			}),
			wantMarked: true, wantStatus: true,
		},
		{name: "long user gate named like an instance", gate: user("team--" + long)},
		{name: "long user gate named like a freeze gate", gate: freeze(map[string]string{"team": "a"})},
		{name: "short user gate", gate: user("team-gate"), wantStatus: true},
		{name: "short gate instance", gate: instance("app-v1-prod-gate"), wantStatus: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, statusWrites := nameRuleClient(tt.gate,
				makeBundleWithImage("app-v1", "default", "ghcr.io/example/app", "1.29.0"))
			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			r.NowFn = func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) }
			key := types.NamespacedName{Name: tt.gate.Name, Namespace: "default"}
			_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
			require.NoError(t, err, "a gate that cannot be written is not retried")

			var got kardinalv1alpha1.PolicyGate
			require.NoError(t, c.Get(context.Background(), key, &got))
			assert.Equal(t, tt.wantMarked, got.Spec.Generated, "spec.generated")
			assert.Equal(t, tt.wantStatus, *statusWrites > 0, "status written")
			if !tt.wantStatus {
				assert.Nil(t, got.Status.LastEvaluatedAt)
			}
		})
	}
}
