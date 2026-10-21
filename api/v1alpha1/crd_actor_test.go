// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCRDActor: the actor stamp's schema (docs/actors.md): agent and sponsor
// exactly for an agent, a controller says why it acted, and the kind is never
// the read-side "unknown".
func TestCRDActor(t *testing.T) {
	crds := loadCRDs(t)
	bundle := func(actor string) map[string]interface{} {
		return crFromYAML(t, `
apiVersion: kardinal.io/v1alpha1
kind: Bundle
metadata: {name: b, namespace: default}
spec:
  type: image
  pipeline: p
  actor: `+actor)
	}
	for _, tc := range []struct {
		name, actor, want string
	}{
		{name: "human", actor: `{kind: human, principal: alice}`},
		{name: "agent", actor: `{kind: agent, principal: "system:serviceaccount:ci:fixer", agent: fixer, sponsor: alice}`},
		{name: "controller", actor: `{kind: controller, principal: c, via: subscription}`},
		// #2451: a person written while the AgentIndex was not Ready.
		{name: "unverified", actor: `{kind: unverified, principal: alice}`},
		{name: "unverified with an agent", actor: `{kind: unverified, principal: p, agent: fixer, sponsor: alice}`,
			want: "actor.agent and actor.sponsor are set exactly when actor.kind is agent"},
		{name: "asserted", actor: `{kind: human, principal: alice, asserted: {model: m, session: s, client: c}}`},
		{name: "agent without sponsor", actor: `{kind: agent, principal: p, agent: fixer}`,
			want: "actor.agent and actor.sponsor are set exactly when actor.kind is agent"},
		{name: "human with an agent", actor: `{kind: human, principal: p, agent: fixer, sponsor: alice}`,
			want: "actor.agent and actor.sponsor are set exactly when actor.kind is agent"},
		{name: "controller without via", actor: `{kind: controller, principal: c}`, want: "a controller actor says why it acted"},
		{name: "unknown kind", actor: `{kind: unknown, principal: p}`, want: "spec.actor.kind in body should be one of [human agent bot controller unverified]"},
		{name: "bad via", actor: `{kind: human, principal: p, via: cli}`, want: "spec.actor.via in body should be one of"},
		{name: "no principal", actor: `{kind: human, principal: ""}`, want: "spec.actor.principal in body should be at least 1 chars long"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs := validateCR(t, crds, bundle(tc.actor))
			if tc.want == "" {
				assert.Empty(t, errs)
				return
			}
			assertRejectedWith(t, errs, tc.want)
		})
	}
}

// TestCRDBundleActorImmutable: spec.actor cannot be added, changed or
// removed after the Bundle is created; a Bundle without one updates as before.
func TestCRDBundleActorImmutable(t *testing.T) {
	crds := loadCRDs(t)
	doc := func(actor string) map[string]interface{} {
		y := `
apiVersion: kardinal.io/v1alpha1
kind: Bundle
metadata: {name: b, namespace: default}
spec:
  type: image
  pipeline: p`
		if actor != "" {
			y += "\n  actor: " + actor
		}
		return crFromYAML(t, y)
	}
	alice, bob := `{kind: human, principal: alice}`, `{kind: human, principal: bob}`
	msg := "spec.actor cannot be added, changed or removed after the Bundle is created"
	assert.Empty(t, celAdmission(t, crds, doc(""), doc(""), false), "v0.11 Bundle without an actor")
	assert.Empty(t, celAdmission(t, crds, doc(alice), doc(alice), false))
	for _, tc := range []struct{ name, old, cur string }{
		{"added", "", alice}, {"changed", alice, bob}, {"removed", alice, ""},
	} {
		errs := celAdmission(t, crds, doc(tc.cur), doc(tc.old), false)
		if assert.Len(t, errs, 1, tc.name) {
			assert.Contains(t, errs[0], msg, tc.name)
		}
	}
}

// TestCRDAgentIdentity: the registration's schema rules. The subject rule
// reads the CEL-escaped __namespace__ (the API server escapes the keyword),
// so it is checked on its own schema node with the escaped key, and dropped
// from the whole-object checks, which see the field as namespace.
func TestCRDAgentIdentity(t *testing.T) {
	crds := loadCRDs(t)
	const nsRule = "a ServiceAccount subject needs a namespace"
	id := func(spec string) map[string]interface{} {
		return crFromYAML(t, `
apiVersion: kardinal.io/v1alpha1
kind: AgentIdentity
metadata: {name: fixer}
spec:
`+spec)
	}
	errsOf := func(obj map[string]interface{}) []string {
		var out []string
		for _, e := range validateCR(t, crds, obj) {
			if !strings.Contains(e, nsRule) {
				out = append(out, e)
			}
		}
		return out
	}
	ok := `  subjects: [{kind: ServiceAccount, namespace: ci, name: fixer}, {kind: User, name: "oidc:bot"}, {kind: Group, name: agents}]
  sponsor: {user: alice, team: payments}
  scope: {namespaces: ["payments-*", ci], pipelines: ["*"], environments: [test]}
  autonomy: auto`
	for _, tc := range []struct{ name, spec, want string }{
		{name: "valid", spec: ok},
		{name: "no subjects", spec: "  subjects: []\n  sponsor: {user: alice}\n  scope: {}",
			want: "spec.subjects in body should have at least 1 items"},
		{name: "everyone is the agent", spec: "  subjects: [{kind: Group, name: \"system:authenticated\"}]\n  sponsor: {user: alice}\n  scope: {}",
			want: "this group holds most of the cluster"},
		{name: "a ServiceAccount sponsor", spec: "  subjects: [{kind: User, name: u}]\n  sponsor: {user: \"system:serviceaccount:ci:x\"}\n  scope: {}",
			want: "the sponsor is a person"},
		{name: "sponsors itself", spec: "  subjects: [{kind: User, name: alice}]\n  sponsor: {user: alice}\n  scope: {}",
			want: "an agent cannot sponsor itself"},
		{name: "a star inside a name", spec: "  subjects: [{kind: User, name: u}]\n  sponsor: {user: alice}\n  scope: {namespaces: [\"pay*ments\"]}",
			want: "spec.scope.namespaces[0] in body should match"},
		{name: "bad autonomy", spec: "  subjects: [{kind: User, name: u}]\n  sponsor: {user: alice}\n  scope: {}\n  autonomy: always",
			want: "spec.autonomy in body should be one of [auto approve deny]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs := errsOf(id(tc.spec))
			if tc.want == "" {
				assert.Empty(t, errs)
				return
			}
			assertRejectedWith(t, errs, tc.want)
		})
	}

	subject := crdSchema(t, "kardinal.io_agentidentities.yaml", "spec", "subjects", "[]")
	for _, tc := range []struct {
		name string
		self map[string]interface{}
		bad  bool
	}{
		{name: "ServiceAccount with a namespace", self: map[string]interface{}{"kind": "ServiceAccount", "name": "f", "__namespace__": "ci"}},
		{name: "ServiceAccount without", self: map[string]interface{}{"kind": "ServiceAccount", "name": "f"}, bad: true},
		{name: "User", self: map[string]interface{}{"kind": "User", "name": "u"}},
		{name: "User with a namespace", self: map[string]interface{}{"kind": "User", "name": "u", "__namespace__": "x"}, bad: true},
	} {
		failed := failingRules(t, subject, tc.self)
		if tc.bad {
			assert.Contains(t, failed, nsRule+"; User and Group subjects have none", tc.name)
		} else {
			assert.NotContains(t, failed, nsRule+"; User and Group subjects have none", tc.name)
		}
	}
}

// TestCRDAuditEventNamesATarget: an AuditEvent names at least one of the
// Bundle, the Pipeline and the environment; a pause names only its Pipeline.
func TestCRDAuditEventNamesATarget(t *testing.T) {
	crds := loadCRDs(t)
	ev := func(target string) map[string]interface{} {
		return crFromYAML(t, `
apiVersion: kardinal.io/v1alpha1
kind: AuditEvent
metadata: {name: e, namespace: default}
spec:
  timestamp: "2026-10-14T00:00:00Z"
  action: APIWrite
  outcome: Success
  request: {route: /api/v1/ui/pause, method: POST}
`+target)
	}
	assert.Empty(t, validateCR(t, crds, ev("  pipelineName: checkout")))
	assertRejectedWith(t, validateCR(t, crds, ev("")), "an AuditEvent names at least one of bundleName, pipelineName and environment")
}
