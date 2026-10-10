// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// remoteHeadsTTL is how long one ls-remote of a repository answers every
// step waiting for a merge on it.
const remoteHeadsTTL = 30 * time.Second

// historyEntries bounds the branch histories kept.
const historyEntries = 64

// remoteCache memoizes reads of remote repositories that every step waiting
// on the same repository needs: the branch heads (one ls-remote per
// repository per remoteHeadsTTL) and the recent history of a branch at a
// given head (immutable for that head). It is a read-through cache of
// external values, not promotion state: a stale entry only delays a PR
// branch refresh by up to remoteHeadsTTL, and an empty cache costs one more
// read. A cached head can be older than a commit the reader already knows
// (another step read the heads before it pushed): a reader that would act on
// such a head reads them again (readHeads). The decisions it feeds are recorded in the step's status.outputs.
//
// Concurrent reads of the same thing share one network read (singleflight):
// a wave of 150 environments checking the same head makes one ls-remote and
// one history or graph read, not 150.
type remoteCache struct {
	mu      sync.Mutex
	heads   map[string]headsEntry
	history map[string][]scm.CommitPaths
	order   []string // history keys, oldest first
	graphs  map[string]branchGraph
	gorder  []string // graph keys, oldest first
	flight  singleflight.Group
	// seq orders requests and flights: a caller is numbered when it asks, a
	// flight when its read starts (shared).
	seq atomic.Uint64
	// gen is the current flight generation of each key; a caller that must
	// not use the flight in progress moves its key to the next one.
	gen map[string]uint64
	// onWait, when set (tests), is called once a caller waits for a read.
	onWait func(key string)
}

// maxGenerations bounds remoteCache.gen.
const maxGenerations = 4096

// flightResult is a shared read's value and the seq its read started at.
type flightResult struct {
	val     any
	started uint64
}

// branchGraph is a cached scm.BranchGraphReader result.
type branchGraph struct {
	head  string
	graph map[string]scm.GraphCommit
}

// shared runs read once for every concurrent caller with the same key. The
// read runs on a context detached from the first caller's cancellation, so
// one caller giving up does not fail the others, bounded by historyTimeout;
// each caller still returns when its own ctx ends.
//
// A caller may join a read that started before it asked, and use its value
// (a wave of steps shares one read). Two exceptions (#1644):
//   - fresh: the caller wants data read after it asked (heads read again
//     because the cached ones are older than a commit it knows). It moves
//     the key to a new generation first, so it starts, or shares with other
//     fresh callers, a new read instead of waiting for the old one.
//   - a read that started before the caller and failed is not the caller's
//     answer: it may have spent most of its historyTimeout before the
//     caller came. The caller reads again, once, sharing that new read with
//     the others that joined the failed one.
func (c *remoteCache) shared(ctx context.Context, key string, fresh bool, read func(context.Context) (any, error)) (any, error) {
	asked := c.seq.Add(1)
	if fresh {
		c.nextGeneration(key, c.generation(key))
	}
	for attempt := 0; ; attempt++ {
		gen := c.generation(key)
		ch := c.flight.DoChan(key+"\x00"+strconv.FormatUint(gen, 10), func() (any, error) {
			started := c.seq.Add(1)
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), historyTimeout)
			defer cancel()
			v, err := read(rctx)
			return flightResult{val: v, started: started}, err
		})
		if c.onWait != nil {
			c.onWait(key)
		}
		var r singleflight.Result
		select {
		case r = <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		res, _ := r.Val.(flightResult)
		if attempt == 0 && res.started < asked && (fresh || r.Err != nil) {
			c.nextGeneration(key, gen)
			continue
		}
		return res.val, r.Err
	}
}

// generation returns key's current flight generation.
func (c *remoteCache) generation(key string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen[key]
}

// nextGeneration moves key past generation gen. Callers that saw gen move it
// once: they then share one read of the next generation.
func (c *remoteCache) nextGeneration(key string, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen == nil || len(c.gen) >= maxGenerations {
		// Forgetting generations is safe: a read of an old one ends within
		// historyTimeout, and its result is still checked against asked.
		c.gen = map[string]uint64{}
	}
	if c.gen[key] == gen {
		c.gen[key] = gen + 1
	}
}

// tokenKey identifies token in a cache key without holding it.
func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

type headsEntry struct {
	at    time.Time
	heads map[string]string
}

// remoteHeads returns the branch heads of url, from the cache when they are
// younger than remoteHeadsTTL.
func (c *remoteCache) remoteHeads(ctx context.Context, rh scm.RemoteHeadReader, url, token string, now time.Time) (map[string]string, error) {
	c.mu.Lock()
	e, ok := c.heads[url]
	c.mu.Unlock()
	if ok && now.Sub(e.at) < remoteHeadsTTL && !now.Before(e.at) {
		return e.heads, nil
	}
	return c.loadHeads(ctx, rh, url, token, now, false)
}

// readHeads reads the branch heads of url now, bypassing the cache, and
// stores them for the others. The read starts after the call: a read another
// step started earlier, which can predate a commit this caller knows of, is
// not used.
func (c *remoteCache) readHeads(ctx context.Context, rh scm.RemoteHeadReader, url, token string, now time.Time) (map[string]string, error) {
	return c.loadHeads(ctx, rh, url, token, now, true)
}

// loadHeads reads the branch heads of url, sharing a read in progress
// (unless fresh: shared), and stores them for the others.
func (c *remoteCache) loadHeads(ctx context.Context, rh scm.RemoteHeadReader, url, token string, now time.Time, fresh bool) (map[string]string, error) {
	v, err := c.shared(ctx, "heads\x00"+url+"\x00"+tokenKey(token), fresh, func(ctx context.Context) (any, error) {
		return rh.RemoteHeads(ctx, url, token)
	})
	if err != nil {
		return nil, err
	}
	heads := v.(map[string]string)
	c.mu.Lock()
	if c.heads == nil {
		c.heads = map[string]headsEntry{}
	}
	c.heads[url] = headsEntry{at: now, heads: heads}
	c.mu.Unlock()
	return heads, nil
}

// branchHistory returns the last maxCommits commits of branch at head.
func (c *remoteCache) branchHistory(ctx context.Context, rh scm.RemoteHeadReader, url, branch, head, token string, maxCommits int) ([]scm.CommitPaths, error) {
	key := url + "\x00" + branch + "\x00" + head + "\x00" + strconv.Itoa(maxCommits)
	c.mu.Lock()
	h, ok := c.history[key]
	c.mu.Unlock()
	if ok {
		return h, nil
	}
	v, err := c.shared(ctx, "history\x00"+key+"\x00"+tokenKey(token), false, func(ctx context.Context) (any, error) {
		return rh.BranchHistory(ctx, url, branch, token, maxCommits)
	})
	if err != nil {
		return nil, err
	}
	h = v.([]scm.CommitPaths)
	if len(h) == 0 || h[0].SHA != head {
		// The branch moved again since the ls-remote: do not file this
		// history under head.
		return h, nil
	}
	c.mu.Lock()
	if c.history == nil {
		c.history = map[string][]scm.CommitPaths{}
	}
	if _, dup := c.history[key]; !dup {
		c.history[key] = h
		c.order = append(c.order, key)
		for len(c.order) > historyEntries {
			delete(c.history, c.order[0])
			c.order = c.order[1:]
		}
	}
	c.mu.Unlock()
	return h, nil
}

// branchGraph returns the commit graph of branch at head, read at depth
// maxCommits (scm.BranchGraphReader): from the cache, or one shared read.
// A graph read after the branch moved past head is returned but not cached
// under head. Graphs are kept per token, so a Pipeline never gets an answer
// read with another Pipeline's credentials.
func (c *remoteCache) branchGraph(ctx context.Context, gr scm.BranchGraphReader, url, branch, head, token string, maxCommits int) (branchGraph, error) {
	key := url + "\x00" + branch + "\x00" + head + "\x00" + strconv.Itoa(maxCommits) + "\x00" + tokenKey(token)
	c.mu.Lock()
	g, ok := c.graphs[key]
	c.mu.Unlock()
	if ok {
		return g, nil
	}
	v, err := c.shared(ctx, "graph\x00"+key, false, func(ctx context.Context) (any, error) {
		h, graph, err := gr.BranchGraph(ctx, url, branch, token, maxCommits)
		return branchGraph{head: h, graph: graph}, err
	})
	if err != nil {
		return branchGraph{}, err
	}
	g = v.(branchGraph)
	if g.head != head {
		return g, nil
	}
	c.mu.Lock()
	if c.graphs == nil {
		c.graphs = map[string]branchGraph{}
	}
	if _, dup := c.graphs[key]; !dup {
		c.graphs[key] = g
		c.gorder = append(c.gorder, key)
		for len(c.gorder) > historyEntries {
			delete(c.graphs, c.gorder[0])
			c.gorder = c.gorder[1:]
		}
	}
	c.mu.Unlock()
	return g, nil
}
