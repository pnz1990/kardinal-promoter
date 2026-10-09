// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// linearRemote is a branch of n commits c<n-1> (head) ... c0.
type linearRemote struct {
	mu       sync.Mutex
	n, reads int
	delay    time.Duration
}

func (l *linearRemote) RemoteHeads(context.Context, string, string) (map[string]string, error) {
	return map[string]string{"main": fmt.Sprintf("c%07d", l.n-1)}, nil
}

// BranchGraph is the last max commits of the chain, each with its parent
// (the oldest one's parent is the shallow boundary).
func (l *linearRemote) BranchGraph(_ context.Context, _, _, _ string, max int) (string, map[string][]string, error) {
	l.mu.Lock()
	l.reads++
	l.mu.Unlock()
	if l.delay > 0 {
		time.Sleep(l.delay)
	}
	g := map[string][]string{}
	for i := l.n - 1; i >= 0 && len(g) < max; i-- {
		g[fmt.Sprintf("c%07d", i)] = nil
		if i > 0 {
			g[fmt.Sprintf("c%07d", i)] = []string{fmt.Sprintf("c%07d", i-1)}
		}
	}
	return fmt.Sprintf("c%07d", l.n-1), g, nil
}

func (l *linearRemote) BranchHistory(_ context.Context, _, _, _ string, max int) ([]scm.CommitPaths, error) {
	var h []scm.CommitPaths
	for i := l.n - 1; i >= 0 && len(h) < max; i-- {
		h = append(h, scm.CommitPaths{SHA: fmt.Sprintf("c%07d", i)})
	}
	return h, nil
}

// TestDescends (#1575): a synced revision contains the promoted commit when
// it is that commit or a later one on the branch; an earlier commit, a
// commit not on the branch, or a promoted commit beyond the deep history do
// not. Steps of one head share one history read.
func TestDescends(t *testing.T) {
	ctx := context.Background()
	rem := &linearRemote{n: 150}
	r := &Reconciler{NowFn: func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) }}
	c := func(i int) string { return fmt.Sprintf("c%07d", i) }
	for _, tc := range []struct {
		name      string
		rev, want string
		ok        bool
	}{
		{"the head contains an earlier push", c(149), c(100), true},
		{"the same commit", c(100), c(100), true},
		{"an earlier commit does not", c(90), c(100), false},
		{"a commit not on the branch", "deadbeef00", c(100), false},
		{"short SHAs compare by prefix", c(149)[:7], c(140), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := r.descends(ctx, rem, rem, "https://git/acc", "main", "tok", tc.rev, tc.want)
			require.NoError(t, err)
			assert.Equal(t, tc.ok, ok)
		})
	}
	fresh := &Reconciler{NowFn: r.NowFn}
	cold := &linearRemote{n: 150}
	_, _ = fresh.descends(ctx, cold, cold, "https://git/acc", "main", "tok", c(149), c(140))
	assert.Equal(t, 1, cold.reads, "a recent commit: the first 20 commits")
	_, _ = fresh.descends(ctx, cold, cold, "https://git/acc", "main", "tok", c(149), c(10))
	assert.Equal(t, 2, cold.reads, "an older commit reads the deep history once")
	for i := range 100 { // 100 environments' checks on the same head
		_, _ = fresh.descends(ctx, cold, cold, "https://git/acc", "main", "tok", c(149), c(11+i%100))
	}
	assert.Equal(t, 2, cold.reads, "cached for the head")

	deep := &linearRemote{n: deepHistoryDepth + 50}
	ok, err := r.descends(ctx, deep, deep, "https://git/deep", "main", "tok", c(deep.n-1), c(0))
	require.NoError(t, err)
	assert.False(t, ok, "a promoted commit past the deep history is not assumed")
}

// TestDescends_OneReadPerHead (#1575 QA): a wave's concurrent checks of the
// same head share one graph read (singleflight) and then the cache.
func TestDescends_OneReadPerHead(t *testing.T) {
	ctx := context.Background()
	rem := &linearRemote{n: 30, delay: 50 * time.Millisecond}
	r := &Reconciler{NowFn: func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) }}
	var wg sync.WaitGroup
	for i := range 150 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := r.descends(ctx, rem, rem, "https://git/wave", "main", "tok", fmt.Sprintf("c%07d", 29), fmt.Sprintf("c%07d", 10+i%19))
			assert.NoError(t, err)
			assert.True(t, ok)
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, rem.reads, "150 concurrent checks of one head: one graph read")
}
