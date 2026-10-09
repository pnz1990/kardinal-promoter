// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package shard

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func namespace(name, shard string) *corev1.Namespace {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if shard != "" {
		ns.Labels = map[string]string{LabelShard: shard}
	}
	return ns
}

// flaky is one shard's view of the API server: its calls can be made to
// fail, and its writes and API reads are counted.
type flaky struct {
	client.Client
	failUpdates, failNamespaceList atomic.Bool
	writes, reads                  atomic.Int64
}

var errDown = errors.New("api server unreachable")

func (f *flaky) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	f.writes.Add(1)
	if f.failUpdates.Load() {
		return errDown
	}
	return f.Client.Create(ctx, obj, opts...)
}

func (f *flaky) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	f.writes.Add(1)
	if f.failUpdates.Load() {
		return errDown
	}
	return f.Client.Update(ctx, obj, opts...)
}

func (f *flaky) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*corev1.NamespaceList); ok && f.failNamespaceList.Load() {
		return errDown
	}
	return f.Client.List(ctx, list, opts...)
}

// apiReader counts the reads that go to the API server (Options.Reader).
type apiReader struct {
	client.Reader
	n *atomic.Int64
}

func (r apiReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	r.n.Add(1)
	return r.Reader.Get(ctx, key, obj, opts...)
}

func (r apiReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	r.n.Add(1)
	return r.Reader.List(ctx, list, opts...)
}

type world struct {
	t *testing.T
	c client.Client
}

func newWorld(t *testing.T, objs ...client.Object) *world {
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, coordinationv1.AddToScheme(s))
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
		WithIndex(&coordinationv1.Lease{}, "metadata.name", func(o client.Object) []string { return []string{o.GetName()} }).
		Build()
	return &world{t: t, c: c}
}

// shard is one shard's controller: its gate, clock and API view.
type shard struct {
	*Gate
	clk *clock
	api *flaky
	rec *events.FakeRecorder
}

func (w *world) shard(name string, at time.Time) *shard {
	api := &flaky{Client: w.c}
	clk := &clock{t: at}
	rec := events.NewFakeRecorder(100)
	g := New(Options{Name: name, Home: "kardinal-" + name, Client: api, Reader: apiReader{w.c, &api.reads},
		Recorder: rec, Log: zerolog.Nop()})
	g.now = clk.now
	return &shard{Gate: g, clk: clk, api: api, rec: rec}
}

var t0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func (w *world) holder(ns string) string {
	var l coordinationv1.Lease
	require.NoError(w.t, w.c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: LeaseName}, &l))
	if l.Spec.HolderIdentity == nil {
		return ""
	}
	return *l.Spec.HolderIdentity
}

func (w *world) relabel(ns, shard string) {
	var n corev1.Namespace
	require.NoError(w.t, w.c.Get(context.Background(), types.NamespacedName{Name: ns}, &n))
	n.Labels = map[string]string{LabelShard: shard}
	require.NoError(w.t, w.c.Update(context.Background(), &n))
}

// run advances every shard's clock by d in passInterval steps, running a
// pass of each live shard at every step.
func run(d time.Duration, shards ...*shard) {
	for elapsed := time.Duration(0); elapsed < d; elapsed += passInterval {
		for _, s := range shards {
			s.clk.add(passInterval)
			s.Pass(context.Background())
		}
	}
}

// counting is a reconciler that counts calls by namespace and can block
// until released or its context is cancelled.
type counting struct {
	mu        sync.Mutex
	calls     map[string]int
	block     chan struct{}
	in        chan struct{}
	cancelled atomic.Bool
}

func (c *counting) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	c.mu.Lock()
	if c.calls == nil {
		c.calls = map[string]int{}
	}
	c.calls[req.Namespace]++
	block := c.block
	c.mu.Unlock()
	if block != nil {
		c.in <- struct{}{}
		select {
		case <-block:
		case <-ctx.Done():
			c.cancelled.Store(true)
		}
	}
	return reconcile.Result{}, nil
}

func (c *counting) count(ns string) int { c.mu.Lock(); defer c.mu.Unlock(); return c.calls[ns] }

func req(ns string) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "obj"}}
}

// TestGate_AssignsNamespacesByLabel: shard "default" takes namespaces
// without the label and the cluster-scoped kinds, shard "b" the namespaces
// labelled b; each reconciles only what it holds, and retries a namespace it
// is still taking. A Pass is idempotent.
func TestGate_AssignsNamespacesByLabel(t *testing.T) {
	w := newWorld(t, namespace("team-a", ""), namespace("team-b", "b"), namespace("team-c", "c"))
	def, b := w.shard(DefaultShard, t0), w.shard("b", t0)
	ctx := context.Background()

	r := &counting{}
	wrapped := b.Wrap(r)
	res, err := wrapped.Reconcile(ctx, req("team-b"))
	require.NoError(t, err)
	assert.Equal(t, pendingRequeue, res.RequeueAfter, "assigned but not held yet: retried")
	assert.Zero(t, r.count("team-b"))

	run(2*passInterval, def, b)
	assert.Equal(t, "kardinal-shard/default", w.holder("team-a"))
	assert.Equal(t, "kardinal-shard/b", w.holder("team-b"))
	var l coordinationv1.Lease
	assert.Error(t, w.c.Get(ctx, types.NamespacedName{Namespace: "team-c", Name: LeaseName}, &l), "no shard c running: no token")

	assert.True(t, def.Owns("team-a"))
	assert.False(t, def.Owns("team-b"))
	assert.True(t, b.Owns("team-b"))
	assert.True(t, def.Owns(""), "default owns cluster-scoped kinds")
	assert.False(t, b.Owns(""))

	for _, ns := range []string{"team-a", "team-b", "team-c", ""} {
		res, err := wrapped.Reconcile(ctx, req(ns))
		require.NoError(t, err)
		assert.Zero(t, res.RequeueAfter, ns)
	}
	assert.Equal(t, 1, r.count("team-b"))
	assert.Zero(t, r.count("team-a"))
	assert.Zero(t, r.count("team-c"))
	assert.Zero(t, r.count(""), "shard b drops cluster-scoped objects")

	off := &Gate{}
	assert.True(t, off.Owns("anything"))
	_, err = off.Wrap(r).Reconcile(ctx, req("team-c"))
	require.NoError(t, err)
	assert.Equal(t, 1, r.count("team-c"), "sharding off: everything")
}

// TestGate_PendingRequeueBacksOff (#1505 QA): a reconcile of a namespace
// assigned to this shard but held by another is retried later the longer
// the wait has lasted, up to maxPendingRequeue.
func TestGate_PendingRequeueBacksOff(t *testing.T) {
	w := newWorld(t, namespace("team", ""))
	def, b := w.shard(DefaultShard, t0), w.shard("b", t0)
	run(passInterval, def, b)
	r := &counting{block: make(chan struct{}), in: make(chan struct{})}
	go func() { _, _ = def.Wrap(r).Reconcile(context.Background(), req("team")) }()
	<-r.in
	defer close(r.block)
	w.relabel("team", "b")

	var delays []time.Duration
	for range 8 {
		res, err := b.Wrap(&counting{}).Reconcile(context.Background(), req("team"))
		require.NoError(t, err)
		delays = append(delays, res.RequeueAfter)
		b.clk.add(30 * time.Second)
	}
	assert.Equal(t, pendingRequeue, delays[0])
	for i := 1; i < len(delays); i++ {
		assert.GreaterOrEqual(t, delays[i], delays[i-1], "never shorter")
	}
	assert.Equal(t, maxPendingRequeue, delays[len(delays)-1])
}

// TestGate_HandoffWaitsForInFlight (#1505 QA): relabelling a namespace from
// default to b while default has a reconcile running there, for longer than
// leaseDuration: default stops starting reconciles at once, keeps its
// heartbeat and the token until the running one returns; b waits all along,
// then takes the namespace and announces it. There is never a moment with
// two owners.
func TestGate_HandoffWaitsForInFlight(t *testing.T) {
	w := newWorld(t, namespace("team", ""))
	def, b := w.shard(DefaultShard, t0), w.shard("b", t0)
	ctx := context.Background()
	events := b.subscribe()
	run(passInterval, def, b)
	require.True(t, def.Owns("team"))

	r := &counting{block: make(chan struct{}), in: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		_, _ = def.Wrap(r).Reconcile(ctx, req("team"))
		close(done)
	}()
	<-r.in // the reconcile is running

	w.relabel("team", "b")
	for elapsed := time.Duration(0); elapsed < 5*leaseDuration; elapsed += passInterval {
		run(passInterval, def, b)
		require.False(t, def.Owns("team"), "no new reconcile starts after the relabel")
		require.False(t, b.Owns("team"), "b waits for the reconcile in flight (%s)", elapsed)
		require.Equal(t, "kardinal-shard/default", w.holder("team"))
	}
	assert.False(t, r.cancelled.Load(), "default kept renewing: the reconcile was not fenced")

	close(r.block)
	<-done
	run(passInterval, def, b)
	assert.True(t, b.Owns("team"))
	assert.Equal(t, "kardinal-shard/b", w.holder("team"))
	select {
	case ev := <-events:
		assert.Equal(t, "team", ev.Object.GetName())
	case <-time.After(5 * time.Second):
		t.Fatal("b did not announce the namespace it took")
	}
	var l coordinationv1.Lease
	require.NoError(t, w.c.Get(ctx, types.NamespacedName{Namespace: "team", Name: LeaseName}, &l))
	require.NotNil(t, l.Spec.LeaseTransitions)
	assert.Equal(t, int32(1), *l.Spec.LeaseTransitions)
	assert.Equal(t, "kardinal-b", l.Annotations[AnnotationHeartbeat])
}

// TestGate_DeadShardExpires (#1505 QA): a shard that stopped renewing keeps
// its namespaces until its heartbeat has not changed for leaseDuration, as
// the taking shard measures it from when it first saw that heartbeat
// version, on its own clock: neither a clock far behind (renewTime in the
// past) nor far ahead (renewTime in the future) on the dead shard changes
// that. A new leader of the same shard takes its tokens at once.
func TestGate_DeadShardExpires(t *testing.T) {
	for _, skew := range []time.Duration{0, -time.Hour, time.Hour} {
		t.Run(skew.String(), func(t *testing.T) {
			w := newWorld(t, namespace("team", ""))
			def := w.shard(DefaultShard, t0.Add(skew))
			run(passInterval, def)
			// default's leader dies; the namespace moves to b.
			w.relabel("team", "b")
			b := w.shard("b", t0)
			run(leaseDuration-passInterval, b)
			assert.False(t, b.Owns("team"), "not expired yet")
			assert.Equal(t, "kardinal-shard/default", w.holder("team"))
			run(2*passInterval, b)
			assert.True(t, b.Owns("team"), "expired: taken over")

			next := w.shard("b", t0) // b's next leader
			run(passInterval, next)
			assert.True(t, next.Owns("team"), "same shard, same holder: at once")
		})
	}
}

// TestGate_FencesBeforeTakeover (#1505 QA): a shard that can no longer
// renew its heartbeat (writes fail) stops reconciling and cancels the
// reconciles in flight at leaseDuration-fenceMargin after its last renewal,
// before the shard the namespace moved to may take it over. Once its writes
// work again it re-reads its tokens and gives up the namespaces taken
// meanwhile.
func TestGate_FencesBeforeTakeover(t *testing.T) {
	w := newWorld(t, namespace("team", ""), namespace("other", ""))
	def, b := w.shard(DefaultShard, t0), w.shard("b", t0)
	run(passInterval, def, b)
	require.True(t, def.Owns("team"))

	r := &counting{block: make(chan struct{}), in: make(chan struct{})}
	done := make(chan struct{})
	go func() { _, _ = def.Wrap(r).Reconcile(context.Background(), req("team")); close(done) }()
	<-r.in

	def.api.failUpdates.Store(true)
	w.relabel("team", "b")
	fencedAt, takenAt := time.Duration(-1), time.Duration(-1)
	for elapsed := passInterval; elapsed <= 2*leaseDuration; elapsed += passInterval {
		run(passInterval, def, b)
		if fencedAt < 0 && !def.Owns("other") {
			fencedAt = elapsed
		}
		if takenAt < 0 && b.Owns("team") {
			takenAt = elapsed
		}
	}
	require.GreaterOrEqual(t, fencedAt, time.Duration(0), "default fenced itself")
	require.GreaterOrEqual(t, takenAt, time.Duration(0), "b took the namespace of the fenced shard")
	assert.Less(t, fencedAt, takenAt, "fenced before the takeover")
	assert.LessOrEqual(t, fencedAt, leaseDuration-fenceMargin+passInterval)
	<-done
	assert.True(t, r.cancelled.Load(), "the reconcile in flight was cancelled by the fence")
	res, err := def.Wrap(&counting{}).Reconcile(context.Background(), req("other"))
	require.NoError(t, err)
	assert.Equal(t, pendingRequeue, res.RequeueAfter, "fenced: retried, not run")

	def.api.failUpdates.Store(false)
	run(passInterval, def, b)
	assert.True(t, def.Owns("other"), "renewed: reconciling its namespaces again")
	assert.False(t, def.Owns("team"), "the namespace b took is not reclaimed")
	assert.True(t, b.Owns("team"))
}

// TestGate_FencesWhenNamespaceListFails (#1505 QA): a shard that cannot
// list namespaces cannot see a relabel, so it does not renew its heartbeat:
// it fences itself and the namespace's new shard takes over.
func TestGate_FencesWhenNamespaceListFails(t *testing.T) {
	w := newWorld(t, namespace("team", ""))
	def, b := w.shard(DefaultShard, t0), w.shard("b", t0)
	run(passInterval, def, b)
	require.True(t, def.Owns("team"))
	def.api.failNamespaceList.Store(true)
	w.relabel("team", "b")
	run(leaseDuration-fenceMargin+passInterval, def, b)
	assert.False(t, def.Owns("team"), "fenced")
	assert.False(t, b.Owns("team"), "not yet expired for b")
	run(fenceMargin+2*passInterval, def, b)
	assert.True(t, b.Owns("team"))
}

// TestGate_SteadyStateCost (#1505 QA): with 1000 namespaces, after taking
// them a shard writes only its heartbeat (one write per renewInterval) and
// reads the API server only to renew it; the namespaces and tokens come
// from the cache (and the default shard lists the heartbeats every
// warnInterval). Three minutes of passes are measured.
func TestGate_SteadyStateCost(t *testing.T) {
	const n = 1000
	objs := make([]client.Object, 0, n)
	for i := range n {
		objs = append(objs, namespace("team-"+string(rune('a'+i%26))+"-"+itoa(i), ""))
	}
	w := newWorld(t, objs...)
	def := w.shard(DefaultShard, t0)
	run(passInterval, def)
	for _, o := range objs {
		require.True(t, def.Owns(o.GetName()))
	}
	takeWrites := def.api.writes.Load()
	assert.Equal(t, int64(n+1), takeWrites, "one token per namespace and the heartbeat")

	def.api.writes.Store(0)
	def.api.reads.Store(0)
	const window = 3 * time.Minute
	run(window, def)
	writes, reads := def.api.writes.Load(), def.api.reads.Load()
	want := int64(window / renewInterval)
	assert.LessOrEqual(t, writes, want+1, "only heartbeat renewals")
	assert.LessOrEqual(t, reads, want+int64(window/warnInterval)+1, "only heartbeat reads, and the default shard's heartbeat list every warnInterval")
	t.Logf("1000 namespaces, steady state: %.2f writes/s, %.2f API reads/s (per-namespace renewals every 20s were %.0f writes/s)",
		float64(writes)/window.Seconds(), float64(reads)/window.Seconds(), float64(n)/20)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

// TestGate_WarnsUnrunShard (#1505 QA): the default shard emits a Warning
// Event, once, on a namespace labelled for a shard whose heartbeat it has
// not seen move, after giving shards leaseDuration to start; a running
// shard's namespace gets none.
func TestGate_WarnsUnrunShard(t *testing.T) {
	w := newWorld(t, namespace("team-b", "b"), namespace("team-c", "c"))
	def, b := w.shard(DefaultShard, t0), w.shard("b", t0)
	run(leaseDuration+warnInterval+passInterval, def, b)
	run(2*warnInterval, def, b)
	var got []string
	for len(def.rec.Events) > 0 {
		got = append(got, <-def.rec.Events)
	}
	require.Len(t, got, 1, "%v", got)
	assert.Contains(t, got[0], "Warning "+ReasonShardNotRunning)
	assert.Contains(t, got[0], "namespace team-c is labelled kardinal.io/shard=c, but no controller runs shard c")
}

// TestGate_ConcurrentReconciles: many reconciles in parallel are counted and
// a handoff still waits for all of them (run with -race).
func TestGate_ConcurrentReconciles(t *testing.T) {
	w := newWorld(t, namespace("team", ""))
	g := w.shard(DefaultShard, t0)
	ctx := context.Background()
	run(passInterval, g)
	var running atomic.Int32
	release := make(chan struct{})
	r := reconcile.Func(func(context.Context, reconcile.Request) (reconcile.Result, error) {
		running.Add(1)
		<-release
		return reconcile.Result{}, nil
	})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = g.Wrap(r).Reconcile(ctx, req("team")) }()
	}
	require.Eventually(t, func() bool { return running.Load() == 20 }, 5*time.Second, 10*time.Millisecond)
	w.relabel("team", "b")
	run(passInterval, g)
	assert.Equal(t, "kardinal-shard/default", w.holder("team"))
	close(release)
	wg.Wait()
	run(passInterval, g)
	assert.Equal(t, "", w.holder("team"))
}
