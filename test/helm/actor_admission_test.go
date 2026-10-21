// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
)

// actorAdmissionObjects is every admission object actor-admission.yaml renders.
func actorAdmissionObjects(release string) []string {
	var out []string
	for _, p := range []string{"actor", "actor-records", "actor-overrides", "override-marker", "actor-record-marker", "audit-actor", "agent-identities", "agent-index"} {
		out = append(out, "ValidatingAdmissionPolicy/"+release+"-"+p, "ValidatingAdmissionPolicyBinding/"+release+"-"+p)
	}
	return out
}

// actorRegistry is the test registry, as the controller would index it:
// fixer (a ServiceAccount and a group, payments-* namespaces, test and uat),
// reviewer (an OIDC user), parked (suspended), and late (conflicts with fixer).
func actorRegistry(t *testing.T) *v1alpha1.AgentIndexStatus {
	t.Helper()
	return actorRegistryWith(t, actor.Evaluate)
}

// actorRegistryCompact is actorRegistry in the compact format (#2071), as the
// controller writes it once the actor policies read it: Ready False,
// CompactFormat, and CompactReady True.
func actorRegistryCompact(t *testing.T) *v1alpha1.AgentIndexStatus {
	t.Helper()
	st := actorRegistryWith(t, actor.EvaluateCompact)
	st.Conditions = []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionFalse, Reason: actor.ReasonCompactFormat},
		{Type: actor.ConditionCompactReady, Status: metav1.ConditionTrue, Reason: "Built"},
	}
	return st
}

func actorRegistryWith(t *testing.T, evaluate func([]v1alpha1.AgentIdentity, int) actor.Registry) *v1alpha1.AgentIndexStatus {
	t.Helper()
	at := func(m int) metav1.Time { return metav1.NewTime(time.Date(2026, 10, 1, 0, m, 0, 0, time.UTC)) }
	id := func(name string, m int, sponsor string, scope v1alpha1.AgentScope, subjects ...v1alpha1.AgentSubject) v1alpha1.AgentIdentity {
		return v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: at(m)},
			Spec: v1alpha1.AgentIdentitySpec{Subjects: subjects, Sponsor: v1alpha1.AgentSponsor{User: sponsor}, Scope: scope}}
	}
	scope := v1alpha1.AgentScope{Namespaces: []string{"payments-*"}, Pipelines: []string{"checkout", "cart-*"},
		Environments: []string{"test", "uat"}}
	parked := id("parked", 2, "bob", scope, v1alpha1.AgentSubject{Kind: "ServiceAccount", Namespace: "ci", Name: "parked"})
	parked.Spec.Suspended = true
	reg := evaluate([]v1alpha1.AgentIdentity{
		id("fixer", 0, "alice", scope, v1alpha1.AgentSubject{Kind: "ServiceAccount", Namespace: "ci", Name: "fixer"},
			v1alpha1.AgentSubject{Kind: "Group", Name: "oidc:agents-fixer"}),
		id("reviewer", 1, "alice", v1alpha1.AgentScope{Namespaces: []string{"*"}}, v1alpha1.AgentSubject{Kind: "User", Name: "oidc:reviewer"}),
		parked,
		id("late", 3, "carol", scope, v1alpha1.AgentSubject{Kind: "ServiceAccount", Namespace: "ci", Name: "fixer"},
			v1alpha1.AgentSubject{Kind: "ServiceAccount", Namespace: "ci", Name: "late"}),
	}, actor.MaxIndexBytes)
	require.False(t, reg.Verdicts["late"].Ready)
	return &reg.Index
}

// asMap is v as the API server hands it to CEL: JSON-decoded.
func asMap(t *testing.T, v interface{}) map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// actorReq is one admission request for the actor policy.
type actorReq struct {
	resource, namespace, name string
	user                      string
	groups                    []string
	object, old               map[string]interface{}
}

// evalActor evaluates vap as the API server would, with params, and returns
// the message of the first validation that fails ("" when admitted).
func evalActor(t *testing.T, vap *admissionregistrationv1.ValidatingAdmissionPolicy, params map[string]interface{}, r actorReq) string {
	t.Helper()
	env, err := cel.NewEnv(cel.Variable("object", cel.DynType), cel.Variable("oldObject", cel.DynType),
		cel.Variable("request", cel.DynType), cel.Variable("params", cel.DynType), cel.Variable("variables", cel.DynType),
		ext.Strings(), ext.Lists())
	require.NoError(t, err)
	groups := make([]interface{}, 0, len(r.groups))
	for _, g := range r.groups {
		groups = append(groups, g)
	}
	req := map[string]interface{}{
		"userInfo":  map[string]interface{}{"username": r.user, "groups": groups},
		"operation": operation(r.object, r.old), "namespace": r.namespace, "name": r.name,
		"resource": map[string]interface{}{"group": "kardinal.io", "version": "v1alpha1", "resource": r.resource},
	}
	variables := map[string]interface{}{}
	vars := map[string]interface{}{"object": r.object, "request": req, "params": params, "variables": variables}
	if r.old == nil {
		vars["oldObject"] = nil
	} else {
		vars["oldObject"] = r.old
	}
	eval := func(expr string) interface{} {
		ast, iss := env.Compile(expr)
		require.NoError(t, iss.Err(), expr)
		prg, err := env.Program(ast)
		require.NoError(t, err)
		out, _, err := prg.Eval(vars)
		require.NoError(t, err, expr)
		return out.Value()
	}
	for _, v := range vap.Spec.Variables {
		variables[v.Name] = eval(v.Expression)
	}
	for _, v := range vap.Spec.Validations {
		if eval(v.Expression) == true {
			continue
		}
		if v.MessageExpression != "" {
			return eval(v.MessageExpression).(string)
		}
		return v.Message
	}
	return ""
}

func bundleObj(t *testing.T, ns, pipeline string, stamp *v1alpha1.Actor) map[string]interface{} {
	return asMap(t, v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: ns},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: pipeline, Actor: stamp}})
}

func approvalObj(t *testing.T, ns, env string, stamp *v1alpha1.Actor) map[string]interface{} {
	return asMap(t, v1alpha1.Approval{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: ns,
		Labels: map[string]string{"kardinal.io/bundle": "b", "kardinal.io/environment": env}},
		Spec: v1alpha1.ApprovalSpec{Bundle: "b", BundleUID: "u", Environment: env, User: "x", Decision: "approve", Actor: stamp}})
}

func gateObj(t *testing.T, ns, env string, overrides ...v1alpha1.PolicyGateOverride) map[string]interface{} {
	return asMap(t, v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: ns,
		Labels: map[string]string{"kardinal.io/bundle": "b", "kardinal.io/pipeline": "checkout", "kardinal.io/environment": env}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true", Overrides: overrides}})
}

// TestActorAdmission runs the admission cases of docs/design/17-actors.md
// section 13 through the chart's <release>-actor policy, and each through
// actorCase is one request of TestActorAdmission.
type actorCase struct {
	name  string
	req   actorReq
	where actor.Where
	deny  string // a substring of the refusal; "" admits
	// stamp marks a refusal about the stamp the client wrote, which only
	// the policy sees; noParity a request that stamps nothing new.
	stamp, noParity bool
}

// actor.Derive and actor.Scope: the policy is a transliteration of them, and
// the two must agree on every case, over an index in each format (#2071):
// the object format, the compact one, and the compact one an older
// controller wrote the object format over (its stale compact entries are
// not read).
// Covers ACTOR-INDEX-01.
func TestActorAdmission(t *testing.T) {
	t.Parallel()
	vap := identityPolicy(t, "actor")
	require.NotNil(t, vap.Spec.ParamKind)
	assert.Equal(t, "AgentIndex", vap.Spec.ParamKind.Kind)
	legacy := *actorRegistry(t)
	legacy.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Built"}}
	compact := actorRegistryCompact(t)
	// An older controller rewrote the legacy maps and Ready after this
	// release's wrote the compact ones (pkg/actor/compact.go): the compact
	// maps left on the server are stale, here one that knows no agent but
	// parked, unblocked, and must not be read.
	stale := legacy
	staleIDs := actor.EvaluateCompact([]v1alpha1.AgentIdentity{{ObjectMeta: metav1.ObjectMeta{Name: "parked"},
		Spec: v1alpha1.AgentIdentitySpec{Subjects: []v1alpha1.AgentSubject{{Kind: "ServiceAccount", Namespace: "ci", Name: "parked"}},
			Sponsor: v1alpha1.AgentSponsor{User: "bob"}, Scope: v1alpha1.AgentScope{Namespaces: []string{"*"}}}}}, actor.MaxIndexBytes)
	stale.CompactUsers, stale.CompactGroups = staleIDs.Index.CompactUsers, staleIDs.Index.CompactGroups
	stale.Conditions = append([]metav1.Condition{}, legacy.Conditions...)
	stale.Conditions = append(stale.Conditions, metav1.Condition{Type: actor.ConditionCompactReady, Status: metav1.ConditionTrue, Reason: "Built"})

	fixerSA := "system:serviceaccount:ci:fixer"
	fixer := &v1alpha1.Actor{Kind: "agent", Principal: fixerSA, Agent: "fixer", Sponsor: "alice"}
	human := func(p string) *v1alpha1.Actor { return &v1alpha1.Actor{Kind: "human", Principal: p} }
	with := func(a *v1alpha1.Actor, f func(*v1alpha1.Actor)) *v1alpha1.Actor { c := *a; f(&c); return &c }
	override := func(by string, a *v1alpha1.Actor) v1alpha1.PolicyGateOverride {
		return v1alpha1.PolicyGateOverride{Reason: "r", Stage: "uat", ExpiresAt: metav1.NewTime(time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)),
			CreatedBy: by, Actor: a}
	}
	type where = actor.Where
	tests := []actorCase{
		{name: "1 human without a stamp", req: actorReq{resource: "bundles", namespace: "payments-eu", user: "alice",
			object: bundleObj(t, "payments-eu", "checkout", nil)}},
		{name: "2 human stamped human", req: actorReq{resource: "bundles", namespace: "x", user: "alice",
			object: bundleObj(t, "x", "checkout", human("alice"))}},
		{name: "3 forged principal", req: actorReq{resource: "bundles", namespace: "x", user: "alice",
			object: bundleObj(t, "x", "checkout", human("bob"))}, deny: "actor must be {kind: human, principal: alice}", stamp: true},
		{name: "4 human claims to be an agent", req: actorReq{resource: "bundles", namespace: "x", user: "alice",
			object: bundleObj(t, "x", "checkout", with(fixer, func(a *v1alpha1.Actor) { a.Principal = "alice" }))},
			deny: "actor must be {kind: human", stamp: true},
		{name: "5 agent without a stamp", req: actorReq{resource: "bundles", namespace: "payments-eu", user: fixerSA,
			object: bundleObj(t, "payments-eu", "checkout", nil)}, deny: "you are agent fixer (sponsor alice)", stamp: true},
		{name: "6 agent stamped agent with its sponsor", req: actorReq{resource: "bundles", namespace: "payments-eu", user: fixerSA,
			object: bundleObj(t, "payments-eu", "checkout", fixer)}, where: where{Namespace: "payments-eu", Pipeline: "checkout"}},
		{name: "7 agent names another sponsor", req: actorReq{resource: "bundles", namespace: "payments-eu", user: fixerSA,
			object: bundleObj(t, "payments-eu", "checkout", with(fixer, func(a *v1alpha1.Actor) { a.Sponsor = "mallory" }))},
			deny: "sponsor: alice", stamp: true},
		{name: "8 agent stamps itself human", req: actorReq{resource: "bundles", namespace: "payments-eu", user: fixerSA,
			object: bundleObj(t, "payments-eu", "checkout", human(fixerSA))}, deny: "actor must be {kind: agent", stamp: true},
		{name: "9 agent by group", req: actorReq{resource: "bundles", namespace: "payments-us", user: "oidc:someone",
			groups: []string{"system:authenticated", "oidc:agents-fixer"},
			object: bundleObj(t, "payments-us", "cart-v2", with(fixer, func(a *v1alpha1.Actor) { a.Principal = "oidc:someone" }))},
			where: where{Namespace: "payments-us", Pipeline: "cart-v2"}},
		{name: "10 namespace out of scope", req: actorReq{resource: "bundles", namespace: "billing", user: fixerSA,
			object: bundleObj(t, "billing", "checkout", fixer)}, where: where{Namespace: "billing", Pipeline: "checkout"},
			deny: "may not act here (namespace billing"},
		{name: "11 pipeline out of scope", req: actorReq{resource: "bundles", namespace: "payments-eu", user: fixerSA,
			object: bundleObj(t, "payments-eu", "ledger", fixer)}, where: where{Namespace: "payments-eu", Pipeline: "ledger"},
			deny: "pipeline ledger"},
		{name: "12 approval for an environment out of scope", req: actorReq{resource: "approvals", namespace: "payments-eu", user: fixerSA,
			object: approvalObj(t, "payments-eu", "prod", fixer)}, where: where{Namespace: "payments-eu", Environment: "prod"},
			deny: "environment prod"},
		{name: "12b approval in scope", req: actorReq{resource: "approvals", namespace: "payments-eu", user: fixerSA,
			object: approvalObj(t, "payments-eu", "uat", fixer)}, where: where{Namespace: "payments-eu", Environment: "uat"}},
		{name: "13 suspended agent", req: actorReq{resource: "bundles", namespace: "payments-eu", user: "system:serviceaccount:ci:parked",
			object: bundleObj(t, "payments-eu", "checkout", nil)}, deny: "agent parked may not act: suspended"},
		{name: "14 the losing AgentIdentity's other subject is blocked", req: actorReq{resource: "bundles", namespace: "payments-eu",
			user: "system:serviceaccount:ci:late", object: bundleObj(t, "payments-eu", "checkout", nil)}, deny: "agent late may not act: not ready: SubjectConflict"},
		{name: "14b two agents at once", req: actorReq{resource: "bundles", namespace: "payments-eu", user: "oidc:reviewer",
			groups: []string{"oidc:agents-fixer"}, object: bundleObj(t, "payments-eu", "checkout", nil)},
			deny: "match more than one AgentIdentity (reviewer, fixer)"},
		{name: "15 unregistered ServiceAccount is a bot", req: actorReq{resource: "bundles", namespace: "x", user: "system:serviceaccount:ci:builder",
			object: bundleObj(t, "x", "checkout", &v1alpha1.Actor{Kind: "bot", Principal: "system:serviceaccount:ci:builder"})}},
		{name: "16 the controller stamps for a UI caller", req: actorReq{resource: "bundles", namespace: "billing", user: controllerUser,
			object: bundleObj(t, "billing", "checkout", with(fixer, func(a *v1alpha1.Actor) { a.Via = "ui" }))}},
		{name: "17 only the controller sets via", req: actorReq{resource: "bundles", namespace: "x", user: "alice",
			object: bundleObj(t, "x", "checkout", with(human("alice"), func(a *v1alpha1.Actor) { a.Via = "ui" }))}, deny: "with no via", stamp: true},
		// QA-3 on #2184: the stamp kardinal trusts for an automatic rollback
		// (lifecycle.IsAutoRollback) is the controller's alone.
		{name: "17b a person forges the automatic rollback's stamp", req: actorReq{resource: "bundles", namespace: "x", user: "mallory",
			object: bundleObj(t, "x", "checkout", &v1alpha1.Actor{Kind: v1alpha1.ActorController, Principal: "kardinal-controller",
				Via: v1alpha1.ViaAutoRollback})}, deny: "actor must be {kind: human, principal: mallory}", stamp: true},
		{name: "17c a person names the controller kind as themselves", req: actorReq{resource: "bundles", namespace: "x", user: "mallory",
			object: bundleObj(t, "x", "checkout", &v1alpha1.Actor{Kind: v1alpha1.ActorController, Principal: "mallory"})},
			deny: "actor must be {kind: human, principal: mallory}", stamp: true},
		{name: "17d the controller's automatic rollback", req: actorReq{resource: "bundles", namespace: "x", user: controllerUser,
			object: bundleObj(t, "x", "checkout", &v1alpha1.Actor{Kind: v1alpha1.ActorController, Principal: controllerUser,
				Via: v1alpha1.ViaAutoRollback})}},
		{name: "18 agent override stamped", req: actorReq{resource: "policygates", namespace: "payments-eu", user: fixerSA,
			old: gateObj(t, "payments-eu", "uat"), object: gateObj(t, "payments-eu", "uat", override(fixerSA, fixer))},
			where: where{Namespace: "payments-eu", Pipeline: "checkout", Environment: "uat"}},
		{name: "18b agent override forged", req: actorReq{resource: "policygates", namespace: "payments-eu", user: fixerSA,
			old: gateObj(t, "payments-eu", "uat"), object: gateObj(t, "payments-eu", "uat", override(fixerSA, human(fixerSA)))},
			deny: "actor must be {kind: agent", stamp: true},
		{name: "18c agent override in prod", req: actorReq{resource: "policygates", namespace: "payments-eu", user: fixerSA,
			old: gateObj(t, "payments-eu", "prod"), object: gateObj(t, "payments-eu", "prod", override(fixerSA, fixer))},
			where: where{Namespace: "payments-eu", Pipeline: "checkout", Environment: "prod"}, deny: "environment prod"},
		{name: "18d an existing override is not checked again", req: actorReq{resource: "policygates", namespace: "payments-eu",
			user: "system:serviceaccount:ci:parked",
			old:  gateObj(t, "payments-eu", "uat", override(fixerSA, fixer)), object: gateObj(t, "payments-eu", "uat", override(fixerSA, fixer))}, noParity: true},
		{name: "18e kro creates a gate instance", req: actorReq{resource: "policygates", namespace: "payments-eu",
			user: "system:serviceaccount:payments-eu:kardinal-graph", object: gateObj(t, "payments-eu", "prod")}, noParity: true},
	}
	gates := identityPolicy(t, "actor-overrides")
	for _, format := range []struct {
		name string
		st   *v1alpha1.AgentIndexStatus
	}{{"legacy", &legacy}, {"compact", compact}, {"stale compact maps after an older writer", &stale}} {
		params := asMap(t, v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}, Status: *format.st})
		// What the controller and the CLI decide with: actor.Source reads
		// the current format the same way (actor.Readable).
		idx, ok := actor.Readable(format.st)
		require.True(t, ok, format.name)
		t.Run(format.name, func(t *testing.T) {
			actorCases(t, vap, gates, params, idx, tests)
		})
	}
}

// actorCases runs TestActorAdmission's cases against params, and checks
// pkg/actor's answer with idx.
func actorCases(t *testing.T, vap, gates *admissionregistrationv1.ValidatingAdmissionPolicy, params map[string]interface{},
	idx *v1alpha1.AgentIndexStatus, tests []actorCase) {
	t.Helper()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Gate overrides are checked by their own policy (#1936), with
			// the same expressions.
			policy := vap
			if tc.req.resource == "policygates" {
				policy = gates
			}
			got := evalActor(t, policy, params, tc.req)
			if tc.deny == "" {
				assert.Empty(t, got, "admitted")
			} else {
				assert.Contains(t, got, tc.deny)
			}
			// Parity with pkg/actor (the controller and the CLI): the same
			// requester gets the same answer. The controller is exempt, and
			// a refusal of a stamp's fields is the policy's alone.
			if tc.req.user == controllerUser || tc.stamp || tc.noParity {
				return
			}
			r, err := actor.Derive(actor.User{Username: tc.req.user, Groups: tc.req.groups}, idx)
			if err == nil {
				err = actor.Scope(r.Entry, tc.where)
			}
			if tc.deny == "" {
				require.NoError(t, err, "Derive and Scope admit what the policy admits")
				return
			}
			require.Error(t, err, "Derive and Scope refuse what the policy refuses")
			assert.True(t, errors.Is(err, actor.ErrBlocked) || errors.Is(err, actor.ErrConflict) || errors.Is(err, actor.ErrOutOfScope))
		})
	}
}

// TestActorAdmission_IndexNotReady: an AgentIndex with no status (just
// created), or one not Ready (IndexFull), denies every write the policy
// judges, people's included, until the controller has built it: the
// registry fails closed. A full index says so, and what to do (retrying
// does not help: QA-3 on #2384), in either format. A Ready index with no
// agents admits people and bots.
func TestActorAdmission_IndexNotReady(t *testing.T) {
	t.Parallel()
	vap := identityPolicy(t, "actor")
	req := actorReq{resource: "bundles", namespace: "x", user: "alice", object: bundleObj(t, "x", "checkout", nil)}
	empty := asMap(t, v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}})
	delete(empty, "status")
	assert.Contains(t, evalActor(t, vap, empty, req), "kardinal's agent index is not ready yet")
	full := asMap(t, v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"},
		Status: v1alpha1.AgentIndexStatus{Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Reason: "IndexFull"}}}})
	isFull := func(got string) {
		t.Helper()
		assert.Contains(t, got, "kardinal's agent index is full (IndexFull")
		assert.Contains(t, got, "remove AgentIdentities or subjects")
		assert.NotContains(t, got, "retry")
	}
	isFull(evalActor(t, vap, full, req))
	ready := asMap(t, v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"},
		Status: v1alpha1.AgentIndexStatus{Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Built"}}}})
	assert.Empty(t, evalActor(t, vap, ready, req))
	// The compact format (#2071): CompactReady decides, Ready is False.
	compact := func(status metav1.ConditionStatus, reason string) map[string]interface{} {
		return asMap(t, v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"},
			Status: v1alpha1.AgentIndexStatus{Conditions: []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionFalse, Reason: actor.ReasonCompactFormat},
				{Type: actor.ConditionCompactReady, Status: status, Reason: reason}}}})
	}
	assert.Empty(t, evalActor(t, vap, compact(metav1.ConditionTrue, "Built"), req))
	isFull(evalActor(t, vap, compact(metav1.ConditionFalse, "IndexFull"), req))
	// The controller is exempt: it refuses on its own when its index is not ready.
	req.user = controllerUser
	assert.Empty(t, evalActor(t, vap, empty, req))
}

// TestActorRecords_IndexNotReady: <release>-actor-records judges a stamp
// only against a built index, in either format: absence from it means "not
// an agent", so against an index not yet built (no status) or full
// (IndexFull, written empty; CompactReady False in the compact format) an
// agent could record a pause stamped as a person or a bot. Every stamped
// record is refused until the controller has built it; unstamped records go
// on, and a built index, in either format, admits the honest stamp.
// Covers ACTOR-INDEX-05.
func TestActorRecords_IndexNotReady(t *testing.T) {
	t.Parallel()
	vap := identityPolicy(t, "actor-records")
	fixerSA := "system:serviceaccount:ci:fixer"
	req := func(stamp *v1alpha1.Actor) actorReq {
		o, old := pipelineRecord(stamp)
		return actorReq{resource: "pipelines", namespace: "payments-eu", user: fixerSA,
			groups: []string{"system:authenticated"}, object: o, old: old}
	}
	bot := &v1alpha1.Actor{Kind: "bot", Principal: fixerSA}
	index := func(st v1alpha1.AgentIndexStatus) map[string]interface{} {
		return asMap(t, v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}, Status: st})
	}
	empty := asMap(t, v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}})
	delete(empty, "status")
	fullCompact := *actorRegistryCompact(t)
	fullCompact.CompactUsers, fullCompact.CompactGroups = nil, nil
	fullCompact.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Reason: actor.ReasonCompactFormat},
		{Type: actor.ConditionCompactReady, Status: metav1.ConditionFalse, Reason: actor.ReasonIndexFull}}
	for name, tc := range map[string]struct {
		params map[string]interface{}
		deny   string
	}{
		"never built":   {empty, "kardinal's agent index is not ready yet"},
		"full":          {index(v1alpha1.AgentIndexStatus{Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Reason: "IndexFull"}}}), "kardinal's agent index is full (IndexFull"},
		"full, compact": {index(fullCompact), "kardinal's agent index is full (IndexFull"},
	} {
		assert.Contains(t, evalActor(t, vap, tc.params, req(bot)), tc.deny, name)
		assert.Empty(t, evalActor(t, vap, tc.params, req(nil)), "%s: an unstamped pause", name)
	}
	legacy := *actorRegistry(t)
	legacy.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Built"}}
	for name, st := range map[string]v1alpha1.AgentIndexStatus{"built": legacy, "built, compact": *actorRegistryCompact(t)} {
		params := index(st)
		assert.Contains(t, evalActor(t, vap, params, req(bot)), "actor must be {kind: agent", "%s: the agent may not pass as a bot", name)
		assert.Empty(t, evalActor(t, vap, params, req(&v1alpha1.Actor{Kind: "agent", Principal: fixerSA, Agent: "fixer", Sponsor: "alice"})), name)
	}
}

// TestActorAdmission_Binding: the binding reads the release's AgentIndex and
// denies when it is missing (fail closed).
func TestActorAdmission_Binding(t *testing.T) {
	t.Parallel()
	for _, d := range render(t, "kardinal-promoter") {
		if d.Kind != "ValidatingAdmissionPolicyBinding" || d.Name != "kardinal-promoter-actor" {
			continue
		}
		var b admissionregistrationv1.ValidatingAdmissionPolicyBinding
		decodeStrict(t, d, &b)
		require.NotNil(t, b.Spec.ParamRef)
		assert.Equal(t, "kardinal-promoter", b.Spec.ParamRef.Name)
		require.NotNil(t, b.Spec.ParamRef.ParameterNotFoundAction)
		assert.Equal(t, admissionregistrationv1.DenyAction, *b.Spec.ParamRef.ParameterNotFoundAction)
		assert.Empty(t, b.Spec.ParamRef.Namespace, "AgentIndex is cluster-scoped")
		return
	}
	t.Fatal("no actor binding")
}

// TestAuditActorAdmission: only the controller writes an AuditEvent that
// names an actor; one without is admitted as before.
func TestAuditActorAdmission(t *testing.T) {
	t.Parallel()
	vap := identityPolicy(t, "audit-actor")
	ev := func(a *v1alpha1.Actor) map[string]interface{} {
		return asMap(t, v1alpha1.AuditEvent{ObjectMeta: metav1.ObjectMeta{Name: "e", Namespace: "x"},
			Spec: v1alpha1.AuditEventSpec{BundleName: "b", Action: "APIWrite", Outcome: "Success", Actor: a}})
	}
	alice := &v1alpha1.Actor{Kind: "human", Principal: "alice"}
	assert.True(t, admits(t, vap, ev(nil), nil, "alice"))
	assert.False(t, admits(t, vap, ev(alice), nil, "alice"))
	assert.True(t, admits(t, vap, ev(alice), nil, controllerUser))
}

// TestAgentIdentityDeleteAdmission: an AgentIdentity is deleted only once
// suspended.
func TestAgentIdentityDeleteAdmission(t *testing.T) {
	t.Parallel()
	vap := identityPolicy(t, "agent-identities")
	id := func(suspended bool) map[string]interface{} {
		return asMap(t, v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: "fixer"},
			Spec: v1alpha1.AgentIdentitySpec{Subjects: []v1alpha1.AgentSubject{{Kind: "User", Name: "u"}},
				Sponsor: v1alpha1.AgentSponsor{User: "alice"}, Suspended: suspended}})
	}
	assert.False(t, admits(t, vap, nil, id(false), "cluster-admin"))
	assert.True(t, admits(t, vap, nil, id(true), "cluster-admin"))
}

// TestActorPolicies_Match (#1936): the policies that read the agent index
// match only what they check before the index is read. <release>-actor
// matches new Bundles, Approvals and Verdicts; <release>-actor-overrides matches gates
// labelled kardinal.io/override-check, so kro's re-apply of a gate instance
// reaches neither, and both run the same expressions.
//
// Covers ACTOR-PERF-01.
func TestActorPolicies_Match(t *testing.T) {
	t.Parallel()
	actorVAP, gates := identityPolicy(t, "actor"), identityPolicy(t, "actor-overrides")
	require.Len(t, actorVAP.Spec.MatchConstraints.ResourceRules, 2)
	r := actorVAP.Spec.MatchConstraints.ResourceRules[0]
	assert.Equal(t, []string{"bundles", "approvals", "verdicts"}, r.Resources)
	assert.Equal(t, []admissionregistrationv1.OperationType{"CREATE"}, r.Operations)
	// Freeze exemptions (#1843): ChangeWindows, rarely written.
	assert.Equal(t, []string{"changewindows"}, actorVAP.Spec.MatchConstraints.ResourceRules[1].Resources)
	for _, rule := range append(actorVAP.Spec.MatchConstraints.ResourceRules, gates.Spec.MatchConstraints.ResourceRules...) {
		assert.NotContains(t, rule.Resources, "policygates/status")
	}
	for _, rule := range actorVAP.Spec.MatchConstraints.ResourceRules {
		assert.NotContains(t, rule.Resources, "policygates", "gates go to actor-overrides, labelled only")
	}

	sel := gates.Spec.MatchConstraints.ObjectSelector
	require.NotNil(t, sel, "only labelled gates")
	assert.Equal(t, []metav1.LabelSelectorRequirement{{Key: "kardinal.io/override-check", Operator: metav1.LabelSelectorOpExists}},
		sel.MatchExpressions)
	assert.Equal(t, []string{"policygates"}, gates.Spec.MatchConstraints.ResourceRules[0].Resources)
	assert.Equal(t, actorVAP.Spec.Variables, gates.Spec.Variables)
	assert.Equal(t, actorVAP.Spec.Validations, gates.Spec.Validations)
	assert.Equal(t, actorVAP.Spec.ParamKind, gates.Spec.ParamKind)
}

// TestOverrideMarkerAdmission (#1936): a write that adds or changes an
// override entry carries kardinal.io/override-check: "true", whoever
// writes it but the controller; a write that adds none (kro's re-apply, an
// expired entry removed) needs no label.
//
// Covers ACTOR-PERF-01.
func TestOverrideMarkerAdmission(t *testing.T) {
	t.Parallel()
	vap := identityPolicy(t, "override-marker")
	assert.Nil(t, vap.Spec.ParamKind, "no params: it judges every gate write at no cost that grows with the registry")
	o := v1alpha1.PolicyGateOverride{Reason: "r", ExpiresAt: metav1.NewTime(time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)), CreatedBy: "alice"}
	labelled := func(m map[string]interface{}) map[string]interface{} {
		m["metadata"].(map[string]interface{})["labels"].(map[string]interface{})["kardinal.io/override-check"] = "true"
		return m
	}
	changed := o
	changed.Reason = "other"
	for _, tc := range []struct {
		name string
		req  actorReq
		deny bool
	}{
		{"adds an override without the label", actorReq{resource: "policygates", user: "alice",
			old: gateObj(t, "x", "prod"), object: gateObj(t, "x", "prod", o)}, true},
		{"adds an override with the label", actorReq{resource: "policygates", user: "alice",
			old: gateObj(t, "x", "prod"), object: labelled(gateObj(t, "x", "prod", o))}, false},
		{"changes an entry without the label", actorReq{resource: "policygates", user: "alice",
			old: gateObj(t, "x", "prod", o), object: gateObj(t, "x", "prod", changed)}, true},
		{"creates a gate with an override, no label", actorReq{resource: "policygates", user: "alice",
			object: gateObj(t, "x", "prod", o)}, true},
		{"kro re-applies an overridden gate", actorReq{resource: "policygates", user: "system:serviceaccount:x:kardinal-graph",
			old: gateObj(t, "x", "prod", o), object: gateObj(t, "x", "prod", o)}, false},
		{"an entry removed", actorReq{resource: "policygates", user: "alice",
			old: gateObj(t, "x", "prod", o), object: gateObj(t, "x", "prod")}, false},
		{"the controller", actorReq{resource: "policygates", user: controllerUser,
			old: gateObj(t, "x", "prod"), object: gateObj(t, "x", "prod", o)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := evalActor(t, vap, nil, tc.req)
			if tc.deny {
				assert.Contains(t, got, "kardinal.io/override-check")
			} else {
				assert.Empty(t, got)
			}
		})
	}
}

// TestAgentIndexChart (#1929): the chart creates the release's AgentIndex,
// kept on uninstall, so the actor policies' param exists before they are
// bound; <release>-agent-index lets only the controller delete it or write
// its status.
//
// Covers ACTOR-ADMIT-06.
func TestAgentIndexChart(t *testing.T) {
	t.Parallel()
	var found bool
	for _, d := range render(t, "kardinal-promoter") {
		if d.Kind != "AgentIndex" {
			continue
		}
		var idx v1alpha1.AgentIndex
		decodeStrict(t, d, &idx)
		assert.Equal(t, "kardinal-promoter", idx.Name)
		assert.Equal(t, "keep", idx.Annotations["helm.sh/resource-policy"])
		found = true
	}
	require.True(t, found, "the chart creates the AgentIndex")
	vap := identityPolicy(t, "agent-index")
	assert.Nil(t, vap.Spec.ParamKind)
	for _, r := range vap.Spec.MatchConstraints.ResourceRules {
		// A collection delete carries no name: resourceNames would not
		// match it (QA on #1959). The rule checks the object's name.
		assert.Empty(t, r.ResourceNames, "every delete is matched")
	}
	named := func(name string) map[string]interface{} {
		return asMap(t, v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: name}})
	}
	del := actorReq{resource: "agentindexes", user: "alice", old: named("kardinal-promoter")}
	assert.Contains(t, evalActor(t, vap, nil, del), "only kardinal writes the agent index kardinal-promoter")
	del.user = controllerUser
	assert.Empty(t, evalActor(t, vap, nil, del))
	other := actorReq{resource: "agentindexes", user: "alice", old: named("another-release")}
	assert.Empty(t, evalActor(t, vap, nil, other), "another release's index is its own release's concern")
}

// TestActorAdmission_FreezeLift: who lifts a freeze (a new
// spec.freeze.lifted) is stamped and checked by <release>-actor like who
// froze: an agent's lift must name its registered agent and sponsor. A lift
// already recorded is not checked again: a person adding an exemption to a
// freeze an agent lifted is admitted on their own stamp (QA on #1959: the
// lifted clause of freezeStamps had no test).
func TestActorAdmission_FreezeLift(t *testing.T) {
	t.Parallel()
	vap := identityPolicy(t, "actor")
	ready := *actorRegistry(t)
	ready.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Built"}}
	params := asMap(t, v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}, Status: ready})
	fixerSA := "system:serviceaccount:ci:fixer"
	fixer := &v1alpha1.Actor{Kind: "agent", Principal: fixerSA, Agent: "fixer", Sponsor: "alice"}
	freeze := func(lifted *v1alpha1.FreezeLift, exemptions ...v1alpha1.FreezeExemption) map[string]interface{} {
		return asMap(t, v1alpha1.ChangeWindow{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-freeze-agents"},
			Spec: v1alpha1.ChangeWindowSpec{Type: "blackout", Start: metav1.NewTime(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)),
				Reason: "INC-1", Freeze: &v1alpha1.ChangeWindowFreeze{Actors: v1alpha1.FreezeAgents, CreatedBy: "alice",
					Actor: &v1alpha1.Actor{Kind: "human", Principal: "alice"}, Lifted: lifted, Exemptions: exemptions}}})
	}
	forged := *fixer
	forged.Sponsor = "mallory"
	assert.Contains(t, evalActor(t, vap, params, actorReq{resource: "changewindows", user: fixerSA,
		old: freeze(nil), object: freeze(&v1alpha1.FreezeLift{By: fixerSA, Actor: &forged})}),
		"actor must be {kind: agent", "an agent's lift with a forged sponsor")
	assert.Empty(t, evalActor(t, vap, params, actorReq{resource: "changewindows", user: fixerSA,
		old: freeze(nil), object: freeze(&v1alpha1.FreezeLift{By: fixerSA, Actor: fixer})}), "an agent's own lift")

	lifted := &v1alpha1.FreezeLift{By: fixerSA, Actor: fixer}
	ex := v1alpha1.FreezeExemption{Namespace: "payments-eu", Pipeline: "checkout", Reason: "hotfix", CreatedBy: "bob",
		Actor: &v1alpha1.Actor{Kind: "human", Principal: "bob"}, ExpiresAt: metav1.NewTime(time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))}
	assert.Empty(t, evalActor(t, vap, params, actorReq{resource: "changewindows", user: "bob",
		old: freeze(lifted), object: freeze(lifted, ex)}), "the recorded lift is not checked against bob")
}

// olderActorPolicies are testdata/actor-policies-v0.12.yaml's: the actor
// policies of v0.12.0 to v0.13, by suffix.
func olderActorPolicies(t *testing.T, release string) map[string]*admissionregistrationv1.ValidatingAdmissionPolicy {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "actor-policies-"+release+".yaml"))
	require.NoError(t, err)
	out := map[string]*admissionregistrationv1.ValidatingAdmissionPolicy{}
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	for {
		var vap admissionregistrationv1.ValidatingAdmissionPolicy
		if err := dec.Decode(&vap); err != nil {
			break
		}
		out[strings.TrimPrefix(vap.Name, "kardinal-promoter-")] = &vap
	}
	require.Len(t, out, 3)
	return out
}

// pipelineRecord is a Pipeline write that adds spec.lastChange with stamp,
// as kardinal pause does, for <release>-actor-records.
func pipelineRecord(stamp *v1alpha1.Actor) (object, old map[string]interface{}) {
	change := map[string]interface{}{"reason": "r"}
	if stamp != nil {
		change["actor"] = map[string]interface{}{"kind": stamp.Kind, "principal": stamp.Principal,
			"agent": stamp.Agent, "sponsor": stamp.Sponsor}
		if stamp.Agent == "" {
			delete(change["actor"].(map[string]interface{}), "agent")
			delete(change["actor"].(map[string]interface{}), "sponsor")
		}
	}
	return map[string]interface{}{"metadata": map[string]interface{}{"labels": map[string]interface{}{"kardinal.io/actor-check": "true"}},
			"spec": map[string]interface{}{"lastChange": change}},
		map[string]interface{}{"spec": map[string]interface{}{}}
}

// actorNameLimits is the maxLength of the agent and of the sponsor of every
// actor stamp in the CRDs (config/crd/bases): every object property named
// actor (spec.actor, a hold's, a lastChange's, a rejection's, an
// override's...) that has agent and sponsor.
func actorNameLimits(t *testing.T) map[string]int64 {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(repoRoot(t), "config", "crd", "bases", "*.yaml"))
	require.NoError(t, err)
	out := map[string]int64{}
	var walk func(path string, n map[string]interface{})
	walk = func(path string, n map[string]interface{}) {
		props, _ := n["properties"].(map[string]interface{})
		for name, v := range props {
			child, _ := v.(map[string]interface{})
			if name == "actor" {
				if cp, ok := child["properties"].(map[string]interface{}); ok && cp["agent"] != nil && cp["sponsor"] != nil {
					for _, f := range []string{"agent", "sponsor"} {
						switch ml := cp[f].(map[string]interface{})["maxLength"].(type) {
						case int64:
							out[path+".actor."+f] = ml
						case float64:
							out[path+".actor."+f] = int64(ml)
						default:
							t.Fatalf("%s.actor.%s has no maxLength (%T)", path, f, ml)
						}
					}
				}
			}
			walk(path+"."+name, child)
		}
		if items, ok := n["items"].(map[string]interface{}); ok {
			walk(path+"[]", items)
		}
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		require.NoError(t, err)
		var crd map[string]interface{}
		require.NoError(t, utilyaml.Unmarshal(raw, &crd))
		spec, _ := crd["spec"].(map[string]interface{})
		names, _ := spec["names"].(map[string]interface{})
		versions, _ := spec["versions"].([]interface{})
		for _, v := range versions {
			schema := v.(map[string]interface{})["schema"].(map[string]interface{})["openAPIV3Schema"].(map[string]interface{})
			walk(fmt.Sprint(names["kind"]), schema)
		}
	}
	return out
}

// TestLegacyReaderGuardCannotBeStamped (QA-3 on #2372): v0.12's
// <release>-actor-records, which judges a stamp against the index without
// checking Ready, finds actor.LegacyReaderGuard for every requester of a
// compact index and admits a stamp naming its agent and sponsor. Both are
// longer than any actor stamp in any CRD may name, so the API server's
// schema refuses such a write before admission; a helm rollback leaves the
// CRDs in place.
// Covers ACTOR-INDEX-01.
func TestLegacyReaderGuardCannotBeStamped(t *testing.T) {
	t.Parallel()
	limits := actorNameLimits(t)
	require.GreaterOrEqual(t, len(limits), 10, "actor stamps found in the CRDs: %v", limits)
	for path, max := range limits {
		assert.LessOrEqual(t, max, int64(actor.MaxActorNameLength), path)
		if strings.HasSuffix(path, ".agent") {
			assert.Greater(t, int64(len(actor.LegacyReaderGuard.Agent)), max, "%s: the guard's agent cannot be stamped", path)
		} else {
			assert.Greater(t, int64(len(actor.LegacyReaderGuard.Sponsor)), max, "%s: the guard's sponsor cannot be stamped", path)
		}
	}
}

// TestOlderActorPoliciesFailClosed (#2071): the actor policies of an older
// release (v0.12, and v0.13 with #2384), against an index this release wrote
// in the compact format (left in place by a helm rollback, or a controller
// upgraded before its chart), refuse every write they check, people's and
// agents', and never admit an agent as a person.
//
//   - v0.12: <release>-actor and -actor-overrides see Ready False;
//     -actor-records, which does not check Ready, finds
//     actor.LegacyReaderGuard for every authenticated requester, so it
//     admits only a stamp naming the guard (QA-3's forged pauses), which the
//     schema refuses (TestLegacyReaderGuardCannotBeStamped).
//   - v0.13: every policy refuses a compact index ("in the compact format
//     of a newer release"), the forged guard stamps included.
//
// The controller is exempt as before, and a write with no stamp to check is
// admitted.
// Covers ACTOR-INDEX-01, ACTOR-INDEX-05.
func TestOlderActorPoliciesFailClosed(t *testing.T) {
	t.Parallel()
	params := asMap(t, v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}, Status: *actorRegistryCompact(t)})
	authn := []string{"system:authenticated"}
	fixerSA := "system:serviceaccount:ci:fixer"
	fixer := &v1alpha1.Actor{Kind: "agent", Principal: fixerSA, Agent: "fixer", Sponsor: "alice"}
	guard := func(p string) *v1alpha1.Actor {
		return &v1alpha1.Actor{Kind: "agent", Principal: p, Agent: actor.LegacyReaderGuard.Agent, Sponsor: actor.LegacyReaderGuard.Sponsor}
	}
	reviewer := "oidc:reviewer" // an OIDC user registered as an agent
	record := func(user string, stamp *v1alpha1.Actor) actorReq {
		o, old := pipelineRecord(stamp)
		return actorReq{resource: "pipelines", namespace: "payments-eu", user: user, groups: authn, object: o, old: old}
	}
	const (
		notReady = "agent index is not ready"
		newer    = "in the compact format of a newer release"
		// schema: the policy cannot tell, the CRD schema refuses the stamp.
		schema = "schema"
	)
	for _, release := range []string{"v0.12", "v0.13"} {
		older := olderActorPolicies(t, release)
		v12 := release == "v0.12"
		pick := func(at12, at13 string) string {
			if v12 {
				return at12
			}
			return at13
		}
		for _, tc := range []struct {
			name, policy string
			req          actorReq
			deny         string
		}{
			{"a person's version", "actor", actorReq{resource: "bundles", namespace: "x", user: "alice", groups: authn,
				object: bundleObj(t, "x", "checkout", nil)}, pick(notReady, newer)},
			{"an agent's stamped version", "actor", actorReq{resource: "bundles", namespace: "payments-eu", user: fixerSA,
				groups: authn, object: bundleObj(t, "payments-eu", "checkout", fixer)}, pick(notReady, newer)},
			{"an OIDC agent stamps itself human", "actor", actorReq{resource: "bundles", namespace: "x", user: reviewer, groups: authn,
				object: bundleObj(t, "x", "checkout", &v1alpha1.Actor{Kind: "human", Principal: reviewer})}, pick(notReady, newer)},
			{"a person's version stamped with the guard", "actor", actorReq{resource: "bundles", namespace: "x", user: "alice",
				groups: authn, object: bundleObj(t, "x", "checkout", guard("alice"))}, pick(notReady, newer)},
			{"an agent's override", "actor-overrides", actorReq{resource: "policygates", namespace: "payments-eu", user: fixerSA, groups: authn,
				old: gateObj(t, "payments-eu", "uat"), object: gateObj(t, "payments-eu", "uat", v1alpha1.PolicyGateOverride{Reason: "r", Stage: "uat",
					ExpiresAt: metav1.NewTime(time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)), CreatedBy: fixerSA, Actor: fixer})}, pick(notReady, newer)},
			{"a person's stamped pause", "actor-records", record("alice", &v1alpha1.Actor{Kind: "human", Principal: "alice"}),
				pick("agent: "+actor.LegacyReaderGuard.Agent, newer)},
			{"an OIDC agent's pause stamped human", "actor-records", record(reviewer, &v1alpha1.Actor{Kind: "human", Principal: reviewer}),
				pick("agent: "+actor.LegacyReaderGuard.Agent, newer)},
			{"an agent's pause stamped as itself", "actor-records", record(fixerSA, fixer),
				pick("agent: "+actor.LegacyReaderGuard.Agent, newer)},
			{"QA-3: a person's pause stamped with the guard", "actor-records", record("alice", guard("alice")), pick(schema, newer)},
			{"QA-3: an agent's pause stamped with the guard", "actor-records", record(fixerSA, guard(fixerSA)), pick(schema, newer)},
			{"a pause with no stamp", "actor-records", record("alice", nil), ""},
			{"the controller", "actor", actorReq{resource: "bundles", namespace: "x", user: controllerUser, groups: authn,
				object: bundleObj(t, "x", "checkout", &v1alpha1.Actor{Kind: "human", Principal: "alice", Via: "ui"})}, ""},
		} {
			t.Run(release+"/"+tc.name, func(t *testing.T) {
				got := evalActor(t, older[tc.policy], params, tc.req)
				switch tc.deny {
				case "":
					assert.Empty(t, got)
				case schema:
					assert.Empty(t, got, "%s's -actor-records cannot tell the guard's stamp from an agent's", release)
					assert.Greater(t, len(actor.LegacyReaderGuard.Agent), actor.MaxActorNameLength, "the schema refuses it")
					assert.Greater(t, len(actor.LegacyReaderGuard.Sponsor), actor.MaxActorNameLength, "the schema refuses it")
				default:
					assert.Contains(t, got, tc.deny)
				}
			})
		}
	}
}

// TestActorRecordsAdmission: <release>-actor-records reads either format
// (#2071) and agrees with pkg/actor on who the requester is.
// Covers ACTOR-INDEX-01.
func TestActorRecordsAdmission(t *testing.T) {
	t.Parallel()
	vap := identityPolicy(t, "actor-records")
	legacy := *actorRegistry(t)
	legacy.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Built"}}
	fixerSA := "system:serviceaccount:ci:fixer"
	fixer := &v1alpha1.Actor{Kind: "agent", Principal: fixerSA, Agent: "fixer", Sponsor: "alice"}
	authn := []string{"system:authenticated"}
	for _, st := range []*v1alpha1.AgentIndexStatus{&legacy, actorRegistryCompact(t)} {
		params := asMap(t, v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}, Status: *st})
		for _, tc := range []struct {
			name, user string
			stamp      *v1alpha1.Actor
			deny       string
		}{
			{"a person", "alice", &v1alpha1.Actor{Kind: "human", Principal: "alice"}, ""},
			{"an agent as itself", fixerSA, fixer, ""},
			{"an agent stamps itself human", fixerSA, &v1alpha1.Actor{Kind: "human", Principal: fixerSA}, "actor must be {kind: agent"},
			{"an OIDC agent stamps itself human", "oidc:reviewer", &v1alpha1.Actor{Kind: "human", Principal: "oidc:reviewer"}, "actor must be {kind: agent"},
		} {
			t.Run(fmt.Sprintf("%v/%s", actor.IsCompact(st), tc.name), func(t *testing.T) {
				o, old := pipelineRecord(tc.stamp)
				got := evalActor(t, vap, params, actorReq{resource: "pipelines", namespace: "payments-eu", user: tc.user,
					groups: authn, object: o, old: old})
				if tc.deny == "" {
					assert.Empty(t, got)
				} else {
					assert.Contains(t, got, tc.deny)
				}
			})
		}
	}
}
