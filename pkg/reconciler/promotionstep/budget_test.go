// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

const budgetLedger = "kardinal-promoter"

var budgetNow = time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)

// budgeted is agent fixer with a maxConcurrent rule on test, and its ledger
// at generation gen (nil ledger: none yet).
func budgeted(ledger *v1alpha1.AgentUsage) *v1alpha1.AgentIdentity {
	id := &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: "fixer", Generation: 1},
		Spec: v1alpha1.AgentIdentitySpec{Subjects: []v1alpha1.AgentSubject{{Kind: "User", Name: "oidc:fixer"}},
			Sponsor: v1alpha1.AgentSponsor{User: "alice"},
			Budgets: []v1alpha1.AgentBudgetRule{{Name: "test", Environments: []string{"test"}, MaxConcurrent: 1}}}}
	if ledger != nil {
		id.Status.Usage = map[string]v1alpha1.AgentUsage{budgetLedger: *ledger}
	}
	return id
}

func ledgerAt(gen int64, grants ...v1alpha1.AgentBudgetGrant) *v1alpha1.AgentUsage {
	return &v1alpha1.AgentUsage{ObservedGeneration: gen, Grants: grants,
		Rules: []v1alpha1.AgentBudgetRuleUsage{{Name: "test", Exhausted: []string{"maxConcurrent"}}}}
}

func grantFor(env, exemption string) v1alpha1.AgentBudgetGrant {
	return v1alpha1.AgentBudgetGrant{BundleUID: "u-1", Environment: env, Rules: []string{"test"},
		GrantedAt: metav1.NewTime(budgetNow), Exemption: exemption}
}

type budgetCase struct {
	t   *testing.T
	c   client.WithWatch
	r   *promotionstep.Reconciler
	req ctrl.Request
	ctx context.Context
}

// newBudgetCase is step step-test of bundle-1 (UID u-1) into test, by
// author, with objs; budgets on unless off.
func newBudgetCase(t *testing.T, author *v1alpha1.Actor, off bool, objs ...client.Object) *budgetCase {
	t.Helper()
	step := makeStep("step-test", "nginx-demo", "bundle-1", "test")
	step.Labels = map[string]string{"kardinal.io/pipeline": "nginx-demo", "kardinal.io/bundle": "bundle-1",
		"kardinal.io/environment": "test"}
	bundle := makeBundle("bundle-1", "nginx-demo")
	bundle.UID = "u-1"
	bundle.Spec.Actor = author
	all := append([]client.Object{step, makePipeline("nginx-demo"), bundle}, objs...)
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}, &v1alpha1.AgentIdentity{}).
		WithObjects(all...).Build()
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
		NowFn:     func() time.Time { return budgetNow }}
	if !off {
		r.Budgets = &promotionstep.BudgetConfig{Ledger: budgetLedger}
	}
	return &budgetCase{t: t, c: c, r: r, ctx: context.Background(),
		req: ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-test", Namespace: "default"}}}
}

func (b *budgetCase) reconcile() ctrl.Result {
	b.t.Helper()
	res, err := b.r.Reconcile(b.ctx, b.req)
	require.NoError(b.t, err)
	return res
}

func (b *budgetCase) step() v1alpha1.PromotionStep {
	b.t.Helper()
	var s v1alpha1.PromotionStep
	require.NoError(b.t, b.c.Get(b.ctx, b.req.NamespacedName, &s))
	return s
}

func (b *budgetCase) audits(action string) int {
	b.t.Helper()
	var events v1alpha1.AuditEventList
	require.NoError(b.t, b.c.List(b.ctx, &events))
	n := 0
	for _, e := range events.Items {
		if e.Spec.Action == action {
			n++
		}
	}
	return n
}

var fixerActor = &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "oidc:fixer", Agent: "fixer", Sponsor: "alice"}

// TestBudget_Hold: holdForBudget passes people's versions, agents without
// budgets or without a matching rule, and budgets off; it holds an
// unregistered agent's step, and one whose ledger is missing or behind the
// spec, and one with no grant, in Pending with a message that does not
// change while counts move; a grant lets the step go on (docs/design/
// 24-agent-budgets.md section 7.2).
// Covers BUDGET-HOLD-01.
func TestBudget_Hold(t *testing.T) {
	person := &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "bob"}
	controller := &v1alpha1.Actor{Kind: v1alpha1.ActorController, Principal: "system:serviceaccount:kardinal:kp", Via: "rollback"}
	noBudgets := budgeted(nil)
	noBudgets.Spec.Budgets = nil
	otherEnv := budgeted(ledgerAt(1))
	otherEnv.Spec.Budgets[0].Environments = []string{"prod"}
	stale := budgeted(ledgerAt(1))
	stale.Generation = 2
	for _, tc := range []struct {
		name    string
		author  *v1alpha1.Actor
		off     bool
		id      *v1alpha1.AgentIdentity
		message string // held with this message; "" passes
		waiting bool
	}{
		{name: "budgets off", author: fixerActor, off: true, id: budgeted(ledgerAt(1))},
		{name: "a person's version", author: person, id: budgeted(ledgerAt(1))},
		{name: "the controller's version", author: controller, id: budgeted(ledgerAt(1))},
		{name: "an unstamped version", id: budgeted(ledgerAt(1))},
		{name: "an agent with no budgets", author: fixerActor, id: noBudgets},
		{name: "no rule for this environment", author: fixerActor, id: otherEnv},
		{name: "an unregistered agent", author: fixerActor, message: "agent fixer is not registered (AgentIdentity fixer not found)"},
		{name: "no ledger yet", author: fixerActor, id: budgeted(nil), message: "waiting for agent fixer's budget to be evaluated", waiting: true},
		{name: "a ledger behind the spec", author: fixerActor, id: stale, message: "waiting for agent fixer's budget to be evaluated", waiting: true},
		{name: "no grant", author: fixerActor, id: budgeted(ledgerAt(1)),
			message: "waiting for agent fixer's budget (rule test: maxConcurrent); kardinal budget fixer", waiting: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var objs []client.Object
			if tc.id != nil {
				objs = append(objs, tc.id)
			}
			b := newBudgetCase(t, tc.author, tc.off, objs...)
			res := b.reconcile()
			got := b.step()
			if tc.message == "" {
				assert.NotEqual(t, "Pending", got.Status.State, "not held: %s", got.Status.Message)
				assert.Nil(t, got.Status.Budget)
				return
			}
			assert.Contains(t, []string{"", "Pending"}, got.Status.State)
			assert.Equal(t, tc.message, got.Status.Message)
			assert.Equal(t, time.Minute, res.RequeueAfter, "the AgentIdentity watch wakes it; the minute is the fallback")
			if tc.waiting {
				require.NotNil(t, got.Status.Budget)
				assert.Equal(t, "fixer", got.Status.Budget.Agent)
				assert.Equal(t, promotionstep.BudgetWaiting, got.Status.Budget.State)
				assert.Equal(t, []string{"test"}, got.Status.Budget.Rules)
				assert.True(t, budgetNow.Equal(got.Status.Budget.Since.Time), "since: %s", got.Status.Budget.Since)
			}
			// A second reconcile, a minute later, rewrites nothing: the
			// message names no counts and since keeps the queue place.
			b.r.NowFn = func() time.Time { return budgetNow.Add(time.Minute) }
			b.reconcile()
			again := b.step()
			assert.Equal(t, got.ResourceVersion, again.ResourceVersion, "the held step is not rewritten")
		})
	}
}

// TestBudget_Granted: a grant lets the step go on, recorded as Granted; a
// step recreated by the Graph finds the same grant; a grant is never
// revoked, so a spec that no longer matches the step does not hold it.
// Covers BUDGET-HOLD-01.
func TestBudget_Granted(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   *v1alpha1.AgentIdentity
	}{
		{"its grant", budgeted(ledgerAt(1, grantFor("test", "")))},
		{"its grant from before a spec change", func() *v1alpha1.AgentIdentity {
			id := budgeted(ledgerAt(1, grantFor("test", "")))
			id.Generation = 2
			return id
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBudgetCase(t, fixerActor, false, tc.id)
			res := b.reconcile()
			got := b.step()
			require.NotNil(t, got.Status.Budget)
			assert.Equal(t, promotionstep.BudgetGranted, got.Status.Budget.State)
			assert.Positive(t, res.RequeueAfter, "recorded first, then it goes on")
			b.reconcile()
			assert.NotEqual(t, "Pending", b.step().Status.State, b.step().Status.Message)
		})
	}

	t.Run("a recreated step finds the grant", func(t *testing.T) {
		b := newBudgetCase(t, fixerActor, false, budgeted(ledgerAt(1, grantFor("test", ""))))
		b.reconcile()
		b.reconcile()
		require.NoError(t, b.c.Delete(b.ctx, &v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Name: "step-test", Namespace: "default"}}))
		again := makeStep("step-test", "nginx-demo", "bundle-1", "test")
		again.Labels = map[string]string{"kardinal.io/pipeline": "nginx-demo", "kardinal.io/bundle": "bundle-1",
			"kardinal.io/environment": "test"}
		require.NoError(t, b.c.Create(b.ctx, again))
		b.reconcile()
		got := b.step()
		require.NotNil(t, got.Status.Budget)
		assert.Equal(t, promotionstep.BudgetGranted, got.Status.Budget.State, "neither charged again nor held")
	})

	t.Run("a waiting step whose rules are gone goes on", func(t *testing.T) {
		id := budgeted(nil)
		b := newBudgetCase(t, fixerActor, false, id)
		b.reconcile()
		require.NotNil(t, b.step().Status.Budget)
		var cur v1alpha1.AgentIdentity
		require.NoError(t, b.c.Get(b.ctx, types.NamespacedName{Name: "fixer"}, &cur))
		cur.Spec.Budgets = nil
		require.NoError(t, b.c.Update(b.ctx, &cur))
		b.reconcile()
		assert.Nil(t, b.step().Status.Budget, "the request is withdrawn")
		b.reconcile()
		assert.NotEqual(t, "Pending", b.step().Status.State)
	})
}

// TestBudget_Exempted: a step that goes on under an exemption writes one
// BudgetExempted AuditEvent naming who made it and why, not repeated on a
// re-run (section 11.3).
// Covers BUDGET-HOLD-01.
func TestBudget_Exempted(t *testing.T) {
	ledger := ledgerAt(1, grantFor("test", "inc-1"))
	ledger.Exemptions = []v1alpha1.AgentBudgetExemptionState{{Name: "inc-1", FirstSeenAt: metav1.NewTime(budgetNow),
		ExpiresAt: metav1.NewTime(budgetNow.Add(time.Hour))}}
	id := budgeted(ledger)
	id.Spec.BudgetExemptions = []v1alpha1.AgentBudgetExemption{{Name: "inc-1", Namespace: "default", Pipeline: "nginx-demo",
		Environment: "test", Reason: "hotfix for INC-4211", ExpiresAt: metav1.NewTime(budgetNow.Add(time.Hour)), CreatedBy: "bob",
		Actor: &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "bob"}}}
	b := newBudgetCase(t, fixerActor, false, id)
	b.reconcile()
	assert.Equal(t, 1, b.audits(promotionstep.AuditActionBudgetExempted))
	var events v1alpha1.AuditEventList
	require.NoError(t, b.c.List(b.ctx, &events))
	for _, e := range events.Items {
		if e.Spec.Action == promotionstep.AuditActionBudgetExempted {
			assert.Contains(t, e.Spec.Message, "by exemption inc-1, made by bob until 2026-10-15T11:00:00Z: hotfix for INC-4211")
		}
	}
	b.reconcile()
	b.reconcile()
	assert.Equal(t, 1, b.audits(promotionstep.AuditActionBudgetExempted), "one event")
	got := b.step()
	assert.Equal(t, "inc-1", got.Status.Budget.Exemption)
	assert.NotEqual(t, "Pending", got.Status.State)
}
