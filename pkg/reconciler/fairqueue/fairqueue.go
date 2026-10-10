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
// process when it is added: an item of a namespace with little work goes
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
	"sync"
	"time"

	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/controller/priorityqueue"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// span is the width of one priority level: an item's priority is
// base*span minus its namespace's backlog, capped at span-1, so a backlog
// never crosses into the next lower level.
const span = 1000

// state is the bookkeeping of one key.
type state struct {
	// queued: in the inner queue (ready or waiting); processing: handed to
	// a worker and not yet Done.
	queued, processing bool
	// base is the highest base priority the key was added with since its
	// last Get: what GetWithPriority returns, so the controller requeues
	// with the priority the reconciler meant, not the shifted one.
	base int
	// readyAt is when a queued key is due (the earliest of its adds).
	readyAt time.Time
}

// Queue is a priorityqueue.PriorityQueue that orders items fairly between
// namespaces.
type Queue struct {
	inner priorityqueue.PriorityQueue[reconcile.Request]

	mu sync.Mutex
	// keys is every key that is queued or processing, by namespace.
	keys map[string]map[reconcile.Request]*state
	now  func() time.Time
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
	return &Queue{inner: inner, keys: map[string]map[reconcile.Request]*state{}, now: time.Now}
}

// lookup returns the state of key, creating it with create.
func (q *Queue) lookup(key reconcile.Request, create bool) *state {
	ns := q.keys[key.Namespace]
	if ns == nil {
		if !create {
			return nil
		}
		ns = map[reconcile.Request]*state{}
		q.keys[key.Namespace] = ns
	}
	st := ns[key]
	if st == nil && create {
		st = &state{}
		ns[key] = st
	}
	return st
}

// backlogOf counts the keys of ns other than self that are processing or
// queued and due at now.
func (q *Queue) backlogOf(ns string, self reconcile.Request, now time.Time) int {
	n := 0
	for k, st := range q.keys[ns] {
		if k != self && (st.processing || (st.queued && !st.readyAt.After(now))) {
			n++
		}
	}
	return n
}

// shift is the priority an item of base priority gets when its namespace
// has backlog other keys queued or processing.
func shift(base, backlog int) int {
	return base*span - min(backlog, span-1)
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
		st := q.lookup(it, true)
		if st.queued {
			st.base = max(st.base, base)
			if ready.Before(st.readyAt) {
				st.readyAt = ready
			}
		} else {
			st.base, st.readyAt = base, ready
		}
		st.queued = true
		// The backlog the item will meet when it is due.
		p := shift(base, q.backlogOf(it.Namespace, it, ready))
		q.mu.Unlock()
		opts := o
		opts.Priority = &p
		q.inner.AddWithOpts(opts, it)
	}
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
	st := q.lookup(it, false)
	if st == nil || !st.queued {
		// Not added through this queue (or bookkeeping lost it): count it
		// now, at the priority level it came with.
		st = q.lookup(it, true)
		st.base = p / span
	}
	st.queued, st.processing = false, true
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
	st := q.lookup(item, false)
	if st == nil {
		return
	}
	st.processing = false
	if !st.queued {
		delete(q.keys[item.Namespace], item)
		if len(q.keys[item.Namespace]) == 0 {
			delete(q.keys, item.Namespace)
		}
	}
}

// Backlog returns the keys of namespace ns that are processing or queued
// and due now.
func (q *Queue) Backlog(ns string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.backlogOf(ns, reconcile.Request{}, q.now())
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
