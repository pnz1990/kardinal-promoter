// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package metriccheck

import (
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
//   - slots are handed out first come, first served: a check that finds no
//     slot joins a FIFO queue, and a later check may not take a slot while
//     an earlier waiting check that could use it is still waiting.
//
// It never blocks: TryAcquire reports at once whether the caller may query,
// so a waiting check does not hold a reconcile worker. A waiting check must
// ask again within queueTTL to keep its place (the reconciler requeues it
// every busyRetry); one that stops asking (deleted, suspended) leaves the
// queue.
type Limiter struct {
	Global       int
	PerNamespace int
	// NowFn returns the current time (tests); nil means time.Now.
	NowFn func() time.Time

	mu      sync.Mutex
	running map[string]int
	total   int
	queue   []waiter
}

type waiter struct {
	key      types.NamespacedName
	lastSeen time.Time
}

// queueTTL is how long a waiting check keeps its place without asking again.
const queueTTL = 15 * time.Second

// NewLimiter returns a Limiter with global and per-namespace caps.
func NewLimiter(global, perNamespace int) *Limiter {
	return &Limiter{Global: global, PerNamespace: perNamespace}
}

func (l *Limiter) now() time.Time {
	if l.NowFn != nil {
		return l.NowFn()
	}
	return time.Now()
}

// TryAcquire returns a release function when key may query now, or
// ok=false, with its place in the queue kept, when it must wait.
func (l *Limiter) TryAcquire(key types.NamespacedName) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.running == nil {
		l.running = map[string]int{}
	}
	now := l.now()

	// Drop waiters that stopped asking, and refresh this one.
	pos := -1
	kept := l.queue[:0]
	for _, w := range l.queue {
		if w.key == key {
			w.lastSeen = now
		}
		if now.Sub(w.lastSeen) > queueTTL {
			continue
		}
		if w.key == key {
			pos = len(kept)
		}
		kept = append(kept, w)
	}
	l.queue = kept

	free := func(ns string) bool { return l.running[ns] < l.PerNamespace }
	if l.total < l.Global && free(key.Namespace) {
		// FIFO: an earlier waiter that could run now goes first.
		limit := len(l.queue)
		if pos >= 0 {
			limit = pos
		}
		first := true
		for _, w := range l.queue[:limit] {
			if free(w.key.Namespace) {
				first = false
				break
			}
		}
		if first {
			if pos >= 0 {
				l.queue = append(l.queue[:pos], l.queue[pos+1:]...)
			}
			l.running[key.Namespace]++
			l.total++
			var once sync.Once
			return func() {
				once.Do(func() {
					l.mu.Lock()
					defer l.mu.Unlock()
					l.running[key.Namespace]--
					if l.running[key.Namespace] <= 0 {
						delete(l.running, key.Namespace)
					}
					l.total--
				})
			}, true
		}
	}
	if pos < 0 {
		l.queue = append(l.queue, waiter{key: key, lastSeen: now})
	}
	return nil, false
}

// Waiting is how many checks are waiting for a slot.
func (l *Limiter) Waiting() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.queue)
}
