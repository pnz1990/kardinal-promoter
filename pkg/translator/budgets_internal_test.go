// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package translator

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// TestAgentBudgetRules: with agent budgets on, the translator reads the
// budget rules of the agent that made a version, to fold them into its fleet
// pacing; a person's version, an unregistered agent and budgets off fold
// nothing, so their Graphs are unchanged (docs/design/24-agent-budgets.md
// section 8).
// Covers BUDGET-FOLD-01.
func TestAgentBudgetRules(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	rules := []kardinalv1alpha1.AgentBudgetRule{{Name: "prod", Environments: []string{"prod"}, MaxConcurrent: 1}}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(&kardinalv1alpha1.AgentIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: "fixer"}, Spec: kardinalv1alpha1.AgentIdentitySpec{Budgets: rules}}).Build()
	tr := New(nil, graph.NewBuilder(), c, nil, zerolog.Nop())
	by := func(a *kardinalv1alpha1.Actor) *kardinalv1alpha1.Bundle {
		return &kardinalv1alpha1.Bundle{Spec: kardinalv1alpha1.BundleSpec{Actor: a}}
	}
	fixer := &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorAgent, Principal: "oidc:fixer", Agent: "fixer", Sponsor: "alice"}
	ctx := context.Background()

	got, err := tr.agentBudgetRules(ctx, by(fixer))
	require.NoError(t, err)
	assert.Nil(t, got, "budgets off")

	tr.WithAgentBudgets(true)
	got, err = tr.agentBudgetRules(ctx, by(fixer))
	require.NoError(t, err)
	assert.Equal(t, rules, got)
	for name, b := range map[string]*kardinalv1alpha1.Bundle{
		"a person's version": by(&kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "bob"}),
		"an unstamped one":   by(nil),
		"an unregistered agent": by(&kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorAgent, Principal: "x", Agent: "gone",
			Sponsor: "alice"}),
	} {
		got, err := tr.agentBudgetRules(ctx, b)
		require.NoError(t, err, name)
		assert.Nil(t, got, name)
	}
}
