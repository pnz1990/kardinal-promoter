// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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
func TestPolicyGateReconciler_OrgGateReadsOrgMetrics(t *testing.T) {
	tests := []struct {
		name       string
		templateNS string // "" means no kardinal.io/gate-template-namespace label
		policyNS   []string
		orgResult  string
		teamResult string
		wantReady  bool
	}{
		{name: "org gate, org metric passes, team metric fails",
			templateNS: "platform-policies", orgResult: "Pass", teamResult: "Fail", wantReady: true},
		{name: "org gate, org metric fails, team metric passes",
			templateNS: "platform-policies", orgResult: "Fail", teamResult: "Pass", wantReady: false},
		{name: "org gate from a configured policy namespace",
			templateNS: "org-b", policyNS: []string{"org-a", "org-b"}, orgResult: "Fail", teamResult: "Pass", wantReady: false},
		{name: "team gate reads the Pipeline namespace",
			templateNS: "team-a", orgResult: "Fail", teamResult: "Pass", wantReady: true},
		{name: "gate from a namespace that is not an org policy namespace",
			templateNS: "platform-policies", policyNS: []string{"org-a"}, orgResult: "Fail", teamResult: "Pass", wantReady: true},
		{name: "instance without the label (made before the fix)",
			orgResult: "Fail", teamResult: "Pass", wantReady: true},
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
			org := makeMetricCheck("error-rate", orgNS, "0.01", tt.orgResult)
			team := makeMetricCheck("error-rate", "team-a", "0.01", tt.teamResult)
			c := fake.NewClientBuilder().WithScheme(newScheme()).
				WithObjects(gate, makeBundle("app-v1", "team-a"), org, team).
				WithStatusSubresource(gate).Build()
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
