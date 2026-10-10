// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package fairqueue is a controller work queue that shares the workers
// fairly between namespaces (#1577).
//
// controller-runtime's priority queue serves items by priority, then in the
// order they were added. One namespace with hundreds of runnable items then
// holds every worker until its backlog drains, and an item of another
// namespace waits behind all of it. This queue wraps the priority queue and
// lowers an item's priority within its priority level by the size of its
// namespace's backlog, in a few steps (none, 1-3, 4-15, 16-63, 64-255, 256
// or more): an item of a namespace with little work goes ahead of a
// namespace with a large backlog.
//
// The backlog counts the namespace's keys that are in process or due. A key
// waiting for a RequeueAfter or a rate-limit backoff does not count until it
// is due, so a namespace with many idle or failing objects is not penalised;
// when it becomes due its priority is computed again (the inner queue keeps
// the higher of the two), so a requeue is not stuck with the penalty of the
// moment it was added.
//
// Aging bounds the unfairness the other way: a key that has been due for
// longer than the aging bound (10 s by default) is raised back to its base
// priority, so a busy namespace still gets a share of the workers under a
// steady stream of small namespaces.
//
// Guarantees:
//   - work-conserving: a worker never idles while an item is ready, so a
//     namespace alone on the controller gets every worker, as before;
//   - within a namespace and a priority step, keys are served in the order
//     they became due;
//   - priority levels (handler.LowPriority, the branch-turn waiters) are
//     kept apart: no backlog or aging moves a key out of its level;
//   - a due key waits at most about the aging bound behind keys of its own
//     level before it competes as if it had no backlog.
//
// The counts are process-local bookkeeping of the queue itself, like the
// queue's own contents: they order work and decide nothing, and a restart
// loses only the ordering.
package fairqueue

import (
	"container/heap"
	"sort"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/priorityqueue"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// span is the width of one priority level: an item's priority is
// base*span minus its namespace's backlog step, so a backlog never crosses
// into the next lower level.
const span = 1000

// buckets are the backlog bounds of the priority steps within a level: a
// backlog of 0 keeps the priority, 1-3 lowers it by one, 4-15 by two, and
// so on. Few steps, because controller-runtime's work-queue depth metric
// is labelled by priority and stops tracking a queue that uses more than 25
// priorities (its depth gauges then never return to zero); with two levels
// in use (normal and low) this queue uses at most 12.
var buckets = []int{1, 4, 16, 64, 256}

// DefaultAging is how long a key may be due before it is raised back to
// its base priority.
const DefaultAging = 10 * time.Second

// state is the bookkeeping of one key.
type state struct {
	// queued: in the inner queue (ready or waiting); processing: handed to
	// a worker and not yet Done; due: queued and its ready time has come;
	// aged: raised to its base priority since it became due.
	queued, processing, due, aged bool
	// base is the highest base priority the key was added with since its
	// last Get: what GetWithPriority returns, so the controller requeues
	// with the priority the reconciler meant, not the shifted one.
	base int
	// readyAt is when a queued key is due (the earliest of its adds);
	// dueSince when it became due; gen invalidates its older entries in the
	// namespace's waiting heap.
	readyAt, dueSince time.Time
	gen               uint64
	// seq is the queue's add counter when the key was last queued: the tie
	// break between keys that became due at the same time.
	seq uint64
}

// waiter is a queued key that is not due yet.
type waiter struct {
	at  time.Time
	key reconcile.Request
	gen uint64
}

// waiters is a min-heap of waiters by ready time.
type waiters []waiter

func (w waiters) Len() int            { return len(w) }
func (w waiters) Less(i, j int) bool  { return w[i].at.Before(w[j].at) }
func (w waiters) Swap(i, j int)       { w[i], w[j] = w[j], w[i] }
func (w *waiters) Push(x interface{}) { *w = append(*w, x.(waiter)) }
func (w *waiters) Pop() interface{} {
	old := *w
	x := old[len(old)-1]
	*w = old[:len(old)-1]
	return x
}

// namespace is the bookkeeping of one namespace's keys. active counts the
// keys that are processing or due, kept up to date as keys come and go, so
// an add costs O(log n), not a scan of the namespace.
type namespace struct {
	keys    map[reconcile.Request]*state
	active  int
	waiting waiters
}

// Queue is a priorityqueue.PriorityQueue that orders items fairly between
// namespaces.
type Queue struct {
	inner   priorityqueue.PriorityQueue[reconcile.Request]
	limiter workqueue.TypedRateLimiter[reconcile.Request]
	aging   time.Duration

	mu         sync.Mutex
	seq        uint64
	namespaces map[string]*namespace
	now        func() time.Time
	stop       chan struct{}
	stopOnce   sync.Once
}

var _ priorityqueue.PriorityQueue[reconcile.Request] = (*Queue)(nil)

// Option configures a Queue.
type Option func(*Queue)

// Aging sets how long a key may be due before it is raised back to its
// base priority (DefaultAging). Zero turns aging off.
func Aging(d time.Duration) Option { return func(q *Queue) { q.aging = d } }

// RateLimiter is the limiter the queue asks for an AddRateLimited's delay,
// so the key counts only once its backoff is over. It must be the inner
// queue's limiter (Forget and NumRequeues go there).
func RateLimiter(l workqueue.TypedRateLimiter[reconcile.Request]) Option {
	return func(q *Queue) { q.limiter = l }
}

// NewFor returns the controller.Options.NewQueue of a controller of mgr:
// the fair queue over controller-runtime's priority queue, logged like the
// default queue. With the manager's UsePriorityQueue set to false it
// returns the plain rate-limited queue, as controller-runtime would.
func NewFor(mgr manager.Manager) func(string, workqueue.TypedRateLimiter[reconcile.Request]) workqueue.TypedRateLimitingInterface[reconcile.Request] {
	usePQ := ptr.Deref(mgr.GetControllerOptions().UsePriorityQueue, true)
	log := mgr.GetLogger()
	return func(name string, rl workqueue.TypedRateLimiter[reconcile.Request]) workqueue.TypedRateLimitingInterface[reconcile.Request] {
		if !usePQ {
			return workqueue.NewTypedRateLimitingQueueWithConfig(rl, workqueue.TypedRateLimitingQueueConfig[reconcile.Request]{Name: name})
		}
		return newQueue(name, rl, log.WithValues("controller", name))
	}
}

// New wraps a controller-runtime priority queue named name with
// rateLimiter. It has the signature of controller.Options.NewQueue.
func New(name string, rateLimiter workqueue.TypedRateLimiter[reconcile.Request]) workqueue.TypedRateLimitingInterface[reconcile.Request] {
	return newQueue(name, rateLimiter, logr.Discard())
}

func newQueue(name string, rateLimiter workqueue.TypedRateLimiter[reconcile.Request], log logr.Logger) *Queue {
	return Wrap(priorityqueue.New(name, func(o *priorityqueue.Opts[reconcile.Request]) {
		o.RateLimiter = rateLimiter
		o.Log = log
	}), RateLimiter(rateLimiter))
}

// Wrap makes inner fair between namespaces. Without the RateLimiter option
// a rate-limited add is forwarded as it is, and the key counts as due at
// once although the inner queue holds it for its backoff; New and NewFor
// set the option.
func Wrap(inner priorityqueue.PriorityQueue[reconcile.Request], opts ...Option) *Queue {
	q := &Queue{inner: inner, aging: DefaultAging, namespaces: map[string]*namespace{}, now: time.Now, stop: make(chan struct{})}
	for _, o := range opts {
		o(q)
	}
	go q.tick()
	return q
}

// tick runs sweep until ShutDown: often enough that a waiter's recomputed
// priority and an aged key take effect well within the aging bound.
func (q *Queue) tick() {
	every := q.aging / 4
	if every <= 0 || every > time.Second {
		every = time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-q.stop:
			return
		case <-t.C:
			q.sweep()
		}
	}
}

// readd is a key to add to the inner queue again at priority p.
type readd struct {
	key reconcile.Request
	p   int
}

// sweep re-adds the keys that became due since the last sweep at their
// recomputed priority, and the keys due longer than the aging bound at
// their base priority. The inner queue keeps the higher priority.
func (q *Queue) sweep() {
	now := q.now()
	var out []readd
	q.mu.Lock()
	for _, n := range q.namespaces {
		for _, pr := range n.promote(now) {
			out = append(out, readd{pr.key, shift(pr.st.base, n.active-1)})
		}
		if q.aging <= 0 {
			continue
		}
		// In the order they became due, so aging keeps a namespace's keys
		// first in, first out.
		var aged []promoted
		for k, st := range n.keys {
			if st.due && !st.aged && now.Sub(st.dueSince) >= q.aging {
				st.aged = true
				aged = append(aged, promoted{k, st})
			}
		}
		sort.Slice(aged, func(i, j int) bool {
			a, b := aged[i].st, aged[j].st
			if !a.dueSince.Equal(b.dueSince) {
				return a.dueSince.Before(b.dueSince)
			}
			return a.seq < b.seq
		})
		for _, pr := range aged {
			out = append(out, readd{pr.key, pr.st.base * span})
		}
	}
	q.mu.Unlock()
	q.readd(out)
}

// readd adds keys that are due to the inner queue again at their priority.
func (q *Queue) readd(out []readd) {
	for _, r := range out {
		p := r.p
		q.inner.AddWithOpts(priorityqueue.AddOpts{Priority: &p}, r.key)
	}
}

// promoted is a key promote marked due.
type promoted struct {
	key reconcile.Request
	st  *state
}

// promote counts the waiters of n that are due at now and returns them.
func (n *namespace) promote(now time.Time) []promoted {
	var out []promoted
	for len(n.waiting) > 0 && !n.waiting[0].at.After(now) {
		w := heap.Pop(&n.waiting).(waiter)
		if st := n.keys[w.key]; st != nil && st.gen == w.gen && st.queued && !st.due {
			st.due, st.dueSince = true, w.at
			n.active++
			out = append(out, promoted{w.key, st})
		}
	}
	return out
}

// ns returns the bookkeeping of name, creating it.
func (q *Queue) ns(name string) *namespace {
	n := q.namespaces[name]
	if n == nil {
		n = &namespace{keys: map[reconcile.Request]*state{}}
		q.namespaces[name] = n
	}
	return n
}

// release drops n's bookkeeping of key when it is neither queued nor
// processing, and n itself when it has no keys.
func (q *Queue) release(name string, n *namespace, key reconcile.Request, st *state) {
	if st.queued || st.processing {
		return
	}
	delete(n.keys, key)
	if len(n.keys) == 0 {
		delete(q.namespaces, name)
	}
}

// shift is the priority an item of base priority gets when its namespace
// has backlog other keys processing or due.
func shift(base, backlog int) int {
	step := 0
	for _, b := range buckets {
		if backlog >= b {
			step++
		}
	}
	return base*span - step
}

// AddWithOpts adds items with o, each at its namespace-shifted priority. A
// rate-limited add asks the rate limiter for its delay here, so the key is
// counted only once its backoff is over.
func (q *Queue) AddWithOpts(o priorityqueue.AddOpts, items ...reconcile.Request) {
	base := 0
	if o.Priority != nil {
		base = *o.Priority
	}
	for _, it := range items {
		opts := o
		if opts.RateLimited && q.limiter != nil {
			if d := q.limiter.When(it); opts.After <= 0 || d < opts.After {
				opts.After = d
			}
			opts.RateLimited = false
		}
		q.mu.Lock()
		now := q.now()
		ready := now
		if opts.After > 0 {
			ready = now.Add(opts.After)
		}
		n := q.ns(it.Namespace)
		promoted := n.promote(now)
		st := n.keys[it]
		if st == nil {
			st = &state{}
			n.keys[it] = st
		}
		switch {
		case !st.queued:
			q.seq++
			st.base, st.queued, st.readyAt, st.aged, st.seq = base, true, ready, false, q.seq
			q.schedule(n, it, st, now)
		default:
			st.base = max(st.base, base)
			if !st.due && ready.Before(st.readyAt) {
				st.readyAt = ready
				q.schedule(n, it, st, now)
			}
		}
		backlog := n.active
		if st.due || st.processing {
			backlog-- // not itself
		}
		p := shift(base, backlog)
		var again []readd
		for _, pr := range promoted {
			if pr.key != it {
				again = append(again, readd{pr.key, shift(pr.st.base, n.active-1)})
			}
		}
		q.mu.Unlock()
		opts.Priority = &p
		q.inner.AddWithOpts(opts, it)
		q.readd(again)
	}
}

// schedule counts st as due now, or keeps it waiting until its readyAt.
func (q *Queue) schedule(n *namespace, key reconcile.Request, st *state, now time.Time) {
	st.gen++
	if !st.readyAt.After(now) {
		if !st.due {
			st.due, st.dueSince = true, now
			n.active++
		}
		return
	}
	heap.Push(&n.waiting, waiter{at: st.readyAt, key: key, gen: st.gen})
}

// Add adds item at the normal priority.
func (q *Queue) Add(item reconcile.Request) { q.AddWithOpts(priorityqueue.AddOpts{}, item) }

// AddAfter adds item after d.
func (q *Queue) AddAfter(item reconcile.Request, d time.Duration) {
	q.AddWithOpts(priorityqueue.AddOpts{After: d}, item)
}

// AddRateLimited adds item after its rate limiter's delay.
func (q *Queue) AddRateLimited(item reconcile.Request) {
	q.AddWithOpts(priorityqueue.AddOpts{RateLimited: true}, item)
}

// GetWithPriority returns the next item and the base priority it was added
// with.
func (q *Queue) GetWithPriority() (reconcile.Request, int, bool) {
	it, p, shutdown := q.inner.GetWithPriority()
	if shutdown {
		return it, p, shutdown
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	n := q.ns(it.Namespace)
	st := n.keys[it]
	if st == nil || !st.queued {
		// Not added through this queue (or the bookkeeping lost it): count
		// it now, at the priority level it came with.
		if st == nil {
			st = &state{}
			n.keys[it] = st
		}
		st.base = p / span
	}
	if st.due {
		st.due = false
		n.active--
	}
	st.queued, st.aged = false, false
	st.gen++ // its waiting entry, if any, is stale
	if !st.processing {
		st.processing = true
		n.active++
	}
	return it, st.base, false
}

// Get returns the next item.
func (q *Queue) Get() (reconcile.Request, bool) {
	it, _, shutdown := q.GetWithPriority()
	return it, shutdown
}

// Done marks item as processed. A key added again while it was processed
// stays counted; the inner queue hands it out again.
func (q *Queue) Done(item reconcile.Request) {
	q.inner.Done(item)
	q.mu.Lock()
	defer q.mu.Unlock()
	n := q.namespaces[item.Namespace]
	if n == nil {
		return
	}
	st := n.keys[item]
	if st == nil {
		return
	}
	if st.processing {
		st.processing = false
		n.active--
	}
	q.release(item.Namespace, n, item, st)
}

// Backlog returns the keys of namespace ns that are processing or queued
// and due now.
func (q *Queue) Backlog(ns string) int {
	q.mu.Lock()
	n := q.namespaces[ns]
	if n == nil {
		q.mu.Unlock()
		return 0
	}
	var again []readd
	for _, pr := range n.promote(q.now()) {
		again = append(again, readd{pr.key, shift(pr.st.base, n.active-1)})
	}
	active := n.active
	q.mu.Unlock()
	q.readd(again)
	return active
}

// Forget forwards to the inner queue.
func (q *Queue) Forget(item reconcile.Request) { q.inner.Forget(item) }

// NumRequeues forwards to the inner queue.
func (q *Queue) NumRequeues(item reconcile.Request) int { return q.inner.NumRequeues(item) }

// Len forwards to the inner queue.
func (q *Queue) Len() int { return q.inner.Len() }

// ShutDown stops the queue.
func (q *Queue) ShutDown() {
	q.stopOnce.Do(func() { close(q.stop) })
	q.inner.ShutDown()
}

// ShutDownWithDrain stops the queue once its items are done.
func (q *Queue) ShutDownWithDrain() {
	q.stopOnce.Do(func() { close(q.stop) })
	q.inner.ShutDownWithDrain()
}

// ShuttingDown forwards to the inner queue.
func (q *Queue) ShuttingDown() bool { return q.inner.ShuttingDown() }
