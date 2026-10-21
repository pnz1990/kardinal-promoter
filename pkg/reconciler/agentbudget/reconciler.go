// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package agentbudget is the budget reconciler of
// docs/design/24-agent-budgets.md: the one writer that grants an agent's
// promotions against its AgentIdentity spec.budgets, in the ledger
// status.usage.<ledger>. Steps ask by writing status.budget (Waiting); the
// reconciler grants them in order, one agent at a time, and each write is
// conditional on the ledger it read (an optimistic lock), so a grant
// computed from a stale ledger never lands.
package agentbudget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/kubeevent"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/shard"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
)

// Event reasons, on the AgentIdentity.
const (
	// ReasonExemptionIgnored: an exemption made by an agent, or without an
	// actor stamp, never counts.
	ReasonExemptionIgnored = "BudgetExemptionIgnored"
	// ReasonLedgerFull: a cap keeps the ledger from granting.
	ReasonLedgerFull = "BudgetLedgerFull"
	// ReasonLedgerWriteRefused: the API server refused the ledger's write
	// (for example a 17th ledger), so this installation cannot grant.
	ReasonLedgerWriteRefused = "BudgetLedgerWriteRefused"
)

// conflictRetries is how many times a write refused with Conflict is
// recomputed from a fresh read before the reconcile returns the error.
const conflictRetries = 3

// Reconciler grants the requests of each agent in one ledger.
type Reconciler struct {
	// Client reads from the cache and writes the ledger.
	Client client.Client
	// APIReader re-reads an AgentIdentity after a Conflict, uncached.
	APIReader client.Reader
	// Ledger is the status.usage key this installation writes
	// (--agent-budget-ledger).
	Ledger string
	// Recorder emits the Warning events. Nil records none (namespace mode,
	// where an event on a cluster-scoped object cannot be written).
	Recorder events.EventRecorder
	// MaxObjectBytes bounds the AgentIdentity's JSON (0:
	// DefaultMaxObjectBytes).
	MaxObjectBytes int
	// NowFn is the clock (nil: time.Now). It is read once per evaluation,
	// only to write the ledger (section 6.2).
	NowFn func() time.Time
	// ExemptionPolicy reports whether the admission policy that checks a
	// budget exemption's actor stamp is installed and bound now
	// (--agent-budget-exemption-policies; policygate.IdentityPolicyCheck, as
	// for gate overrides). Nil or inactive: no stamped exemption counts, it
	// is listed ignored: Unverified.
	ExemptionPolicy PolicyCheck
}

// PolicyCheck reports whether an admission policy is in force.
type PolicyCheck interface {
	Active(ctx context.Context) bool
}

// AllPolicies is in force while every one of its checks is; none is never.
type AllPolicies []PolicyCheck

// Active reports whether every check is active.
func (a AllPolicies) Active(ctx context.Context) bool {
	for _, c := range a {
		if !c.Active(ctx) {
			return false
		}
	}
	return len(a) > 0
}

func (r *Reconciler) now() time.Time {
	if r.NowFn != nil {
		return r.NowFn()
	}
	return time.Now()
}

// Reconcile evaluates one agent's ledger and writes it when it changed.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := zerolog.Ctx(ctx).With().Str("agentidentity", req.Name).Str("ledger", r.Ledger).Logger()
	for attempt := 0; ; attempt++ {
		reader := client.Reader(r.Client)
		if attempt > 0 {
			reader = r.APIReader // after a Conflict the cache may lag
		}
		var id v1alpha1.AgentIdentity
		if err := reader.Get(ctx, req.NamespacedName, &id); err != nil {
			if apierrors.IsNotFound(err) {
				return reconcile.Result{}, nil
			}
			return reconcile.Result{}, fmt.Errorf("get agent identity %s: %w", req.Name, err)
		}
		var old *v1alpha1.AgentUsage
		if u, ok := id.Status.Usage[r.Ledger]; ok {
			old = &u
		}
		if len(id.Spec.Budgets) == 0 && old == nil {
			return reconcile.Result{}, nil
		}
		now := r.now()
		f, err := r.facts(ctx, &id, old)
		if err != nil {
			return reconcile.Result{}, err
		}
		out := evaluate(&id, old, f, now)
		result := reconcile.Result{RequeueAfter: out.requeue}
		if unchanged(old, out.usage) {
			return result, nil
		}
		base := id.DeepCopy()
		if out.usage == nil {
			delete(id.Status.Usage, r.Ledger)
		} else {
			if id.Status.Usage == nil {
				id.Status.Usage = map[string]v1alpha1.AgentUsage{}
			}
			id.Status.Usage[r.Ledger] = *out.usage
		}
		err = r.Client.Status().Patch(ctx, &id, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		switch {
		case apierrors.IsConflict(err) && attempt < conflictRetries:
			log.Debug().Int("attempt", attempt+1).Msg("agent budget ledger changed since it was read: evaluating again")
			continue
		case apierrors.IsNotFound(err):
			return reconcile.Result{}, nil
		case err != nil && refused(err):
			// Refused for what it is: retrying with backoff would be refused
			// again. Say so, and wait for the next event (TerminalError).
			kubeevent.Emit(r.Recorder, base, corev1.EventTypeWarning, ReasonLedgerWriteRefused, "Grant",
				fmt.Sprintf("ledger %s could not be written, so this installation grants nothing for the agent: %v", r.Ledger, err))
			if old != nil {
				r.markWriteRefused(ctx, req.Name, err)
			}
			return reconcile.Result{}, reconcile.TerminalError(fmt.Errorf("write agent budget ledger %s of %s: %w", r.Ledger, req.Name, err))
		case err != nil:
			return reconcile.Result{}, fmt.Errorf("write agent budget ledger %s of %s: %w", r.Ledger, req.Name, err)
		}
		for _, e := range out.ignored {
			kubeevent.Emit(r.Recorder, &id, corev1.EventTypeWarning, ReasonExemptionIgnored, "Grant", ignoredNote(e))
		}
		if out.full != "" {
			kubeevent.Emit(r.Recorder, &id, corev1.EventTypeWarning, ReasonLedgerFull, "Grant",
				fmt.Sprintf("ledger %s: %s", r.Ledger, fullMessage(out.full)))
		}
		for _, g := range out.granted {
			log.Info().Str("bundleUID", g.BundleUID).Str("environment", g.Environment).Strs("rules", g.Rules).
				Str("exemption", g.Exemption).Msg("agent budget granted")
		}
		return result, nil
	}
}

// markWriteRefused tries a minimal write of the existing ledger's LedgerFull
// condition (reason WriteRefused), so the ledger says why it stopped
// granting. A ledger the API server will not take at all (a 17th) has no
// entry to write it in: the event and kardinal doctor say so. Its own
// failure is only logged.
func (r *Reconciler) markWriteRefused(ctx context.Context, name string, cause error) {
	var id v1alpha1.AgentIdentity
	if err := r.APIReader.Get(ctx, types.NamespacedName{Name: name}, &id); err != nil {
		return
	}
	u, ok := id.Status.Usage[r.Ledger]
	if !ok {
		return
	}
	if c := meta.FindStatusCondition(u.Conditions, ConditionLedgerFull); c != nil && c.Reason == FullWriteRefused {
		return
	}
	base := id.DeepCopy()
	meta.SetStatusCondition(&u.Conditions, metav1.Condition{Type: ConditionLedgerFull, Status: metav1.ConditionTrue,
		Reason: FullWriteRefused, ObservedGeneration: u.ObservedGeneration, LastTransitionTime: metav1.NewTime(r.now()),
		Message: kubeevent.Truncate("the API server refused the ledger's last write, so nothing more is granted: " + cause.Error())})
	id.Status.Usage[r.Ledger] = u
	if err := r.Client.Status().Patch(ctx, &id, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("agentidentity", name).Msg("the refused agent budget ledger's condition could not be written either")
	}
}

// refused reports whether the API server refused the write for what it is
// (a 17th ledger, a schema bound, its size, RBAC), not for a moment (a
// timeout or a Conflict past the retries).
func refused(err error) bool {
	return apierrors.IsInvalid(err) || apierrors.IsRequestEntityTooLargeError(err) ||
		apierrors.IsForbidden(err) || apierrors.IsBadRequest(err)
}

func ignoredNote(e ignoredExemption) string {
	if e.reason == v1alpha1.ExemptionMadeByAgent {
		who := "an agent"
		if e.by != "" {
			who = "agent " + e.by
		}
		return fmt.Sprintf("budget exemption %s was made by %s and does not count: break-glass is for people", e.name, who)
	}
	return fmt.Sprintf("budget exemption %s has no actor stamp, so nothing verified a person made it: it does not count "+
		"(kardinal override --budget stamps it)", e.name)
}

// unchanged reports whether next is old but for the times each evaluation
// moves (evaluatedAt, windowStart, nextEvaluationAt): a re-run writes
// nothing.
func unchanged(old, next *v1alpha1.AgentUsage) bool {
	if old == nil || next == nil {
		return old == nil && next == nil
	}
	return equality.Semantic.DeepEqual(clockless(old), clockless(next))
}

func clockless(u *v1alpha1.AgentUsage) *v1alpha1.AgentUsage {
	c := u.DeepCopy()
	c.EvaluatedAt, c.NextEvaluationAt = nil, nil
	for i := range c.Rules {
		for _, n := range []*v1alpha1.AgentBudgetCount{c.Rules[i].Versions, c.Rules[i].FleetTargets} {
			if n != nil {
				n.WindowStart = nil
			}
		}
	}
	return c
}

// facts reads what one evaluation needs from the cache: the requests, the
// live state of each grant, the steps to seed and the sizes.
func (r *Reconciler) facts(ctx context.Context, id *v1alpha1.AgentIdentity, old *v1alpha1.AgentUsage) (facts, error) {
	f := facts{grants: map[grantKey]grantState{}, maxBytes: r.MaxObjectBytes}
	if len(id.Spec.BudgetExemptions) > 0 && r.ExemptionPolicy != nil {
		f.stampsVerified = r.ExemptionPolicy.Active(ctx)
	}
	if f.maxBytes == 0 {
		f.maxBytes = DefaultMaxObjectBytes
	}
	rd := &reads{c: r.Client, bundles: map[types.NamespacedName]*v1alpha1.Bundle{}, byUID: map[string]*v1alpha1.Bundle{},
		pipelines: map[types.NamespacedName]*v1alpha1.Pipeline{}, steps: map[types.NamespacedName][]v1alpha1.PromotionStep{}}

	// The steps that asked (status.budget names this agent).
	var asked v1alpha1.PromotionStepList
	if err := r.Client.List(ctx, &asked, client.MatchingFields{IndexStepBudgetAgent: id.Name}); err != nil {
		return f, fmt.Errorf("list the steps of agent %s: %w", id.Name, err)
	}
	live := map[grantKey]*v1alpha1.PromotionStep{}
	for i := range asked.Items {
		s := &asked.Items[i]
		b, err := rd.bundle(ctx, s.Namespace, s.Spec.BundleName)
		if err != nil {
			return f, err
		}
		if b == nil || !ownedBy(b, id.Name) || (s.Labels[graph.LabelBundleUID] != "" && s.Labels[graph.LabelBundleUID] != string(b.UID)) {
			continue // not this agent's version (or a step of a deleted one)
		}
		k := grantKey{string(b.UID), s.Spec.Environment}
		live[k] = s
		if s.Status.Budget == nil || s.Status.Budget.State != "Waiting" || startedState(s.Status.State) {
			continue
		}
		q := request{key: k, fleet: s.Labels[graph.LabelFleet], namespace: s.Namespace, pipeline: b.Spec.Pipeline,
			bundle: b.Name, name: s.Name, created: s.CreationTimestamp.Time}
		if s.Status.Budget.Since != nil {
			q.since = s.Status.Budget.Since.Time
		}
		if q.fleet != "" {
			if q.targets, err = rd.fleetSize(ctx, s.Namespace, b.Spec.Pipeline, q.fleet); err != nil {
				return f, err
			}
		}
		f.requests = append(f.requests, q)
	}

	// The live state of each grant.
	if old != nil {
		for _, g := range old.Grants {
			k := grantKey{g.BundleUID, g.Environment}
			st := grantState{}
			b, err := rd.bundleByUID(ctx, g.BundleUID)
			if err != nil {
				return f, err
			}
			if b != nil {
				st.bundleFound, st.namespace = true, b.Namespace
				s := live[k]
				if s == nil {
					// A seeded grant's step, or one recreated since: by its
					// Bundle's label.
					if s, err = rd.stepOf(ctx, b, g.Environment); err != nil {
						return f, err
					}
				}
				if s != nil {
					st.stepFound, st.stepState = true, s.Status.State
				}
				if st.current, err = rd.current(ctx, b, g.Environment); err != nil {
					return f, err
				}
			}
			f.grants[k] = st
		}
	}

	// Seeding: on a new ledger or a spec change, the agent's steps that
	// started without a grant (section 17.2).
	if old == nil || old.ObservedGeneration != id.Generation {
		var bundles v1alpha1.BundleList
		if err := r.Client.List(ctx, &bundles, client.MatchingFields{IndexBundleAgent: id.Name}); err != nil {
			return f, fmt.Errorf("list the bundles of agent %s: %w", id.Name, err)
		}
		for i := range bundles.Items {
			b := &bundles.Items[i]
			if !ownedBy(b, id.Name) {
				continue
			}
			steps, err := rd.stepsOf(ctx, b)
			if err != nil {
				return f, err
			}
			for j := range steps {
				s := &steps[j]
				if !startedState(s.Status.State) {
					continue
				}
				f.seeds = append(f.seeds, request{key: grantKey{string(b.UID), s.Spec.Environment},
					fleet: s.Labels[graph.LabelFleet], namespace: b.Namespace, pipeline: b.Spec.Pipeline, bundle: b.Name,
					name: s.Name, started: startedAt(s), settled: settledState(s.Status.State)})
			}
		}
	}

	// The sizes: the agent's other ledgers, and the object without this one.
	rest := id.DeepCopy()
	for name, u := range rest.Status.Usage {
		if name != r.Ledger {
			f.otherGrants += len(u.Grants)
		}
	}
	delete(rest.Status.Usage, r.Ledger)
	rest.ManagedFields = nil
	b, err := json.Marshal(rest)
	if err != nil {
		return f, fmt.Errorf("size agent identity %s: %w", id.Name, err)
	}
	f.baseBytes = len(b)
	return f, nil
}

// ownedBy reports whether b is a version agent created (its admission
// verified, immutable spec.actor).
func ownedBy(b *v1alpha1.Bundle, agent string) bool {
	return b.Spec.Actor != nil && b.Spec.Actor.Kind == v1alpha1.ActorAgent && b.Spec.Actor.Agent == agent
}

// startedAt is when a step started: its first step's start, else its last
// state change, else its creation.
func startedAt(s *v1alpha1.PromotionStep) time.Time {
	var first *time.Time
	for i := range s.Status.Steps {
		if t := s.Status.Steps[i].StartedAt; t != nil && (first == nil || t.Time.Before(*first)) {
			tt := t.Time
			first = &tt
		}
	}
	switch {
	case first != nil:
		return *first
	case s.Status.StateTransitionTime != nil:
		return s.Status.StateTransitionTime.Time
	}
	return s.CreationTimestamp.Time
}

// reads caches one evaluation's reads.
type reads struct {
	c         client.Client
	bundles   map[types.NamespacedName]*v1alpha1.Bundle
	byUID     map[string]*v1alpha1.Bundle
	pipelines map[types.NamespacedName]*v1alpha1.Pipeline
	steps     map[types.NamespacedName][]v1alpha1.PromotionStep
}

func (rd *reads) bundle(ctx context.Context, ns, name string) (*v1alpha1.Bundle, error) {
	k := types.NamespacedName{Namespace: ns, Name: name}
	if b, ok := rd.bundles[k]; ok {
		return b, nil
	}
	var b v1alpha1.Bundle
	if err := rd.c.Get(ctx, k, &b); err != nil {
		if apierrors.IsNotFound(err) {
			rd.bundles[k] = nil
			return nil, nil
		}
		return nil, fmt.Errorf("get bundle %s: %w", k, err)
	}
	rd.bundles[k] = &b
	return &b, nil
}

func (rd *reads) bundleByUID(ctx context.Context, uid string) (*v1alpha1.Bundle, error) {
	if b, ok := rd.byUID[uid]; ok {
		return b, nil
	}
	var list v1alpha1.BundleList
	if err := rd.c.List(ctx, &list, client.MatchingFields{IndexBundleUID: uid}); err != nil {
		return nil, fmt.Errorf("list bundle %s: %w", uid, err)
	}
	var b *v1alpha1.Bundle
	if len(list.Items) > 0 {
		b = &list.Items[0]
	}
	rd.byUID[uid] = b
	return b, nil
}

func (rd *reads) pipeline(ctx context.Context, ns, name string) (*v1alpha1.Pipeline, error) {
	k := types.NamespacedName{Namespace: ns, Name: name}
	if p, ok := rd.pipelines[k]; ok {
		return p, nil
	}
	var p v1alpha1.Pipeline
	if err := rd.c.Get(ctx, k, &p); err != nil {
		if apierrors.IsNotFound(err) {
			rd.pipelines[k] = nil
			return nil, nil
		}
		return nil, fmt.Errorf("get pipeline %s: %w", k, err)
	}
	rd.pipelines[k] = &p
	return &p, nil
}

// stepsOf are Bundle b's steps (its kardinal.io/bundle label, and its UID
// when the step carries one).
func (rd *reads) stepsOf(ctx context.Context, b *v1alpha1.Bundle) ([]v1alpha1.PromotionStep, error) {
	k := types.NamespacedName{Namespace: b.Namespace, Name: b.Name}
	if s, ok := rd.steps[k]; ok {
		return s, nil
	}
	var list v1alpha1.PromotionStepList
	if err := rd.c.List(ctx, &list, client.InNamespace(b.Namespace), client.MatchingLabels{lifecycle.LabelBundle: b.Name}); err != nil {
		return nil, fmt.Errorf("list the steps of bundle %s: %w", k, err)
	}
	out := list.Items[:0]
	for _, s := range list.Items {
		if uid := s.Labels[graph.LabelBundleUID]; uid == "" || uid == string(b.UID) {
			out = append(out, s)
		}
	}
	rd.steps[k] = out
	return out, nil
}

func (rd *reads) stepOf(ctx context.Context, b *v1alpha1.Bundle, env string) (*v1alpha1.PromotionStep, error) {
	steps, err := rd.stepsOf(ctx, b)
	if err != nil {
		return nil, err
	}
	for i := range steps {
		if steps[i].Spec.Environment == env {
			return &steps[i], nil
		}
	}
	return nil, nil
}

// current reports whether b's Pipeline still runs b in env: its
// status.environments entry names b. A deleted Pipeline does not.
func (rd *reads) current(ctx context.Context, b *v1alpha1.Bundle, env string) (bool, error) {
	p, err := rd.pipeline(ctx, b.Namespace, b.Spec.Pipeline)
	if err != nil || p == nil {
		return false, err
	}
	for _, e := range p.Status.Environments {
		if e.Name == env {
			return e.Bundle == b.Name, nil
		}
	}
	return false, nil
}

// fleetSize is how many targets fleet has in the Pipeline now (section 8.1:
// a fleet grown since translation uses its new size); 0 when unknown.
func (rd *reads) fleetSize(ctx context.Context, ns, pipeline, fleet string) (int, error) {
	p, err := rd.pipeline(ctx, ns, pipeline)
	if err != nil || p == nil {
		return 0, err
	}
	targets, err := graph.FleetTargetEnvironments(p)
	if err != nil {
		return 0, nil // an invalid fleet: unknown, so fleetShare holds
	}
	return len(targets[fleet]), nil
}

// Ledger works out the ledger an installation counts in, and whether this
// controller writes it (section 15.2): ledger is --agent-budget-ledger
// (default release, the controller's --agent-index), and only the
// controller whose release is the ledger, and that owns cluster-scoped kinds
// (the default shard), writes it; every other shard reads it.
func Ledger(ledger, release string, ownsClusterScoped bool) (string, bool, error) {
	if ledger == "" {
		ledger = release
	}
	switch {
	case ledger == "":
		return "", false, errors.New("--agent-budgets needs --agent-index or --agent-budget-ledger")
	case len(validation.IsDNS1123Label(ledger)) > 0:
		return "", false, fmt.Errorf("--agent-budget-ledger %q: %s", ledger, strings.Join(validation.IsDNS1123Label(ledger), "; "))
	}
	return ledger, ledger == release && ownsClusterScoped, nil
}

// SetupWithManager registers the reconciler, its field indexes and its
// watches. The caller registers it only with budgets on, in the controller
// that owns cluster-scoped kinds and whose release is the ledger (section
// 15.2).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Ledger == "" {
		return errors.New("agent budgets: no ledger name")
	}
	ctx := context.Background()
	idx := mgr.GetFieldIndexer()
	for _, register := range []func(context.Context, client.FieldIndexer) error{
		IndexStepsByBudgetAgent, IndexBundlesByUID, IndexBundlesByAgent, IndexAgentsWithUnavailable} {
		if err := register(ctx, idx); err != nil {
			return err
		}
	}
	b := ctrl.NewControllerManagedBy(mgr).Named("agentbudget").
		WithOptions(controller.Options{MaxConcurrentReconciles: 2}).
		// The spec (budgets, exemptions), not the ledger's own writes.
		For(&v1alpha1.AgentIdentity{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		// A request, a grant's step settling or failing.
		Watches(&v1alpha1.PromotionStep{}, handler.EnqueueRequestsFromMapFunc(agentOfStep),
			builder.WithPredicates(stepBudgetChanged)).
		// An environment moving on releases maxUnavailable: the agents that
		// hold an unavailable grant (the ledger names no Pipeline).
		Watches(&v1alpha1.Pipeline{}, handler.EnqueueRequestsFromMapFunc(r.agentsWithUnavailable),
			builder.WithPredicates(environmentsChanged))
	return shard.Active().Complete(b, tracing.WrapReconciler("agentbudget", r), &v1alpha1.AgentIdentityList{})
}

func agentOfStep(_ context.Context, o client.Object) []reconcile.Request {
	s, ok := o.(*v1alpha1.PromotionStep)
	if !ok || s.Status.Budget == nil || s.Status.Budget.Agent == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: s.Status.Budget.Agent}}}
}

func (r *Reconciler) agentsWithUnavailable(ctx context.Context, _ client.Object) []reconcile.Request {
	var list v1alpha1.AgentIdentityList
	if err := r.Client.List(ctx, &list, client.MatchingFields{IndexUnavailableLedgers: r.Ledger}); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("list the agents with an unavailable grant")
		return nil
	}
	out := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Name: list.Items[i].Name}})
	}
	return out
}

// stepBudgetChanged passes the step events the ledger depends on: a step
// with status.budget created or deleted, and a change of its state or of
// its status.budget.
var stepBudgetChanged = predicate.Funcs{
	CreateFunc: func(e event.CreateEvent) bool { return hasBudget(e.Object) },
	DeleteFunc: func(e event.DeleteEvent) bool { return hasBudget(e.Object) },
	UpdateFunc: func(e event.UpdateEvent) bool {
		o, ok := e.ObjectOld.(*v1alpha1.PromotionStep)
		n, ok2 := e.ObjectNew.(*v1alpha1.PromotionStep)
		if !ok || !ok2 || (o.Status.Budget == nil && n.Status.Budget == nil) {
			return false
		}
		return o.Status.State != n.Status.State || !equality.Semantic.DeepEqual(o.Status.Budget, n.Status.Budget)
	},
	GenericFunc: func(event.GenericEvent) bool { return false },
}

func hasBudget(o client.Object) bool {
	s, ok := o.(*v1alpha1.PromotionStep)
	return ok && s.Status.Budget != nil
}

// environmentsChanged passes a Pipeline whose status.environments changed.
var environmentsChanged = predicate.Funcs{
	CreateFunc: func(event.CreateEvent) bool { return false },
	DeleteFunc: func(event.DeleteEvent) bool { return true },
	UpdateFunc: func(e event.UpdateEvent) bool {
		o, ok := e.ObjectOld.(*v1alpha1.Pipeline)
		n, ok2 := e.ObjectNew.(*v1alpha1.Pipeline)
		return !ok || !ok2 || !equality.Semantic.DeepEqual(o.Status.Environments, n.Status.Environments)
	},
	GenericFunc: func(event.GenericEvent) bool { return false },
}
