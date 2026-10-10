// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package fairqueue_test

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/controller/priorityqueue"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/fairqueue"
)

func req(ns, name string) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}
}

var queueN atomic.Int64

func newQueue(t *testing.T) *fairqueue.Queue {
	t.Helper()
	name := fmt.Sprintf("fair-%d", queueN.Add(1))
	q := fairqueue.New(name, workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](time.Millisecond, time.Second))
	t.Cleanup(q.ShutDown)
	fq, ok := q.(*fairqueue.Queue)
	require.True(t, ok)
	var _ priorityqueue.PriorityQueue[reconcile.Request] = fq
	return fq
}

// get takes the next item with a timeout.
func get(t *testing.T, q *fairqueue.Queue) (reconcile.Request, int) {
	t.Helper()
	type res struct {
		it reconcile.Request
		p  int
	}
	ch := make(chan res, 1)
	go func() {
		it, p, _ := q.GetWithPriority()
		ch <- res{it, p}
	}()
	select {
	case r := <-ch:
		return r.it, r.p
	case <-time.After(5 * time.Second):
		t.Fatal("no item")
	}
	return reconcile.Request{}, 0
}

// TestQueue_OtherNamespaceGoesFirst (#1577): an item of a namespace with no
// backlog is served before the backlog of a busy namespace; within one
// namespace the order stays FIFO.
//
// Covers PERF-FAIRQ-01.
func TestQueue_OtherNamespaceGoesFirst(t *testing.T) {
	q := newQueue(t)
	for i := range 50 {
		q.Add(req("a", fmt.Sprintf("s%02d", i)))
	}
	q.Add(req("b", "s0"))
	// a/s00 was added with no backlog; b/s0 next, ahead of 49 a items.
	first, _ := get(t, q)
	second, _ := get(t, q)
	assert.Equal(t, req("a", "s00"), first)
	assert.Equal(t, req("b", "s0"), second)
	for i := 1; i < 50; i++ {
		it, p := get(t, q)
		assert.Equal(t, req("a", fmt.Sprintf("s%02d", i)), it, "FIFO within a namespace")
		assert.Zero(t, p, "the base priority comes back, not the shifted one")
	}
}

// TestQueue_PriorityLevelsKept: a low-priority item (a turn waiter) never
// goes ahead of a normal one, whatever the backlogs.
//
// Covers PERF-FAIRQ-01.
func TestQueue_PriorityLevelsKept(t *testing.T) {
	q := newQueue(t)
	low := handler.LowPriority
	q.AddWithOpts(priorityqueue.AddOpts{Priority: &low}, req("b", "waiter"))
	for i := range 20 {
		q.Add(req("a", fmt.Sprintf("s%02d", i)))
	}
	for range 20 {
		it, p := get(t, q)
		assert.Equal(t, "a", it.Namespace)
		assert.Zero(t, p)
	}
	it, p := get(t, q)
	assert.Equal(t, req("b", "waiter"), it)
	assert.Equal(t, low, p, "the low priority comes back as it was added")
}

// TestQueue_BacklogBookkeeping: a key counts while queued or processing,
// once however often it is added; an item added again while processed is
// handed out again after Done; a key waiting for RequeueAfter does not
// count until it is due.
//
// Covers PERF-FAIRQ-01.
func TestQueue_BacklogBookkeeping(t *testing.T) {
	q := newQueue(t)
	q.Add(req("a", "x"))
	q.Add(req("a", "x"))
	assert.Equal(t, 1, q.Backlog("a"), "de-duplicated")
	it, _ := get(t, q)
	assert.Equal(t, 1, q.Backlog("a"), "processing counts")
	q.Add(it) // added again while processed
	q.Done(it)
	assert.Equal(t, 1, q.Backlog("a"))
	again, _ := get(t, q)
	assert.Equal(t, it, again)
	q.Done(again)
	assert.Zero(t, q.Backlog("a"))

	q.AddAfter(req("a", "later"), time.Hour)
	assert.Zero(t, q.Backlog("a"), "not due yet")
}

// TestQueue_StarvationSimulation counts how many of namespace a's items
// are handed out after one item of namespace b is added and before b's is,
// while a keeps 16 workers busy with a 300-item backlog of 5 ms reconciles
// that requeue themselves, with the plain priority queue and with this one.
// The fair queue hands out b's item within one round of the workers; the
// plain one after a's whole
// backlog. (Counted, not timed, so a loaded machine cannot flake it.)
//
// Covers PERF-FAIRQ-01.
func TestQueue_StarvationSimulation(t *testing.T) {
	plain := simulate(t, false)
	fair := simulate(t, true)
	t.Logf("a's items handed out before b's: %d with the plain priority queue, %d with the fair queue", plain, fair)
	// Items already on their way to a worker when b's arrives (the queue
	// hands items over asynchronously) may still go first: at most one per
	// worker.
	assert.LessOrEqual(t, fair, int64(16), "b's item is served within one round of the workers")
	assert.Greater(t, plain, int64(200), "the plain queue serves a's backlog first")
}

func simulate(t *testing.T, fair bool) int64 {
	t.Helper()
	name := fmt.Sprintf("sim-%d", queueN.Add(1))
	q := priorityqueue.New[reconcile.Request](name)
	if fair {
		q = fairqueue.Wrap(q)
	}
	const workers, backlog, work = 16, 300, 5 * time.Millisecond
	var bAt atomic.Int64
	var bAdded atomic.Bool
	var aAfterB atomic.Int64
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				it, _, shutdown := q.GetWithPriority()
				if shutdown {
					return
				}
				if it.Namespace == "b" {
					bAt.Store(time.Now().UnixNano())
					q.Done(it)
					continue
				}
				if bAdded.Load() && bAt.Load() == 0 {
					aAfterB.Add(1)
				}
				time.Sleep(work)
				q.Done(it)
				select {
				case <-stop:
				default:
					q.Add(it) // a's steps keep coming back
				}
			}
		}()
	}
	for i := range backlog {
		q.Add(req("a", fmt.Sprintf("s%03d", i)))
	}
	time.Sleep(20 * time.Millisecond)
	bAdded.Store(true)
	q.Add(req("b", "s0"))
	deadline := time.Now().Add(10 * time.Second)
	for bAt.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(stop)
	q.ShutDown()
	wg.Wait()
	require.NotZero(t, bAt.Load(), "b was never served")
	return aAfterB.Load()
}

// TestQueue_NothingLost: every key added, across namespaces, priorities
// and delays, is served exactly once, and the queue is empty after: the
// shifted priorities reorder work, never drop or duplicate it.
//
// Covers PERF-FAIRQ-01.
func TestQueue_NothingLost(t *testing.T) {
	q := newQueue(t)
	low := handler.LowPriority
	want := map[reconcile.Request]bool{}
	for i := range 300 {
		it := req([]string{"a", "b", "c"}[i%3], fmt.Sprintf("s%03d", i))
		want[it] = true
		switch i % 4 {
		case 0:
			q.Add(it)
		case 1:
			q.AddWithOpts(priorityqueue.AddOpts{Priority: &low}, it)
		case 2:
			q.AddAfter(it, 10*time.Millisecond)
		default:
			q.AddRateLimited(it)
		}
		q.Add(it) // a second add is de-duplicated
	}
	got := map[reconcile.Request]bool{}
	for range len(want) {
		it, _ := get(t, q)
		assert.False(t, got[it], "%s served twice", it)
		got[it] = true
		q.Done(it)
	}
	assert.Equal(t, want, got)
	for _, ns := range []string{"a", "b", "c"} {
		assert.Zero(t, q.Backlog(ns), ns)
	}
}

// BenchmarkQueue compares the plain and the fair queue serving 200 keys over
// three namespaces with 8 workers.
func BenchmarkQueue(b *testing.B) {
	for _, fair := range []bool{false, true} {
		b.Run(fmt.Sprintf("fair=%v", fair), func(b *testing.B) {
			q := priorityqueue.New[reconcile.Request](fmt.Sprintf("bench-%d", queueN.Add(1)))
			if fair {
				q = fairqueue.Wrap(q)
			}
			defer q.ShutDown()
			var wg sync.WaitGroup
			var n atomic.Int64
			done := make(chan struct{})
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						it, _, shutdown := q.GetWithPriority()
						if shutdown {
							return
						}
						q.Done(it)
						if n.Add(1) >= int64(b.N) {
							select {
							case <-done:
							default:
								close(done)
							}
							continue
						}
						q.Add(it)
					}
				}()
			}
			b.ResetTimer()
			for i := range 200 {
				q.Add(req([]string{"a", "b", "c"}[i%3], fmt.Sprintf("s%03d", i)))
			}
			<-done
		})
	}
}
