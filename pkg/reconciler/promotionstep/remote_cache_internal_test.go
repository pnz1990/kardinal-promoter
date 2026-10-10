// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

type countingRemote struct {
	lsRemote, fetches int
	head              string
}

func (c *countingRemote) RemoteHeads(context.Context, string, string) (map[string]string, error) {
	c.lsRemote++
	return map[string]string{"main": c.head}, nil
}

func (c *countingRemote) BranchHistory(context.Context, string, string, string, int) ([]scm.CommitPaths, error) {
	c.fetches++
	return []scm.CommitPaths{{SHA: c.head}}, nil
}

// TestRemoteCache (#1504 QA): every step waiting on one repository shares
// one ls-remote per remoteHeadsTTL, and one history fetch per base head.
func TestRemoteCache(t *testing.T) {
	ctx := context.Background()
	rem := &countingRemote{head: "a"}
	var c remoteCache
	t0 := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for i := range 100 { // 100 waiting steps, checked within the TTL
		h, err := c.remoteHeads(ctx, rem, "https://git/x", "tok", t0.Add(time.Duration(i)*100*time.Millisecond))
		require.NoError(t, err)
		assert.Equal(t, "a", h["main"])
		_, err = c.branchHistory(ctx, rem, "https://git/x", "main", h["main"], "tok", 20)
		require.NoError(t, err)
	}
	assert.Equal(t, 1, rem.lsRemote)
	assert.Equal(t, 1, rem.fetches)

	rem.head = "b"
	h, _ := c.remoteHeads(ctx, rem, "https://git/x", "tok", t0.Add(remoteHeadsTTL+time.Second))
	assert.Equal(t, "b", h["main"], "expired: read again")
	_, _ = c.branchHistory(ctx, rem, "https://git/x", "main", "b", "tok", 20)
	assert.Equal(t, 2, rem.lsRemote)
	assert.Equal(t, 2, rem.fetches, "a new head: fetched once")
	_, _ = c.remoteHeads(ctx, rem, "https://git/other", "tok", t0)
	assert.Equal(t, 3, rem.lsRemote, "per repository")
}

// within receives from ch, failing the test after 5 seconds instead of
// hanging when what does not happen.
func within[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

// gatedRemote answers RemoteHeads and BranchHistory in call order: call n
// signals entered[n] and then waits for release[n], answering heads[n] or,
// when fail[n], the deadline error a read that ran out of time gets.
type gatedRemote struct {
	mu      sync.Mutex
	calls   int
	entered []chan struct{}
	release []chan struct{}
	heads   []string
	fail    []bool
}

func newGatedRemote(heads []string, fail []bool) *gatedRemote {
	g := &gatedRemote{heads: heads, fail: fail}
	for range heads {
		g.entered = append(g.entered, make(chan struct{}))
		g.release = append(g.release, make(chan struct{}))
	}
	return g
}

func (g *gatedRemote) next() (int, error) {
	g.mu.Lock()
	n := g.calls
	g.calls++
	g.mu.Unlock()
	close(g.entered[n])
	<-g.release[n]
	if g.fail[n] {
		return n, context.DeadlineExceeded
	}
	return n, nil
}

func (g *gatedRemote) RemoteHeads(context.Context, string, string) (map[string]string, error) {
	n, err := g.next()
	if err != nil {
		return nil, err
	}
	return map[string]string{"main": g.heads[n]}, nil
}

func (g *gatedRemote) BranchHistory(context.Context, string, string, string, int) ([]scm.CommitPaths, error) {
	n, err := g.next()
	if err != nil {
		return nil, err
	}
	return []scm.CommitPaths{{SHA: g.heads[n]}}, nil
}

func (g *gatedRemote) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

// TestRemoteCache_FreshHeadsDoNotJoinAnOlderRead (#1644): readHeads, the
// re-read a step makes when the cached heads are older than a commit it
// knows, never takes the answer of an ls-remote another step started
// before it asked: that read can predate the commit. remoteHeads callers
// still share it.
func TestRemoteCache_FreshHeadsDoNotJoinAnOlderRead(t *testing.T) {
	ctx := context.Background()
	rem := newGatedRemote([]string{"old", "new"}, []bool{false, false})
	waiting := make(chan string, 8)
	c := remoteCache{onWait: func(key string) { waiting <- key }}
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	type answer struct {
		head string
		err  error
	}
	first := make(chan answer, 1)
	go func() {
		h, err := c.remoteHeads(ctx, rem, "https://git/x", "tok", now)
		first <- answer{h["main"], err}
	}()
	within(t, rem.entered[0], "read 0 to start") // the other step's read is in progress
	within(t, waiting, "a caller to wait")
	shared := make(chan answer, 1)
	go func() { // a cache miss joins it
		h, err := c.remoteHeads(ctx, rem, "https://git/x", "tok", now)
		shared <- answer{h["main"], err}
	}()
	within(t, waiting, "a caller to wait")
	fresh := make(chan answer, 1)
	go func() {
		h, err := c.readHeads(ctx, rem, "https://git/x", "tok", now)
		fresh <- answer{h["main"], err}
	}()
	within(t, rem.entered[1], "read 1 to start") // the fresh read starts its own ls-remote, not waiting for the old one
	close(rem.release[1])
	assert.Equal(t, answer{"new", nil}, within(t, fresh, "the fresh answer"), "the fresh read is not the one started before it")
	close(rem.release[0])
	assert.Equal(t, answer{"old", nil}, within(t, first, "the first answer"))
	assert.Equal(t, answer{"old", nil}, within(t, shared, "the shared answer"), "a plain cache miss shares the read in progress")
	assert.Equal(t, 2, rem.callCount())
}

// TestRemoteCache_OlderFailedReadIsRetried (#1644): a caller that joined a
// read started before it asked, and that failed (ran out of its
// historyTimeout, much of it spent before the caller came), reads again
// once instead of taking the failure. Concurrent such callers share the
// new read. A caller whose own read failed gets the failure.
func TestRemoteCache_OlderFailedReadIsRetried(t *testing.T) {
	ctx := context.Background()
	rem := newGatedRemote([]string{"a", "a"}, []bool{true, false})
	waiting := make(chan string, 16)
	c := remoteCache{onWait: func(key string) { waiting <- key }}
	type answer struct {
		n   int
		err error
	}
	first := make(chan answer, 1)
	go func() {
		h, err := c.branchHistory(ctx, rem, "https://git/x", "main", "a", "tok", 20)
		first <- answer{len(h), err}
	}()
	within(t, rem.entered[0], "read 0 to start")
	within(t, waiting, "a caller to wait")
	late := make(chan answer, 3)
	for range 3 {
		go func() {
			h, err := c.branchHistory(ctx, rem, "https://git/x", "main", "a", "tok", 20)
			late <- answer{len(h), err}
		}()
	}
	for range 3 {
		within(t, waiting, "a caller to wait") // the three wait for the read in progress before it fails
	}
	close(rem.release[0])
	got := within(t, first, "the first answer")
	assert.ErrorIs(t, got.err, context.DeadlineExceeded, "the caller that started the read gets its failure")
	within(t, rem.entered[1], "read 1 to start")
	for range 3 {
		within(t, waiting, "a caller to wait") // all three wait for the new read
	}
	close(rem.release[1])
	for range 3 {
		assert.Equal(t, answer{1, nil}, within(t, late, "a later answer"), "a later caller reads again")
	}
	assert.Equal(t, 2, rem.callCount(), "the later callers share one new read")
}

// TestRemoteCache_OlderHeadsFinishingLastAreNotCached (#1653 QA): a fresh
// read overlaps an older shared one, and the older one finishes last. Its
// caller gets its answer, but the cache keeps the heads of the read that
// started last, so the next steps within remoteHeadsTTL see the newer head.
func TestRemoteCache_OlderHeadsFinishingLastAreNotCached(t *testing.T) {
	ctx := context.Background()
	rem := newGatedRemote([]string{"old", "new"}, []bool{false, false})
	waiting := make(chan string, 8)
	c := remoteCache{onWait: func(key string) { waiting <- key }}
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	type answer struct {
		head string
		err  error
	}
	older := make(chan answer, 1)
	go func() {
		h, err := c.remoteHeads(ctx, rem, "https://git/x", "tok", now)
		older <- answer{h["main"], err}
	}()
	within(t, rem.entered[0], "the older read to start")
	within(t, waiting, "the older caller to wait")
	h, err := func() (map[string]string, error) {
		done := make(chan struct{})
		var h map[string]string
		var err error
		go func() { h, err = c.readHeads(ctx, rem, "https://git/x", "tok", now); close(done) }()
		within(t, rem.entered[1], "the fresh read to start")
		close(rem.release[1]) // the fresh read finishes first
		within(t, done, "the fresh answer")
		return h, err
	}()
	require.NoError(t, err)
	assert.Equal(t, "new", h["main"])

	close(rem.release[0]) // the older read finishes last
	assert.Equal(t, answer{"old", nil}, within(t, older, "the older answer"), "its own caller gets its answer")
	cached, err := c.remoteHeads(ctx, rem, "https://git/x", "tok", now.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, "new", cached["main"], "the cache keeps the heads read last, not the ones that finished last")
	assert.Equal(t, 2, rem.callCount(), "served from the cache")
}

// graphRemote is countingRemote with a commit graph reader.
type graphRemote struct {
	countingRemote
	graphs int
}

func (g *graphRemote) BranchGraph(context.Context, string, string, string, int) (string, map[string]scm.GraphCommit, error) {
	g.graphs++
	return g.head, map[string]scm.GraphCommit{g.head: {}}, nil
}

// TestRemoteCache_StoreBeforeFlightEnds (#1653 QA cache audit): a caller
// that missed the cache while another caller's read was in flight, and
// gets to its own read only after that flight ended, finds the stored
// result instead of reading again: the flight stores before it ends, and
// a new flight checks the cache first. For heads, history and graphs.
func TestRemoteCache_StoreBeforeFlightEnds(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for _, kind := range []string{"heads", "history", "graph"} {
		t.Run(kind, func(t *testing.T) {
			rem := &graphRemote{countingRemote: countingRemote{head: "a"}}
			inMiss, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			c := remoteCache{onMiss: func(string) {
				first := false
				once.Do(func() { first = true })
				if first { // the late caller: it missed, then waits here
					close(inMiss)
					<-release
				}
			}}
			call := func() error {
				var err error
				switch kind {
				case "heads":
					_, err = c.remoteHeads(ctx, rem, "https://git/x", "tok", now)
				case "history":
					_, err = c.branchHistory(ctx, rem, "https://git/x", "main", "a", "tok", 20)
				case "graph":
					_, err = c.branchGraph(ctx, rem, "https://git/x", "main", "a", "tok", 20)
				}
				return err
			}
			late := make(chan error, 1)
			go func() { late <- call() }()
			within(t, inMiss, "the late caller to miss the cache")
			require.NoError(t, call()) // another caller reads, stores, and its flight ends
			close(release)
			require.NoError(t, within(t, late, "the late caller"))
			reads := map[string]int{"heads": rem.lsRemote, "history": rem.fetches, "graph": rem.graphs}[kind]
			assert.Equal(t, 1, reads, "one read: the late caller's flight found it stored")
		})
	}
}
