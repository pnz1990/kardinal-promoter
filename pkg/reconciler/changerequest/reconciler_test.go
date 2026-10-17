// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package changerequest_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changerequest"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone/objectgonetest"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/slots"
)

var t0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

const (
	ns     = "checkout"
	uid    = "uid-1"
	corrID = "kardinal:checkout/app-v1/prod/uid-1"
)

// ticketing is a fake ticketing system: tickets by key, with the
// correlation id each carries. Hooks make a call fail, or lose a create's
// response after the ticket was made.
type ticketing struct {
	mu      sync.Mutex
	tickets map[string]*changerequest.Reading
	order   []string
	calls   map[string]int // by method
	// createErr, when set, is returned by Create; with createdAnyway the
	// ticket is created first (a lost response).
	createErr     error
	createdAnyway bool
	// err, when set, is returned by every call.
	err error
	// searchLags hides tickets from Find (a search index behind).
	searchLags bool
}

func newTicketing() *ticketing {
	return &ticketing{tickets: map[string]*changerequest.Reading{}, calls: map[string]int{}}
}

func (f *ticketing) add(key, corr, state string) {
	f.tickets[key] = &changerequest.Reading{Ticket: v1alpha1.ChangeRequestTicket{Key: key, ID: "id-" + key,
		URL: "https://example.service-now.com/" + key}, State: state, ProviderState: "raw " + state, CorrelationID: corr,
		Fields: map[string]string{"cmdb_ci": "checkout"}}
	f.order = append(f.order, key)
}

func (f *ticketing) set(key, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tickets[key].State, f.tickets[key].ProviderState = state, "raw "+state
}

func (f *ticketing) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

func (f *ticketing) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls["Create"] + f.calls["Find"] + f.calls["Get"]
}

func (f *ticketing) Create(_ context.Context, req changerequest.Request) (changerequest.Reading, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["Create"]++
	if f.err != nil {
		return changerequest.Reading{}, f.err
	}
	if f.createErr != nil && !f.createdAnyway {
		return changerequest.Reading{}, f.createErr
	}
	key := fmt.Sprintf("CHG%07d", len(f.order)+1)
	f.add(key, req.CorrelationID, v1alpha1.ChangeRequestPending)
	if f.createErr != nil {
		return changerequest.Reading{}, f.createErr
	}
	return *f.tickets[key], nil
}

func (f *ticketing) Find(_ context.Context, req changerequest.Request) (changerequest.Reading, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["Find"]++
	if f.err != nil {
		return changerequest.Reading{}, false, f.err
	}
	if f.searchLags {
		return changerequest.Reading{}, false, nil
	}
	for _, k := range f.order {
		if f.tickets[k].CorrelationID == req.CorrelationID {
			return *f.tickets[k], true, nil
		}
	}
	return changerequest.Reading{}, false, nil
}

func (f *ticketing) Get(_ context.Context, _ changerequest.Request, key string) (changerequest.Reading, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["Get"]++
	if f.err != nil {
		return changerequest.Reading{}, f.err
	}
	t, ok := f.tickets[key]
	if !ok {
		return changerequest.Reading{}, fmt.Errorf("ticket %s: %w", key, changerequest.ErrNotFound)
	}
	return *t, nil
}

// instance is a per-promotion ChangeRequest of Bundle app-v1 into prod.
func instance(mut ...func(*v1alpha1.ChangeRequest)) *v1alpha1.ChangeRequest {
	cr := &v1alpha1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-change-prod--app-v1", Namespace: ns, Generation: 1, Labels: map[string]string{
			"kardinal.io/pipeline": "app", "kardinal.io/bundle": "app-v1", "kardinal.io/environment": "prod",
			"kardinal.io/bundle-uid": uid, "kardinal.io/change-request-template": "prod-change"}},
		Spec: v1alpha1.ChangeRequestSpec{Provider: "servicenow", Mode: "open", PollInterval: "1m", Timeout: "24h",
			ServiceNow: &v1alpha1.ServiceNowChangeRequest{InstanceURL: "https://example.service-now.com"}},
	}
	for _, m := range mut {
		m(cr)
	}
	return cr
}

type env struct {
	t   *testing.T
	c   client.Client
	r   *changerequest.Reconciler
	f   *ticketing
	now time.Time
	key types.NamespacedName
	// auditCreates counts AuditEvent create calls, AlreadyExists included.
	auditCreates int
}

func newEnv(t *testing.T, cr *v1alpha1.ChangeRequest, objs ...client.Object) *env {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	e := &env{t: t, f: newTicketing(), now: t0, key: types.NamespacedName{Namespace: cr.Namespace, Name: cr.Name}}
	e.c = fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.ChangeRequest{}, &v1alpha1.PolicyGate{}).
		WithObjects(append([]client.Object{cr}, objs...)...).
		WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object,
			opts ...client.CreateOption) error {
			if _, ok := obj.(*v1alpha1.AuditEvent); ok {
				e.auditCreates++
			}
			return c.Create(ctx, obj, opts...)
		}}).Build()
	c := e.c
	e.r = &changerequest.Reconciler{Client: c, Providers: map[string]changerequest.Provider{"servicenow": e.f},
		NowFn: func() time.Time { return e.now }}
	return e
}

func (e *env) reconcile() ctrl.Result {
	e.t.Helper()
	res, err := e.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: e.key})
	require.NoError(e.t, err)
	return res
}

func (e *env) get() *v1alpha1.ChangeRequest {
	e.t.Helper()
	var cr v1alpha1.ChangeRequest
	require.NoError(e.t, e.c.Get(context.Background(), e.key, &cr))
	return &cr
}

func (e *env) auditActions() []string {
	e.t.Helper()
	var l v1alpha1.AuditEventList
	require.NoError(e.t, e.c.List(context.Background(), &l, client.InNamespace(ns)))
	var out []string
	for _, a := range l.Items {
		out = append(out, a.Spec.Action)
	}
	return out
}

func cond(cr *v1alpha1.ChangeRequest, typ string) *metav1.Condition {
	return meta.FindStatusCondition(cr.Status.Conditions, typ)
}

// TestChangeRequest_OpenPendingApproved (spec 23 §4, §7.3): an instance
// opens one ticket carrying the correlation id, reads it every poll interval
// and no sooner, writes the clock (validUntil = observedAt + 2 polls), and
// records ChangeRequestOpened and, once, ChangeRequestApproved.
func TestChangeRequest_OpenPendingApproved(t *testing.T) {
	e := newEnv(t, instance())
	res := e.reconcile()
	cr := e.get()
	require.NotNil(t, cr.Status.Ticket)
	assert.Equal(t, "CHG0000001", cr.Status.Ticket.Key)
	assert.Equal(t, "pending", cr.Status.State)
	assert.Equal(t, "raw pending", cr.Status.ProviderState)
	assert.Equal(t, corrID, cr.Status.CorrelationID)
	assert.True(t, cr.Status.Ready)
	assert.Nil(t, cr.Status.PendingCreate, "the outbox entry is cleared once the ticket is recorded")
	assert.Equal(t, t0, cr.Status.ObservedAt.UTC())
	assert.Equal(t, t0.Add(2*time.Minute), cr.Status.ValidUntil.UTC())
	assert.Equal(t, t0, cr.Status.StartedAt.UTC())
	assert.Equal(t, time.Minute, res.RequeueAfter)
	assert.Equal(t, 1, e.f.count("Find"), "searched before creating")
	assert.Equal(t, 1, e.f.count("Create"))
	assert.Equal(t, corrID, e.f.tickets["CHG0000001"].CorrelationID, "the ticket carries the correlation id")
	assert.Equal(t, metav1.ConditionTrue, cond(cr, changerequest.CondReady).Status)
	assert.Equal(t, []string{changerequest.AuditActionOpened}, e.auditActions())
	assert.Empty(t, cr.Status.PendingAuditEvents, "flushed")

	// Before the poll interval: no call.
	e.now = t0.Add(30 * time.Second)
	res = e.reconcile()
	assert.Equal(t, 2, e.f.total())
	assert.Equal(t, 30*time.Second, res.RequeueAfter)

	e.f.set("CHG0000001", v1alpha1.ChangeRequestApproved)
	e.now = t0.Add(time.Minute)
	e.reconcile()
	cr = e.get()
	assert.Equal(t, "approved", cr.Status.State)
	assert.Equal(t, 1, e.f.count("Get"))
	assert.Equal(t, 1, e.f.count("Create"), "one ticket")
	assert.Equal(t, t0.Add(3*time.Minute), cr.Status.ValidUntil.UTC())
	assert.ElementsMatch(t, []string{changerequest.AuditActionOpened, changerequest.AuditActionApproved}, e.auditActions())

	// Approved again on the next poll: no second record, not even a create
	// the API server refuses as AlreadyExists.
	creates := e.auditCreates
	e.now = t0.Add(2 * time.Minute)
	e.reconcile()
	assert.Len(t, e.auditActions(), 2)
	assert.Equal(t, creates, e.auditCreates, "an unchanged state records nothing")

	e.f.set("CHG0000001", v1alpha1.ChangeRequestRejected)
	e.now = t0.Add(3 * time.Minute)
	e.reconcile()
	assert.Equal(t, "rejected", e.get().Status.State)
	assert.Contains(t, e.auditActions(), changerequest.AuditActionRejected)
}

// TestChangeRequest_LostCreateResponse (spec 23 §7.3): the provider creates
// the ticket and answers 500. The create stays announced (pendingCreate), the
// next attempt waits for retryAt, searches, and adopts the ticket: one ticket.
func TestChangeRequest_LostCreateResponse(t *testing.T) {
	e := newEnv(t, instance())
	e.f.createErr, e.f.createdAnyway = &changerequest.RetryError{Status: 500, Err: errors.New("servicenow: HTTP 500")}, true
	e.reconcile()
	cr := e.get()
	require.NotNil(t, cr.Status.PendingCreate)
	assert.Equal(t, corrID, cr.Status.PendingCreate.CorrelationID)
	assert.Nil(t, cr.Status.Ticket)
	require.NotNil(t, cr.Status.RetryAt)
	assert.Equal(t, t0.Add(time.Minute), cr.Status.RetryAt.UTC())
	assert.Equal(t, metav1.ConditionTrue, cond(cr, changerequest.CondProviderError).Status)
	assert.Len(t, e.f.tickets, 1, "created, response lost")

	e.f.createErr = nil
	e.now = t0.Add(30 * time.Second)
	e.reconcile()
	assert.Equal(t, 1, e.f.count("Find"), "no call before retryAt")

	e.now = t0.Add(time.Minute)
	e.reconcile()
	cr = e.get()
	require.NotNil(t, cr.Status.Ticket)
	assert.Equal(t, "CHG0000001", cr.Status.Ticket.Key, "adopted")
	assert.Nil(t, cr.Status.PendingCreate)
	assert.Nil(t, cr.Status.RetryAt)
	assert.Equal(t, 1, e.f.count("Create"))
	assert.Len(t, e.f.tickets, 1)
	assert.Equal(t, []string{changerequest.AuditActionOpened}, e.auditActions())
}

// TestChangeRequest_CrashBetweenCreateAndStatus (spec 23 §7.3): the ticket
// exists and the status only has pendingCreate (the controller died after
// the create): the next reconcile adopts it.
func TestChangeRequest_CrashBetweenCreateAndStatus(t *testing.T) {
	cr := instance()
	since := metav1.NewTime(t0.Add(-time.Hour))
	cr.Status.PendingCreate = &v1alpha1.ChangeRequestPendingCreate{CorrelationID: corrID, Since: since}
	e := newEnv(t, cr)
	e.f.add("CHG0000001", corrID, v1alpha1.ChangeRequestPending)
	e.reconcile()
	got := e.get()
	require.NotNil(t, got.Status.Ticket)
	assert.Equal(t, "CHG0000001", got.Status.Ticket.Key)
	assert.Equal(t, 0, e.f.count("Create"))
}

// TestChangeRequest_SearchLagWaitsForGrace: a create was announced moments
// ago and the search does not show it (an index behind): no second create
// until CreateGrace has passed since the attempt.
func TestChangeRequest_SearchLagWaitsForGrace(t *testing.T) {
	cr := instance()
	cr.Status.PendingCreate = &v1alpha1.ChangeRequestPendingCreate{CorrelationID: corrID, Since: metav1.NewTime(t0)}
	e := newEnv(t, cr)
	e.f.searchLags = true
	e.now = t0.Add(time.Minute)
	res := e.reconcile()
	assert.Equal(t, 0, e.f.count("Create"))
	assert.Equal(t, changerequest.CreateGrace-time.Minute, res.RequeueAfter)
	assert.Equal(t, changerequest.ReasonCreating, cond(e.get(), changerequest.CondReady).Reason)

	e.now = t0.Add(changerequest.CreateGrace)
	e.reconcile()
	assert.Equal(t, 1, e.f.count("Create"), "past the grace, the create is sent again")
}

// TestChangeRequest_StaleCacheCannotCreateTwice (spec 23 §7.3): a second
// reconcile that reads the object from before the first one's pendingCreate
// write (a stale cache, another replica) loses the optimistic lock and
// creates nothing.
func TestChangeRequest_StaleCacheCannotCreateTwice(t *testing.T) {
	cr := instance()
	e := newEnv(t, cr)
	stale := e.get().DeepCopy()
	e.reconcile()
	require.Equal(t, 1, e.f.count("Create"))

	staleClient := interceptor.NewClient(e.c.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if o, ok := obj.(*v1alpha1.ChangeRequest); ok {
				stale.DeepCopyInto(o)
				return nil
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	// The search does not show the ticket yet (an index behind), so only
	// the lock stops the second create.
	e.f.searchLags = true
	r2 := &changerequest.Reconciler{Client: staleClient, Providers: e.r.Providers, NowFn: e.r.NowFn}
	_, err := r2.Reconcile(context.Background(), ctrl.Request{NamespacedName: e.key})
	require.NoError(t, err)
	assert.Equal(t, 1, e.f.count("Create"), "the stale reconcile lost the lock before creating")
	assert.Len(t, e.f.tickets, 1)
	assert.Equal(t, "CHG0000001", e.get().Status.Ticket.Key, "the first reconcile's record stands")
}

// TestChangeRequest_RecreatedBundleGetsNewTicket: the Bundle UID is part of
// the correlation id, so an instance of a re-created Bundle of the same name
// does not adopt the old Bundle's ticket.
func TestChangeRequest_RecreatedBundleGetsNewTicket(t *testing.T) {
	e := newEnv(t, instance(func(cr *v1alpha1.ChangeRequest) { cr.Labels["kardinal.io/bundle-uid"] = "uid-2" }))
	e.f.add("CHG0000001", corrID, v1alpha1.ChangeRequestApproved)
	e.reconcile()
	cr := e.get()
	assert.Equal(t, "kardinal:checkout/app-v1/prod/uid-2", cr.Status.CorrelationID)
	assert.Equal(t, "CHG0000002", cr.Status.Ticket.Key)
	assert.Equal(t, "pending", cr.Status.State)
}

// TestChangeRequest_ProviderErrors (spec 23 §7.2): 429 with Retry-After,
// a refused credential and an egress refusal each write a condition and a
// retryAt, keep the last reading (it goes stale), and send nothing before
// retryAt: no hot loop.
func TestChangeRequest_ProviderErrors(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		cond  string
		retry time.Duration
	}{
		{"429 Retry-After", &changerequest.RetryError{Status: 429, After: 10 * time.Minute, Err: errors.New("HTTP 429")},
			changerequest.CondProviderError, 10 * time.Minute},
		{"429 short Retry-After waits a poll", &changerequest.RetryError{Status: 429, After: time.Second, Err: errors.New("HTTP 429")},
			changerequest.CondProviderError, time.Minute},
		{"credential refused", fmt.Errorf("HTTP 401: %w", changerequest.ErrCredentials), changerequest.CondCredentialsInvalid, 5 * time.Minute},
		{"egress refused", fmt.Errorf("dial: %w", egress.ErrBlockedAddress), changerequest.CondRefused, 5 * time.Minute},
		{"other", errors.New("boom"), changerequest.CondProviderError, time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, instance())
			e.reconcile() // a reading at t0
			e.f.err = tc.err
			e.now = t0.Add(time.Minute)
			e.reconcile()
			cr := e.get()
			c := cond(cr, tc.cond)
			require.NotNil(t, c, "condition %s", tc.cond)
			assert.Equal(t, metav1.ConditionTrue, c.Status)
			assert.Contains(t, c.Message, tc.err.Error())
			require.NotNil(t, cr.Status.RetryAt)
			assert.Equal(t, e.now.Add(tc.retry), cr.Status.RetryAt.UTC())
			assert.Equal(t, "pending", cr.Status.State, "the last reading stays")
			assert.Equal(t, t0.Add(2*time.Minute), cr.Status.ValidUntil.UTC(), "and goes stale")
			calls := e.f.total()
			for i := 1; i <= 5; i++ {
				e.now = t0.Add(time.Minute + time.Duration(i)*time.Second)
				e.reconcile()
			}
			assert.Equal(t, calls, e.f.total(), "nothing before retryAt")
			e.now = t0.Add(time.Minute + tc.retry)
			e.f.err = nil
			e.reconcile()
			cr = e.get()
			assert.Nil(t, cr.Status.RetryAt)
			assert.Equal(t, metav1.ConditionFalse, cond(cr, tc.cond).Status, "cleared by a call that works")
		})
	}
}

// TestChangeRequest_StaleCondition: past validUntil without a reading the
// Stale condition is True; a reading clears it.
func TestChangeRequest_StaleCondition(t *testing.T) {
	e := newEnv(t, instance())
	e.reconcile()
	e.f.err = errors.New("down")
	e.now = t0.Add(time.Minute)
	e.reconcile()
	e.now = t0.Add(2 * time.Minute)
	e.reconcile()
	assert.Equal(t, metav1.ConditionTrue, cond(e.get(), changerequest.CondStale).Status)
	e.f.err = nil
	e.now = t0.Add(3 * time.Minute) // the retryAt of the failure at 2m
	e.reconcile()
	assert.Equal(t, metav1.ConditionFalse, cond(e.get(), changerequest.CondStale).Status)
}

// TestChangeRequest_Timeout: status.timedOut turns true spec.timeout after
// the first reconcile, and the requeue lands on that boundary.
func TestChangeRequest_Timeout(t *testing.T) {
	e := newEnv(t, instance(func(cr *v1alpha1.ChangeRequest) { cr.Spec.Timeout = "90s" }))
	e.reconcile()
	assert.False(t, e.get().Status.TimedOut)
	e.now = t0.Add(time.Minute)
	res := e.reconcile()
	assert.Equal(t, 30*time.Second, res.RequeueAfter, "the timeout comes before the next poll")
	e.now = t0.Add(90 * time.Second)
	e.reconcile()
	assert.True(t, e.get().Status.TimedOut)

	e2 := newEnv(t, instance(func(cr *v1alpha1.ChangeRequest) { cr.Spec.Timeout = "0" }))
	e2.now = t0.Add(1000 * time.Hour)
	e2.reconcile()
	assert.False(t, e2.get().Status.TimedOut, "0 is no timeout")
}

// TestChangeRequest_NoCalls: a template, an object without the instance
// labels, a suspended instance, an unknown provider and one waiting for a
// Limiter slot call nothing.
func TestChangeRequest_NoCalls(t *testing.T) {
	t.Run("template", func(t *testing.T) {
		e := newEnv(t, instance(func(cr *v1alpha1.ChangeRequest) { cr.Spec.PerPromotion = true }))
		res := e.reconcile()
		assert.Zero(t, res.RequeueAfter)
		assert.Equal(t, changerequest.ReasonTemplate, cond(e.get(), changerequest.CondReady).Reason)
		rv := e.get().ResourceVersion
		e.now = t0.Add(time.Hour)
		e.reconcile()
		assert.Equal(t, rv, e.get().ResourceVersion, "an unchanged template writes nothing")
		assert.Zero(t, e.f.total())
	})
	t.Run("not an instance", func(t *testing.T) {
		e := newEnv(t, instance(func(cr *v1alpha1.ChangeRequest) { delete(cr.Labels, "kardinal.io/bundle-uid") }))
		e.reconcile()
		assert.Equal(t, changerequest.ReasonNotAnInstance, cond(e.get(), changerequest.CondReady).Reason)
		assert.Zero(t, e.f.total())
	})
	t.Run("suspended", func(t *testing.T) {
		e := newEnv(t, instance(func(cr *v1alpha1.ChangeRequest) { cr.Spec.Suspend = true }))
		e.reconcile()
		assert.Equal(t, changerequest.ReasonSuspended, cond(e.get(), changerequest.CondReady).Reason)
		assert.Zero(t, e.f.total())
	})
	t.Run("unknown provider", func(t *testing.T) {
		e := newEnv(t, instance(func(cr *v1alpha1.ChangeRequest) { cr.Spec.Provider = "jira" }))
		e.reconcile()
		c := cond(e.get(), changerequest.CondProviderError)
		require.NotNil(t, c)
		assert.Contains(t, c.Message, `provider "jira" is not available in this controller`)
	})
	t.Run("waiting for a slot", func(t *testing.T) {
		e := newEnv(t, instance())
		e.r.Limiter = slots.NewLimiter(1, 1)
		release, ok := e.r.Limiter.TryAcquire(types.NamespacedName{Namespace: ns, Name: "other"})
		require.True(t, ok)
		res := e.reconcile()
		assert.Zero(t, e.f.total())
		assert.Equal(t, 30*time.Second, res.RequeueAfter)
		assert.Equal(t, changerequest.ReasonWaitingSlot, cond(e.get(), changerequest.CondReady).Reason)
		release()
	})
}

// TestChangeRequest_LinkFromBundle (spec 23 §7.4): mode link reads the key
// from a Bundle label or annotation, never creates, and verifies the ticket
// by the correlation id or link.requireField; otherwise verified is false
// and the ChangeRequestLinked record says so.
func TestChangeRequest_LinkFromBundle(t *testing.T) {
	bundle := func(labels, annotations map[string]string) *v1alpha1.Bundle {
		return &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: ns, UID: uid,
			Labels: labels, Annotations: annotations}}
	}
	link := func(from string, require *v1alpha1.ChangeRequestFieldMatch) func(*v1alpha1.ChangeRequest) {
		return func(cr *v1alpha1.ChangeRequest) {
			cr.Spec.Mode = "link"
			cr.Spec.Link = &v1alpha1.ChangeRequestLink{From: from, Name: "change", RequireField: require}
		}
	}
	cases := []struct {
		name     string
		bundle   *v1alpha1.Bundle
		link     func(*v1alpha1.ChangeRequest)
		ticket   string // its correlation id
		verified bool
	}{
		{"label, correlation id", bundle(map[string]string{"change": "CHG0000009"}, nil),
			link("bundleLabel", nil), corrID, true},
		{"annotation, unrelated ticket", bundle(nil, map[string]string{"change": "CHG0000009"}),
			link("bundleAnnotation", nil), "kardinal:other/x/prod/u", false},
		{"requireField matches", bundle(map[string]string{"change": "CHG0000009"}, nil),
			link("bundleLabel", &v1alpha1.ChangeRequestFieldMatch{Name: "cmdb_ci", Value: "checkout"}), "", true},
		{"requireField differs", bundle(map[string]string{"change": "CHG0000009"}, nil),
			link("bundleLabel", &v1alpha1.ChangeRequestFieldMatch{Name: "cmdb_ci", Value: "payments"}), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, instance(tc.link), tc.bundle)
			e.f.add("CHG0000009", tc.ticket, v1alpha1.ChangeRequestApproved)
			e.reconcile()
			cr := e.get()
			require.NotNil(t, cr.Status.Ticket)
			assert.Equal(t, "CHG0000009", cr.Status.Ticket.Key)
			assert.Equal(t, "approved", cr.Status.State)
			require.NotNil(t, cr.Status.Verified)
			assert.Equal(t, tc.verified, *cr.Status.Verified)
			assert.Zero(t, e.f.count("Create"), "link never creates")
			assert.Zero(t, e.f.count("Find"))
			var l v1alpha1.AuditEventList
			require.NoError(t, e.c.List(context.Background(), &l))
			require.Len(t, l.Items, 2)
			for _, a := range l.Items {
				if a.Spec.Action == changerequest.AuditActionLinked {
					assert.Equal(t, !tc.verified, strings.Contains(a.Spec.Message, "not verified"), a.Spec.Message)
				}
			}
		})
	}

	t.Run("no key yet, then a re-created Bundle", func(t *testing.T) {
		e := newEnv(t, instance(link("bundleLabel", nil)), bundle(nil, nil))
		e.reconcile()
		cr := e.get()
		assert.Equal(t, changerequest.ReasonWaitingForKey, cond(cr, changerequest.CondReady).Reason)
		assert.Contains(t, cond(cr, changerequest.CondReady).Message, "label change")
		assert.Empty(t, cr.Status.State)
		assert.Zero(t, e.f.total())

		var b v1alpha1.Bundle
		require.NoError(t, e.c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "app-v1"}, &b))
		require.NoError(t, e.c.Delete(context.Background(), &b))
		require.NoError(t, e.c.Create(context.Background(), &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1",
			Namespace: ns, UID: "uid-other", Labels: map[string]string{"change": "CHG0000009"}}}))
		e.now = t0.Add(time.Minute)
		e.reconcile()
		assert.Contains(t, cond(e.get(), changerequest.CondReady).Message, "re-created")
		assert.Zero(t, e.f.total(), "the other Bundle's label is not this promotion's key")
	})
	t.Run("a ticket that does not exist", func(t *testing.T) {
		e := newEnv(t, instance(link("bundleLabel", nil)), bundle(map[string]string{"change": "CHG-9"}, nil))
		e.reconcile()
		cr := e.get()
		assert.Equal(t, "unknown", cr.Status.State)
		assert.Contains(t, cond(cr, changerequest.CondProviderError).Message, "CHG-9")
	})
}

// TestChangeRequest_LinkFromApprovalField (spec 23 §7.4, §8): the key is the
// field of the environment's counted approvals; an approval that does not
// count gives none; two different keys are refused; a revoked approval
// clears the ticket.
func TestChangeRequest_LinkFromApprovalField(t *testing.T) {
	gate := func(approvals ...v1alpha1.GateApprovalStatus) *v1alpha1.PolicyGate {
		return &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "change-prod-app-v1", Namespace: ns,
			Labels: map[string]string{"kardinal.io/bundle": "app-v1", "kardinal.io/environment": "prod"}},
			Spec:   v1alpha1.PolicyGateSpec{Expression: "true"},
			Status: v1alpha1.PolicyGateStatus{Approvals: approvals}}
	}
	vote := func(user string, counted bool, decision, ticket string) v1alpha1.GateApprovalStatus {
		return v1alpha1.GateApprovalStatus{User: user, Decision: decision, Counted: counted,
			Fields: map[string]string{"ticketID": ticket}}
	}
	link := func(cr *v1alpha1.ChangeRequest) {
		cr.Spec.Mode = "link"
		cr.Spec.Link = &v1alpha1.ChangeRequestLink{From: "approvalField", Name: "ticketID"}
	}
	other := gate(vote("bob", false, "approve", "CHG0000777"), vote("erin", true, "reject", "CHG0000666"))
	other.Labels["kardinal.io/environment"] = "uat"
	other.Name = "uat-gate"

	e := newEnv(t, instance(link), gate(vote("alice", true, "approve", "CHG0000009"),
		vote("bob", false, "approve", "CHG0000777"), vote("erin", true, "reject", "CHG0000666")), other)
	e.f.add("CHG0000009", corrID, v1alpha1.ChangeRequestApproved)
	e.reconcile()
	cr := e.get()
	require.NotNil(t, cr.Status.Ticket)
	assert.Equal(t, "CHG0000009", cr.Status.Ticket.Key, "only the counted approve's field")
	assert.True(t, *cr.Status.Verified)

	// Two counted approvals with different tickets: refused, nothing read.
	var g v1alpha1.PolicyGate
	require.NoError(t, e.c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "change-prod-app-v1"}, &g))
	g.Status.Approvals = append(g.Status.Approvals, vote("carol", true, "approve", "CHG0000010"))
	require.NoError(t, e.c.Status().Update(context.Background(), &g))
	gets := e.f.count("Get")
	e.now = t0.Add(time.Minute)
	e.reconcile()
	cr = e.get()
	assert.Equal(t, gets, e.f.count("Get"))
	assert.Contains(t, cond(cr, changerequest.CondProviderError).Message, "name 2 tickets in field ticketID (CHG0000009, CHG0000010)")

	// The approvals are revoked: nothing is tracked.
	g.Status.Approvals = nil
	require.NoError(t, e.c.Status().Update(context.Background(), &g))
	e.now = t0.Add(3 * time.Minute)
	e.reconcile()
	cr = e.get()
	assert.Nil(t, cr.Status.Ticket)
	assert.Empty(t, cr.Status.State)
	assert.Nil(t, cr.Status.ValidUntil)
	assert.Contains(t, cond(cr, changerequest.CondReady).Message, "waiting for a counted approval of prod with the field ticketID")
}

// TestChangeRequest_DeletedWhileReconciled: a ChangeRequest deleted during
// the reconcile ends it without an error.
func TestChangeRequest_DeletedWhileReconciled(t *testing.T) {
	cr := instance()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(cr).WithObjects(cr).
		WithInterceptorFuncs(objectgonetest.DeleteOnWrite(t, nil)).Build()
	r := &changerequest.Reconciler{Client: c, Providers: map[string]changerequest.Provider{"servicenow": newTicketing()},
		NowFn: func() time.Time { return t0 }}
	var logs bytes.Buffer
	res, err := r.Reconcile(objectgonetest.Context(&logs), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: cr.Name}})
	objectgonetest.AssertQuiet(t, res, err, &logs)
}

// TestChangeRequest_ConflictOnAnnounceCreatesNothing: a conflict on the
// pendingCreate write requeues without a create.
func TestChangeRequest_ConflictOnAnnounceCreatesNothing(t *testing.T) {
	cr := instance()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	f := newTicketing()
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(cr).WithObjects(cr).
		WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: func(context.Context, client.Client, string, client.Object,
			client.Patch, ...client.SubResourcePatchOption) error {
			return apierrors.NewConflict(v1alpha1.GroupVersion.WithResource("changerequests").GroupResource(), cr.Name, errors.New("changed"))
		}}).Build()
	r := &changerequest.Reconciler{Client: c, Providers: map[string]changerequest.Provider{"servicenow": f},
		NowFn: func() time.Time { return t0 }}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: cr.Name}})
	require.NoError(t, err)
	assert.Equal(t, time.Second, res.RequeueAfter)
	assert.Equal(t, 0, f.count("Create"))
}

// TestChangeRequest_RecreatedInstanceAdoptsItsTicket: an instance deleted
// and created again by the Graph (an upstream left Verified and came back)
// finds its promotion's ticket by the correlation id and opens no other.
func TestChangeRequest_RecreatedInstanceAdoptsItsTicket(t *testing.T) {
	e := newEnv(t, instance())
	e.f.add("CHG0000001", corrID, v1alpha1.ChangeRequestApproved)
	e.reconcile()
	cr := e.get()
	assert.Equal(t, "CHG0000001", cr.Status.Ticket.Key)
	assert.Equal(t, "approved", cr.Status.State)
	assert.Zero(t, e.f.count("Create"))
	assert.ElementsMatch(t, []string{changerequest.AuditActionOpened, changerequest.AuditActionApproved}, e.auditActions())
}

// TestChangeRequest_ReadingIsNormalised: a provider state outside the
// normalised set is unknown (status.state admits no other), and at most 32
// approvers are kept.
func TestChangeRequest_ReadingIsNormalised(t *testing.T) {
	e := newEnv(t, instance())
	e.f.add("CHG0000001", corrID, "Scheduled")
	for i := 0; i < 40; i++ {
		e.f.tickets["CHG0000001"].Approvers = append(e.f.tickets["CHG0000001"].Approvers,
			v1alpha1.ChangeRequestApprover{Name: fmt.Sprintf("a%d", i)})
	}
	e.reconcile()
	cr := e.get()
	assert.Equal(t, "unknown", cr.Status.State)
	assert.Equal(t, "raw Scheduled", cr.Status.ProviderState)
	assert.Len(t, cr.Status.Approvers, 32)
}
