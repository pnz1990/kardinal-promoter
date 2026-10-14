// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// actorContext is the actor CEL variable: the Bundle's actor stamp
// (docs/design/18-actor-policy.md section 2.1), or for a Bundle without one
// {kind: unknown, principal: kardinal.io/created-by}. Every key is always
// present.
func actorContext(stamp *kardinalv1alpha1.Actor, author kardinalv1alpha1.Actor) map[string]interface{} {
	asserted := map[string]interface{}{"model": "", "session": "", "client": ""}
	if a := author.Asserted; a != nil {
		asserted = map[string]interface{}{"model": a.Model, "session": a.Session, "client": a.Client}
	}
	return map[string]interface{}{
		"kind":      author.Kind,
		"principal": author.Principal,
		"agent":     author.Agent,
		"sponsor":   author.Sponsor,
		"via":       author.Via,
		"verified":  stamp != nil,
		"asserted":  asserted,
	}
}

// authorFrom rebuilds the author from the actor variable of a context
// buildContext built.
func authorFrom(celCtx map[string]interface{}) kardinalv1alpha1.Actor {
	m, _ := celCtx["actor"].(map[string]interface{})
	str := func(k string) string { v, _ := m[k].(string); return v }
	return kardinalv1alpha1.Actor{Kind: str("kind"), Principal: str("principal"), Agent: str("agent"),
		Sponsor: str("sponsor"), Via: str("via")}
}

// readsAgent reports whether gate's expression or approval.requiredWhen may
// read agent.*: only then is the AgentIdentity read, and only such gates are
// re-evaluated when an AgentIdentity changes.
func readsAgent(gate *kardinalv1alpha1.PolicyGate) bool {
	if strings.Contains(gate.Spec.Expression, "agent") {
		return true
	}
	return gate.Spec.Approval != nil && strings.Contains(gate.Spec.Approval.RequiredWhen, "agent")
}

// buildAgentContext is the agent CEL variable: the AgentIdentity of the
// Bundle's agent as it is now (section 2.2): registered (it exists and its
// status.ready is true), name, sponsor, autonomy (default approve) and
// suspended. A Bundle whose author is not an agent, or a gate that does not
// read agent.*, gets the unregistered value without a read. A read error
// other than NotFound is returned: the gate fails closed.
//
// Graph-first: a cross-CRD read of the gate context, like ChangeWindows
// (ledger G8). A kro ref node would need a cluster-wide grant for every
// Graph identity (G18, G19).
func (r *Reconciler) buildAgentContext(ctx context.Context, gate *kardinalv1alpha1.PolicyGate,
	author kardinalv1alpha1.Actor) (map[string]interface{}, error) {
	out := map[string]interface{}{"registered": false, "name": "", "sponsor": "", "autonomy": "", "suspended": false}
	if author.Kind != kardinalv1alpha1.ActorAgent {
		return out, nil
	}
	out["name"] = author.Agent
	if !readsAgent(gate) {
		return out, nil
	}
	var id kardinalv1alpha1.AgentIdentity
	if err := r.Get(ctx, client.ObjectKey{Name: author.Agent}, &id); err != nil {
		if apierrors.IsNotFound(err) {
			return out, nil
		}
		return nil, fmt.Errorf("get AgentIdentity %s: %w", author.Agent, err)
	}
	autonomy := id.Spec.Autonomy
	if autonomy == "" {
		autonomy = kardinalv1alpha1.AutonomyApprove
	}
	out["registered"] = id.Status.Ready
	out["sponsor"] = id.Spec.Sponsor.User
	out["autonomy"] = autonomy
	out["suspended"] = id.Spec.Suspended
	return out, nil
}

// approvalRequired evaluates gate's approval.requiredWhen in celCtx: true
// without a policy expression. An evaluation error is returned, and the
// policy is taken as required (fail closed).
func (r *Reconciler) approvalRequired(ctx context.Context, gate *kardinalv1alpha1.PolicyGate,
	celCtx map[string]interface{}) (bool, error) {
	p := gate.Spec.Approval
	if p == nil || p.RequiredWhen == "" {
		return true, nil
	}
	required, _, err := r.eval.evaluate(ctx, p.RequiredWhen, celCtx)
	if err != nil {
		return true, err
	}
	return required, nil
}

// agentIdentityGateRequests maps an AgentIdentity event to the gate
// instances that read agent.*, so a suspension or an autonomy change
// reaches in-flight gates at once.
func (r *Reconciler) agentIdentityGateRequests(ctx context.Context, _ client.Object) []reconcile.Request {
	return r.instanceGateRequestsWhere(ctx, "AgentIdentity", readsAgent)
}

func equalBoolPtr(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
