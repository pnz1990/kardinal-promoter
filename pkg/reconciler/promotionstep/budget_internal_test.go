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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentbudget"
)

// TestBudgetWakeups: a ledger write wakes exactly the steps of the grants
// it added; one that grants nothing wakes none; the ledger becoming current,
// or a spec change, wakes the agent's waiting steps; started steps and other
// agents' steps are never woken (section 7.2, Waking).
// Covers BUDGET-HOLD-01.
func TestBudgetWakeups(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	at := metav1.NewTime(time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC))
	waiting := func(name, uid, agent, state string) *v1alpha1.PromotionStep {
		return &v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "payments",
			Labels: map[string]string{graph.LabelBundleUID: uid}},
			Spec:   v1alpha1.PromotionStepSpec{PipelineName: "checkout", BundleName: "b-" + uid, Environment: "prod"},
			Status: v1alpha1.PromotionStepStatus{State: state, Budget: &v1alpha1.PromotionStepBudget{Agent: agent, State: BudgetWaiting, Since: &at}}}
	}
	// A step of a non-compact Graph: no UID label, found through its Bundle.
	plain := waiting("v3-prod", "u-3", "fixer", "Pending")
	plain.Labels = nil
	c := fake.NewClientBuilder().WithScheme(s).
		WithIndex(&v1alpha1.PromotionStep{}, agentbudget.IndexStepBudgetAgent, agentbudget.StepBudgetAgent).
		WithObjects(waiting("v1-prod", "u-1", "fixer", "Pending"), waiting("v2-prod", "u-2", "fixer", ""), plain,
			waiting("v4-prod", "u-4", "fixer", "Promoting"), waiting("o-prod", "u-1", "other", "Pending"),
			&v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "b-u-3", Namespace: "payments", UID: "u-3"}}).Build()
	r := &Reconciler{Client: c, Budgets: &BudgetConfig{Ledger: "kardinal-promoter"}}
	id := func(gen, observed int64, uids ...string) *v1alpha1.AgentIdentity {
		u := v1alpha1.AgentUsage{ObservedGeneration: observed}
		for _, uid := range uids {
			u.Grants = append(u.Grants, v1alpha1.AgentBudgetGrant{BundleUID: uid, Environment: "prod", GrantedAt: at})
		}
		return &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: "fixer", Generation: gen},
			Status: v1alpha1.AgentIdentityStatus{Usage: map[string]v1alpha1.AgentUsage{"kardinal-promoter": u}}}
	}
	names := func(reqs []reconcile.Request) []string {
		var out []string
		for _, q := range reqs {
			out = append(out, q.Name)
		}
		return out
	}
	ctx := context.Background()
	assert.Equal(t, []string{"v1-prod"}, names(r.budgetWakeups(ctx, id(1, 1), id(1, 1, "u-1"))), "the new grant's step only")
	assert.Equal(t, []string{"v3-prod"}, names(r.budgetWakeups(ctx, id(1, 1, "u-1"), id(1, 1, "u-1", "u-3"))),
		"found through its Bundle's UID")
	assert.Empty(t, r.budgetWakeups(ctx, id(1, 1, "u-1"), id(1, 1, "u-1")), "nothing granted, nothing woken")
	assert.Empty(t, r.budgetWakeups(ctx, id(1, 1), id(1, 1, "u-4")), "a started step is not woken")
	assert.ElementsMatch(t, []string{"v1-prod", "v2-prod", "v3-prod"}, names(r.budgetWakeups(ctx, id(2, 1), id(2, 2))),
		"the ledger caught up with the spec: every waiting step re-reads it")
	assert.ElementsMatch(t, []string{"v1-prod", "v2-prod", "v3-prod"}, names(r.budgetWakeups(ctx, id(1, 1), id(2, 1))),
		"a spec change")

	none := &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: "fixer", Generation: 1}}
	assert.Empty(t, r.budgetWakeups(ctx, none, none))
}
