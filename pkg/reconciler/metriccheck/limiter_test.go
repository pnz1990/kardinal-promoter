// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package metriccheck_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/metriccheck"
)

func nn(ns, name string) types.NamespacedName { return types.NamespacedName{Namespace: ns, Name: name} }

// TestLimiter_PerNamespaceAndGlobal (QA #1479): one namespace with slow
// endpoints holds at most one slot, so other namespaces still query; the
// global cap holds; release frees the slot once.
func TestLimiter_PerNamespaceAndGlobal(t *testing.T) {
	l := metriccheck.NewLimiter(2, 1)
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
	relC()
	rel, ok := l.TryAcquire(nn("noisy", "b"))
	assert.True(t, ok, "b had waited first")
	rel()
}

// TestLimiter_FIFO: a check that waited first gets the free slot before a
// later one, and a waiter that stops asking leaves the queue.
func TestLimiter_FIFO(t *testing.T) {
	now := time.Now()
	l := metriccheck.NewLimiter(1, 1)
	l.NowFn = func() time.Time { return now }
	hold, ok := l.TryAcquire(nn("x", "holder"))
	require.True(t, ok)
	_, ok = l.TryAcquire(nn("a", "first"))
	require.False(t, ok)
	_, ok = l.TryAcquire(nn("b", "second"))
	require.False(t, ok)
	assert.Equal(t, 2, l.Waiting())
	hold()
	_, ok = l.TryAcquire(nn("b", "second"))
	assert.False(t, ok, "second may not jump ahead of first")
	rel, ok := l.TryAcquire(nn("a", "first"))
	require.True(t, ok)
	rel()

	// first is served; second stops asking and expires.
	now = now.Add(time.Minute)
	rel, ok = l.TryAcquire(nn("c", "late"))
	assert.True(t, ok, "an expired waiter does not block the queue")
	rel()
	assert.Zero(t, l.Waiting())
}

// TestLimiter_Concurrent: under contention the caps are never exceeded.
func TestLimiter_Concurrent(t *testing.T) {
	l := metriccheck.NewLimiter(3, 1)
	var mu sync.Mutex
	running, perNS, maxTotal := 0, map[string]int{}, 0
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ns := []string{"a", "b", "c", "d", "e"}[i%5]
			for {
				rel, ok := l.TryAcquire(nn(ns, string(rune('a'+i))))
				if !ok {
					time.Sleep(time.Millisecond)
					continue
				}
				mu.Lock()
				running++
				perNS[ns]++
				assert.LessOrEqual(t, perNS[ns], 1)
				if running > maxTotal {
					maxTotal = running
				}
				mu.Unlock()
				time.Sleep(time.Millisecond)
				mu.Lock()
				running--
				perNS[ns]--
				mu.Unlock()
				rel()
				return
			}
		}(i)
	}
	wg.Wait()
	assert.LessOrEqual(t, maxTotal, 3)
}
