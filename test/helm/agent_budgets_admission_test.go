// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"reflect"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// budgetAdmits evaluates <release>-agent-budgets as the API server would,
// with the authorizer's answers (mayManage, mayExempt) given: cel-go has no
// authorizer, so the test decides who holds which verb and the envtest
// (TestAgentBudgetPoliciesOnAPIServer) checks the real one.
func budgetAdmits(t *testing.T, vap *admissionregistrationv1.ValidatingAdmissionPolicy, object, oldObject map[string]interface{},
	user string, groups []string, manage, exempt bool) bool {
	t.Helper()
	env, err := cel.NewEnv(cel.Variable("object", cel.DynType), cel.Variable("oldObject", cel.DynType),
		cel.Variable("request", cel.DynType), cel.Variable("variables", cel.DynType), ext.Strings())
	require.NoError(t, err)
	gs := make([]interface{}, len(groups))
	for i, g := range groups {
		gs[i] = g
	}
	variables := map[string]interface{}{"mayManage": manage, "mayExempt": exempt}
	vars := map[string]interface{}{"object": object, "oldObject": nil, "variables": variables,
		"request": map[string]interface{}{"userInfo": map[string]interface{}{"username": user, "groups": gs},
			"operation": operation(object, oldObject)}}
	if oldObject != nil {
		vars["oldObject"] = oldObject
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
		if _, given := variables[v.Name]; given {
			assert.Contains(t, v.Expression, "authorizer.group('kardinal.io').resource('agentidentities')", v.Name)
			continue
		}
		variables[v.Name] = eval(v.Expression)
	}
	ok := true
	for _, v := range vap.Spec.Validations {
		if eval(v.Expression) != true {
			ok = false
		}
		if v.MessageExpression != "" {
			ast, iss := env.Compile(v.MessageExpression)
			require.NoError(t, iss.Err(), v.MessageExpression)
			assert.Equal(t, cel.StringType, ast.OutputType())
		}
	}
	return ok
}

// TestAgentBudgetsAdmission: who changes an AgentIdentity's budgets and who
// lets an agent through them (docs/design/24-agent-budgets.md section 12):
// an edit beyond the exemptions needs manage; adding or removing an
// exemption needs exempt; a new exemption names its maker (createdBy), is
// stamped with them, and its maker is not of the agent's party; an existing
// one never changes; the controller is exempt.
// Covers BUDGET-ADMISSION-01.
func TestAgentBudgetsAdmission(t *testing.T) {
	t.Parallel()
	vap := identityPolicy(t, "agent-budgets")
	rules := vap.Spec.MatchConstraints.ResourceRules
	require.Len(t, rules, 1)
	assert.Equal(t, []string{"agentidentities"}, rules[0].Resources, "the main resource only: status writes are not matched")
	assert.ElementsMatch(t, []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update},
		rules[0].Operations)

	controller := "system:serviceaccount:" + releaseNS + ":kardinal-promoter"
	human := func(user string) map[string]interface{} {
		return map[string]interface{}{"kind": "human", "principal": user}
	}
	exemption := func(name, by string, actor map[string]interface{}) map[string]interface{} {
		e := map[string]interface{}{"name": name, "namespace": "payments", "pipeline": "checkout", "environment": "prod",
			"reason": "hotfix", "expiresAt": "2026-10-15T12:00:00Z", "createdBy": by}
		if actor != nil {
			e["actor"] = actor
		}
		return e
	}
	identity := func(budgets int, exemptions ...map[string]interface{}) map[string]interface{} {
		spec := map[string]interface{}{
			"subjects": []interface{}{
				map[string]interface{}{"kind": "ServiceAccount", "__namespace__": "ci", "name": "fixer"},
				map[string]interface{}{"kind": "User", "name": "oidc:fixer"},
				map[string]interface{}{"kind": "Group", "name": "agents-fixer"}},
			"sponsor":  map[string]interface{}{"user": "alice"},
			"scope":    map[string]interface{}{"namespaces": []interface{}{"*"}},
			"autonomy": "auto",
			"budgets": []interface{}{map[string]interface{}{"name": "prod", "environments": []interface{}{"prod"},
				"versionsPerDay": int64(budgets)}},
		}
		md := map[string]interface{}{"name": "fixer"}
		if len(exemptions) > 0 {
			list := make([]interface{}, len(exemptions))
			for i, e := range exemptions {
				list[i] = e
			}
			spec["budgetExemptions"] = list
			md["labels"] = map[string]interface{}{"kardinal.io/budget-exemptions": "true"}
		}
		return map[string]interface{}{"metadata": md, "spec": spec}
	}
	unlabelled := func(o map[string]interface{}) map[string]interface{} {
		delete(o["metadata"].(map[string]interface{}), "labels")
		return o
	}
	labelled := func(o map[string]interface{}) map[string]interface{} {
		o["metadata"].(map[string]interface{})["labels"] = map[string]interface{}{"kardinal.io/budget-exemptions": "true"}
		return o
	}
	bobs := exemption("inc-1", "bob", human("bob"))
	for _, tc := range []struct {
		name           string
		old, cur       map[string]interface{}
		user           string
		groups         []string
		manage, exempt bool
		want           bool
	}{
		{name: "an admin raises the budget", old: identity(5), cur: identity(10), user: "root", manage: true, want: true},
		{name: "an exempter cannot raise the budget", old: identity(5), cur: identity(10), user: "bob", exempt: true},
		{name: "an exempter adds an exemption", old: identity(5), cur: identity(5, bobs), user: "bob", exempt: true, want: true},
		{name: "without the exempt verb", old: identity(5), cur: identity(5, bobs), user: "bob"},
		{name: "a manager without exempt cannot add one", old: identity(5), cur: identity(5, bobs), user: "bob", manage: true},
		{name: "an exempter cannot add one and raise the budget", old: identity(5), cur: identity(10, bobs), user: "bob", exempt: true},
		{name: "a forged createdBy", old: identity(5), cur: identity(5, exemption("inc-1", "carol", human("bob"))), user: "bob", exempt: true},
		{name: "no actor stamp", old: identity(5), cur: identity(5, exemption("inc-1", "bob", nil)), user: "bob", exempt: true},
		{name: "a stamp naming someone else", old: identity(5), cur: identity(5, exemption("inc-1", "bob", human("carol"))), user: "bob", exempt: true},
		{name: "a stamp with via", old: identity(5), cur: identity(5, exemption("inc-1", "bob",
			map[string]interface{}{"kind": "human", "principal": "bob", "via": "ui"})), user: "bob", exempt: true},
		{name: "a person's stamp on a ServiceAccount", old: identity(5), cur: identity(5, exemption("inc-1", "system:serviceaccount:ops:ic",
			human("system:serviceaccount:ops:ic"))), user: "system:serviceaccount:ops:ic", exempt: true},
		{name: "the sponsor", old: identity(5), cur: identity(5, exemption("inc-1", "alice", human("alice"))), user: "alice", exempt: true},
		{name: "a User subject", old: identity(5), cur: identity(5, exemption("inc-1", "oidc:fixer", human("oidc:fixer"))),
			user: "oidc:fixer", exempt: true},
		{name: "the ServiceAccount subject", old: identity(5), cur: identity(5, exemption("inc-1", "system:serviceaccount:ci:fixer",
			map[string]interface{}{"kind": "agent", "principal": "system:serviceaccount:ci:fixer"})), user: "system:serviceaccount:ci:fixer", exempt: true},
		{name: "a member of a Group subject", old: identity(5), cur: identity(5, exemption("inc-1", "dave", human("dave"))),
			user: "dave", groups: []string{"agents-fixer"}, exempt: true},
		{name: "change an existing exemption", old: identity(5, bobs),
			cur: identity(5, map[string]interface{}{"name": "inc-1", "namespace": "payments", "pipeline": "checkout", "environment": "prod",
				"reason": "longer", "expiresAt": "2027-01-01T00:00:00Z", "createdBy": "bob", "actor": human("bob")}),
			user: "root", manage: true, exempt: true},
		{name: "remove an exemption", old: identity(5, bobs), cur: identity(5), user: "carol", exempt: true, want: true},
		{name: "remove one without the exempt verb", old: identity(5, bobs), cur: identity(5), user: "carol"},
		{name: "a second exemption keeps the first", old: identity(5, bobs),
			cur: identity(5, bobs, exemption("inc-2", "carol", human("carol"))), user: "carol", exempt: true, want: true},
		{name: "an admin creates an identity with an exemption", cur: identity(5, exemption("inc-1", "root", human("root"))), user: "root",
			want: true},
		{name: "a create with a forged exemption", cur: identity(5, exemption("inc-1", "bob", human("bob"))), user: "root"},
		{name: "the controller writes for a UI caller", old: identity(5), cur: identity(5, bobs), user: controller, want: true},
		{name: "an edit that changes nothing", old: identity(5, bobs), cur: identity(5, bobs), user: "bob", want: true},
		{name: "exemptions without the label", old: identity(5), cur: unlabelled(identity(5, bobs)), user: "bob", exempt: true},
		{name: "the label without exemptions", old: identity(5), cur: labelled(identity(5)), user: "root", manage: true},
		{name: "the controller keeps the label too", old: identity(5), cur: unlabelled(identity(5, bobs)), user: controller},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, budgetAdmits(t, vap, tc.cur, tc.old, tc.user, append(tc.groups, "system:authenticated"),
				tc.manage, tc.exempt))
		})
	}
}

// TestAgentBudgetsPolicyNamesEverySpecField: the policy's list of spec
// fields a manage check guards is AgentIdentitySpec's, budgetExemptions
// aside, so a new spec field cannot slip past it.
// Covers BUDGET-ADMISSION-01.
func TestAgentBudgetsPolicyNamesEverySpecField(t *testing.T) {
	t.Parallel()
	vap := identityPolicy(t, "agent-budgets")
	var unchanged string
	for _, v := range vap.Spec.Variables {
		if v.Name == "otherSpecUnchanged" {
			unchanged = v.Expression
		}
	}
	require.NotEmpty(t, unchanged)
	typ := reflect.TypeOf(v1alpha1.AgentIdentitySpec{})
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name == "budgetExemptions" {
			assert.NotContains(t, unchanged, "object.spec.budgetExemptions")
			continue
		}
		assert.Contains(t, unchanged, "object.spec."+name+" == oldObject.spec."+name, "spec.%s is not guarded by manage", name)
	}
}

// TestAgentBudgetsChart: off by default (no flags, the warn-only policy);
// actors.budgets.enabled passes --agent-budgets, the ledger and the stamp
// policy, and drops the warning; the exempter role, the admin's verbs and
// the controller's read of the stamp policy (section 17.4).
// Covers BUDGET-ADMISSION-01.
func TestAgentBudgetsChart(t *testing.T) {
	t.Parallel()
	args := func(docs []map[string]interface{}) []string {
		for _, d := range docs {
			if d["kind"] == "Deployment" {
				for _, c := range dig(d, "spec", "template", "spec", "containers").([]interface{}) {
					if dig(c, "name") == "manager" || dig(c, "name") == "controller" {
						var out []string
						for _, a := range dig(c, "args").([]interface{}) {
							out = append(out, a.(string))
						}
						return out
					}
				}
			}
		}
		t.Fatal("no controller container")
		return nil
	}
	named := func(docs []map[string]interface{}, kind, name string) map[string]interface{} {
		for _, d := range docs {
			if d["kind"] == kind && dig(d, "metadata", "name") == name {
				return d
			}
		}
		return nil
	}
	off := renderChart(t, "kardinal-promoter")
	for _, a := range args(off) {
		assert.NotContains(t, a, "--agent-budget", "off by default")
	}
	warn := named(off, "ValidatingAdmissionPolicyBinding", "kardinal-promoter-agent-budgets-off")
	require.NotNil(t, warn, "budgets set while off get a warning")
	assert.Equal(t, []interface{}{"Warn"}, dig(warn, "spec", "validationActions"), "a warning, never a refusal")
	require.NotNil(t, named(off, "ValidatingAdmissionPolicy", "kardinal-promoter-agent-budgets"), "the exemption checks apply either way")

	on := renderChart(t, "kardinal-promoter", "--set", "actors.budgets.enabled=true")
	assert.Subset(t, args(on), []string{"--agent-budgets=true", "--agent-budget-ledger=kardinal-promoter",
		"--agent-budget-exemption-policies=kardinal-promoter-agent-budgets,kardinal-promoter-actor-budgets"})
	assert.Nil(t, named(on, "ValidatingAdmissionPolicy", "kardinal-promoter-agent-budgets-off"))
	shard := renderChart(t, "kardinal-payments", "--set", "actors.budgets.enabled=true", "--set", "actors.budgets.ledger=kardinal-promoter")
	assert.Contains(t, args(shard), "--agent-budget-ledger=kardinal-promoter", "a shard names the default shard's ledger")

	exempter := named(off, "ClusterRole", "kardinal-promoter-agent-budget-exempter")
	require.NotNil(t, exempter)
	assert.True(t, rulesGrant(exempter, "kardinal.io", "agentidentities", "exempt"))
	assert.True(t, rulesGrant(exempter, "kardinal.io", "agentidentities", "patch"))
	assert.False(t, rulesGrant(exempter, "kardinal.io", "agentidentities", "manage"), "an exempter edits nothing but exemptions")
	extra := named(off, "ClusterRole", "kardinal-promoter-admin-extra")
	assert.True(t, rulesGrant(extra, "kardinal.io", "agentidentities", "manage"))
	assert.True(t, rulesGrant(extra, "kardinal.io", "agentidentities", "exempt"))
	manager := named(off, "ClusterRole", "kardinal-promoter-manager-role")
	require.NotNil(t, manager)
	found := false
	for _, r := range dig(manager, "rules").([]interface{}) {
		names, _ := dig(r, "resourceNames").([]interface{})
		for _, n := range names {
			if n == "kardinal-promoter-agent-budgets" {
				found = true
				assert.Equal(t, []interface{}{"get"}, dig(r, "verbs"))
			}
		}
	}
	assert.True(t, found, "the controller reads the stamp policy and its binding by name")
}
