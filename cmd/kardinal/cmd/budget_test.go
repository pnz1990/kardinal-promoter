// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

var budgetT0 = time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)

// budgetedAgent is fixer with versionsPerDay 5 on prod, its ledger
// kardinal-promoter counting used of them, and the given exemptions.
func budgetedAgent(used int32, exemptions ...v1alpha1.AgentBudgetExemption) *v1alpha1.AgentIdentity {
	frees := metav1.NewTime(budgetT0.Add(4 * time.Hour))
	at := metav1.NewTime(budgetT0)
	id := &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: "fixer", Generation: 1},
		Spec: v1alpha1.AgentIdentitySpec{Subjects: []v1alpha1.AgentSubject{{Kind: "ServiceAccount", Namespace: "ci", Name: "fixer"}},
			Sponsor:          v1alpha1.AgentSponsor{User: "alice"},
			Budgets:          []v1alpha1.AgentBudgetRule{{Name: "prod", Environments: []string{"prod"}, VersionsPerDay: 5, MaxConcurrent: 2}},
			BudgetExemptions: exemptions},
		Status: v1alpha1.AgentIdentityStatus{Usage: map[string]v1alpha1.AgentUsage{"kardinal-promoter": {ObservedGeneration: 1,
			EvaluatedAt: &at,
			Rules: []v1alpha1.AgentBudgetRuleUsage{{Name: "prod", Waiting: 1,
				Versions:   &v1alpha1.AgentBudgetCount{Limit: 5, Used: used, FreesAt: &frees},
				Concurrent: &v1alpha1.AgentBudgetCount{Limit: 2, Used: 1}}}}}}}
	for _, e := range exemptions {
		u := id.Status.Usage["kardinal-promoter"]
		u.Exemptions = append(u.Exemptions, v1alpha1.AgentBudgetExemptionState{Name: e.Name, FirstSeenAt: at, ExpiresAt: e.ExpiresAt})
		id.Status.Usage["kardinal-promoter"] = u
	}
	return id
}

// waitingStep is version bundle's prod step, waiting for fixer's budget.
func waitingStep(name, bundle string) *v1alpha1.PromotionStep {
	since := metav1.NewTime(budgetT0)
	return &v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "payments",
		Labels: map[string]string{"kardinal.io/pipeline": "checkout"}},
		Spec: v1alpha1.PromotionStepSpec{PipelineName: "checkout", BundleName: bundle, Environment: "prod", StepType: "kustomize-set-image"},
		Status: v1alpha1.PromotionStepStatus{Message: "waiting for agent fixer's budget (rule prod: versionsPerDay); kardinal budget fixer",
			Budget: &v1alpha1.PromotionStepBudget{Agent: "fixer", State: "Waiting", Since: &since, Rules: []string{"prod"}}}}
}

// cancelledStep is version bundle's prod step that waited for fixer's
// budget until its version was superseded: it failed before it started and
// kept its last status.budget.
func cancelledStep(name, bundle string) *v1alpha1.PromotionStep {
	s := waitingStep(name, bundle)
	s.Status.State, s.Status.Message = "Failed", "bundle "+bundle+" was superseded before this step started"
	return s
}

// TestBudgetCommand: kardinal budget shows each rule's counts, windows and
// when room frees, the versions that wait, and the exemptions; -o json
// prints status.usage; an agent without a ledger says why
// (docs/design/24-agent-budgets.md section 11.2).
// Covers BUDGET-CLI-01.
func TestBudgetCommand(t *testing.T) {
	ex := v1alpha1.AgentBudgetExemption{Name: "inc-1", Namespace: "payments", Pipeline: "checkout", Environment: "prod",
		Bundle: "checkout-v6", Reason: "hotfix", CreatedBy: "bob", ExpiresAt: metav1.NewTime(budgetT0.Add(time.Hour))}
	c := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).
		WithObjects(budgetedAgent(5, ex), waitingStep("checkout-v7-prod", "checkout-v7"),
			cancelledStep("checkout-v5-prod", "checkout-v5")).Build()
	var out bytes.Buffer
	require.NoError(t, budgetFn(context.Background(), &out, c, "fixer", "", budgetT0))
	got := squeeze(out.String()) // the table's column widths aside
	assert.NotContains(t, got, "checkout-v5", "a step that failed unstarted (its version superseded) no longer waits")
	for _, want := range []string{
		"Agent fixer (sponsor alice): 1 budget rule(s).",
		"Ledger kardinal-promoter (evaluated 2026-10-15T10:00:00Z; current)",
		"prod versionsPerDay 5/5 24h 2026-10-15T14:00:00Z 1 exhausted",
		"prod maxConcurrent 1/2 - - 1",
		"inc-1 payments/checkout prod checkout-v6, by bob until 2026-10-15T11:00:00Z in effect",
		"payments/checkout prod checkout-v7 since 2026-10-15T10:00:00Z rules prod",
	} {
		assert.Contains(t, got, want)
	}

	out.Reset()
	require.NoError(t, budgetFn(context.Background(), &out, c, "fixer", "", budgetT0.Add(2*time.Hour)))
	assert.Contains(t, squeeze(out.String()), "until 2026-10-15T11:00:00Z expired")

	globalOutput = "json"
	t.Cleanup(func() { globalOutput = "" })
	out.Reset()
	require.NoError(t, budgetFn(context.Background(), &out, c, "fixer", "", budgetT0))
	var usage map[string]v1alpha1.AgentUsage
	require.NoError(t, json.Unmarshal(out.Bytes(), &usage))
	assert.Equal(t, int32(5), usage["kardinal-promoter"].Rules[0].Versions.Used)
	globalOutput = ""

	none := budgetedAgent(0)
	none.Status.Usage = nil
	c = fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithObjects(none).Build()
	out.Reset()
	require.NoError(t, budgetFn(context.Background(), &out, c, "fixer", "", budgetT0))
	assert.Contains(t, out.String(), "No ledger: no installation counts it yet")
	assert.Error(t, budgetFn(context.Background(), &out, c, "nobody", "", budgetT0))
}

// TestOverrideBudget: override --budget adds a stamped exemption, as the
// caller, with the label the admission policy wants, to the agent whose
// version waits there (or the named version's); an agent caller is refused;
// a pipeline with no waiting version, or several agents', asks for
// --bundle (section 11.3).
// Covers BUDGET-CLI-01.
func TestOverrideBudget(t *testing.T) {
	StubIdentity(t, "bob")
	agentBundle := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "checkout-v7", Namespace: "payments"},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "checkout",
			Actor: &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "system:serviceaccount:ci:fixer", Agent: "fixer", Sponsor: "alice"}}}
	c := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).
		WithObjects(budgetedAgent(5), waitingStep("checkout-v7-prod", "checkout-v7"), agentBundle).Build()
	var out bytes.Buffer
	require.NoError(t, overrideBudgetFn(&out, c, "payments", "checkout", "prod", "", "hotfix", 30*time.Minute, budgetT0))
	assert.Contains(t, out.String(), "added to agent fixer's budget: checkout in prod goes on until 2026-10-15T10:30:00Z")
	assert.Contains(t, out.String(), "Created by: bob")
	var id v1alpha1.AgentIdentity
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "fixer"}, &id))
	require.Len(t, id.Spec.BudgetExemptions, 1)
	e := id.Spec.BudgetExemptions[0]
	assert.Equal(t, "bob", e.CreatedBy)
	assert.Equal(t, "prod", e.Environment)
	require.NotNil(t, e.Actor, "stamped, as override --freeze")
	assert.Equal(t, "bob", e.Actor.Principal)
	assert.Equal(t, "true", id.Labels[lifecycle.LabelBudgetExemptions])

	// A named version, and an expired exemption cleaned up on the way.
	out.Reset()
	require.NoError(t, overrideBudgetFn(&out, c, "payments", "checkout", "prod", "checkout-v7", "again", time.Hour,
		budgetT0.Add(2*time.Hour)))
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "fixer"}, &id))
	require.Len(t, id.Spec.BudgetExemptions, 1, "the expired one goes")
	assert.Equal(t, "checkout-v7", id.Spec.BudgetExemptions[0].Bundle)

	// Nothing waits there.
	assert.ErrorContains(t, overrideBudgetFn(&out, c, "payments", "checkout", "uat", "", "x", 0, budgetT0),
		"no version of checkout waits for an agent's budget in uat")
	// Only a superseded version's step, failed before it started, did.
	gone := cancelledStep("checkout-v5-staging", "checkout-v5")
	gone.Spec.Environment = "staging"
	require.NoError(t, c.Create(context.Background(), gone))
	assert.ErrorContains(t, overrideBudgetFn(&out, c, "payments", "checkout", "staging", "", "x", 0, budgetT0),
		"no version of checkout waits for an agent's budget in staging")
	// A person's version.
	person := agentBundle.DeepCopy()
	person.Name, person.ResourceVersion = "checkout-v8", ""
	person.Spec.Actor = &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "carol"}
	require.NoError(t, c.Create(context.Background(), person))
	assert.ErrorContains(t, overrideBudgetFn(&out, c, "payments", "checkout", "prod", "checkout-v8", "x", 0, budgetT0),
		"not made by an agent")
}

// TestWhyBudget: why says an agent's budget holds the environment, with the
// step's message, and what moves it on.
// Covers BUDGET-CLI-01.
func TestWhyBudget(t *testing.T) {
	p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "payments"}}
	e := v1alpha1.PipelineEnvironmentStatus{Name: "prod", State: "Pending", Bundle: "checkout-v6", Version: "1.6",
		IncomingBundle: "checkout-v7", IncomingVersion: "1.7", BudgetHeldBy: "fixer"}
	a := whyEnvironment(p, e, []v1alpha1.PipelineEnvironmentStatus{e}, nil,
		[]v1alpha1.PromotionStep{*waitingStep("checkout-v7-prod", "checkout-v7")}, map[string]*v1alpha1.Bundle{}, nil, nil, budgetT0)
	assert.Equal(t, "prod waits for agent fixer's budget.", a.sentence)
	lines := map[string]string{}
	for _, l := range a.lines {
		lines[l.label] = l.value
	}
	assert.Contains(t, lines["Why"], "rule prod: versionsPerDay")
	assert.Equal(t, "kardinal budget fixer", lines["Next"])
	assert.Contains(t, lines["Or"], "kardinal override checkout prod --budget --reason <why>")
	assert.Equal(t, "Held by agent fixer's budget", envState(e).Label)
}

// TestDoctorAgentBudgets: doctor says when budgets are kept but not
// enforced, names a ledger no writer evaluates (a shard naming a release
// that does not run its reconciler) and one behind its spec, and passes
// when every budgeted agent is counted (sections 15.2, 17.4).
// Covers BUDGET-CLI-01.
func TestDoctorAgentBudgets(t *testing.T) {
	deploy := func(args ...string) *appsv1.Deployment {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter", Namespace: "kardinal-system",
			Labels: map[string]string{"app.kubernetes.io/name": "kardinal-promoter"}},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
				{Name: "manager", Args: append([]string{"--agent-index=kardinal-promoter"}, args...)}}}}}}
	}
	behind := budgetedAgent(1)
	behind.Generation = 2
	for _, tc := range []struct {
		name   string
		objs   []sigs_client.Object
		shown  bool
		failed bool
		warned bool
		detail string
	}{
		{name: "no budgets", objs: []sigs_client.Object{deploy()}},
		{name: "budgets off", objs: []sigs_client.Object{deploy(), budgetedAgent(1)}, shown: true, warned: true,
			detail: "budgets are not enforced in this release (actors.budgets.enabled is false); kept, not enforced: fixer"},
		{name: "counted", objs: []sigs_client.Object{deploy("--agent-budgets=true"), budgetedAgent(1)}, shown: true,
			detail: "1 agent(s) with budgets, counted in ledger kardinal-promoter"},
		{name: "a shard's ledger nobody writes", objs: []sigs_client.Object{deploy("--agent-budgets=true", "--agent-budget-ledger=kardinal-main"),
			budgetedAgent(1)}, shown: true, failed: true, detail: "ledger kardinal-main has no entry for fixer: no controller evaluates it"},
		{name: "behind its spec", objs: []sigs_client.Object{deploy("--agent-budgets=true"), behind}, shown: true, warned: true,
			detail: "ledger kardinal-promoter is behind the spec of fixer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(tc.objs...).Build()
			r, shown := checkAgentBudgets(context.Background(), c, "kardinal-system")
			assert.Equal(t, tc.shown, shown)
			if !shown {
				return
			}
			assert.Equal(t, tc.failed, r.failed, r.detail)
			assert.Equal(t, tc.warned, r.warned, r.detail)
			assert.Contains(t, r.detail, tc.detail)
		})
	}
}

// squeeze is s with every run of spaces and tabs one space, line by line.
func squeeze(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.Join(strings.Fields(l), " ")
	}
	return strings.Join(lines, "\n")
}
