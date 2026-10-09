// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package shard splits kardinal's reconcilers across controller
// installations by namespace (--namespace-shard).
//
// A namespace labelled kardinal.io/shard=<name> belongs to shard <name>; a
// namespace without the label (or labelled "default") belongs to shard
// "default", which also owns the cluster-scoped kinds (ChangeWindow). Every
// controller of a sharded cluster runs with a shard name; without one
// (sharding off) a controller owns everything, as before.
//
// The label says where a namespace should go; a Lease says who has it. A
// shard reconciles a namespace only while its leader holds the Lease
// kardinal-shard in that namespace. When a namespace is relabelled, the old
// shard stops starting reconciles there, waits for those in flight (a git
// push or an open PR is not idempotent across controllers) and releases the
// Lease; the new shard then takes it and enqueues every object of the
// namespace. A shard that dies holds its Leases until they expire
// (leaseDuration), so a handoff never has two owners.
//
// The Gate is controller plumbing, not promotion state: which namespaces a
// shard holds is recorded in the Lease objects, and a restarted leader takes
// its Leases back at once (the holder is the shard, not the Pod).
package shard

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	// LabelShard assigns a namespace to a shard.
	LabelShard = "kardinal.io/shard"
	// DefaultShard owns namespaces without LabelShard and cluster-scoped kinds.
	DefaultShard = "default"
	// LeaseName is the per-namespace Lease that records which shard holds it.
	LeaseName = "kardinal-shard"
	// leaseDuration is how long a Lease holds without renewal: how long a
	// namespace waits when its shard died before another shard takes it.
	leaseDuration = 60 * time.Second
	// passInterval is how often the leader takes, renews and releases Leases.
	passInterval = 5 * time.Second
	// renewAfter is how old a held Lease's renewal may get before the next
	// pass renews it: a third of leaseDuration, so a busy API server can
	// miss two renewals.
	renewAfter = leaseDuration / 3
	// pendingRequeue is how soon a reconcile of a namespace this shard is
	// taking over is retried.
	pendingRequeue = 5 * time.Second
)

// Gate decides whether this controller reconciles a namespace. The zero
// Gate (sharding off) owns everything.
type Gate struct {
	name   string
	client client.Client
	reader client.Reader
	log    zerolog.Logger
	now    func() time.Time

	mu        sync.Mutex
	held      map[string]bool // Leases this shard holds
	releasing map[string]bool // held, but relabelled away: no new reconciles
	inflight  map[string]int  // running reconciles per namespace
	subs      []chan event.GenericEvent
}

var active = &Gate{}

// Install makes g the Gate the reconcilers use (Active). main calls it
// before the reconcilers are set up.
func Install(g *Gate) { active = g }

// Active returns the installed Gate; sharding is off until Install.
func Active() *Gate { return active }

// ValidateName checks a --namespace-shard value: a label value.
func ValidateName(name string) error {
	if errs := validation.IsValidLabelValue(name); len(errs) > 0 {
		return fmt.Errorf("--namespace-shard %q is not a valid label value: %v", name, errs)
	}
	return nil
}

// New returns the Gate of shard name. c reads Namespaces and writes Leases;
// reader reads Leases (the manager's cached client is fine: an Update with a
// stale resourceVersion fails and the next pass retries).
func New(name string, c client.Client, reader client.Reader, log zerolog.Logger) *Gate {
	return &Gate{name: name, client: c, reader: reader, log: log.With().Str("shard", name).Logger(),
		now: time.Now, held: map[string]bool{}, releasing: map[string]bool{}, inflight: map[string]int{}}
}

// Enabled reports whether sharding is on.
func (g *Gate) Enabled() bool { return g != nil && g.name != "" }

// Name is the shard name, "" when sharding is off.
func (g *Gate) Name() string { return g.name }

// identity is the Lease holder: the shard, so its next leader takes over.
func (g *Gate) identity() string { return "kardinal-shard/" + g.name }

// OwnsClusterScoped reports whether this controller reconciles
// cluster-scoped kinds and runs cluster-wide sweeps: when sharding is off,
// and in the default shard.
func (g *Gate) OwnsClusterScoped() bool { return !g.Enabled() || g.name == DefaultShard }

// Mine reports whether ns's label assigns it to this shard.
func (g *Gate) Mine(ns *corev1.Namespace) bool {
	v := ns.Labels[LabelShard]
	if g.name == DefaultShard {
		return v == "" || v == DefaultShard
	}
	return v == g.name
}

// Owns reports whether this controller may reconcile objects in namespace
// now ("" is a cluster-scoped object).
func (g *Gate) Owns(namespace string) bool {
	if !g.Enabled() {
		return true
	}
	if namespace == "" {
		return g.OwnsClusterScoped()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.held[namespace] && !g.releasing[namespace]
}

// Wrap gates r: a request for a namespace this shard holds runs (and is
// counted as in flight, so a handoff waits for it); one for a namespace
// assigned to this shard whose Lease it is still taking is retried in
// pendingRequeue; any other is dropped, the owning shard reconciles it.
func (g *Gate) Wrap(r reconcile.Reconciler) reconcile.Reconciler {
	if !g.Enabled() {
		return r
	}
	return reconcile.Func(func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
		ns := req.Namespace
		if ns == "" {
			if g.OwnsClusterScoped() {
				return r.Reconcile(ctx, req)
			}
			return reconcile.Result{}, nil
		}
		g.mu.Lock()
		if g.held[ns] && !g.releasing[ns] {
			g.inflight[ns]++
			g.mu.Unlock()
			defer func() {
				g.mu.Lock()
				g.inflight[ns]--
				g.mu.Unlock()
			}()
			return r.Reconcile(ctx, req)
		}
		g.mu.Unlock()
		var n corev1.Namespace
		if err := g.client.Get(ctx, client.ObjectKey{Name: ns}, &n); err == nil && g.Mine(&n) && n.DeletionTimestamp == nil {
			return reconcile.Result{RequeueAfter: pendingRequeue}, nil
		}
		return reconcile.Result{}, nil
	})
}

// Complete finishes b with r gated by g and, when sharding is on, a watch
// that enqueues every object of list's kind in a namespace when this shard
// takes it over. list is the kind's list type (&v1alpha1.BundleList{}).
func (g *Gate) Complete(b *builder.Builder, r reconcile.Reconciler, list client.ObjectList) error {
	if g.Enabled() {
		b = b.WatchesRawSource(source.Channel(g.subscribe(), handler.EnqueueRequestsFromMapFunc(g.objectsIn(list))))
	}
	return b.Complete(g.Wrap(r))
}

// subscribe returns a channel that receives a Namespace event each time this
// shard takes the namespace over.
func (g *Gate) subscribe() chan event.GenericEvent {
	ch := make(chan event.GenericEvent, 256)
	g.mu.Lock()
	g.subs = append(g.subs, ch)
	g.mu.Unlock()
	return ch
}

// objectsIn maps a Namespace event to the objects of list's kind in it.
func (g *Gate) objectsIn(list client.ObjectList) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		l, ok := list.DeepCopyObject().(client.ObjectList)
		if !ok {
			return nil
		}
		if err := g.client.List(ctx, l, client.InNamespace(obj.GetName())); err != nil {
			g.log.Warn().Err(err).Str("namespace", obj.GetName()).Msg("list objects of a namespace taken over")
			return nil
		}
		items, err := meta.ExtractList(l)
		if err != nil {
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(items))
		for _, it := range items {
			if o, ok := it.(client.Object); ok {
				reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(o)})
			}
		}
		return reqs
	}
}

// NeedLeaderElection: only the shard's leader takes Leases.
func (g *Gate) NeedLeaderElection() bool { return true }

// Start runs the Lease loop until ctx is done. It does not release the
// Leases on shutdown: the shard's next leader holds them under the same
// identity at once, and reconciles still in flight keep their namespace.
func (g *Gate) Start(ctx context.Context) error {
	if !g.Enabled() {
		<-ctx.Done()
		return nil
	}
	t := time.NewTicker(passInterval)
	defer t.Stop()
	for {
		g.Pass(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Pass takes the Leases of the namespaces assigned to this shard, renews
// the ones it holds, and releases the ones relabelled away once nothing is in
// flight there. Exported for tests.
func (g *Gate) Pass(ctx context.Context) {
	var list corev1.NamespaceList
	if err := g.client.List(ctx, &list); err != nil {
		g.log.Warn().Err(err).Msg("list namespaces")
		return
	}
	seen := map[string]bool{}
	for i := range list.Items {
		ns := &list.Items[i]
		seen[ns.Name] = true
		mine := g.Mine(ns) && ns.DeletionTimestamp == nil
		g.mu.Lock()
		held := g.held[ns.Name]
		if !mine && held {
			g.releasing[ns.Name] = true
		}
		idle := g.inflight[ns.Name] == 0
		g.mu.Unlock()
		switch {
		case mine:
			g.take(ctx, ns)
		case held && idle:
			g.release(ctx, ns.Name)
		}
	}
	// A deleted namespace takes its Lease with it.
	g.mu.Lock()
	for ns := range g.held {
		if !seen[ns] {
			delete(g.held, ns)
			delete(g.releasing, ns)
		}
	}
	g.mu.Unlock()
}

// take acquires or renews the Lease of ns; a newly taken namespace is
// announced to every reconciler.
func (g *Gate) take(ctx context.Context, ns *corev1.Namespace) {
	now := metav1.NewMicroTime(g.now())
	id := g.identity()
	secs := int32(leaseDuration / time.Second)
	var lease coordinationv1.Lease
	err := g.reader.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: LeaseName}, &lease)
	g.mu.Lock()
	was := g.held[ns.Name]
	g.mu.Unlock()
	switch {
	case apierrors.IsNotFound(err):
		lease = coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Name: LeaseName, Namespace: ns.Name,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "kardinal-promoter"}},
			Spec: coordinationv1.LeaseSpec{HolderIdentity: &id, LeaseDurationSeconds: &secs,
				AcquireTime: &now, RenewTime: &now},
		}
		if err := g.client.Create(ctx, &lease); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				g.log.Warn().Err(err).Str("namespace", ns.Name).Msg("create shard Lease")
			}
			return
		}
		g.acquired(ns)
		return
	case err != nil:
		g.log.Warn().Err(err).Str("namespace", ns.Name).Msg("read shard Lease")
		return
	}
	holder := ""
	if lease.Spec.HolderIdentity != nil {
		holder = *lease.Spec.HolderIdentity
	}
	if holder != id && holder != "" && !expired(&lease, now.Time) {
		g.log.Debug().Str("namespace", ns.Name).Str("holder", holder).Msg("namespace assigned to this shard; waiting for its holder to release it")
		return
	}
	takeover := holder != id
	if !takeover && was && lease.Spec.RenewTime != nil && now.Sub(lease.Spec.RenewTime.Time) < renewAfter {
		return // renewed recently
	}
	lease.Spec.HolderIdentity = &id
	lease.Spec.LeaseDurationSeconds = &secs
	lease.Spec.RenewTime = &now
	if takeover {
		lease.Spec.AcquireTime = &now
		if lease.Spec.LeaseTransitions == nil {
			lease.Spec.LeaseTransitions = new(int32)
		}
		*lease.Spec.LeaseTransitions++
	}
	if err := g.client.Update(ctx, &lease); err != nil {
		if !apierrors.IsConflict(err) {
			g.log.Warn().Err(err).Str("namespace", ns.Name).Msg("update shard Lease")
		}
		return
	}
	if takeover || !was {
		if takeover {
			g.log.Info().Str("namespace", ns.Name).Str("from", holder).Msg("took over namespace")
		}
		g.acquired(ns)
	}
}

func expired(l *coordinationv1.Lease, now time.Time) bool {
	if l.Spec.RenewTime == nil || l.Spec.LeaseDurationSeconds == nil {
		return true
	}
	return now.After(l.Spec.RenewTime.Add(time.Duration(*l.Spec.LeaseDurationSeconds) * time.Second))
}

// acquired records ns as held and enqueues its objects.
func (g *Gate) acquired(ns *corev1.Namespace) {
	g.mu.Lock()
	g.held[ns.Name] = true
	delete(g.releasing, ns.Name)
	subs := append([]chan event.GenericEvent(nil), g.subs...)
	g.mu.Unlock()
	g.log.Info().Str("namespace", ns.Name).Msg("holding namespace")
	for _, ch := range subs {
		go func() { ch <- event.GenericEvent{Object: ns.DeepCopy()} }()
	}
}

// release gives up the Lease of ns, so the shard it is assigned to takes it
// at once instead of after leaseDuration.
func (g *Gate) release(ctx context.Context, ns string) {
	var lease coordinationv1.Lease
	if err := g.reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: LeaseName}, &lease); err == nil &&
		lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity == g.identity() {
		empty := ""
		lease.Spec.HolderIdentity = &empty
		if err := g.client.Update(ctx, &lease); err != nil {
			g.log.Warn().Err(err).Str("namespace", ns).Msg("release shard Lease")
			return
		}
	}
	g.mu.Lock()
	delete(g.held, ns)
	delete(g.releasing, ns)
	g.mu.Unlock()
	g.log.Info().Str("namespace", ns).Msg("released namespace")
}

// Setup installs g as the Active gate and adds its Lease loop to mgr.
func Setup(mgr ctrl.Manager, g *Gate) error {
	Install(g)
	if !g.Enabled() {
		return nil
	}
	if err := mgr.Add(g); err != nil {
		return fmt.Errorf("add shard gate: %w", err)
	}
	return nil
}
