// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package metriccheck

import (
	"container/heap"
	"container/list"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

// Limiter shares the outbound query slots fairly between namespaces. Every
// provider dials an address a MetricCheck's author chose, and a blackhole
// endpoint holds a slot until its timeout, so:
//
//   - at most Global queries run at once;
//   - at most PerNamespace queries of one namespace run at once;
//   - slots go first come, first served: a check that finds no slot waits
//     in a FIFO queue, and a freed slot goes to the earliest waiting check
//     whose namespace is under its cap.
//
// It never blocks a reconcile worker: TryAcquire answers at once. A waiting
// check is not polled: when a slot frees, the Limiter reserves it for the
// next waiter and calls Wake with its key, so the reconciler re-runs it, and
// that TryAcquire claims the reservation. A reservation not claimed within
// ClaimTTL (the check was deleted, or the wake was lost) goes to the next
// waiter. Cancel removes a check that no longer queries.
//
// Every operation costs O(log n) in the number of namespaces with waiters:
// each namespace has its own FIFO, and a heap orders the namespaces that
// could take a slot by the arrival of their first waiter.
//
// The queue is process-local and holds no promotion state: after a restart
// the checks simply ask again (approved exception to the in-memory-state
// rule, recorded in docs/design/16-graph-capability-ledger.md G8).
type Limiter struct {
	Global       int
	PerNamespace int
	// ClaimTTL is how long a slot reserved for a woken waiter is held for
	// it; zero means defaultClaimTTL.
	ClaimTTL time.Duration
	// Wake is called, outside the Limiter's lock, with the key of a waiter
	// a slot was reserved for. Nil means nobody is woken (tests poll).
	Wake func(types.NamespacedName)

	mu       sync.Mutex
	seq      uint64
	total    int // running plus reserved
	ns       map[string]*nsQueue
	waiting  map[types.NamespacedName]*list.Element
	reserved map[types.NamespacedName]*reservation
	ready    readyHeap
}

// defaultClaimTTL bounds how long a reserved slot waits for its check.
const defaultClaimTTL = 10 * time.Second

// nsQueue is one namespace: its slots in use (running or reserved) and its
// waiters in arrival order.
type nsQueue struct {
	name  string
	used  int
	queue *list.List // of *waiter
}

type waiter struct {
	key types.NamespacedName
	seq uint64
}

type reservation struct {
	gen   uint64
	timer *time.Timer
}

// readyEntry says namespace ns could take a slot for its waiter seq. Entries
// go stale (the waiter left, or the namespace filled up); stale entries are
// dropped when popped.
type readyEntry struct {
	ns  string
	seq uint64
}

type readyHeap []readyEntry

func (h readyHeap) Len() int           { return len(h) }
func (h readyHeap) Less(i, j int) bool { return h[i].seq < h[j].seq }
func (h readyHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *readyHeap) Push(x any)        { *h = append(*h, x.(readyEntry)) }
func (h *readyHeap) Pop() any {
	old := *h
	e := old[len(old)-1]
	*h = old[:len(old)-1]
	return e
}

// NewLimiter returns a Limiter with global and per-namespace caps.
func NewLimiter(global, perNamespace int) *Limiter {
	return &Limiter{Global: global, PerNamespace: perNamespace}
}

func (l *Limiter) init() {
	if l.ns == nil {
		l.ns = map[string]*nsQueue{}
		l.waiting = map[types.NamespacedName]*list.Element{}
		l.reserved = map[types.NamespacedName]*reservation{}
	}
}

func (l *Limiter) nsq(name string) *nsQueue {
	q, ok := l.ns[name]
	if !ok {
		q = &nsQueue{name: name, queue: list.New()}
		l.ns[name] = q
	}
	return q
}

// gc forgets a namespace with nothing running, reserved or waiting.
func (l *Limiter) gc(q *nsQueue) {
	if q.used == 0 && q.queue.Len() == 0 {
		delete(l.ns, q.name)
	}
}

// markReady records that q could take a slot for its first waiter.
func (l *Limiter) markReady(q *nsQueue) {
	if q.used < l.PerNamespace && q.queue.Len() > 0 {
		heap.Push(&l.ready, readyEntry{ns: q.name, seq: q.queue.Front().Value.(*waiter).seq})
	}
}

// dispatch reserves free slots for the earliest eligible waiters and returns
// the keys to wake. l.mu is held.
func (l *Limiter) dispatch() []types.NamespacedName {
	var wake []types.NamespacedName
	for l.total < l.Global && l.ready.Len() > 0 {
		e := heap.Pop(&l.ready).(readyEntry)
		q, ok := l.ns[e.ns]
		if !ok || q.used >= l.PerNamespace || q.queue.Len() == 0 || q.queue.Front().Value.(*waiter).seq != e.seq {
			continue // stale
		}
		w := q.queue.Remove(q.queue.Front()).(*waiter)
		delete(l.waiting, w.key)
		q.used++
		l.total++
		l.seq++
		gen := l.seq
		ttl := l.ClaimTTL
		if ttl <= 0 {
			ttl = defaultClaimTTL
		}
		key := w.key
		l.reserved[key] = &reservation{gen: gen, timer: time.AfterFunc(ttl, func() { l.expire(key, gen) })}
		wake = append(wake, key)
		l.markReady(q)
	}
	return wake
}

// free gives back one slot of namespace name. l.mu is held.
func (l *Limiter) free(name string) []types.NamespacedName {
	q := l.nsq(name)
	q.used--
	l.total--
	l.markReady(q)
	wake := l.dispatch()
	l.gc(q)
	return wake
}

func (l *Limiter) wake(keys []types.NamespacedName) {
	if l.Wake == nil {
		return
	}
	for _, k := range keys {
		l.Wake(k)
	}
}

// expire frees a reservation that was not claimed in time.
func (l *Limiter) expire(key types.NamespacedName, gen uint64) {
	l.mu.Lock()
	r, ok := l.reserved[key]
	if !ok || r.gen != gen {
		l.mu.Unlock()
		return
	}
	delete(l.reserved, key)
	wake := l.free(key.Namespace)
	l.mu.Unlock()
	l.wake(wake)
}

// TryAcquire returns a release function when key may query now, or
// ok=false, with its place in the queue kept, when it must wait.
func (l *Limiter) TryAcquire(key types.NamespacedName) (release func(), ok bool) {
	l.mu.Lock()
	l.init()
	switch r, isReserved := l.reserved[key]; {
	case isReserved:
		// The slot dispatch reserved for this check: claim it.
		r.timer.Stop()
		delete(l.reserved, key)
	case l.waiting[key] != nil:
		l.mu.Unlock()
		return nil, false
	default:
		q := l.nsq(key.Namespace)
		// dispatch keeps no eligible waiter behind a free slot, so a free
		// slot here is nobody else's.
		if l.total >= l.Global || q.used >= l.PerNamespace {
			l.seq++
			l.waiting[key] = q.queue.PushBack(&waiter{key: key, seq: l.seq})
			if q.queue.Len() == 1 {
				l.markReady(q)
			}
			l.mu.Unlock()
			return nil, false
		}
		q.used++
		l.total++
	}
	l.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			w := l.free(key.Namespace)
			l.mu.Unlock()
			l.wake(w)
		})
	}, true
}

// Cancel removes key from the queue, or gives back the slot reserved for it:
// the check was deleted or no longer queries.
func (l *Limiter) Cancel(key types.NamespacedName) {
	l.mu.Lock()
	l.init()
	var wake []types.NamespacedName
	if el := l.waiting[key]; el != nil {
		q := l.ns[key.Namespace]
		head := q.queue.Front() == el
		q.queue.Remove(el)
		delete(l.waiting, key)
		if head {
			l.markReady(q)
		}
		l.gc(q)
	}
	if r, ok := l.reserved[key]; ok {
		r.timer.Stop()
		delete(l.reserved, key)
		wake = l.free(key.Namespace)
	}
	l.mu.Unlock()
	l.wake(wake)
}

// Waiting is how many checks are waiting for a slot.
func (l *Limiter) Waiting() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.waiting)
}

// InUse is how many slots are running or reserved.
func (l *Limiter) InUse() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total
}
