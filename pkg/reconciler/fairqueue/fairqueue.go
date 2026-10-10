// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package fairqueue is a controller work queue that shares the workers
// fairly between namespaces (#1577).
//
// controller-runtime's priority queue serves items by priority, then in the
// order they were added. One namespace with hundreds of runnable items (a
// 150-environment wave) then holds every worker until its backlog drains,
// and an item of another namespace waits behind all of it. This queue wraps
// the priority queue and lowers an item's priority, within its priority
// level, by the number of other items its namespace has queued or in
// process when it is added (in a few steps: none, 1-3, 4-15, 16-63, 64-255,
// 256 or more): an item of a namespace with little work goes
// ahead of a namespace with a large backlog. Within one namespace the order
// stays first in, first out, and the priority levels (handler.LowPriority,
// the turn waiters' low priority) are kept apart. The backlog counts keys
// that are ready or processing; one waiting for a RequeueAfter (a health
// check's next poll) does not count until it is due, so a namespace with
// many idle objects is not penalised.
//
// It is work-conserving: a worker never idles while an item is ready, so a
// namespace alone on the controller gets every worker, as before. The
// counts are process-local bookkeeping of the queue itself, like the
// queue's own contents: they order work and decide nothing.
package fairqueue

import (
	"container/heap"
	"sync"
	"time"

	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/controller/priorityqueue"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// span is the width of one priority level: an item's priority is
// base*span minus its namespace's backlog bucket, so a backlog never
// crosses into the next lower level.
const span = 1000

// buckets are the backlog bounds of the priority steps within a level: a
// backlog of 0 keeps the priority, 1-3 lowers it by one, 4-15 by two, and
// so on. Few steps, because controller-runtime's work-queue depth metric
// is labelled by priority and stops tracking a queue that uses more than 25
// priorities (its depth gauges then never return to zero); with two levels
// in use (normal and low) this queue uses at most 12.
var buckets = []int{1, 4, 16, 64, 256}

// state is the bookkeeping of one key.
type state struct {
	// queued: in the inner queue (ready or waiting); processing: handed to
	// a worker and not yet Done; due: queued and its ready time has come.
	queued, processing, due bool
	// base is the highest base priority the key was added with since its
	// last Get: what GetWithPriority returns, so the controller requeues
	// with the priority the reconciler meant, not the shifted one.
	base int
	// readyAt is when a queued key is due (the earliest of its adds); gen
	// invalidates its older entries in the namespace's waiting heap.
	readyAt time.Time
	gen     uint64
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
	inner priorityqueue.PriorityQueue[reconcile.Request]

	mu         sync.Mutex
	namespaces map[string]*namespace
	now        func() time.Time
}

var _ priorityqueue.PriorityQueue[reconcile.Request] = (*Queue)(nil)

// New wraps a controller-runtime priority queue named name with
// rateLimiter. It has the signature of controller.Options.NewQueue.
func New(name string, rateLimiter workqueue.TypedRateLimiter[reconcile.Request]) workqueue.TypedRateLimitingInterface[reconcile.Request] {
	return Wrap(priorityqueue.New(name, func(o *priorityqueue.Opts[reconcile.Request]) {
		o.RateLimiter = rateLimiter
	}))
}

// Wrap makes inner fair between namespaces.
func Wrap(inner priorityqueue.PriorityQueue[reconcile.Request]) *Queue {
	return &Queue{inner: inner, namespaces: map[string]*namespace{}, now: time.Now}
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

// promote counts the waiters of n that are due at now.
func (n *namespace) promote(now time.Time) {
	for len(n.waiting) > 0 && !n.waiting[0].at.After(now) {
		w := heap.Pop(&n.waiting).(waiter)
		if st := n.keys[w.key]; st != nil && st.gen == w.gen && st.queued && !st.due {
			st.due = true
			n.active++
		}
	}
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

// AddWithOpts adds items with o, each at its namespace-shifted priority.
func (q *Queue) AddWithOpts(o priorityqueue.AddOpts, items ...reconcile.Request) {
	base := 0
	if o.Priority != nil {
		base = *o.Priority
	}
	for _, it := range items {
		q.mu.Lock()
		now := q.now()
		ready := now
		if o.After > 0 {
			ready = now.Add(o.After)
		}
		n := q.ns(it.Namespace)
		n.promote(now)
		st := n.keys[it]
		if st == nil {
			st = &state{}
			n.keys[it] = st
		}
		switch {
		case !st.queued:
			st.base, st.queued, st.readyAt = base, true, ready
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
		q.mu.Unlock()
		opts := o
		opts.Priority = &p
		q.inner.AddWithOpts(opts, it)
	}
}

// schedule counts st as due now, or keeps it waiting until its readyAt.
func (q *Queue) schedule(n *namespace, key reconcile.Request, st *state, now time.Time) {
	st.gen++
	if !st.readyAt.After(now) {
		if !st.due {
			st.due = true
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
	st.queued = false
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
	defer q.mu.Unlock()
	n := q.namespaces[ns]
	if n == nil {
		return 0
	}
	n.promote(q.now())
	return n.active
}

// Forget forwards to the inner queue.
func (q *Queue) Forget(item reconcile.Request) { q.inner.Forget(item) }

// NumRequeues forwards to the inner queue.
func (q *Queue) NumRequeues(item reconcile.Request) int { return q.inner.NumRequeues(item) }

// Len forwards to the inner queue.
func (q *Queue) Len() int { return q.inner.Len() }

// ShutDown forwards to the inner queue.
func (q *Queue) ShutDown() { q.inner.ShutDown() }

// ShutDownWithDrain forwards to the inner queue.
func (q *Queue) ShutDownWithDrain() { q.inner.ShutDownWithDrain() }

// ShuttingDown forwards to the inner queue.
func (q *Queue) ShuttingDown() bool { return q.inner.ShuttingDown() }
