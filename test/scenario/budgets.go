// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/admission"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	abrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentbudget"
	airec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentidentity"
	psrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// KindAgentBudget is the budget reconciler's name in Reconciles: it
// reconciles AgentIdentities, as the AgentIndex reconciler's rebuilds are
// counted under "AgentIndex".
const KindAgentBudget = "AgentBudget"

// The chart's budget admission policies (actors.budgets.enabled), which the
// controller is started with (--agent-budget-exemption-policies).
var budgetPolicies = []string{Release + "-agent-budgets", Release + "-actor-budgets"}

// startBudgets turns agent budgets on, as main.go wires --agent-budgets for
// the controller whose release is the ledger and that owns cluster-scoped
// kinds: the budget reconciler, the step hold and budget.* in gates, all on
// the release's ledger.
func (w *World) startBudgets() {
	w.budgets = &abrec.Reconciler{Client: w.Client, APIReader: w.store, Ledger: Release, NowFn: w.Now,
		ExemptionPolicy: servedPolicies{w: w, names: budgetPolicies}}
	w.steps.Budgets = &psrec.BudgetConfig{Ledger: Release}
	w.gates.BudgetLedger = Release
}

// servedPolicies is policygate.IdentityPolicyCheck for the World: active
// while admission serves every one of the policies.
type servedPolicies struct {
	w     *World
	names []string
}

func (s servedPolicies) Active(context.Context) bool {
	served := s.w.Policies()
	for _, n := range s.names {
		if !slices.Contains(served, n) {
			return false
		}
	}
	return len(s.names) > 0
}

// Ledger is agent's entry in the release's ledger, or nil.
func (w *World) Ledger(agent string) *v1alpha1.AgentUsage {
	w.T.Helper()
	var id v1alpha1.AgentIdentity
	if err := w.Client.Get(w.Ctx, types.NamespacedName{Name: agent}, &id); err != nil {
		w.T.Fatalf("get AgentIdentity %s: %v", agent, err)
	}
	u, ok := id.Status.Usage[Release]
	if !ok {
		return nil
	}
	return &u
}

// prevWrites are the last written copies the watch predicates compare a
// write with: an update event carries the object before and after, and
// the World sees only the write.
type prevWrites struct {
	mu         sync.Mutex
	identities map[string]*v1alpha1.AgentIdentity
	steps      map[string]stepBudget
	envs       map[string][]v1alpha1.PipelineEnvironmentStatus
}

// stepBudget is what the budget reconciler's step watch compares.
type stepBudget struct {
	state  string
	budget *v1alpha1.PromotionStepBudget
}

// watchKeys are the keys a write of o emits (emitKeys), with the
// AgentIdentity watches' predicates applied to what the write changed:
// the agent index rebuilds only for a change it depends on
// (agentidentity.IdentityChanged); with budgets on, the budget reconciler
// hears its agents' steps and the Pipelines that free a maxUnavailable
// grant, a step hears its grant, and a gate that reads budget.* its agent's
// ledger (the SetupWithManager of agentbudget, promotionstep and
// policygate).
func (w *World) watchKeys(o client.Object, op admission.Operation) []string {
	keys := emitKeys(o, w.policyNS)
	switch x := o.(type) {
	case *v1alpha1.AgentIdentity:
		return w.identityKeys(keys, x, op)
	case *v1alpha1.PromotionStep:
		if w.budgets != nil {
			keys = append(keys, w.stepKeys(x, op)...)
		}
	case *v1alpha1.Pipeline:
		if w.budgets != nil && w.environmentsChanged(x, op) {
			keys = append(keys, "BP:*")
		}
	}
	return keys
}

func (w *World) identityKeys(keys []string, n *v1alpha1.AgentIdentity, op admission.Operation) []string {
	w.prev.mu.Lock()
	if w.prev.identities == nil {
		w.prev.identities = map[string]*v1alpha1.AgentIdentity{}
	}
	o := w.prev.identities[n.Name]
	if op == admission.Delete {
		delete(w.prev.identities, n.Name)
	} else {
		w.prev.identities[n.Name] = n.DeepCopy()
	}
	w.prev.mu.Unlock()
	if op != admission.Update || o == nil {
		return keys
	}
	e := event.UpdateEvent{ObjectOld: o, ObjectNew: n}
	if !airec.IdentityChanged.Update(e) {
		keys = slices.DeleteFunc(keys, func(k string) bool { return k == "AI:*" })
	}
	if w.budgets == nil {
		return keys
	}
	ou, hadOld := o.Status.Usage[Release]
	nu, hasNew := n.Status.Usage[Release]
	// The steps (promotionstep budgetWakeups): every waiting one of the
	// agent when its spec changed or the ledger became current, else the
	// steps of the grants this write added.
	if o.Generation != n.Generation || (hasNew && nu.ObservedGeneration == n.Generation &&
		(!hadOld || ou.ObservedGeneration != n.Generation)) {
		keys = append(keys, "BW:"+n.Name)
	}
	for _, g := range nu.Grants {
		if hadOld && grantIn(&ou, g.BundleUID, g.Environment) {
			continue
		}
		var bundles v1alpha1.BundleList
		if err := w.store.List(w.Ctx, &bundles, client.MatchingFields{abrec.IndexBundleUID: g.BundleUID}); err != nil {
			w.T.Errorf("list the Bundle of grant %s: %v", g.BundleUID, err)
			continue
		}
		for _, b := range bundles.Items {
			keys = append(keys, "GR:"+b.Namespace+"/"+b.Name+"/"+g.Environment)
		}
	}
	// The gates that read budget.* (policygate ledgerUsageChanged and
	// budgetGateRequests): the instances of the agent's versions.
	if hadOld != hasNew || !equality.Semantic.DeepEqual(ou, nu) {
		var bundles v1alpha1.BundleList
		if err := w.store.List(w.Ctx, &bundles, client.MatchingFields{abrec.IndexBundleAgent: n.Name}); err != nil {
			w.T.Errorf("list the Bundles of agent %s: %v", n.Name, err)
		}
		for _, b := range bundles.Items {
			keys = append(keys, "BL:"+b.Namespace+"/"+b.Name)
		}
	}
	return keys
}

func grantIn(u *v1alpha1.AgentUsage, uid, env string) bool {
	for i := range u.Grants {
		if u.Grants[i].BundleUID == uid && u.Grants[i].Environment == env {
			return true
		}
	}
	return false
}

// stepKeys: the budget reconciler's step watch (agentbudget stepBudgetChanged
// and agentOfStep, on the step before and after the write).
func (w *World) stepKeys(n *v1alpha1.PromotionStep, op admission.Operation) []string {
	key := n.Namespace + "/" + n.Name
	cur := stepBudget{state: n.Status.State, budget: n.Status.Budget.DeepCopy()}
	w.prev.mu.Lock()
	if w.prev.steps == nil {
		w.prev.steps = map[string]stepBudget{}
	}
	o, had := w.prev.steps[key]
	if op == admission.Delete {
		delete(w.prev.steps, key)
	} else {
		w.prev.steps[key] = cur
	}
	w.prev.mu.Unlock()
	agents := func(bs ...*v1alpha1.PromotionStepBudget) []string {
		var out []string
		for _, b := range bs {
			if b != nil && b.Agent != "" && !slices.Contains(out, "BS:"+b.Agent) {
				out = append(out, "BS:"+b.Agent)
			}
		}
		return out
	}
	switch {
	case op != admission.Update || !had:
		return agents(cur.budget)
	case o.budget == nil && cur.budget == nil:
		return nil
	case o.state != cur.state || !equality.Semantic.DeepEqual(o.budget, cur.budget):
		return agents(o.budget, cur.budget)
	}
	return nil
}

// environmentsChanged: the budget reconciler's Pipeline watch (agentbudget
// environmentsChanged).
func (w *World) environmentsChanged(n *v1alpha1.Pipeline, op admission.Operation) bool {
	key := n.Namespace + "/" + n.Name
	w.prev.mu.Lock()
	defer w.prev.mu.Unlock()
	if w.prev.envs == nil {
		w.prev.envs = map[string][]v1alpha1.PipelineEnvironmentStatus{}
	}
	o, had := w.prev.envs[key]
	if op == admission.Delete {
		delete(w.prev.envs, key)
		return true
	}
	cur := make([]v1alpha1.PipelineEnvironmentStatus, len(n.Status.Environments))
	for i := range n.Status.Environments {
		n.Status.Environments[i].DeepCopyInto(&cur[i])
	}
	w.prev.envs[key] = cur
	return op == admission.Update && (!had || !equality.Semantic.DeepEqual(o, cur))
}

// watchVersion is what a watch-mode World compares to tell that o changed
// since its last reconcile: its resourceVersion, or for the budget
// reconciler, whose watch of AgentIdentities passes only spec changes, its
// generation.
func watchVersion(kind string, o client.Object) string {
	if kind == KindAgentBudget {
		return strconv.FormatInt(o.GetGeneration(), 10)
	}
	return o.GetResourceVersion()
}

// budgetListenKeys are the keys an AgentIdentity listens to as the budget
// reconciler's object: its agent's steps, and the Pipelines while it holds
// an unavailable grant in the release's ledger.
func budgetListenKeys(o client.Object) []string {
	keys := []string{"BS:" + o.GetName()}
	if slices.Contains(abrec.UnavailableLedgers(o), Release) {
		keys = append(keys, "BP:*")
	}
	return keys
}

// stepBudgetListenKeys: a step hears the write that grants it, and, while
// it waits for its budget, every evaluation of its agent's ledger.
func stepBudgetListenKeys(o client.Object) []string {
	s, ok := o.(*v1alpha1.PromotionStep)
	if !ok {
		return nil
	}
	keys := []string{"GR:" + s.Namespace + "/" + s.Spec.BundleName + "/" + s.Spec.Environment}
	if b := s.Status.Budget; b != nil && b.State == psrec.BudgetWaiting {
		keys = append(keys, "BW:"+b.Agent)
	}
	return keys
}

// gateBudgetListenKeys: a gate instance that reads budget.* hears its
// version's agent's ledger (policygate readsBudget).
func gateBudgetListenKeys(o client.Object, ns, bundle string) []string {
	g, ok := o.(*v1alpha1.PolicyGate)
	if !ok {
		return nil
	}
	if strings.Contains(g.Spec.Expression, "budget") ||
		(g.Spec.Approval != nil && strings.Contains(g.Spec.Approval.RequiredWhen, "budget")) {
		return []string{"BL:" + ns + "/" + bundle}
	}
	return nil
}
