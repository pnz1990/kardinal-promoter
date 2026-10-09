// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package metriccheck_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/metriccheck"
)

func nn(ns, name string) types.NamespacedName { return types.NamespacedName{Namespace: ns, Name: name} }

// wakes records the Limiter's wake-ups.
type wakes struct {
	mu   sync.Mutex
	keys []types.NamespacedName
}

func (w *wakes) wake(k types.NamespacedName) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.keys = append(w.keys, k)
}

func (w *wakes) take() []types.NamespacedName {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.keys
	w.keys = nil
	return out
}

func newLimiter(global, perNS int) (*metriccheck.Limiter, *wakes) {
	w := &wakes{}
	l := metriccheck.NewLimiter(global, perNS)
	l.Wake = w.wake
	return l, w
}

// TestLimiter_PerNamespaceAndGlobal (QA #1479): one namespace with slow
// endpoints holds at most one slot, so other namespaces still query; the
// global cap holds; release frees the slot once.
func TestLimiter_PerNamespaceAndGlobal(t *testing.T) {
	l, w := newLimiter(2, 1)
	relA, ok := l.TryAcquire(nn("noisy", "a"))
	require.True(t, ok)
	_, ok = l.TryAcquire(nn("noisy", "b"))
	assert.False(t, ok, "a second check of the same namespace waits")
	relC, ok := l.TryAcquire(nn("quiet", "c"))
	assert.True(t, ok, "another namespace is not starved")
	_, ok = l.TryAcquire(nn("third", "d"))
	assert.False(t, ok, "the global cap holds")
	relA()
	relA() // idempotent
	assert.Equal(t, []types.NamespacedName{nn("noisy", "b")}, w.take(), "the slot is reserved for b, which waited first")
	assert.Equal(t, 2, l.InUse())
	relC()
	assert.Equal(t, []types.NamespacedName{nn("third", "d")}, w.take())
	rel, ok := l.TryAcquire(nn("noisy", "b"))
	assert.True(t, ok, "b claims its reservation")
	rel()
	rel, ok = l.TryAcquire(nn("third", "d"))
	assert.True(t, ok)
	rel()
	assert.Zero(t, l.InUse())
	assert.Zero(t, l.Waiting())
}

// TestLimiter_FIFO: the check that waited first gets the free slot, a later
// one cannot take it, and the woken check claims it.
func TestLimiter_FIFO(t *testing.T) {
	l, w := newLimiter(1, 1)
	hold, ok := l.TryAcquire(nn("x", "holder"))
	require.True(t, ok)
	for _, k := range []types.NamespacedName{nn("a", "first"), nn("b", "second"), nn("c", "third")} {
		_, ok = l.TryAcquire(k)
		require.False(t, ok)
	}
	_, ok = l.TryAcquire(nn("a", "first"))
	require.False(t, ok, "asking again keeps the place")
	assert.Equal(t, 3, l.Waiting())
	hold()
	assert.Equal(t, []types.NamespacedName{nn("a", "first")}, w.take())
	_, ok = l.TryAcquire(nn("b", "second"))
	assert.False(t, ok, "second may not jump ahead of first")
	_, ok = l.TryAcquire(nn("z", "newcomer"))
	assert.False(t, ok, "nor may a newcomer")
	rel, ok := l.TryAcquire(nn("a", "first"))
	require.True(t, ok)
	rel()
	assert.Equal(t, []types.NamespacedName{nn("b", "second")}, w.take())
}

// TestLimiter_FullNamespaceDoesNotHoldOthers (QA round 3): waiters of a
// namespace at its cap are not ahead of a check of another namespace while
// global slots are free; with PerNamespace 2, only as many waiters of a
// namespace as it has free slots are served ahead.
func TestLimiter_FullNamespaceDoesNotHoldOthers(t *testing.T) {
	l, w := newLimiter(3, 1)
	holdA, ok := l.TryAcquire(nn("a", "0"))
	require.True(t, ok)
	for i := 1; i <= 5; i++ {
		_, ok = l.TryAcquire(nn("a", fmt.Sprint(i)))
		require.False(t, ok)
	}
	rel, ok := l.TryAcquire(nn("b", "1"))
	assert.True(t, ok, "five waiters of the full namespace a are not ahead of b")
	rel()
	assert.Empty(t, w.take(), "freeing b's slot wakes nobody: namespace a is still full")
	holdA()
	assert.Equal(t, []types.NamespacedName{nn("a", "1")}, w.take(), "only one a waiter: PerNamespace is 1")

	l2, w2 := newLimiter(4, 2)
	hold, ok := l2.TryAcquire(nn("a", "0"))
	require.True(t, ok)
	hold2, ok := l2.TryAcquire(nn("a", "1"))
	require.True(t, ok)
	for i := 2; i <= 6; i++ {
		_, ok = l2.TryAcquire(nn("a", fmt.Sprint(i)))
		require.False(t, ok)
	}
	hold()
	hold2()
	assert.Equal(t, []types.NamespacedName{nn("a", "2"), nn("a", "3")}, w2.take(), "two a waiters, PerNamespace 2")
	rel, ok = l2.TryAcquire(nn("b", "1"))
	assert.True(t, ok, "two global slots are still free for b")
	rel()
}

// TestLimiter_UnclaimedReservationExpires: a woken check that never comes
// back (deleted, or the wake was lost) gives its slot to the next waiter
// after ClaimTTL.
func TestLimiter_UnclaimedReservationExpires(t *testing.T) {
	l, w := newLimiter(1, 1)
	l.ClaimTTL = 20 * time.Millisecond
	hold, ok := l.TryAcquire(nn("x", "holder"))
	require.True(t, ok)
	_, _ = l.TryAcquire(nn("a", "gone"))
	_, _ = l.TryAcquire(nn("b", "next"))
	hold()
	require.Equal(t, []types.NamespacedName{nn("a", "gone")}, w.take())
	require.Eventually(t, func() bool {
		k := w.take()
		return len(k) == 1 && k[0] == nn("b", "next")
	}, time.Second, 5*time.Millisecond, "after ClaimTTL the slot goes to the next waiter")
	_, ok = l.TryAcquire(nn("a", "gone"))
	assert.False(t, ok, "the expired check queues again")
	rel, ok := l.TryAcquire(nn("b", "next"))
	require.True(t, ok)
	rel()
}

// TestLimiter_Cancel: a waiting check that is deleted or suspended leaves the
// queue, and a reserved one gives its slot on.
func TestLimiter_Cancel(t *testing.T) {
	l, w := newLimiter(1, 1)
	hold, ok := l.TryAcquire(nn("x", "holder"))
	require.True(t, ok)
	for _, k := range []types.NamespacedName{nn("a", "1"), nn("b", "2"), nn("c", "3")} {
		_, _ = l.TryAcquire(k)
	}
	l.Cancel(nn("a", "1"))
	l.Cancel(nn("never", "seen"))
	assert.Equal(t, 2, l.Waiting())
	hold()
	assert.Equal(t, []types.NamespacedName{nn("b", "2")}, w.take())
	l.Cancel(nn("b", "2"))
	assert.Equal(t, []types.NamespacedName{nn("c", "3")}, w.take(), "the cancelled reservation goes to the next waiter")
	rel, ok := l.TryAcquire(nn("c", "3"))
	require.True(t, ok)
	rel()
	assert.Zero(t, l.InUse())
	assert.Zero(t, l.Waiting())
}

// TestLimiter_20kWaiters (QA round 3): no polling and no scan. 20,000
// waiters, in one namespace or one namespace each, are served in arrival
// order, each release waking exactly the next one.
func TestLimiter_20kWaiters(t *testing.T) {
	const n = 20000
	for _, spread := range []bool{false, true} {
		t.Run(fmt.Sprintf("one namespace each=%v", spread), func(t *testing.T) {
			l, w := newLimiter(12, 1)
			key := func(i int) types.NamespacedName {
				if spread {
					return nn(fmt.Sprintf("ns-%05d", i), "mc")
				}
				return nn("one", fmt.Sprintf("mc-%05d", i))
			}
			var running []func()
			for i := range 12 {
				rel, ok := l.TryAcquire(nn(fmt.Sprintf("holder-%d", i), "h"))
				require.True(t, ok)
				running = append(running, rel)
			}
			for i := range n {
				_, ok := l.TryAcquire(key(i))
				require.False(t, ok)
			}
			require.Equal(t, n, l.Waiting())

			start := time.Now()
			next, served := 0, 0
			for _, rel := range running {
				rel()
			}
			for served < n {
				woken := w.take()
				require.NotEmpty(t, woken, "a release always wakes the next waiter (served %d)", served)
				for _, k := range woken {
					require.Equal(t, key(next), k, "arrival order")
					next++
					rel, ok := l.TryAcquire(k)
					require.True(t, ok)
					served++
					rel()
				}
			}
			elapsed := time.Since(start)
			t.Logf("%d waiters served in %s", n, elapsed)
			assert.Less(t, elapsed, 10*time.Second)
			assert.Zero(t, l.Waiting())
			assert.Zero(t, l.InUse())
		})
	}
}

// TestLimiter_Concurrent: under contention, with wake-ups delivered to
// goroutines, the caps are never exceeded and every check is served.
func TestLimiter_Concurrent(t *testing.T) {
	l := metriccheck.NewLimiter(3, 1)
	chans := map[types.NamespacedName]chan struct{}{}
	for i := range 50 {
		chans[nn([]string{"a", "b", "c", "d", "e"}[i%5], fmt.Sprint(i))] = make(chan struct{}, 1)
	}
	l.Wake = func(k types.NamespacedName) { chans[k] <- struct{}{} }
	var mu sync.Mutex
	running, perNS, maxTotal := 0, map[string]int{}, 0
	var wg sync.WaitGroup
	for k, ch := range chans {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, ok := l.TryAcquire(k)
			for !ok {
				select {
				case <-ch:
				case <-time.After(time.Second):
				}
				rel, ok = l.TryAcquire(k)
			}
			mu.Lock()
			running++
			perNS[k.Namespace]++
			assert.LessOrEqual(t, perNS[k.Namespace], 1)
			maxTotal = max(maxTotal, running)
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			running--
			perNS[k.Namespace]--
			mu.Unlock()
			rel()
		}()
	}
	wg.Wait()
	assert.LessOrEqual(t, maxTotal, 3)
	assert.Zero(t, l.InUse())
}
