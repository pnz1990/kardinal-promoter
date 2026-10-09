// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package shard

import (
	"context"
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

type world struct {
	t   *testing.T
	c   client.Client
	clk *clock
}

func newWorld(t *testing.T, objs ...client.Object) *world {
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, coordinationv1.AddToScheme(s))
	return &world{t: t, c: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build(),
		clk: &clock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}}
}

func (w *world) gate(name string) *Gate {
	g := New(name, w.c, w.c, zerolog.Nop())
	g.now = w.clk.now
	return g
}

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

// counting is a reconciler that counts calls by namespace and can block.
type counting struct {
	mu    sync.Mutex
	calls map[string]int
	block chan struct{}
	in    chan struct{}
}

func (c *counting) Reconcile(_ context.Context, req reconcile.Request) (reconcile.Result, error) {
	c.mu.Lock()
	if c.calls == nil {
		c.calls = map[string]int{}
	}
	c.calls[req.Namespace]++
	block := c.block
	c.mu.Unlock()
	if block != nil {
		c.in <- struct{}{}
		<-block
	}
	return reconcile.Result{}, nil
}

func (c *counting) count(ns string) int { c.mu.Lock(); defer c.mu.Unlock(); return c.calls[ns] }

func req(ns string) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "obj"}}
}

// TestGate_AssignsNamespacesByLabel: shard "default" takes namespaces
// without the label and the cluster-scoped kinds, shard "b" the namespaces
// labelled b; each reconciles only what it holds, and requeues a namespace it
// is still taking. A Pass is idempotent.
func TestGate_AssignsNamespacesByLabel(t *testing.T) {
	w := newWorld(t, namespace("team-a", ""), namespace("team-b", "b"), namespace("team-c", "c"))
	def, b := w.gate(DefaultShard), w.gate("b")
	ctx := context.Background()

	r := &counting{}
	wrapped := b.Wrap(r)
	res, err := wrapped.Reconcile(ctx, req("team-b"))
	require.NoError(t, err)
	assert.Equal(t, pendingRequeue, res.RequeueAfter, "assigned but not held yet: retried")
	assert.Zero(t, r.count("team-b"))

	for range 2 {
		def.Pass(ctx)
		b.Pass(ctx)
	}
	assert.Equal(t, "kardinal-shard/default", w.holder("team-a"))
	assert.Equal(t, "kardinal-shard/b", w.holder("team-b"))
	var l coordinationv1.Lease
	assert.Error(t, w.c.Get(ctx, types.NamespacedName{Namespace: "team-c", Name: LeaseName}, &l), "no shard c running: no Lease")

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

// TestGate_HandoffWaitsForInFlight: relabelling a namespace from default to
// b while default has a reconcile running there: default stops starting
// reconciles at once but keeps the Lease until the running one returns; b
// waits, then takes the namespace and announces it, so its reconcilers
// enqueue the namespace's objects. There is never a moment with two owners.
func TestGate_HandoffWaitsForInFlight(t *testing.T) {
	w := newWorld(t, namespace("team", ""))
	def, b := w.gate(DefaultShard), w.gate("b")
	ctx := context.Background()
	events := b.subscribe()
	def.Pass(ctx)
	require.True(t, def.Owns("team"))

	r := &counting{block: make(chan struct{}), in: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		_, _ = def.Wrap(r).Reconcile(ctx, req("team"))
		close(done)
	}()
	<-r.in // the reconcile is running

	w.relabel("team", "b")
	def.Pass(ctx)
	assert.False(t, def.Owns("team"), "no new reconcile starts after the relabel")
	b.Pass(ctx)
	assert.False(t, b.Owns("team"), "b waits for the reconcile in flight")
	assert.Equal(t, "kardinal-shard/default", w.holder("team"))

	close(r.block)
	<-done
	def.Pass(ctx)
	assert.Equal(t, "", w.holder("team"), "released after the reconcile returned")
	b.Pass(ctx)
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
}

// TestGate_DeadShardExpires: a shard that stopped renewing keeps its
// namespaces until the Lease expires; then the shard they are assigned to
// takes them. A new leader of the same shard takes its Leases at once.
func TestGate_DeadShardExpires(t *testing.T) {
	w := newWorld(t, namespace("team", ""))
	ctx := context.Background()
	def := w.gate(DefaultShard)
	def.Pass(ctx)
	// default's leader dies; the namespace moves to b.
	w.relabel("team", "b")
	b := w.gate("b")
	b.Pass(ctx)
	assert.False(t, b.Owns("team"))
	w.clk.add(leaseDuration - time.Second)
	b.Pass(ctx)
	assert.False(t, b.Owns("team"), "not expired yet")
	w.clk.add(2 * time.Second)
	b.Pass(ctx)
	assert.True(t, b.Owns("team"), "expired: taken over")

	next := w.gate("b") // b's next leader
	next.Pass(ctx)
	assert.True(t, next.Owns("team"), "same shard, same holder: at once")
}

// TestGate_RenewsOnlyWhenDue: a held Lease is renewed once its renewal is a
// third of the Lease duration old, not on every pass.
func TestGate_RenewsOnlyWhenDue(t *testing.T) {
	w := newWorld(t, namespace("team", ""))
	ctx := context.Background()
	g := w.gate(DefaultShard)
	g.Pass(ctx)
	renew := func() time.Time {
		var l coordinationv1.Lease
		require.NoError(t, w.c.Get(ctx, types.NamespacedName{Namespace: "team", Name: LeaseName}, &l))
		return l.Spec.RenewTime.Time
	}
	first := renew()
	w.clk.add(passInterval)
	g.Pass(ctx)
	assert.True(t, first.Equal(renew()), "renewed recently: no write")
	w.clk.add(renewAfter)
	g.Pass(ctx)
	assert.True(t, renew().After(first))
}

// TestGate_ConcurrentReconciles: many reconciles in parallel are counted and
// a handoff still waits for all of them (run with -race).
func TestGate_ConcurrentReconciles(t *testing.T) {
	w := newWorld(t, namespace("team", ""))
	ctx := context.Background()
	g := w.gate(DefaultShard)
	g.Pass(ctx)
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
	g.Pass(ctx)
	assert.Equal(t, "kardinal-shard/default", w.holder("team"))
	close(release)
	wg.Wait()
	g.Pass(ctx)
	assert.Equal(t, "", w.holder("team"))
}
