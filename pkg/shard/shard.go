// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package shard splits kardinal's reconcilers across controller
// installations by namespace (--namespace-shard).
//
// A namespace labelled kardinal.io/shard=<name> belongs to shard <name>; a
// namespace without the label (or labelled "default") belongs to shard
// "default", which also owns the cluster-scoped kinds (ChangeWindow). Every
// controller of a sharded cluster runs with a shard name; without one
// (sharding off) a controller owns everything, as before.
//
// The label says where a namespace should go; two kinds of Lease say who has
// it and whether that shard is alive:
//
//   - The token: Lease kardinal-shard in each namespace names the shard that
//     holds it (spec.holderIdentity kardinal-shard/<name>, and the namespace
//     of that shard's heartbeat in an annotation). It is written only when a
//     namespace changes hands, with the resourceVersion of the read, so two
//     shards cannot both take it.
//   - The heartbeat: Lease kardinal-shard-heartbeat in each shard's own
//     namespace, renewed by the shard's leader every renewInterval. Steady
//     state costs one write per shard per renewInterval, however many
//     namespaces it holds.
//
// A shard reconciles a namespace only while it holds the token and its own
// heartbeat is fresh. When a namespace is relabelled, the old shard stops
// starting reconciles there, waits for the ones in flight (a git push or an
// open PR is not idempotent across controllers; its heartbeat keeps the token
// valid meanwhile) and gives the token up; the new shard then takes it and
// enqueues every object of the namespace. A token whose holder's heartbeat
// did not change for leaseDuration, as measured by the taking shard's own
// clock from when it saw the heartbeat change (as client-go leader election
// does: wall-clock renew times are not compared across hosts), is taken over.
//
// A shard fences itself: once leaseDuration-fenceMargin passed on its own
// monotonic clock since the start of its last successful heartbeat renewal,
// it starts no reconcile and cancels the ones in flight, before any other
// shard can consider it dead. It renews only in a pass that also listed the
// namespaces, so a shard that cannot see the labels fences itself and lets
// the others take over. After a fence it re-reads its tokens from the API
// server before it reconciles again.
//
// The Gate is controller plumbing, not promotion state: which namespaces a
// shard holds is recorded in the token Leases, and a restarted leader takes
// its tokens back at once (the holder is the shard, not the Pod).
package shard

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
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
	// LeaseName is the token Lease in each namespace: which shard holds it.
	LeaseName = "kardinal-shard"
	// HeartbeatPrefix names each shard's heartbeat Lease, in the shard's own
	// namespace: kardinal-shard-heartbeat-<shard>, so two shards installed in
	// one namespace do not share one.
	HeartbeatPrefix = "kardinal-shard-heartbeat-"
	// labelHeartbeat marks heartbeat Leases, so the default shard lists them.
	labelHeartbeat = "kardinal.io/shard-heartbeat"
	// AnnotationHeartbeat on a token is the namespace of its holder's heartbeat.
	AnnotationHeartbeat = "kardinal.io/shard-heartbeat"
	// labelManagedBy marks both kinds of Lease; the controller caches only
	// Leases named LeaseName with this label (CacheByObject).
	labelManagedBy = "app.kubernetes.io/managed-by"
	managedBy      = "kardinal-promoter"

	// leaseDuration is how long a heartbeat may stay unchanged before the
	// shard is taken for dead: how long a namespace waits when its shard died.
	leaseDuration = 60 * time.Second
	// renewInterval is how often the leader renews its heartbeat.
	renewInterval = 10 * time.Second
	// fenceMargin: a shard stops acting fenceMargin before the others may
	// take its namespaces, which covers clock rate differences and the time
	// between a renewal's start and another shard seeing it.
	fenceMargin = 16 * time.Second
	// passInterval is how often the leader takes and releases tokens.
	passInterval = 5 * time.Second
	// fenceTick is how often the fence is checked on its own ticker, so a
	// pass blocked in an API call cannot delay it.
	fenceTick = time.Second
	// callTimeout bounds every API call of a pass, well under renewInterval.
	callTimeout = 5 * time.Second
	// maxClockRate is the clock rate difference between hosts the fence
	// margin is computed for (1%; NTP-disciplined clocks are within 0.05%).
	maxClockRate = 0.01
	// pendingRequeue and maxPendingRequeue bound the retry of a reconcile of
	// a namespace this shard waits for: it grows with the wait. Taking the
	// namespace enqueues all its objects anyway.
	pendingRequeue    = 5 * time.Second
	maxPendingRequeue = 2 * time.Minute
	// warnInterval is how often the default shard checks for namespaces
	// labelled with a shard nobody runs.
	warnInterval = time.Minute
	// ReasonShardNotRunning is the Warning Event on such a namespace.
	ReasonShardNotRunning = "ShardNotRunning"
	// ReasonShardHomeConflict is the Warning Event on a namespace whose token
	// this shard holds from another live installation (two installations
	// of one shard name).
	ReasonShardHomeConflict = "ShardHomeConflict"
)

// Options configure a Gate.
type Options struct {
	// Name is the shard ("" turns sharding off).
	Name string
	// Home is the namespace of this shard's heartbeat (the controller's).
	Home string
	// Client reads Namespaces and tokens (the manager's cached client) and
	// writes Leases.
	Client client.Client
	// Reader reads heartbeats and, after a fence, tokens from the API server.
	Reader client.Reader
	// Recorder emits ShardNotRunning (default shard). Optional.
	Recorder events.EventRecorder
	Log      zerolog.Logger
}

// observation is when this shard last saw a heartbeat change, by its clock.
type observation struct {
	rv string
	at time.Time
}

// Gate decides whether this controller reconciles a namespace. The zero
// Gate (sharding off) owns everything.
type Gate struct {
	name, home string
	client     client.Client
	reader     client.Reader
	recorder   events.EventRecorder
	log        zerolog.Logger
	now        func() time.Time

	mu        sync.Mutex
	held      map[string]bool // tokens this shard holds
	releasing map[string]bool // held, but relabelled away: no new reconciles
	inflight  map[string]int  // running reconciles per namespace
	cancels   map[uint64]context.CancelFunc
	nextID    uint64
	renewedAt time.Time // start of the last successful heartbeat renewal
	fenced    bool
	waitSince map[string]time.Time // assigned here, held elsewhere
	subs      []chan event.GenericEvent

	// Lease loop only (no lock).
	observed  map[string]observation // heartbeat namespace -> last change seen
	startedAt time.Time
	warnedAt  time.Time
	warned    map[string]string // namespace -> shard it was warned about
	conflicts map[string]bool   // namespaces a ShardHomeConflict was emitted for
	timeout   time.Duration     // callTimeout; tests shorten it
}

var active = &Gate{}

// Install makes g the Gate the reconcilers use (Active). main calls it
// before the reconcilers are set up.
func Install(g *Gate) { active = g }

// Active returns the installed Gate; sharding is off until Install.
func Active() *Gate { return active }

// ValidateName checks a --namespace-shard value: a label value that is also
// a DNS label, since it is part of the heartbeat Lease name.
func ValidateName(name string) error {
	if errs := validation.IsValidLabelValue(name); len(errs) > 0 {
		return fmt.Errorf("--namespace-shard %q is not a valid label value: %v", name, errs)
	}
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		return fmt.Errorf("--namespace-shard %q is not a lowercase DNS label: %v", name, errs)
	}
	return nil
}

// HeartbeatName is the heartbeat Lease of shard.
func HeartbeatName(shard string) string { return HeartbeatPrefix + shard }

// New returns the Gate of shard o.Name.
func New(o Options) *Gate {
	return &Gate{name: o.Name, home: o.Home, client: o.Client, reader: o.Reader, recorder: o.Recorder,
		log: o.Log.With().Str("shard", o.Name).Logger(), now: time.Now,
		held: map[string]bool{}, releasing: map[string]bool{}, inflight: map[string]int{},
		cancels: map[uint64]context.CancelFunc{}, waitSince: map[string]time.Time{},
		observed: map[string]observation{}, warned: map[string]string{}, conflicts: map[string]bool{},
		timeout: callTimeout}
}

// CacheByObject restricts the manager's Lease cache to the tokens: Leases
// named LeaseName labelled as kardinal's. The field selector is what lets
// RBAC limit list and watch to that name (resourceNames).
func CacheByObject() map[client.Object]cache.ByObject {
	return map[client.Object]cache.ByObject{&coordinationv1.Lease{}: {
		Field: fields.OneTermEqualSelector("metadata.name", LeaseName),
		Label: labels.SelectorFromSet(labels.Set{labelManagedBy: managedBy}),
	}}
}

// Enabled reports whether sharding is on.
func (g *Gate) Enabled() bool { return g != nil && g.name != "" }

// Name is the shard name, "" when sharding is off.
func (g *Gate) Name() string { return g.name }

// identity is the token holder: the shard, so its next leader takes over.
func (g *Gate) identity() string { return identityOf(g.name) }

func identityOf(shard string) string { return "kardinal-shard/" + shard }

// OwnsClusterScoped reports whether this controller reconciles
// cluster-scoped kinds and runs cluster-wide sweeps: when sharding is off,
// and in the default shard.
func (g *Gate) OwnsClusterScoped() bool { return !g.Enabled() || g.name == DefaultShard }

// shardOf is the shard ns's label assigns it to.
func shardOf(ns *corev1.Namespace) string {
	if v := ns.Labels[LabelShard]; v != "" {
		return v
	}
	return DefaultShard
}

// Mine reports whether ns's label assigns it to this shard.
func (g *Gate) Mine(ns *corev1.Namespace) bool { return shardOf(ns) == g.name }

// Owns reports whether this controller may reconcile objects in namespace
// now ("" is a cluster-scoped object).
func (g *Gate) Owns(namespace string) bool {
	if !g.Enabled() {
		return true
	}
	if namespace == "" {
		return g.OwnsClusterScoped()
	}
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.held[namespace] && !g.releasing[namespace] && !g.fencedAt(now)
}

// fencedAt reports, with g.mu held, whether the shard must not act at now:
// fenced already, or its heartbeat is too old (renewedAt + leaseDuration -
// fenceMargin). Callers check it with a fresh now, so neither the fence
// ticker nor a blocked pass delays it.
func (g *Gate) fencedAt(now time.Time) bool {
	return g.fenced || g.renewedAt.IsZero() || now.Sub(g.renewedAt) >= leaseDuration-fenceMargin
}

// Wrap gates r: a request for a namespace this shard holds runs (counted as
// in flight, so a handoff waits for it, with a context the fence cancels);
// one for a namespace assigned to this shard that it does not hold yet is
// retried with a delay that grows with the wait; any other is dropped, the
// owning shard reconciles it.
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
		now := g.now()
		g.mu.Lock()
		if g.held[ns] && !g.releasing[ns] && !g.fencedAt(now) {
			g.inflight[ns]++
			g.nextID++
			id := g.nextID
			cctx, cancel := context.WithCancel(ctx)
			g.cancels[id] = cancel
			g.mu.Unlock()
			defer func() {
				g.mu.Lock()
				g.inflight[ns]--
				delete(g.cancels, id)
				g.mu.Unlock()
				cancel()
			}()
			return r.Reconcile(cctx, req)
		}
		fenced, held := g.fencedAt(now), g.held[ns]
		g.mu.Unlock()
		if fenced && held {
			return reconcile.Result{RequeueAfter: pendingRequeue}, nil
		}
		var n corev1.Namespace
		if err := g.client.Get(ctx, client.ObjectKey{Name: ns}, &n); err == nil && g.Mine(&n) && n.DeletionTimestamp == nil {
			return reconcile.Result{RequeueAfter: g.pendingDelay(ns)}, nil
		}
		return reconcile.Result{}, nil
	})
}

// pendingDelay is how soon to retry a reconcile of ns, which this shard is
// waiting to take: as long as it has waited, within [pendingRequeue,
// maxPendingRequeue].
func (g *Gate) pendingDelay(ns string) time.Duration {
	now := g.now()
	g.mu.Lock()
	since, ok := g.waitSince[ns]
	if !ok {
		g.waitSince[ns] = now
		since = now
	}
	g.mu.Unlock()
	return min(max(now.Sub(since), pendingRequeue), maxPendingRequeue)
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

// NeedLeaderElection: only the shard's leader holds tokens.
func (g *Gate) NeedLeaderElection() bool { return true }

// Start runs the Lease loop until ctx is done. It does not release the
// tokens on shutdown: the shard's next leader holds them under the same
// identity at once.
func (g *Gate) Start(ctx context.Context) error {
	if !g.Enabled() {
		<-ctx.Done()
		return nil
	}
	// The fence runs on its own ticker with a fresh now: a pass blocked in
	// an API call (each bounded by callTimeout) does not delay it.
	go func() {
		t := time.NewTicker(fenceTick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				g.fence(g.now())
			}
		}
	}()
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

// call bounds one API call of a pass.
func (g *Gate) call(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, g.timeout)
}

// Pass lists the namespaces, renews the heartbeat, and unless the shard is
// fenced takes the tokens of the namespaces assigned to it and gives up the
// ones relabelled away once nothing is in flight there. Every API call is
// bounded by callTimeout. Exported for tests.
func (g *Gate) Pass(ctx context.Context) {
	now := g.now()
	if g.startedAt.IsZero() {
		g.startedAt = now
	}
	var list corev1.NamespaceList
	cctx, cancel := g.call(ctx)
	listErr := g.client.List(cctx, &list)
	cancel()
	if listErr != nil {
		g.log.Warn().Err(listErr).Msg("list namespaces; not renewing the heartbeat")
	} else {
		g.renew(ctx, now)
	}
	if g.fence(g.now()) || listErr != nil {
		return
	}
	beats := map[string]*observation{} // heartbeats read in this pass
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
			g.take(ctx, ns, now, beats)
		case held && idle:
			g.release(ctx, ns.Name)
		case held:
			g.checkStillHeld(ctx, ns.Name)
		}
	}
	// A deleted namespace takes its token with it.
	g.mu.Lock()
	for ns := range g.held {
		if !seen[ns] {
			delete(g.held, ns)
			delete(g.releasing, ns)
		}
	}
	for ns := range g.waitSince {
		if !seen[ns] {
			delete(g.waitSince, ns)
		}
	}
	g.mu.Unlock()
	if g.name == DefaultShard {
		g.warnUnrunShards(ctx, now, list.Items)
	}
}

// renew writes the heartbeat when it is due. now is taken before the write,
// so the fence counts from no later than when other shards can see it.
func (g *Gate) renew(ctx context.Context, now time.Time) {
	g.mu.Lock()
	due := g.fencedAt(now) || now.Sub(g.renewedAt) >= renewInterval
	wasFenced := g.fenced || g.renewedAt.IsZero()
	g.mu.Unlock()
	if !due {
		return
	}
	id := g.identity()
	name := HeartbeatName(g.name)
	secs := int32(leaseDuration / time.Second)
	stamp := metav1.NewMicroTime(now)
	var hb coordinationv1.Lease
	cctx, cancel := g.call(ctx)
	defer cancel()
	err := g.reader.Get(cctx, client.ObjectKey{Namespace: g.home, Name: name}, &hb)
	switch {
	case apierrors.IsNotFound(err):
		hb = coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: g.home,
				Labels: map[string]string{labelManagedBy: managedBy, labelHeartbeat: "true", LabelShard: g.name}},
			Spec: coordinationv1.LeaseSpec{HolderIdentity: &id, LeaseDurationSeconds: &secs,
				AcquireTime: &stamp, RenewTime: &stamp},
		}
		err = g.client.Create(cctx, &hb)
	case err == nil:
		hb.Spec.HolderIdentity = &id
		hb.Spec.LeaseDurationSeconds = &secs
		hb.Spec.RenewTime = &stamp
		err = g.client.Update(cctx, &hb)
	}
	if err != nil {
		g.log.Warn().Err(err).Msg("renew the shard heartbeat")
		return
	}
	if wasFenced && !g.resync(ctx) {
		return
	}
	g.mu.Lock()
	if g.fenced {
		g.log.Info().Msg("heartbeat renewed; reconciling again")
	}
	g.renewedAt = now
	g.fenced = false
	g.mu.Unlock()
}

// resync rebuilds the held tokens from the API server (not the cache):
// after a fence another shard may have taken some.
func (g *Gate) resync(ctx context.Context) bool {
	var leases coordinationv1.LeaseList
	cctx, cancel := g.call(ctx)
	defer cancel()
	if err := g.reader.List(cctx, &leases, client.MatchingFields{"metadata.name": LeaseName},
		client.MatchingLabels{labelManagedBy: managedBy}); err != nil {
		g.log.Warn().Err(err).Msg("re-read the shard tokens")
		return false
	}
	mine := map[string]bool{}
	for i := range leases.Items {
		l := &leases.Items[i]
		if l.Spec.HolderIdentity != nil && *l.Spec.HolderIdentity == g.identity() &&
			l.Annotations[AnnotationHeartbeat] == g.home {
			mine[l.Namespace] = true
		}
	}
	g.mu.Lock()
	for ns := range g.held {
		if !mine[ns] {
			g.log.Warn().Str("namespace", ns).Msg("another shard took the namespace while this one was fenced")
			delete(g.held, ns)
			delete(g.releasing, ns)
		}
	}
	g.mu.Unlock()
	return true
}

// fence stops this shard from acting once its heartbeat may be taken for
// dead (renewedAt + leaseDuration - fenceMargin), cancelling the reconciles
// in flight. It runs on its own ticker (fenceTick) and in each pass. It
// reports whether the shard is fenced.
//
// Worst case: this shard's last successful renewal started at R. Another
// shard saw that renewal no earlier than R and waits leaseDuration on its own
// clock: at least leaseDuration*(1-maxClockRate) of real time. This shard
// starts nothing from R + (leaseDuration-fenceMargin)*(1+maxClockRate), and
// cancels what runs by fenceTick later. Overlap needs a reconcile that keeps
// going for the rest of the margin (FenceMargin) after its context was
// cancelled.
func (g *Gate) fence(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.fencedAt(now) {
		return false
	}
	if !g.fenced {
		g.fenced = true
		n := len(g.cancels)
		for _, cancel := range g.cancels {
			cancel()
		}
		if !g.renewedAt.IsZero() {
			g.log.Warn().Int("cancelled", n).Dur("sinceRenew", now.Sub(g.renewedAt)).
				Msg("heartbeat not renewed in time: fenced, reconciling nothing until it is")
		}
	}
	return true
}

// FenceMargin is the worst-case time between this shard cancelling its
// reconciles and another shard taking its namespaces (see fence).
func FenceMargin() time.Duration {
	takeover := time.Duration(float64(leaseDuration) * (1 - maxClockRate))
	stop := time.Duration(float64(leaseDuration-fenceMargin)*(1+maxClockRate)) + fenceTick
	return takeover - stop
}

// checkStillHeld drops a namespace marked held whose token (in the cache)
// names another holder: someone took it, and this shard must not act there.
func (g *Gate) checkStillHeld(ctx context.Context, ns string) {
	var lease coordinationv1.Lease
	cctx, cancel := g.call(ctx)
	defer cancel()
	err := g.client.Get(cctx, client.ObjectKey{Namespace: ns, Name: LeaseName}, &lease)
	if err != nil && !apierrors.IsNotFound(err) {
		return
	}
	if err == nil && lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity == g.identity() &&
		lease.Annotations[AnnotationHeartbeat] == g.home {
		return
	}
	g.drop(ns, "its token names another holder")
}

// drop forgets a namespace this shard no longer holds.
func (g *Gate) drop(ns, why string) {
	g.mu.Lock()
	was := g.held[ns]
	delete(g.held, ns)
	delete(g.releasing, ns)
	g.mu.Unlock()
	if was {
		g.log.Warn().Str("namespace", ns).Str("why", why).Msg("dropped a namespace this shard held")
	}
}

// take acquires the token of ns, always through a write with the
// resourceVersion read (compare-and-swap), so two shards cannot both hold
// it: a token this shard's previous leader held is adopted by rewriting it, a
// free one is taken, and one held by a shard whose heartbeat stopped is taken
// over. A held namespace whose token (in the cache) names another holder is
// dropped at once. A token that names this shard but another live
// installation of it (another home) is refused with a ShardHomeConflict
// Event. A newly held namespace is announced to every reconciler.
func (g *Gate) take(ctx context.Context, ns *corev1.Namespace, now time.Time, beats map[string]*observation) {
	id := g.identity()
	var lease coordinationv1.Lease
	cctx, cancel := g.call(ctx)
	defer cancel()
	err := g.client.Get(cctx, client.ObjectKey{Namespace: ns.Name, Name: LeaseName}, &lease)
	g.mu.Lock()
	was := g.held[ns.Name]
	g.mu.Unlock()
	stamp := metav1.NewMicroTime(now)
	switch {
	case apierrors.IsNotFound(err):
		if was {
			g.drop(ns.Name, "its token is gone")
		}
		secs := int32(leaseDuration / time.Second)
		lease = coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Name: LeaseName, Namespace: ns.Name,
				Labels:      map[string]string{labelManagedBy: managedBy},
				Annotations: map[string]string{AnnotationHeartbeat: g.home}},
			Spec: coordinationv1.LeaseSpec{HolderIdentity: &id, LeaseDurationSeconds: &secs,
				AcquireTime: &stamp, RenewTime: &stamp},
		}
		if err := g.client.Create(cctx, &lease); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				g.log.Warn().Err(err).Str("namespace", ns.Name).Msg("create shard token")
			}
			return
		}
		g.acquired(ns)
		return
	case err != nil:
		g.log.Warn().Err(err).Str("namespace", ns.Name).Msg("read shard token")
		return
	}
	holder := ""
	if lease.Spec.HolderIdentity != nil {
		holder = *lease.Spec.HolderIdentity
	}
	home := lease.Annotations[AnnotationHeartbeat]
	if holder == id && home == g.home {
		if !was {
			// This shard's previous leader held it: adopt it with a write, so
			// a concurrent taker's write conflicts with ours.
			lease.Spec.RenewTime = &stamp
			if err := g.client.Update(cctx, &lease); err != nil {
				if !apierrors.IsConflict(err) {
					g.log.Warn().Err(err).Str("namespace", ns.Name).Msg("adopt shard token")
				}
				return
			}
			g.acquired(ns)
		}
		return
	}
	if was {
		g.drop(ns.Name, "its token names another holder")
	}
	if holder != "" && !g.heartbeatStopped(ctx, holder, home, beats) {
		if holder == id {
			g.homeConflict(ns, home)
			return
		}
		g.log.Debug().Str("namespace", ns.Name).Str("holder", holder).
			Msg("namespace assigned to this shard; waiting for its holder to release it")
		return
	}
	takeover := holder != id || home != g.home
	lease.Spec.HolderIdentity = &id
	lease.Spec.RenewTime = &stamp
	if lease.Annotations == nil {
		lease.Annotations = map[string]string{}
	}
	lease.Annotations[AnnotationHeartbeat] = g.home
	if takeover {
		lease.Spec.AcquireTime = &stamp
		if lease.Spec.LeaseTransitions == nil {
			lease.Spec.LeaseTransitions = new(int32)
		}
		*lease.Spec.LeaseTransitions++
	}
	if err := g.client.Update(cctx, &lease); err != nil {
		if !apierrors.IsConflict(err) {
			g.log.Warn().Err(err).Str("namespace", ns.Name).Msg("update shard token")
		}
		return
	}
	if holder != "" {
		g.log.Info().Str("namespace", ns.Name).Str("from", holder).Str("fromHome", home).
			Msg("took over the namespace of a shard whose heartbeat stopped")
	}
	g.acquired(ns)
}

// homeConflict reports, once per namespace, a token held by this shard's
// name from another live installation (home): two controllers run one shard.
func (g *Gate) homeConflict(ns *corev1.Namespace, home string) {
	if g.conflicts[ns.Name] {
		return
	}
	g.conflicts[ns.Name] = true
	note := fmt.Sprintf("namespace %s is held by shard %s installed in namespace %s, which is alive; this installation "+
		"(namespace %s) runs the same shard name and does not take it. Run each shard name once", ns.Name, g.name, home, g.home)
	g.log.Warn().Str("namespace", ns.Name).Str("otherHome", home).Msg(note)
	if g.recorder != nil {
		g.recorder.Eventf(ns, nil, corev1.EventTypeWarning, ReasonShardHomeConflict, "Reconcile", "%s", note)
	}
}

// heartbeatStopped reports whether the heartbeat of holder, in namespace
// home, has not changed for leaseDuration as this shard observed it: from
// the first time this shard saw its current resourceVersion (or saw it
// missing), on this shard's clock. A heartbeat that cannot be read is alive.
// beats memoizes the reads of one pass.
func (g *Gate) heartbeatStopped(ctx context.Context, holder, home string, beats map[string]*observation) bool {
	key := home + "/" + holder
	if o, ok := beats[key]; ok {
		return o != nil && g.now().Sub(o.at) >= leaseDuration
	}
	rv := "missing"
	if home != "" {
		var hb coordinationv1.Lease
		cctx, cancel := g.call(ctx)
		err := g.reader.Get(cctx, client.ObjectKey{Namespace: home, Name: HeartbeatName(strings.TrimPrefix(holder, "kardinal-shard/"))}, &hb)
		cancel()
		switch {
		case err == nil && hb.Spec.HolderIdentity != nil && *hb.Spec.HolderIdentity == holder:
			rv = hb.ResourceVersion
		case err == nil, apierrors.IsNotFound(err):
			// another shard's heartbeat, or none: the holder renews nothing
		default:
			g.log.Debug().Err(err).Str("heartbeat", key).Msg("read shard heartbeat")
			beats[key] = nil
			return false
		}
	}
	// The version was seen now, after the read returned, not when the pass
	// started: a slow read must not date a new heartbeat version earlier
	// than this shard could have seen it, which would shorten the wait.
	seen := g.now()
	o, ok := g.observed[key]
	if !ok || o.rv != rv {
		o = observation{rv: rv, at: seen}
		g.observed[key] = o
	}
	beats[key] = &o
	return seen.Sub(o.at) >= leaseDuration
}

// acquired records ns as held and enqueues its objects.
func (g *Gate) acquired(ns *corev1.Namespace) {
	g.mu.Lock()
	g.held[ns.Name] = true
	delete(g.releasing, ns.Name)
	delete(g.waitSince, ns.Name)
	subs := append([]chan event.GenericEvent(nil), g.subs...)
	g.mu.Unlock()
	delete(g.conflicts, ns.Name)
	g.log.Info().Str("namespace", ns.Name).Msg("holding namespace")
	for _, ch := range subs {
		go func() { ch <- event.GenericEvent{Object: ns.DeepCopy()} }()
	}
}

// release gives up the token of ns, so the shard it is assigned to takes it
// at once instead of after leaseDuration.
func (g *Gate) release(ctx context.Context, ns string) {
	var lease coordinationv1.Lease
	cctx, cancel := g.call(ctx)
	defer cancel()
	if err := g.client.Get(cctx, client.ObjectKey{Namespace: ns, Name: LeaseName}, &lease); err == nil &&
		lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity == g.identity() {
		empty := ""
		lease.Spec.HolderIdentity = &empty
		if err := g.client.Update(cctx, &lease); err != nil {
			if !apierrors.IsConflict(err) {
				g.log.Warn().Err(err).Str("namespace", ns).Msg("release shard token")
			}
			return
		}
	} else if err != nil && !apierrors.IsNotFound(err) {
		g.log.Warn().Err(err).Str("namespace", ns).Msg("read shard token to release it")
		return
	}
	g.mu.Lock()
	delete(g.held, ns)
	delete(g.releasing, ns)
	g.mu.Unlock()
	g.log.Info().Str("namespace", ns).Msg("released namespace")
}

// warnUnrunShards (default shard) emits a Warning Event on each namespace
// labelled with a shard whose heartbeat this shard has not seen change
// within leaseDuration, once per namespace and shard: nothing reconciles its
// kardinal objects. A shard is given leaseDuration from this shard's start
// to show up.
func (g *Gate) warnUnrunShards(ctx context.Context, now time.Time, namespaces []corev1.Namespace) {
	if g.recorder == nil || now.Sub(g.startedAt) < leaseDuration || now.Sub(g.warnedAt) < warnInterval {
		return
	}
	g.warnedAt = now
	var beats coordinationv1.LeaseList
	cctx, cancel := g.call(ctx)
	defer cancel()
	if err := g.reader.List(cctx, &beats, client.MatchingLabels{labelManagedBy: managedBy, labelHeartbeat: "true"}); err != nil {
		g.log.Debug().Err(err).Msg("list shard heartbeats")
		return
	}
	seen := g.now() // after the list returned
	running := map[string]bool{DefaultShard: true}
	for i := range beats.Items {
		hb := &beats.Items[i]
		if hb.Spec.HolderIdentity == nil {
			continue
		}
		key := hb.Namespace + "/" + *hb.Spec.HolderIdentity
		o, ok := g.observed[key]
		if !ok || o.rv != hb.ResourceVersion {
			o = observation{rv: hb.ResourceVersion, at: seen}
			g.observed[key] = o
		}
		if seen.Sub(o.at) < leaseDuration+renewInterval {
			running[hb.Labels[LabelShard]] = true
		}
	}
	var missing []string
	for i := range namespaces {
		ns := &namespaces[i]
		s := shardOf(ns)
		if running[s] || ns.DeletionTimestamp != nil {
			delete(g.warned, ns.Name)
			continue
		}
		if g.warned[ns.Name] == s {
			continue
		}
		g.warned[ns.Name] = s
		missing = append(missing, ns.Name)
		g.recorder.Eventf(ns, nil, corev1.EventTypeWarning, ReasonShardNotRunning, "Reconcile",
			"namespace %s is labelled %s=%s, but no controller runs shard %s: its kardinal objects are not reconciled",
			ns.Name, LabelShard, s, s)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		g.log.Warn().Strs("namespaces", missing).Msg("namespaces labelled for a shard nobody runs")
	}
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
