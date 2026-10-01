// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

// TestPolicyGateReconciler_OrgGateReadsOrgMetrics covers bug 5 of the health
// spike: the instance of an org gate lives in the Pipeline namespace, and it
// reads metrics.* from its template's org policy namespace, so a team
// MetricCheck of the same name cannot decide it. A team gate, and a label
// that names a namespace that is not an org policy namespace, read the
// instance's own namespace.
//
// A team can edit the labels of its own instances, so the namespace label is
// honored only when that org policy namespace holds the template the
// kardinal.io/gate-template label names. Otherwise the gate reads its own
// namespace, and an error reading the template blocks the gate.
func TestPolicyGateReconciler_OrgGateReadsOrgMetrics(t *testing.T) {
	const template = "error-budget"
	tests := []struct {
		name       string
		templateNS string // "" means no kardinal.io/gate-template-namespace label
		gateName   string // the kardinal.io/gate-template label; "" means none
		templates  bool   // the org namespace holds the PolicyGate template
		policyNS   []string
		getErr     bool // reading the template fails
		orgResult  string
		teamResult string
		wantReady  bool
	}{
		{name: "org gate, org metric passes, team metric fails", templateNS: "platform-policies",
			gateName: template, templates: true, orgResult: "Pass", teamResult: "Fail", wantReady: true},
		{name: "org gate, org metric fails, team metric passes", templateNS: "platform-policies",
			gateName: template, templates: true, orgResult: "Fail", teamResult: "Pass", wantReady: false},
		{name: "org gate from a configured policy namespace", templateNS: "org-b", policyNS: []string{"org-a", "org-b"},
			gateName: template, templates: true, orgResult: "Fail", teamResult: "Pass", wantReady: false},
		{name: "team gate reads the Pipeline namespace", templateNS: "team-a",
			gateName: template, orgResult: "Fail", teamResult: "Pass", wantReady: true},
		{name: "gate from a namespace that is not an org policy namespace", templateNS: "platform-policies", policyNS: []string{"org-a"},
			gateName: template, templates: true, orgResult: "Fail", teamResult: "Pass", wantReady: true},
		{name: "instance without the label (made before the fix)",
			gateName: template, orgResult: "Fail", teamResult: "Pass", wantReady: true},
		{name: "label names an org namespace without the template", templateNS: "platform-policies",
			gateName: template, orgResult: "Pass", teamResult: "Fail", wantReady: false},
		{name: "label names an org namespace, gate-template label names another gate", templateNS: "platform-policies",
			gateName: "no-weekend-deploys", templates: true, orgResult: "Pass", teamResult: "Fail", wantReady: false},
		{name: "label names an org namespace, no gate-template label", templateNS: "platform-policies",
			templates: true, orgResult: "Pass", teamResult: "Fail", wantReady: false},
		{name: "reading the template fails, the gate blocks", templateNS: "platform-policies",
			gateName: template, templates: true, getErr: true, orgResult: "Pass", teamResult: "Pass", wantReady: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orgNS := tt.templateNS
			if orgNS == "" || orgNS == "team-a" {
				orgNS = "platform-policies"
			}
			gate := makeGateInstance("app-v1-prod-error-budget", "team-a", "app-v1",
				`metrics["error-rate"].result == "Pass"`, "1m")
			if tt.templateNS != "" {
				gate.Labels[graph.LabelGateTemplateNamespace] = tt.templateNS
			}
			if tt.gateName != "" {
				gate.Labels["kardinal.io/gate-template"] = tt.gateName
			}
			objs := []client.Object{gate, makeBundle("app-v1", "team-a"),
				makeMetricCheck("error-rate", orgNS, "0.01", tt.orgResult),
				makeMetricCheck("error-rate", "team-a", "0.01", tt.teamResult)}
			if tt.templates {
				objs = append(objs, &kardinalv1alpha1.PolicyGate{
					ObjectMeta: metav1.ObjectMeta{Name: template, Namespace: orgNS},
					Spec:       kardinalv1alpha1.PolicyGateSpec{Expression: `metrics["error-rate"].result == "Pass"`},
				})
			}
			b := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(objs...).WithStatusSubresource(gate)
			if tt.getErr {
				b = b.WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*kardinalv1alpha1.PolicyGate); ok && key.Namespace == orgNS {
							return errors.New("injected: get failed")
						}
						return c.Get(ctx, key, obj, opts...)
					},
				})
			}
			c := b.Build()
			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			r.PolicyNamespaces = tt.policyNS
			r.NowFn = func() time.Time { return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC) }

			key := types.NamespacedName{Name: gate.Name, Namespace: "team-a"}
			_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
			require.NoError(t, err)
			var got kardinalv1alpha1.PolicyGate
			require.NoError(t, c.Get(context.Background(), key, &got))
			assert.Equal(t, tt.wantReady, got.Status.Ready, got.Status.Reason)
		})
	}
}
