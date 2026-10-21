// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agentbudget

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// IndexStepsByBudgetAgent registers IndexStepBudgetAgent once per indexer:
// the budget reconciler and the step hold both read it.
func IndexStepsByBudgetAgent(ctx context.Context, indexer client.FieldIndexer) error {
	return lifecycle.IndexOnce(ctx, indexer, &v1alpha1.PromotionStep{}, IndexStepBudgetAgent, StepBudgetAgent)
}

// IndexBundlesByAgent registers IndexBundleAgent once per indexer: the
// budget reconciler (seeding) and the gates' budget.* context (W5) both read
// it.
func IndexBundlesByAgent(ctx context.Context, indexer client.FieldIndexer) error {
	return lifecycle.IndexOnce(ctx, indexer, &v1alpha1.Bundle{}, IndexBundleAgent, BundleAgent)
}

// IndexBundlesByUID registers IndexBundleUID once per indexer.
func IndexBundlesByUID(ctx context.Context, indexer client.FieldIndexer) error {
	return lifecycle.IndexOnce(ctx, indexer, &v1alpha1.Bundle{}, IndexBundleUID, BundleUID)
}

// IndexAgentsWithUnavailable registers IndexUnavailableLedgers once per
// indexer.
func IndexAgentsWithUnavailable(ctx context.Context, indexer client.FieldIndexer) error {
	return lifecycle.IndexOnce(ctx, indexer, &v1alpha1.AgentIdentity{}, IndexUnavailableLedgers, UnavailableLedgers)
}

// The cache's field indexes the budget reconciler reads through
// (docs/design/24-agent-budgets.md section 5.2, A5).
const (
	// IndexStepBudgetAgent: PromotionSteps by status.budget.agent, the
	// requests and grants of one agent.
	IndexStepBudgetAgent = "status.budget.agent"
	// IndexBundleUID: Bundles by metadata.uid, the Bundle of a grant (the
	// ledger names Bundles by UID only).
	IndexBundleUID = "metadata.uid"
	// IndexBundleAgent: Bundles by spec.actor.agent, the versions an agent
	// made, for seeding.
	IndexBundleAgent = "spec.actor.agent"
	// IndexUnavailableLedgers: AgentIdentities by the ledgers that hold an
	// unavailable grant, so a Pipeline change wakes only those agents.
	IndexUnavailableLedgers = "status.usage.unavailable"
)

// StepBudgetAgent is the IndexStepBudgetAgent index function.
func StepBudgetAgent(o client.Object) []string {
	s, ok := o.(*v1alpha1.PromotionStep)
	if !ok || s.Status.Budget == nil || s.Status.Budget.Agent == "" {
		return nil
	}
	return []string{s.Status.Budget.Agent}
}

// BundleUID is the IndexBundleUID index function.
func BundleUID(o client.Object) []string {
	if o.GetUID() == "" {
		return nil
	}
	return []string{string(o.GetUID())}
}

// BundleAgent is the IndexBundleAgent index function: only a version an
// agent made.
func BundleAgent(o client.Object) []string {
	b, ok := o.(*v1alpha1.Bundle)
	if !ok || b.Spec.Actor == nil || b.Spec.Actor.Kind != v1alpha1.ActorAgent || b.Spec.Actor.Agent == "" {
		return nil
	}
	return []string{b.Spec.Actor.Agent}
}

// UnavailableLedgers is the IndexUnavailableLedgers index function.
func UnavailableLedgers(o client.Object) []string {
	id, ok := o.(*v1alpha1.AgentIdentity)
	if !ok {
		return nil
	}
	var out []string
	for name, u := range id.Status.Usage {
		for i := range u.Grants {
			if u.Grants[i].Unavailable {
				out = append(out, name)
				break
			}
		}
	}
	return out
}
