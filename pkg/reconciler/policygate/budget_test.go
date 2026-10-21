// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

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

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

// spentIdentity is agent fixer with versionsPerDay 4 on prod and a current
// ledger that counts used of them.
func spentIdentity(used int32) *kardinalv1alpha1.AgentIdentity {
	at := metav1.NewTime(time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC))
	return &kardinalv1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: "fixer", Generation: 1},
		Spec: kardinalv1alpha1.AgentIdentitySpec{Sponsor: kardinalv1alpha1.AgentSponsor{User: "alice"},
			Subjects: []kardinalv1alpha1.AgentSubject{{Kind: "ServiceAccount", Namespace: "ci", Name: "fixer"}},
			Budgets: []kardinalv1alpha1.AgentBudgetRule{
				{Name: "uat", Environments: []string{"uat"}, MaxConcurrent: 3},
				{Name: "prod", Environments: []string{"prod"}, VersionsPerDay: 4, FleetShare: "25%"}}},
		Status: kardinalv1alpha1.AgentIdentityStatus{Ready: true, ObservedGeneration: 1,
			Usage: map[string]kardinalv1alpha1.AgentUsage{indexName: {ObservedGeneration: 1, EvaluatedAt: &at,
				Rules: []kardinalv1alpha1.AgentBudgetRuleUsage{{Name: "prod", Waiting: 2,
					Versions: &kardinalv1alpha1.AgentBudgetCount{Limit: 4, Used: used}}}}}}}
}

// evalBudgetGate reconciles gate against bundle and objs with budget.*
// reading ledger ("" for budgets off) and returns it.
func evalBudgetGate(t *testing.T, ledger string, gate *kardinalv1alpha1.PolicyGate, bundle *kardinalv1alpha1.Bundle,
	objs ...client.Object) *kardinalv1alpha1.PolicyGate {
	t.Helper()
	bundle.Status.Phase = "Promoting"
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(append(objs, gate, bundle)...).
		WithStatusSubresource(gate, bundle).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.AgentIndexName, r.BudgetLedger = indexName, ledger
	r.NowFn = func() time.Time { return time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC) }
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: gate.Name, Namespace: gate.Namespace}}
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	return &got
}

// TestBudgetContext: budget.* in gate expressions and requiredWhen
// (docs/design/24-agent-budgets.md section 10): the first rule that applies
// to the gate's environment and its counts from the ledger; every key
// present for a person's version; applies false with budgets off; more
// scrutiny once half the day's budget is spent.
// Covers BUDGET-CEL-01.
func TestBudgetContext(t *testing.T) {
	agentBundle := func() *kardinalv1alpha1.Bundle {
		b := makeBundle("app-v1", "default")
		b.Spec.Actor = fixerStamp()
		return b
	}
	personBundle := func() *kardinalv1alpha1.Bundle {
		b := makeBundle("app-v1", "default")
		b.Spec.Actor = &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "bob"}
		return b
	}
	for _, tc := range []struct {
		name   string
		ledger string
		bundle *kardinalv1alpha1.Bundle
		used   int32
		expr   string
		want   bool
	}{
		{"the matching rule", indexName, agentBundle(), 1, `budget.applies && budget.agent == "fixer" && budget.env.rule == "prod"`, true},
		{"its counts", indexName, agentBundle(), 1,
			`budget.env.versions.set && budget.env.versions.limit == 4 && budget.env.versions.used == 1 && budget.env.versions.remaining == 3`, true},
		{"a limit the rule does not set", indexName, agentBundle(), 1,
			`!budget.env.concurrent.set && budget.env.concurrent.limit == 0 && budget.env.concurrent.remaining == 0`, true},
		{"fleetShare, waiting and the time", indexName, agentBundle(), 1,
			`budget.env.fleetShare == "25%" && budget.env.waiting == 2 && budget.evaluatedAt == "2026-10-14T09:00:00Z" && budget.current`, true},
		{"every rule by name", indexName, agentBundle(), 1, `budget.rules.uat.concurrent.limit == 3 && budget.rules.prod.versions.used == 1`, true},
		{"not exhausted", indexName, agentBundle(), 3, `!budget.env.exhausted`, true},
		{"exhausted", indexName, agentBundle(), 4, `!budget.env.exhausted`, false},
		{"a person's version: every key present", indexName, personBundle(), 4,
			`!budget.applies && budget.agent == "" && !budget.env.exhausted && budget.env.versions.used == 0 && budget.env.rule == ""`, true},
		{"budgets off", "", agentBundle(), 4, `!budget.applies && budget.agent == "fixer" && !budget.env.exhausted`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := makeGateInstance("g", "default", "app-v1", tc.expr, "5m")
			got := evalBudgetGate(t, tc.ledger, gate, tc.bundle, spentIdentity(tc.used))
			assert.Equal(t, tc.want, got.Status.Ready, got.Status.Reason)
		})
	}

	// More scrutiny as the agent uses its day: a person approves once half
	// the budget is spent (the spec's example).
	when := "budget.applies && budget.env.versions.set && budget.env.versions.used * 2 >= budget.env.versions.limit"
	for _, tc := range []struct {
		used     int32
		required bool
	}{{1, false}, {2, true}, {3, true}} {
		gate := makeGateInstance("g", "default", "app-v1", "true", "5m")
		gate.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true, RequiredWhen: when}
		got := evalBudgetGate(t, indexName, gate, agentBundle(), spentIdentity(tc.used))
		require.NotNil(t, got.Status.ApprovalRequired, "used %d", tc.used)
		assert.Equal(t, tc.required, *got.Status.ApprovalRequired, "used %d of 4", tc.used)
		assert.Equal(t, !tc.required, got.Status.Ready, "used %d: %s", tc.used, got.Status.Reason)
	}
}
