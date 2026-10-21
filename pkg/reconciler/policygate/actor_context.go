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
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
)

// actorContext is the actor CEL variable: the Bundle's actor stamp
// (docs/design/18-actor-policy.md section 2.1), or for a Bundle without one
// {kind: unknown, principal: kardinal.io/created-by}. Every key is always
// present. Only the verified fields are in it: the asserted ones (model,
// session, client) are text the client writes, and no such text reaches a
// gate (the untrusted-input guarantee, GATE-UNTRUSTED-01).
//
// An unverified author (written while the AgentIndex was not Ready, #2451)
// counts as an agent for every restriction: kind is agent, with no agent
// or sponsor (agent.registered is false), unverified is true, and verified
// is false, so actor.kind != "agent" gates (the agent gates among them) and
// actor.verified gates hold it.
func actorContext(stamp *kardinalv1alpha1.Actor, author kardinalv1alpha1.Actor) map[string]interface{} {
	unverified := author.Kind == kardinalv1alpha1.ActorUnverified
	kind := author.Kind
	if unverified {
		kind = kardinalv1alpha1.ActorAgent
	}
	return map[string]interface{}{
		"kind":       kind,
		"principal":  author.Principal,
		"agent":      author.Agent,
		"sponsor":    author.Sponsor,
		"via":        author.Via,
		"verified":   stamp != nil && !unverified,
		"unverified": unverified,
	}
}

// authorFrom rebuilds the author from the actor variable of a context
// buildContext built.
func authorFrom(celCtx map[string]interface{}) kardinalv1alpha1.Actor {
	m, _ := celCtx["actor"].(map[string]interface{})
	str := func(k string) string { v, _ := m[k].(string); return v }
	kind := str("kind")
	if u, _ := m["unverified"].(bool); u {
		kind = kardinalv1alpha1.ActorUnverified
	}
	return kardinalv1alpha1.Actor{Kind: kind, Principal: str("principal"), Agent: str("agent"),
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

// buildAgentContext is the agent CEL variable: the Bundle's agent as the
// registry says now (section 2.2), read only from status other reconcilers
// write: registered is the AgentIdentity's status.ready; sponsor, autonomy
// and suspended come from its entry in this release's AgentIndex
// (r.AgentIndexName), which the index reconciler builds from the
// AgentIdentities (suspended: the entry is blocked while the identity is
// ready). A Bundle whose author is not an agent, or a gate that does not
// read agent.*, gets the unregistered value without a read. A read error
// other than NotFound is returned: the gate fails closed.
//
// Graph-first: cross-CRD reads of the gate context, like ChangeWindows
// (ledger G8). A kro ref node would need a cluster-wide grant for every
// Graph identity (G18, G19).
func (r *Reconciler) buildAgentContext(ctx context.Context, gate *kardinalv1alpha1.PolicyGate,
	author kardinalv1alpha1.Actor) (map[string]interface{}, error) {
	out := map[string]interface{}{"registered": false, "name": "", "sponsor": "", "autonomy": "", "suspended": false}
	if author.Kind != kardinalv1alpha1.ActorAgent {
		return out, nil
	}
	out["name"] = author.Agent
	if !readsAgent(gate) || r.AgentIndexName == "" {
		return out, nil
	}
	var id kardinalv1alpha1.AgentIdentity
	if err := r.Get(ctx, client.ObjectKey{Name: author.Agent}, &id); err != nil {
		if apierrors.IsNotFound(err) {
			return out, nil
		}
		return nil, fmt.Errorf("get AgentIdentity %s: %w", author.Agent, err)
	}
	var idx kardinalv1alpha1.AgentIndex
	if err := r.Get(ctx, client.ObjectKey{Name: r.AgentIndexName}, &idx); err != nil {
		if apierrors.IsNotFound(err) {
			return out, nil
		}
		return nil, fmt.Errorf("get AgentIndex %s: %w", r.AgentIndexName, err)
	}
	entry, ok := indexEntry(&idx.Status, author.Agent)
	if !ok || !id.Status.Ready || id.Status.ObservedGeneration != id.Generation {
		return out, nil
	}
	out["registered"] = true
	out["sponsor"] = entry.Sponsor
	out["autonomy"] = entry.Autonomy
	out["suspended"] = entry.Blocked != ""
	return out, nil
}

// indexEntry is an AgentIndex entry of agent (any of its subjects: they
// carry the same entry), the users first.
func indexEntry(idx *kardinalv1alpha1.AgentIndexStatus, agent string) (kardinalv1alpha1.AgentIndexEntry, bool) {
	for _, m := range []map[string]kardinalv1alpha1.AgentIndexEntry{idx.Users, idx.Groups} {
		for _, e := range m {
			if e.Agent == agent {
				return e, true
			}
		}
	}
	return kardinalv1alpha1.AgentIndexEntry{}, false
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

// agentIndexGateRequests maps an event of this release's AgentIndex to the
// gate instances that read agent.*: the index is rebuilt on every
// AgentIdentity change, so a suspension or an autonomy change reaches
// in-flight gates at once.
func (r *Reconciler) agentIndexGateRequests(ctx context.Context, obj client.Object) []reconcile.Request {
	if r.AgentIndexName == "" || obj.GetName() != r.AgentIndexName {
		return nil
	}
	return r.instanceGateRequestsWhere(ctx, "AgentIndex", readsAgent)
}

func equalBoolPtr(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// reverify is author, an unverified stamp, as this release's AgentIndex
// names its writer once it is Ready (actor.Reverify, #2451): an agent's,
// with its sponsor, for the one-party rule and the agent context. Any other
// author is returned as it is, unread. While the index is missing or not
// Ready the author stays unverified; a read error fails the gate closed.
func (r *Reconciler) reverify(ctx context.Context, author kardinalv1alpha1.Actor) (kardinalv1alpha1.Actor, error) {
	if author.Kind != kardinalv1alpha1.ActorUnverified || r.AgentIndexName == "" {
		return author, nil
	}
	var idx kardinalv1alpha1.AgentIndex
	if err := r.Get(ctx, client.ObjectKey{Name: r.AgentIndexName}, &idx); err != nil {
		if apierrors.IsNotFound(err) {
			return author, nil
		}
		return author, fmt.Errorf("get AgentIndex %s: %w", r.AgentIndexName, err)
	}
	st, ok := actor.Readable(&idx.Status)
	if !ok {
		return author, nil
	}
	return actor.Reverify(author, st), nil
}
