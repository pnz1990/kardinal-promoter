// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agentbudget

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

const ledgerName = "kardinal-promoter"

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

// world is a fake cluster: one agent, its Bundles, Pipeline and steps.
type world struct {
	t       *testing.T
	c       client.WithWatch
	patches atomic.Int32
}

func newWorld(t *testing.T, refuse error, objs ...client.Object) *world {
	t.Helper()
	w := &world{t: t}
	w.c = fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.AgentIdentity{}, &v1alpha1.PromotionStep{}, &v1alpha1.Pipeline{}).
		WithIndex(&v1alpha1.PromotionStep{}, IndexStepBudgetAgent, StepBudgetAgent).
		WithIndex(&v1alpha1.Bundle{}, IndexBundleUID, BundleUID).
		WithIndex(&v1alpha1.Bundle{}, IndexBundleAgent, BundleAgent).
		WithIndex(&v1alpha1.AgentIdentity{}, IndexUnavailableLedgers, UnavailableLedgers).
		WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string,
			obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if _, ok := obj.(*v1alpha1.AgentIdentity); ok {
				w.patches.Add(1)
				// The refusal is for the ledger's writes (they carry
				// grants or rules), not a condition-only write.
				if data, _ := patch.Data(obj); refuse != nil && strings.Contains(string(data), `"rules"`) {
					return refuse
				}
			}
			return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
		}}).Build()
	return w
}

func (w *world) reconciler(now time.Time) *Reconciler {
	return &Reconciler{Client: w.c, APIReader: w.c, Ledger: ledgerName, NowFn: func() time.Time { return now }}
}

func (w *world) reconcile(r *Reconciler) reconcile.Result {
	w.t.Helper()
	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "fixer"}})
	require.NoError(w.t, err)
	return res
}

func (w *world) usage() *v1alpha1.AgentUsage {
	w.t.Helper()
	var id v1alpha1.AgentIdentity
	require.NoError(w.t, w.c.Get(context.Background(), types.NamespacedName{Name: "fixer"}, &id))
	u, ok := id.Status.Usage[ledgerName]
	if !ok {
		return nil
	}
	return &u
}

func (w *world) setState(name, state string) {
	w.t.Helper()
	var s v1alpha1.PromotionStep
	require.NoError(w.t, w.c.Get(context.Background(), types.NamespacedName{Namespace: "payments", Name: name}, &s))
	s.Status.State = state
	if s.Status.Budget != nil && state != "Pending" {
		s.Status.Budget.State = "Granted"
	}
	require.NoError(w.t, w.c.Status().Update(context.Background(), &s))
}

func identity(rules ...v1alpha1.AgentBudgetRule) *v1alpha1.AgentIdentity {
	id := agent(rules...)
	id.Spec.Subjects = []v1alpha1.AgentSubject{{Kind: "User", Name: "oidc:fixer"}}
	id.Spec.Sponsor = v1alpha1.AgentSponsor{User: "alice"}
	return id
}

func bundle(name, uid string) *v1alpha1.Bundle {
	return &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "payments", UID: types.UID(uid)},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "checkout",
			Actor: &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "oidc:fixer", Agent: "fixer", Sponsor: "alice"}}}
}

// step is b's step into env; waiting since at when at is not zero.
func step(b *v1alpha1.Bundle, env, state string, at time.Time) *v1alpha1.PromotionStep {
	s := &v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Name: b.Name + "-" + env, Namespace: "payments",
		CreationTimestamp: metav1.NewTime(at),
		Labels:            map[string]string{lifecycle.LabelBundle: b.Name, graph.LabelBundleUID: string(b.UID)}},
		Spec:   v1alpha1.PromotionStepSpec{PipelineName: "checkout", BundleName: b.Name, Environment: env, StepType: "kustomize-set-image"},
		Status: v1alpha1.PromotionStepStatus{State: state}}
	if !at.IsZero() {
		since := metav1.NewTime(at)
		s.Status.Budget = &v1alpha1.PromotionStepBudget{Agent: "fixer", State: "Waiting", Since: &since}
	}
	return s
}

func pipeline(envs ...v1alpha1.PipelineEnvironmentStatus) *v1alpha1.Pipeline {
	return &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "payments"},
		Status: v1alpha1.PipelineStatus{Environments: envs}}
}

func grantedUIDs(u *v1alpha1.AgentUsage) []string {
	var out []string
	if u == nil {
		return nil
	}
	for _, g := range u.Grants {
		out = append(out, g.BundleUID)
	}
	return out
}

// TestReconcile: the reconciler grants the oldest request within the limit,
// writes nothing when nothing changed, and grants the next one once the
// first settles.
// Covers BUDGET-LEDGER-01.
func TestReconcile(t *testing.T) {
	r := rule("prod", "prod")
	r.MaxConcurrent = 1
	b1, b2 := bundle("v1", "u-1"), bundle("v2", "u-2")
	w := newWorld(t, nil, identity(r), b1, b2, pipeline(),
		step(b1, "prod", "Pending", t0), step(b2, "prod", "Pending", t0.Add(time.Second)))
	rec := w.reconciler(t0.Add(time.Minute))
	w.reconcile(rec)
	assert.Equal(t, []string{"u-1"}, grantedUIDs(w.usage()))
	assert.Equal(t, int32(1), w.patches.Load())
	u := ruleUsage(t, w.usage(), "prod")
	assert.Equal(t, int32(1), u.Waiting)
	assert.True(t, meta.IsStatusConditionTrue(w.usage().Conditions, ConditionCurrent))

	w.setState("v1-prod", "Promoting")
	w.reconcile(w.reconciler(t0.Add(2 * time.Minute)))
	assert.Equal(t, int32(1), w.patches.Load(), "a re-run with nothing new writes nothing")

	w.setState("v1-prod", "Verified")
	w.reconcile(w.reconciler(t0.Add(3 * time.Minute)))
	assert.Equal(t, []string{"u-2"}, grantedUIDs(w.usage()), "the settled grant of a rule with no window is dropped")
}

// TestReconcileStaleRead: a reconcile that read a stale ledger (another
// leader granted since) gets Conflict on its write, re-reads, and grants no
// more than the limit (section 7.3 step 6).
// Covers BUDGET-LEDGER-01.
func TestReconcileStaleRead(t *testing.T) {
	r := rule("prod", "prod")
	r.MaxConcurrent = 1
	bx, b1 := bundle("vx", "u-x"), bundle("v1", "u-1")
	id := identity(r)
	// vx's step was granted and has not started yet, so seeding cannot
	// find it: only the ledger knows the place is taken.
	sx := step(bx, "prod-x", "Pending", t0)
	sx.Status.Budget.State = "Granted"
	w := newWorld(t, nil, id, bx, b1, pipeline(), sx, step(b1, "prod", "Pending", t0.Add(time.Second)))
	rx := r
	rx.Environments = []string{"prod", "prod-x"}
	require.NoError(t, w.c.Get(context.Background(), types.NamespacedName{Name: "fixer"}, id))
	stale := id.DeepCopy()
	// Another leader granted vx meanwhile.
	id.Spec.Budgets = []v1alpha1.AgentBudgetRule{rx}
	require.NoError(t, w.c.Update(context.Background(), id))
	require.NoError(t, w.c.Get(context.Background(), types.NamespacedName{Name: "fixer"}, id))
	id.Status.Usage = map[string]v1alpha1.AgentUsage{ledgerName: {ObservedGeneration: id.Generation, Grants: []v1alpha1.AgentBudgetGrant{
		{BundleUID: "u-x", Environment: "prod-x", Rules: []string{"prod"}, GrantedAt: metav1.NewTime(t0)}}}}
	require.NoError(t, w.c.Status().Update(context.Background(), id))
	stale.Spec.Budgets = []v1alpha1.AgentBudgetRule{rx}

	rec := w.reconciler(t0.Add(time.Minute))
	rec.Client = &staleReader{Client: w.c, id: stale}
	w.reconcile(rec)
	assert.Equal(t, []string{"u-x"}, grantedUIDs(w.usage()), "v1 waits: the write from the stale read was refused")
	assert.GreaterOrEqual(t, w.patches.Load(), int32(1))
}

// staleReader answers the AgentIdentity from an old copy, as a lagging
// cache does.
type staleReader struct {
	client.Client
	id *v1alpha1.AgentIdentity
}

func (s *staleReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if id, ok := obj.(*v1alpha1.AgentIdentity); ok && s.id != nil {
		s.id.DeepCopyInto(id)
		return nil
	}
	return s.Client.Get(ctx, key, obj, opts...)
}

// TestReconcileRemovesTheLedger: budgets removed, the ledger goes; another
// installation's ledger stays.
// Covers BUDGET-LEDGER-01.
func TestReconcileRemovesTheLedger(t *testing.T) {
	id := identity()
	id.Status.Usage = map[string]v1alpha1.AgentUsage{ledgerName: {ObservedGeneration: 1},
		"team-b": {ObservedGeneration: 1, Grants: []v1alpha1.AgentBudgetGrant{{BundleUID: "u-b", Environment: "prod",
			GrantedAt: metav1.NewTime(t0)}}}}
	w := newWorld(t, nil, id)
	w.reconcile(w.reconciler(t0))
	var got v1alpha1.AgentIdentity
	require.NoError(t, w.c.Get(context.Background(), types.NamespacedName{Name: "fixer"}, &got))
	assert.NotContains(t, got.Status.Usage, ledgerName)
	assert.Contains(t, got.Status.Usage, "team-b")
}

// TestReconcileEvents: an exemption an agent made is ignored with one
// Warning event, not repeated on a re-run; a refused write is a Warning
// event and an error.
// Covers BUDGET-LEDGER-02.
func TestReconcileEvents(t *testing.T) {
	r := rule("prod", "prod")
	r.MaxConcurrent = 1
	id := identity(r)
	id.Spec.BudgetExemptions = []v1alpha1.AgentBudgetExemption{{Name: "inc-1", Namespace: "payments", Pipeline: "checkout",
		Environment: "prod", Reason: "r", ExpiresAt: metav1.NewTime(t0.Add(time.Hour)), CreatedBy: "oidc:other",
		Actor: &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "oidc:other", Agent: "other", Sponsor: "carol"}}}
	w := newWorld(t, nil, id)
	rec := w.reconciler(t0)
	fr := events.NewFakeRecorder(10)
	rec.Recorder = fr
	w.reconcile(rec)
	require.Len(t, fr.Events, 1)
	assert.Contains(t, <-fr.Events, "budget exemption inc-1 was made by agent other and does not count")
	w.reconcile(rec)
	assert.Empty(t, fr.Events, "not repeated")
	assert.Equal(t, v1alpha1.ExemptionMadeByAgent, w.usage().Exemptions[0].Ignored)

	// A refused write: a Warning, a terminal error (retried on the next
	// event, not with backoff forever), and LedgerFull WriteRefused in the
	// ledger when it exists.
	refused := apierrors.NewInvalid(schema.GroupKind{Group: "kardinal.io", Kind: "AgentIdentity"}, "fixer", nil)
	existing := identity(r)
	existing.Status.Usage = map[string]v1alpha1.AgentUsage{ledgerName: {ObservedGeneration: 0}}
	w2 := newWorld(t, refused, existing)
	rec2 := w2.reconciler(t0)
	rec2.Recorder = fr
	_, err := rec2.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "fixer"}})
	require.Error(t, err)
	assert.ErrorIs(t, err, reconcile.TerminalError(nil), "not retried with backoff")
	require.Len(t, fr.Events, 1)
	assert.Contains(t, <-fr.Events, "ledger kardinal-promoter could not be written")
	c := meta.FindStatusCondition(w2.usage().Conditions, ConditionLedgerFull)
	require.NotNil(t, c, "the ledger says why it stopped granting")
	assert.Equal(t, FullWriteRefused, c.Reason)

	// No ledger yet (a 17th): nothing to write the condition in.
	w3 := newWorld(t, refused, identity(r))
	_, err = w3.reconciler(t0).Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "fixer"}})
	assert.ErrorIs(t, err, reconcile.TerminalError(nil))
	assert.Nil(t, w3.usage())
}

// TestReconcileRequeuesUnchanged: a re-run that writes nothing still
// requeues for when room frees, so a waiting request is granted then
// (QA-3 on #2456).
// Covers BUDGET-LEDGER-01.
func TestReconcileRequeuesUnchanged(t *testing.T) {
	r := rule("prod", "prod")
	r.VersionsPerDay = 1
	b1, b2 := bundle("v1", "u-1"), bundle("v2", "u-2")
	w := newWorld(t, nil, identity(r), b1, b2, pipeline(),
		step(b1, "prod", "Pending", t0), step(b2, "prod", "Pending", t0.Add(time.Second)))
	w.reconcile(w.reconciler(t0))
	w.setState("v1-prod", "Verified")
	w.reconcile(w.reconciler(t0.Add(time.Minute)))
	patches := w.patches.Load()
	res := w.reconcile(w.reconciler(t0.Add(time.Hour)))
	assert.Equal(t, patches, w.patches.Load(), "unchanged: no write")
	assert.Equal(t, 23*time.Hour, res.RequeueAfter, "v2 waits until v1's grant leaves the 24 hour window")
}

// TestOwnedBy: a step is a request only when its Bundle's admission-verified
// actor is this agent: kind agent, not merely the agent's name (QA-3 on
// #2456).
// Covers BUDGET-LEDGER-01.
func TestOwnedBy(t *testing.T) {
	b := bundle("v1", "u-1")
	assert.True(t, ownedBy(b, "fixer"))
	assert.False(t, ownedBy(b, "other"))
	b.Spec.Actor.Kind = v1alpha1.ActorHuman
	assert.False(t, ownedBy(b, "fixer"), "a person's version is never the agent's")
	b.Spec.Actor = nil
	assert.False(t, ownedBy(b, "fixer"))

	r := rule("prod", "prod")
	r.MaxConcurrent = 1
	person := bundle("v2", "u-2")
	person.Spec.Actor.Kind = v1alpha1.ActorHuman
	w := newWorld(t, nil, identity(r), person, pipeline(), step(person, "prod", "Pending", t0))
	w.reconcile(w.reconciler(t0))
	assert.Empty(t, grantedUIDs(w.usage()), "a person's step is not a request")
}

// TestReconcileSeedsAndUnavailable: a new ledger seeds the agent's started
// steps (no status.budget); a failed one whose environment still runs it
// holds maxUnavailable until the environment moves on.
// Covers BUDGET-LEDGER-03.
func TestReconcileSeedsAndUnavailable(t *testing.T) {
	r := rule("prod", "prod")
	r.MaxUnavailable, r.VersionsPerDay = 1, 5
	b1, b2 := bundle("v1", "u-1"), bundle("v2", "u-2")
	failed := step(b1, "prod", "Failed", time.Time{})
	failed.CreationTimestamp = metav1.NewTime(t0.Add(-time.Hour))
	w := newWorld(t, nil, identity(r), b1, b2, failed,
		pipeline(v1alpha1.PipelineEnvironmentStatus{Name: "prod", Bundle: "v1"}), step(b2, "prod-b", "Pending", t0))
	res := w.reconcile(w.reconciler(t0))
	u := w.usage()
	require.Len(t, u.Grants, 1)
	assert.True(t, u.Grants[0].Seeded)
	assert.Equal(t, "u-1", u.Grants[0].BundleUID)
	assert.NotZero(t, res.RequeueAfter, "a seeded grant settled in a version window is re-read")

	// The next reconcile marks it unavailable; the index finds the agent.
	w.reconcile(w.reconciler(t0.Add(time.Minute)))
	assert.True(t, w.usage().Grants[0].Unavailable)
	var ids v1alpha1.AgentIdentityList
	require.NoError(t, w.c.List(context.Background(), &ids, client.MatchingFields{IndexUnavailableLedgers: ledgerName}))
	require.Len(t, ids.Items, 1)
	rec := w.reconciler(t0)
	assert.Len(t, rec.agentsWithUnavailable(context.Background(), &v1alpha1.Pipeline{}), 1)

	// A newer version runs there now.
	var p v1alpha1.Pipeline
	require.NoError(t, w.c.Get(context.Background(), types.NamespacedName{Namespace: "payments", Name: "checkout"}, &p))
	p.Status.Environments[0].Bundle = "v3"
	require.NoError(t, w.c.Status().Update(context.Background(), &p))
	w.reconcile(w.reconciler(t0.Add(2 * time.Minute)))
	assert.False(t, w.usage().Grants[0].Unavailable)
}

// TestLedgerWriter: only the controller whose release is the ledger, and
// that owns cluster-scoped kinds, writes it; the ledger defaults to the
// release (section 15.2).
// Covers BUDGET-LEDGER-03.
func TestLedgerWriter(t *testing.T) {
	for _, tc := range []struct {
		name, ledger, release string
		owns                  bool
		want                  string
		writes                bool
		err                   bool
	}{
		{"the default shard of an unsharded release", "", "kardinal-promoter", true, "kardinal-promoter", true, false},
		{"the default shard naming its own release", "kardinal-promoter", "kardinal-promoter", true, "kardinal-promoter", true, false},
		{"another shard of the installation", "kardinal-promoter", "kardinal-payments", false, "kardinal-promoter", false, false},
		{"a shard whose release is the ledger but owns no cluster-scoped kinds", "kardinal-promoter", "kardinal-promoter", false, "kardinal-promoter", false, false},
		{"no release and no ledger", "", "", true, "", false, true},
		{"a ledger that is not a DNS label", "Kardinal", "kardinal-promoter", true, "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, writes, err := Ledger(tc.ledger, tc.release, tc.owns)
			if tc.err {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.writes, writes)
		})
	}
}

// TestWatches: the step and Pipeline events that reach the ledger.
// Covers BUDGET-LEDGER-01.
func TestWatches(t *testing.T) {
	b := bundle("v1", "u-1")
	waiting := step(b, "prod", "Pending", t0)
	plain := step(b, "uat", "Pending", time.Time{})
	assert.Equal(t, []reconcile.Request{{NamespacedName: types.NamespacedName{Name: "fixer"}}}, agentOfStep(context.Background(), waiting))
	assert.Empty(t, agentOfStep(context.Background(), plain))
	assert.True(t, stepBudgetChanged.Create(event.CreateEvent{Object: waiting}))
	assert.False(t, stepBudgetChanged.Create(event.CreateEvent{Object: plain}))
	moved := waiting.DeepCopy()
	moved.Status.State = "Promoting"
	assert.True(t, stepBudgetChanged.Update(event.UpdateEvent{ObjectOld: waiting, ObjectNew: moved}))
	msg := waiting.DeepCopy()
	msg.Status.Message = "waiting for agent fixer's budget"
	assert.False(t, stepBudgetChanged.Update(event.UpdateEvent{ObjectOld: waiting, ObjectNew: msg}), "a message is not a change")
	assert.False(t, stepBudgetChanged.Update(event.UpdateEvent{ObjectOld: plain, ObjectNew: plain.DeepCopy()}))

	p := pipeline(v1alpha1.PipelineEnvironmentStatus{Name: "prod", Bundle: "v1"})
	q := p.DeepCopy()
	assert.False(t, environmentsChanged.Update(event.UpdateEvent{ObjectOld: p, ObjectNew: q}))
	q.Status.Environments[0].Bundle = "v2"
	assert.True(t, environmentsChanged.Update(event.UpdateEvent{ObjectOld: p, ObjectNew: q}))
}

type policyState bool

func (p policyState) Active(context.Context) bool { return bool(p) }

// TestReconcileStampPolicy: a person's stamped exemption counts only while
// the admission policies that check stamps are in force; every one of them
// (AllPolicies); none configured is never (QA-3 on #2456).
// Covers BUDGET-LEDGER-02.
func TestReconcileStampPolicy(t *testing.T) {
	assert.False(t, AllPolicies{}.Active(context.Background()), "no policy configured")
	assert.False(t, AllPolicies{policyState(true), policyState(false)}.Active(context.Background()))
	assert.True(t, AllPolicies{policyState(true), policyState(true)}.Active(context.Background()))

	for _, tc := range []struct {
		name    string
		policy  PolicyCheck
		ignored string
	}{
		{"in force", policyState(true), ""},
		{"not in force", policyState(false), v1alpha1.ExemptionUnverified},
		{"not configured", nil, v1alpha1.ExemptionUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := rule("prod", "prod")
			r.MaxConcurrent = 1
			id := identity(r)
			id.Spec.BudgetExemptions = []v1alpha1.AgentBudgetExemption{{Name: "inc-1", Namespace: "payments", Pipeline: "checkout",
				Environment: "prod", Reason: "r", ExpiresAt: metav1.NewTime(t0.Add(time.Hour)), CreatedBy: "bob",
				Actor: &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "bob"}}}
			w := newWorld(t, nil, id)
			rec := w.reconciler(t0)
			rec.ExemptionPolicy = tc.policy
			w.reconcile(rec)
			require.Len(t, w.usage().Exemptions, 1)
			assert.Equal(t, tc.ignored, w.usage().Exemptions[0].Ignored)
		})
	}
}
