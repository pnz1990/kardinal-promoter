// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentbudget"
)

// budgetRoot is the CEL variable of an agent's budget
// (docs/design/24-agent-budgets.md section 10).
const budgetRoot = "budget"

// readsBudget reports whether gate's expression or approval.requiredWhen may
// read budget.*: only then is the AgentIdentity read, and only such gates
// are re-evaluated when the agent's ledger changes.
func readsBudget(gate *kardinalv1alpha1.PolicyGate) bool {
	if strings.Contains(gate.Spec.Expression, budgetRoot) {
		return true
	}
	return gate.Spec.Approval != nil && strings.Contains(gate.Spec.Approval.RequiredWhen, budgetRoot)
}

// budgetCount is one limit as budget.* shows it: set false and zeros when
// the rule does not set it.
func budgetCount(limit int32, c *kardinalv1alpha1.AgentBudgetCount) map[string]interface{} {
	out := map[string]interface{}{"set": limit > 0, "limit": int64(limit), "used": int64(0), "remaining": int64(limit)}
	if limit <= 0 {
		out["remaining"] = int64(0)
		return out
	}
	if c != nil {
		used := int64(c.Used)
		out["used"] = used
		out["remaining"] = max(int64(limit)-used, 0)
	}
	return out
}

// budgetRule is one rule as budget.env and budget.rules.<name> show it,
// from the spec's limits and the ledger's counts (u: the rule's usage, nil
// when the ledger has none).
func budgetRule(r *kardinalv1alpha1.AgentBudgetRule, u *kardinalv1alpha1.AgentBudgetRuleUsage) map[string]interface{} {
	var versions, concurrent, targets, unavailable *kardinalv1alpha1.AgentBudgetCount
	waiting := int64(0)
	if u != nil {
		versions, concurrent, targets, unavailable = u.Versions, u.Concurrent, u.FleetTargets, u.Unavailable
		waiting = int64(u.Waiting)
	}
	out := map[string]interface{}{
		"rule":         "",
		"versions":     budgetCount(0, nil),
		"concurrent":   budgetCount(0, nil),
		"fleetTargets": budgetCount(0, nil),
		"unavailable":  budgetCount(0, nil),
		"fleetShare":   "",
		"waiting":      waiting,
		"exhausted":    false,
	}
	if r == nil {
		return out
	}
	out["rule"], out["fleetShare"] = r.Name, r.FleetShare
	exhausted := false
	for _, k := range []struct {
		key   string
		limit int32
		count *kardinalv1alpha1.AgentBudgetCount
	}{{"versions", r.VersionsPerDay, versions}, {"concurrent", r.MaxConcurrent, concurrent},
		{"fleetTargets", r.FleetTargetsPerHour, targets}, {"unavailable", r.MaxUnavailable, unavailable}} {
		c := budgetCount(k.limit, k.count)
		out[k.key] = c
		if c["set"] == true && c["remaining"] == int64(0) {
			exhausted = true
		}
	}
	out["exhausted"] = exhausted
	return out
}

// emptyBudget is budget.* for a version no budget applies to: every key but
// under rules is present, so expressions evaluate (to false) instead of
// failing with "no such key".
func emptyBudget(agent string) map[string]interface{} {
	return map[string]interface{}{"applies": false, "agent": agent, "current": false,
		"env": budgetRule(nil, nil), "rules": map[string]interface{}{}, "evaluatedAt": ""}
}

// buildBudgetContext is the budget CEL variable: the agent's budget rules
// that apply to the gate's environment and their counts, from this
// installation's ledger (status.usage.<r.BudgetLedger>) in the cache, the
// same source the step hold reads; never a live query, and nothing compares
// them with the clock. A person's version, a gate that does not read
// budget.*, and budgets off (no ledger) get emptyBudget without a read. A
// read error other than NotFound is returned: the gate fails closed.
//
// Graph-first: a cross-CRD read of the gate context, like agent.* (ledger
// G8).
func (r *Reconciler) buildBudgetContext(ctx context.Context, gate *kardinalv1alpha1.PolicyGate,
	author kardinalv1alpha1.Actor) (map[string]interface{}, error) {
	name := ""
	if author.Kind == kardinalv1alpha1.ActorAgent {
		name = author.Agent
	}
	out := emptyBudget(name)
	if name == "" || r.BudgetLedger == "" || !readsBudget(gate) {
		return out, nil
	}
	var id kardinalv1alpha1.AgentIdentity
	if err := r.Get(ctx, client.ObjectKey{Name: name}, &id); err != nil {
		if apierrors.IsNotFound(err) {
			return out, nil
		}
		return nil, fmt.Errorf("get AgentIdentity %s: %w", name, err)
	}
	usage, ok := id.Status.Usage[r.BudgetLedger]
	out["current"] = ok && usage.ObservedGeneration == id.Generation
	if ok && usage.EvaluatedAt != nil {
		out["evaluatedAt"] = usage.EvaluatedAt.UTC().Format(time.RFC3339)
	}
	ruleUsage := func(n string) *kardinalv1alpha1.AgentBudgetRuleUsage {
		for i := range usage.Rules {
			if usage.Rules[i].Name == n {
				return &usage.Rules[i]
			}
		}
		return nil
	}
	rules := map[string]interface{}{}
	env, fleet := gate.Labels[labelEnvironment], gate.Labels[graph.LabelFleet]
	for i := range id.Spec.Budgets {
		rule := &id.Spec.Budgets[i]
		ru := budgetRule(rule, ruleUsage(rule.Name))
		rules[rule.Name] = ru
		if out["applies"] == false && actor.BudgetRuleApplies(rule, gate.Namespace, env, fleet) {
			out["applies"], out["env"] = true, ru // the first matching rule, in spec order
		}
	}
	out["rules"] = rules
	return out, nil
}

// ledgerUsageChanged passes an AgentIdentity update that changes ledger's
// entry of status.usage: only a change of the counts re-evaluates the gates
// that read budget.*.
func ledgerUsageChanged(ledger string) predicate.Funcs {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			o, ok := e.ObjectOld.(*kardinalv1alpha1.AgentIdentity)
			n, ok2 := e.ObjectNew.(*kardinalv1alpha1.AgentIdentity)
			if !ok || !ok2 {
				return false
			}
			ou, hadOld := o.Status.Usage[ledger]
			nu, hasNew := n.Status.Usage[ledger]
			return hadOld != hasNew || !equality.Semantic.DeepEqual(ou, nu)
		},
	}
}

// budgetGateRequests maps an agent's ledger change to the gate instances of
// its versions that read budget.*: its Bundles through the spec.actor.agent
// index, then each Bundle's instances.
func (r *Reconciler) budgetGateRequests(ctx context.Context, obj client.Object) []reconcile.Request {
	var bundles kardinalv1alpha1.BundleList
	if err := r.List(ctx, &bundles, client.MatchingFields{agentbudget.IndexBundleAgent: obj.GetName()}); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Str("agent", obj.GetName()).
			Msg("failed to list an agent's Bundles for its budget gates; they re-evaluate at their recheckInterval")
		return nil
	}
	var out []reconcile.Request
	for i := range bundles.Items {
		b := &bundles.Items[i]
		out = append(out, r.instanceGateRequestsWhere(ctx, "AgentIdentity", readsBudget,
			client.InNamespace(b.Namespace), client.MatchingLabels{labelBundle: b.Name})...)
	}
	return out
}
