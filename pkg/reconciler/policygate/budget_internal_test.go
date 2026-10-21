// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentbudget"
)

// TestBudgetWatchOnlyLedgerChanges: an AgentIdentity write re-evaluates
// gates only when it changes this ledger's counts, and then only the gate
// instances of the agent's versions that read budget.* (section 10).
// Covers BUDGET-CEL-01.
func TestBudgetWatchOnlyLedgerChanges(t *testing.T) {
	const ledger = "kardinal-promoter"
	id := func(gen int64, used int32, other bool) *kardinalv1alpha1.AgentIdentity {
		u := map[string]kardinalv1alpha1.AgentUsage{ledger: {ObservedGeneration: 1,
			Rules: []kardinalv1alpha1.AgentBudgetRuleUsage{{Name: "prod", Versions: &kardinalv1alpha1.AgentBudgetCount{Limit: 4, Used: used}}}}}
		if other {
			u["team-b"] = kardinalv1alpha1.AgentUsage{ObservedGeneration: 7}
		}
		return &kardinalv1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: "fixer", Generation: gen},
			Status: kardinalv1alpha1.AgentIdentityStatus{Usage: u}}
	}
	p := ledgerUsageChanged(ledger)
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: id(1, 1, false), ObjectNew: id(1, 2, false)}), "this ledger's counts")
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: id(1, 1, false), ObjectNew: id(1, 1, true)}), "another ledger")
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: id(1, 1, false), ObjectNew: id(2, 1, false)}), "the spec alone")
	assert.False(t, p.Create(event.CreateEvent{Object: id(1, 1, false)}))

	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	gate := func(name, bundle, expr, when string) *kardinalv1alpha1.PolicyGate {
		g := &kardinalv1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "payments",
			Labels: map[string]string{labelBundle: bundle, labelEnvironment: "prod"}},
			Spec: kardinalv1alpha1.PolicyGateSpec{Expression: expr}}
		if when != "" {
			g.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 1, RequiredWhen: when}
		}
		return g
	}
	agentOf := func(name, agent string) *kardinalv1alpha1.Bundle {
		return &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "payments"},
			Spec: kardinalv1alpha1.BundleSpec{Actor: &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorAgent, Agent: agent}}}
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithIndex(&kardinalv1alpha1.Bundle{}, agentbudget.IndexBundleAgent, agentbudget.BundleAgent).
		WithObjects(agentOf("v1", "fixer"), agentOf("v2", "other"),
			gate("v1-reads", "v1", "!budget.env.exhausted", ""),
			gate("v1-when", "v1", "true", "budget.env.versions.used * 2 >= budget.env.versions.limit"),
			gate("v1-other", "v1", "!schedule.isWeekend", ""),
			gate("v2-reads", "v2", "!budget.env.exhausted", "")).Build()
	r := &Reconciler{Client: c, BudgetLedger: ledger}
	var names []string
	for _, q := range r.budgetGateRequests(context.Background(), id(1, 2, false)) {
		names = append(names, q.Name)
	}
	assert.ElementsMatch(t, []string{"v1-reads", "v1-when"}, names, "the agent's budget-reading gates only")
}
