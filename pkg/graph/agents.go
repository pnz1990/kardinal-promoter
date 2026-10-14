// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// The per-environment agent shorthand (Pipeline environments[].agents,
// docs/design/18-actor-policy.md section 7) compiles into ordinary gate
// templates, added to every environment of a Graph whose Bundle an agent
// created. One template per ceiling, because a template's spec is stored
// once per name (gateCollections). Human versions' Graphs get none.

// AgentGatePrefix starts the name of every agent-autonomy gate template.
const AgentGatePrefix = "kardinal-agents-"

// agentExpression holds an agent's version when its AgentIdentity is gone,
// not ready, suspended or set to deny. It passes every other version, so a
// copy in a human version's Graph is harmless.
const agentExpression = "actor.kind != 'agent' || (agent.registered && !agent.suspended && agent.autonomy != 'deny')"

// AgentGateName is the template name for ceiling (auto, approve or deny).
func AgentGateName(ceiling string) string {
	return AgentGatePrefix + ceiling
}

// IsAgentGate reports whether name is an agent-autonomy gate template.
func IsAgentGate(name string) bool {
	switch name {
	case AgentGateName(kardinalv1alpha1.AutonomyAuto), AgentGateName(kardinalv1alpha1.AutonomyApprove),
		AgentGateName(kardinalv1alpha1.AutonomyDeny):
		return true
	}
	return false
}

// agentCeiling is env's agents ceiling; unset is auto (the AgentIdentity's
// autonomy alone applies).
func agentCeiling(env kardinalv1alpha1.EnvironmentSpec) string {
	if env.Agents == "" {
		return kardinalv1alpha1.AutonomyAuto
	}
	return env.Agents
}

// agentGateTemplate is the gate template of ceiling, in namespace ns.
func agentGateTemplate(ceiling, ns string) kardinalv1alpha1.PolicyGate {
	g := kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      AgentGateName(ceiling),
			Namespace: ns,
			Labels:    map[string]string{"kardinal.io/scope": "system"},
		},
		Spec: kardinalv1alpha1.PolicyGateSpec{Expression: agentExpression},
	}
	switch ceiling {
	case kardinalv1alpha1.AutonomyDeny:
		g.Spec.Expression = "actor.kind != 'agent'"
		g.Spec.Message = "this environment does not admit versions an agent created (Pipeline environments[].agents: deny)"
	case kardinalv1alpha1.AutonomyApprove:
		g.Spec.Message = "the version's agent is not registered, suspended, or its AgentIdentity autonomy is deny"
		g.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true,
			RequiredWhen: "actor.kind == 'agent'"}
	default:
		g.Spec.Message = "the version's agent is not registered, suspended, or its AgentIdentity autonomy is deny"
		g.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true,
			RequiredWhen: "actor.kind == 'agent' && agent.autonomy == 'approve'"}
	}
	return g
}

// agentAuthored reports whether an agent created b (its actor stamp).
func agentAuthored(b *kardinalv1alpha1.Bundle) bool {
	return b != nil && b.Spec.Actor != nil && b.Spec.Actor.Kind == kardinalv1alpha1.ActorAgent
}

// addAgentGates appends the agent-autonomy gate of each environment in envs
// to gatesByEnv when an agent created bundle. A fleet target takes its
// fleet's ceiling. It refuses a user template with a reserved name: its
// instances would carry the same gate name as kardinal's.
func addAgentGates(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle, envs []string,
	members map[string]fleetMember, gates []kardinalv1alpha1.PolicyGate,
	gatesByEnv map[string][]kardinalv1alpha1.PolicyGate) error {
	if !agentAuthored(bundle) {
		return nil
	}
	for _, g := range gates {
		if strings.HasPrefix(g.Name, AgentGatePrefix) {
			return fmt.Errorf("build: PolicyGate %q in namespace %q has a name kardinal reserves for the "+
				"agent gates of Pipeline environments[].agents (%s*); rename it", g.Name, g.Namespace, AgentGatePrefix)
		}
	}
	templates := map[string]kardinalv1alpha1.PolicyGate{}
	for _, env := range envs {
		name := env
		if m, ok := members[env]; ok {
			name = m.fleet // a fleet target takes its fleet environment's ceiling
		}
		spec := findEnvSpec(pipeline, name)
		ceiling := agentCeiling(spec)
		t, ok := templates[ceiling]
		if !ok {
			t = agentGateTemplate(ceiling, pipeline.Namespace)
			templates[ceiling] = t
		}
		gatesByEnv[env] = append(gatesByEnv[env], t)
	}
	return nil
}
