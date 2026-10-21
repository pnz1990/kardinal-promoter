// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package e2e

import (
	"context"
	"fmt"
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
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentbudget"
	psrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// budgetWorld is one cluster with agent fixer's budget, the budget
// reconciler and the step reconciler with the budget hold on, over a clock
// the test moves (docs/design/24-agent-budgets.md section 19, the
// fake-client journeys).
type budgetWorld struct {
	t      *testing.T
	ctx    context.Context
	c      client.WithWatch
	now    time.Time
	steps  *psrec.Reconciler
	ledger *agentbudget.Reconciler
	n      int
}

var fixerStamp = &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "system:serviceaccount:ci:fixer", Agent: "fixer", Sponsor: "alice"}

func newBudgetWorld(t *testing.T, rules []v1alpha1.AgentBudgetRule, namespaces ...string) *budgetWorld {
	t.Helper()
	objs := []client.Object{&v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: "fixer", Generation: 1},
		Spec: v1alpha1.AgentIdentitySpec{Subjects: []v1alpha1.AgentSubject{{Kind: "ServiceAccount", Namespace: "ci", Name: "fixer"}},
			Sponsor: v1alpha1.AgentSponsor{User: "alice"}, Scope: v1alpha1.AgentScope{Namespaces: []string{"*"}},
			Autonomy: v1alpha1.AutonomyAuto, Budgets: rules}}}
	for _, ns := range namespaces {
		objs = append(objs, &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: ns},
			Spec: v1alpha1.PipelineSpec{Git: v1alpha1.PipelineGit{URL: "https://github.com/pnz1990/kardinal-demo", Branch: "main"},
				Environments: []v1alpha1.EnvironmentSpec{{Name: "prod", Approval: "auto"}}}})
	}
	c := fake.NewClientBuilder().WithScheme(journeyScheme(t)).WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.AgentIdentity{}, &v1alpha1.Bundle{}, &v1alpha1.PromotionStep{}, &v1alpha1.Pipeline{}).
		WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		WithIndex(&v1alpha1.PromotionStep{}, agentbudget.IndexStepBudgetAgent, agentbudget.StepBudgetAgent).
		WithIndex(&v1alpha1.Bundle{}, agentbudget.IndexBundleUID, agentbudget.BundleUID).
		WithIndex(&v1alpha1.Bundle{}, agentbudget.IndexBundleAgent, agentbudget.BundleAgent).
		WithIndex(&v1alpha1.AgentIdentity{}, agentbudget.IndexUnavailableLedgers, agentbudget.UnavailableLedgers).Build()
	w := &budgetWorld{t: t, ctx: context.Background(), c: c, now: time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)}
	clock := func() time.Time { return w.now }
	w.steps = &psrec.Reconciler{Client: c, SCM: &mockSCMForLoop{prURL: "https://github.com/pnz1990/kardinal-demo/pull/1", prNumber: 1},
		GitClient: &mockGitForLoop{}, WorkDirFn: func(_, _ string) string { return t.TempDir() }, NowFn: clock,
		Budgets: &psrec.BudgetConfig{Ledger: "kardinal-promoter"}}
	w.ledger = &agentbudget.Reconciler{Client: c, APIReader: c, Ledger: "kardinal-promoter", NowFn: clock}
	return w
}

// version creates a version of the Pipeline in ns by author and its prod
// step, and returns the step's name.
func (w *budgetWorld) version(ns string, author *v1alpha1.Actor) string {
	w.t.Helper()
	w.n++
	name := fmt.Sprintf("checkout-v%d", w.n)
	require.NoError(w.t, w.c.Create(w.ctx, &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns,
		UID: types.UID(fmt.Sprintf("uid-%d", w.n)), Labels: map[string]string{"kardinal.io/pipeline": "checkout"}},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "checkout", Actor: author}}))
	step := makeJourneyStep(name+"-prod", "checkout", name, "prod", "auto")
	step.Namespace = ns
	step.Labels["kardinal.io/bundle"], step.Labels["kardinal.io/environment"] = name, "prod"
	step.CreationTimestamp = metav1.NewTime(w.now)
	require.NoError(w.t, w.c.Create(w.ctx, step))
	w.now = w.now.Add(time.Second)
	return step.Name
}

func (w *budgetWorld) reconcileStep(ns, name string) {
	w.t.Helper()
	_, err := w.steps.Reconcile(w.ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
	require.NoError(w.t, err)
}

// settle runs the budget reconciler and every step's reconciler until
// nothing changes: what the controller's watches would do.
func (w *budgetWorld) settle() {
	w.t.Helper()
	for range 40 {
		_, err := w.ledger.Reconcile(w.ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "fixer"}})
		require.NoError(w.t, err)
		var steps v1alpha1.PromotionStepList
		require.NoError(w.t, w.c.List(w.ctx, &steps))
		for i := range steps.Items {
			if s := steps.Items[i]; s.Status.State != "Verified" && s.Status.State != "Failed" && s.Status.State != "Superseded" {
				w.reconcileStep(s.Namespace, s.Name)
			}
		}
	}
}

func (w *budgetWorld) step(ns, name string) v1alpha1.PromotionStep {
	w.t.Helper()
	var s v1alpha1.PromotionStep
	require.NoError(w.t, w.c.Get(w.ctx, types.NamespacedName{Namespace: ns, Name: name}, &s))
	return s
}

func (w *budgetWorld) verified(ns, name string) bool {
	return w.step(ns, name).Status.State == "Verified"
}

// TestBudgetJourney1VersionsPerDay: an agent ships its fifth version of the
// day to prod; the sixth waits with the message, and ships once the oldest
// grant is 24 hours old.
// Covers BUDGET-HOLD-01.
func TestBudgetJourney1VersionsPerDay(t *testing.T) {
	w := newBudgetWorld(t, []v1alpha1.AgentBudgetRule{{Name: "prod", Environments: []string{"prod"}, VersionsPerDay: 5}}, "payments")
	var names []string
	for range 6 {
		names = append(names, w.version("payments", fixerStamp))
	}
	w.settle()
	for _, n := range names[:5] {
		assert.True(t, w.verified("payments", n), "%s ships: %s", n, w.step("payments", n).Status.Message)
	}
	sixth := w.step("payments", names[5])
	assert.Contains(t, []string{"", "Pending"}, sixth.Status.State, "held, never failed")
	assert.Equal(t, "waiting for agent fixer's budget (rule prod: versionsPerDay); kardinal budget fixer", sixth.Status.Message)

	w.now = w.now.Add(24 * time.Hour)
	w.settle()
	assert.True(t, w.verified("payments", names[5]), "ships once the oldest grant is a day old: %s", w.step("payments", names[5]).Status.Message)
}

// TestBudgetJourney2Concurrent: two agent versions in two namespaces reach
// prod at once with maxConcurrent 1: one starts, the other waits until the
// first is Verified.
// Covers BUDGET-HOLD-01.
func TestBudgetJourney2Concurrent(t *testing.T) {
	w := newBudgetWorld(t, []v1alpha1.AgentBudgetRule{{Name: "prod", Environments: []string{"prod"}, MaxConcurrent: 1}},
		"payments-a", "payments-b")
	a := w.version("payments-a", fixerStamp)
	b := w.version("payments-b", fixerStamp)
	w.reconcileStep("payments-a", a)
	w.reconcileStep("payments-b", b)
	_, err := w.ledger.Reconcile(w.ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "fixer"}})
	require.NoError(t, err)
	var id v1alpha1.AgentIdentity
	require.NoError(t, w.c.Get(w.ctx, types.NamespacedName{Name: "fixer"}, &id))
	require.Len(t, id.Status.Usage["kardinal-promoter"].Grants, 1, "one place, one grant")
	assert.Equal(t, "uid-1", id.Status.Usage["kardinal-promoter"].Grants[0].BundleUID, "the first to ask")
	assert.Contains(t, []string{"", "Pending"}, w.step("payments-b", b).Status.State)

	w.settle()
	assert.True(t, w.verified("payments-a", a))
	assert.True(t, w.verified("payments-b", b), "the second starts once the first is Verified")
}

// TestBudgetJourney5Exemption: an exemption lets one version through,
// writes BudgetExempted, and expires.
// Covers BUDGET-HOLD-01.
func TestBudgetJourney5Exemption(t *testing.T) {
	w := newBudgetWorld(t, []v1alpha1.AgentBudgetRule{{Name: "prod", Environments: []string{"prod"}, VersionsPerDay: 1}}, "payments")
	first := w.version("payments", fixerStamp)
	second := w.version("payments", fixerStamp)
	w.settle()
	require.True(t, w.verified("payments", first))
	require.False(t, w.verified("payments", second))

	var id v1alpha1.AgentIdentity
	require.NoError(t, w.c.Get(w.ctx, types.NamespacedName{Name: "fixer"}, &id))
	id.Spec.BudgetExemptions = []v1alpha1.AgentBudgetExemption{{Name: "inc-4211", Namespace: "payments", Pipeline: "checkout",
		Environment: "prod", Bundle: "checkout-v2", Reason: "hotfix for INC-4211", ExpiresAt: metav1.NewTime(w.now.Add(time.Hour)),
		CreatedBy: "bob", Actor: &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "bob"}}}
	require.NoError(t, w.c.Update(w.ctx, &id))
	w.settle()
	assert.True(t, w.verified("payments", second), "let through: %s", w.step("payments", second).Status.Message)
	byAction := auditByAction(t, w.c, "payments")
	require.Contains(t, byAction, psrec.AuditActionBudgetExempted)
	assert.Contains(t, byAction[psrec.AuditActionBudgetExempted].Spec.Message, "made by bob")

	// It names one version, and it expires.
	third := w.version("payments", fixerStamp)
	w.now = w.now.Add(2 * time.Hour)
	w.settle()
	assert.False(t, w.verified("payments", third), "the exemption named another version, and has expired")
}

// TestBudgetJourney6People: a person's version is never held; a superseded
// waiting version leaves the queue.
// Covers BUDGET-HOLD-01.
func TestBudgetJourney6People(t *testing.T) {
	w := newBudgetWorld(t, []v1alpha1.AgentBudgetRule{{Name: "prod", Environments: []string{"prod"}, MaxConcurrent: 1}}, "payments")
	first := w.version("payments", fixerStamp)
	w.settle()
	require.True(t, w.verified("payments", first))
	// Hold the place: a running grant from another version.
	blocker := w.version("payments", fixerStamp)
	w.reconcileStep("payments", blocker)
	_, err := w.ledger.Reconcile(w.ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "fixer"}})
	require.NoError(t, err)
	waiting := w.version("payments", fixerStamp)
	person := w.version("payments", &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "bob"})
	w.reconcileStep("payments", waiting)
	for range 20 {
		w.reconcileStep("payments", person)
	}
	assert.True(t, w.verified("payments", person), "a person's version is never held: %s", w.step("payments", person).Status.Message)
	require.NotNil(t, w.step("payments", waiting).Status.Budget)

	// The waiting version is superseded: it leaves the queue.
	s := w.step("payments", waiting)
	s.Status.State = "Superseded"
	require.NoError(t, w.c.Status().Update(w.ctx, &s))
	w.settle()
	var id v1alpha1.AgentIdentity
	require.NoError(t, w.c.Get(w.ctx, types.NamespacedName{Name: "fixer"}, &id))
	for _, r := range id.Status.Usage["kardinal-promoter"].Rules {
		assert.Zero(t, r.Waiting, "nothing waits")
	}
	for _, g := range id.Status.Usage["kardinal-promoter"].Grants {
		assert.NotEqual(t, "uid-3", g.BundleUID, "the superseded version was never granted")
	}
}
