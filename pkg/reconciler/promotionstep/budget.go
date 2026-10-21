// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/audit"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentbudget"
)

// AuditActionBudgetExempted is the AuditEvent a step writes once when it
// goes on under a budget exemption (docs/design/24-agent-budgets.md section
// 11.3).
const AuditActionBudgetExempted = "BudgetExempted"

// Budget states (status.budget.state).
const (
	BudgetWaiting = "Waiting"
	BudgetGranted = "Granted"
)

// BudgetConfig turns the agent budget hold on (--agent-budgets): a step of
// an agent's version waits in Pending until the agent's ledger grants it.
type BudgetConfig struct {
	// Ledger is the status.usage key the grants are read from
	// (--agent-budget-ledger): every shard of one installation reads the
	// same one.
	Ledger string
}

// holdForBudget holds an agent's step in Pending until its Bundle and
// environment hold a grant in the agent's ledger (section 7.2). It reads the
// Bundle's admission-verified actor, the AgentIdentity's spec and its
// ledger; it writes only this step's status (status.budget, the message,
// the BudgetExempted AuditEvent). It never reads a clock to decide.
func (r *Reconciler) holdForBudget(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (bool, ctrl.Result, error) {
	if r.Budgets == nil {
		return false, ctrl.Result{}, nil // budgets off for the release (17.4)
	}
	b, err := r.loadBundle(ctx, ps)
	if err != nil {
		return true, ctrl.Result{}, fmt.Errorf("read the version's actor for its budget: %w", err)
	}
	if b.Spec.Actor == nil || b.Spec.Actor.Kind != v1alpha1.ActorAgent || b.Spec.Actor.Agent == "" {
		return false, ctrl.Result{}, nil // people's, bots' and the controller's versions are never held
	}
	agent := b.Spec.Actor.Agent
	var id v1alpha1.AgentIdentity
	if err := r.Get(ctx, types.NamespacedName{Name: agent}, &id); err != nil {
		if !apierrors.IsNotFound(err) {
			return true, ctrl.Result{}, fmt.Errorf("read AgentIdentity %s for its budget: %w", agent, err)
		}
		msg := fmt.Sprintf("agent %s is not registered (AgentIdentity %s not found)", agent, agent)
		return true, ctrl.Result{RequeueAfter: requeuePaused}, r.writeBudget(ctx, ps, msg, ps.Status.Budget)
	}
	rules := agentbudget.Matching(id.Spec.Budgets, ps.Namespace, ps.Spec.Environment, ps.Labels[graph.LabelFleet])
	usage, evaluated := id.Status.Usage[r.Budgets.Ledger]
	// A grant is never revoked: a step that has one goes on, whatever the
	// spec says now.
	if g := grantOf(&usage, string(b.UID), ps.Spec.Environment); evaluated && g != nil {
		return r.budgetGranted(ctx, log, ps, &id, &usage, g)
	}
	if len(rules) == 0 {
		// No rule can hold it: no budgets, or none for this environment.
		if ps.Status.Budget != nil && ps.Status.Budget.State == BudgetWaiting {
			return true, ctrl.Result{RequeueAfter: time.Second}, r.writeBudget(ctx, ps, ps.Status.Message, nil)
		}
		return false, ctrl.Result{}, nil
	}
	want := &v1alpha1.PromotionStepBudget{Agent: agent, State: BudgetWaiting, Rules: rules}
	if prev := ps.Status.Budget; prev != nil && prev.State == BudgetWaiting && prev.Since != nil {
		want.Since = prev.Since // its place in the queue
	} else {
		since := metav1.NewTime(r.now().UTC().Truncate(time.Second))
		want.Since = &since
	}
	msg := fmt.Sprintf("waiting for agent %s's budget to be evaluated", agent)
	if evaluated && usage.ObservedGeneration == id.Generation {
		msg = waitingMessage(agent, rules, &usage)
	}
	if ps.Status.Message != msg || !equality.Semantic.DeepEqual(ps.Status.Budget, want) {
		log.Info().Str("agent", agent).Strs("rules", rules).Str("env", ps.Spec.Environment).
			Msg("agent budget holds the step — step stays in Pending")
	}
	// The AgentIdentity watch wakes the step when its grant lands; the
	// minute is the fallback.
	return true, ctrl.Result{RequeueAfter: requeuePaused}, r.writeBudget(ctx, ps, msg, want)
}

// budgetGranted records that the step found its grant (status.budget
// Granted, and the BudgetExempted AuditEvent for an exempt grant, once), then
// lets it go on in the next reconcile.
func (r *Reconciler) budgetGranted(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep,
	id *v1alpha1.AgentIdentity, usage *v1alpha1.AgentUsage, g *v1alpha1.AgentBudgetGrant) (bool, ctrl.Result, error) {
	want := &v1alpha1.PromotionStepBudget{Agent: id.Name, State: BudgetGranted, Rules: g.Rules, Exemption: g.Exemption}
	if prev := ps.Status.Budget; prev != nil && prev.Since != nil {
		want.Since = prev.Since
	}
	if equality.Semantic.DeepEqual(ps.Status.Budget, want) {
		return false, ctrl.Result{}, nil
	}
	patch := client.MergeFromWithOptions(ps.DeepCopy(), client.MergeFromWithOptimisticLock{})
	firstGrant := ps.Status.Budget == nil || ps.Status.Budget.State != BudgetGranted
	ps.Status.Budget = want
	if g.Exemption != "" && firstGrant {
		if entry, ok := budgetAuditEntry(ps, id, usage, g, r.now()); ok {
			ps.Status.PendingAuditEvents = audit.Enqueue(ctx, auditKind, ps.Status.PendingAuditEvents, entry)
		}
	}
	if err := r.Status().Patch(ctx, ps, patch); err != nil {
		if apierrors.IsNotFound(err) {
			return true, ctrl.Result{}, nil
		}
		return true, ctrl.Result{}, fmt.Errorf("record the agent budget grant: %w", err)
	}
	_ = r.flushAudit(ctx, ps)
	log.Info().Str("agent", id.Name).Str("env", ps.Spec.Environment).Str("exemption", g.Exemption).
		Msg("agent budget granted — step goes on")
	// Go on in a new reconcile, from the status just written (its patches
	// carry the resourceVersion they read).
	return true, ctrl.Result{RequeueAfter: time.Second}, nil
}

// writeBudget writes the step's message and status.budget when they
// changed: the message names the exhausted limits but no counts, so a step
// is not rewritten while counts move.
func (r *Reconciler) writeBudget(ctx context.Context, ps *v1alpha1.PromotionStep, msg string, budget *v1alpha1.PromotionStepBudget) error {
	if ps.Status.Message == msg && equality.Semantic.DeepEqual(ps.Status.Budget, budget) {
		return nil
	}
	patch := client.MergeFromWithOptions(ps.DeepCopy(), client.MergeFromWithOptimisticLock{})
	ps.Status.Message, ps.Status.Budget = msg, budget
	if err := r.Status().Patch(ctx, ps, patch); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("patch the agent budget hold: %w", err)
	}
	return nil
}

// grantOf is the ledger's grant of Bundle uid into env, or nil.
func grantOf(u *v1alpha1.AgentUsage, uid, env string) *v1alpha1.AgentBudgetGrant {
	for i := range u.Grants {
		if u.Grants[i].BundleUID == uid && u.Grants[i].Environment == env {
			return &u.Grants[i]
		}
	}
	return nil
}

// waitingMessage names the rules that hold the step and their exhausted
// limits; `kardinal budget` shows the counts.
func waitingMessage(agent string, rules []string, u *v1alpha1.AgentUsage) string {
	parts := make([]string, 0, len(rules))
	for _, name := range rules {
		part := "rule " + name
		for i := range u.Rules {
			if u.Rules[i].Name == name && len(u.Rules[i].Exhausted) > 0 {
				part += ": " + strings.Join(u.Rules[i].Exhausted, ", ")
			}
		}
		parts = append(parts, part)
	}
	return fmt.Sprintf("waiting for agent %s's budget (%s); kardinal budget %s", agent, strings.Join(parts, "; "), agent)
}

// budgetAuditEntry is the BudgetExempted outbox entry: who made the
// exemption, why and until when. Its name is derived from the step and the
// exemption, so a re-run finds it.
func budgetAuditEntry(ps *v1alpha1.PromotionStep, id *v1alpha1.AgentIdentity, u *v1alpha1.AgentUsage,
	g *v1alpha1.AgentBudgetGrant, now time.Time) (v1alpha1.PendingAuditEvent, bool) {
	var ex *v1alpha1.AgentBudgetExemption
	for i := range id.Spec.BudgetExemptions {
		if id.Spec.BudgetExemptions[i].Name == g.Exemption {
			ex = &id.Spec.BudgetExemptions[i]
		}
	}
	until := ""
	for _, s := range u.Exemptions {
		if s.Name == g.Exemption {
			until = s.ExpiresAt.UTC().Format(time.RFC3339)
		}
	}
	by, reason := "(unknown)", ""
	if ex != nil {
		by, reason = ex.CreatedBy, ex.Reason
	}
	msg := fmt.Sprintf("let through agent %s's budget (%s) by exemption %s, made by %s until %s: %s",
		id.Name, strings.Join(g.Rules, ", "), g.Exemption, by, until, reason)
	e, ok := auditEntry(ps, AuditActionBudgetExempted, AuditOutcomeSuccess, msg, now, nil)
	if !ok {
		return e, false
	}
	sum := sha256.Sum256([]byte(id.Name + "\x00" + g.Exemption + "\x00" + by + "\x00" + until + "\x00" + reason))
	suffix := "-budget-exempted-" + hex.EncodeToString(sum[:])[:10]
	base := ps.Name
	if limit := 253 - len(suffix); len(base) > limit {
		base = base[:limit]
	}
	e.Name = sanitizeK8sName(base + suffix)
	return e, true
}

// budgetWakes enqueues, on an AgentIdentity update, only the steps the
// ledger write concerns: the steps of grants it added, and every waiting
// step of the agent when the ledger became current or the spec changed
// (section 7.2, Waking). A write that grants nothing wakes none.
func (r *Reconciler) budgetWakes() handler.EventHandler {
	return handler.Funcs{UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		o, ok := e.ObjectOld.(*v1alpha1.AgentIdentity)
		n, ok2 := e.ObjectNew.(*v1alpha1.AgentIdentity)
		if !ok || !ok2 {
			return
		}
		for _, req := range r.budgetWakeups(ctx, o, n) {
			q.Add(req)
		}
	}}
}

func (r *Reconciler) budgetWakeups(ctx context.Context, o, n *v1alpha1.AgentIdentity) []reconcile.Request {
	ou, hadOld := o.Status.Usage[r.Budgets.Ledger]
	nu, hasNew := n.Status.Usage[r.Budgets.Ledger]
	all := o.Generation != n.Generation ||
		(hasNew && nu.ObservedGeneration == n.Generation && (!hadOld || ou.ObservedGeneration != n.Generation))
	added := map[[2]string]bool{}
	if hasNew {
		for _, g := range nu.Grants {
			if !hadOld || grantOf(&ou, g.BundleUID, g.Environment) == nil {
				added[[2]string{g.BundleUID, g.Environment}] = true
			}
		}
	}
	if !all && len(added) == 0 {
		return nil
	}
	var steps v1alpha1.PromotionStepList
	if err := r.List(ctx, &steps, client.MatchingFields{agentbudget.IndexStepBudgetAgent: n.Name}); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Str("agent", n.Name).Msg("list the steps an agent's budget holds; they re-check within a minute")
		return nil
	}
	var out []reconcile.Request
	for i := range steps.Items {
		s := &steps.Items[i]
		if s.Status.State != StatePending && s.Status.State != "Pending" {
			continue // started: past its hold
		}
		wake := all && s.Status.Budget != nil && s.Status.Budget.State == BudgetWaiting
		if !wake && len(added) > 0 {
			uid := s.Labels[graph.LabelBundleUID]
			if uid == "" {
				var b v1alpha1.Bundle
				if err := r.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: s.Spec.BundleName}, &b); err == nil {
					uid = string(b.UID)
				}
			}
			wake = added[[2]string{uid, s.Spec.Environment}]
		}
		if wake {
			out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: s.Namespace, Name: s.Name}})
		}
	}
	return out
}
