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
	// onMiss, when set (tests), is called when a caller found nothing in
	// the cache, before it starts or joins a read.
	onMiss func(kind string)
	// onFresh and inFlight, when set (tests), are called after a fresh
	// caller moved the key to a new generation, and when a flight starts.
	onFresh, inFlight func(key string)
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
//     the key to a new generation first and starts a new read, instead of
//     waiting for the old one. Fresh callers share a read only when they
//     overlap (one moved the generation and the others joined before its
//     read started); otherwise each makes its own. The worst case is one
//     ls-remote per waiting step per remoteHeadsTTL, while a step's PR
//     branch rebuild after a force-push keeps failing and every check
//     reads the heads again.
//   - a read that started before the caller and failed is not the caller's
//     answer: it may have spent most of its historyTimeout before the
//     caller came. The caller reads again, once, sharing that new read with
//     the others that joined the failed one. The new read runs within the
//     caller's remaining ctx, and may fail too.
//
// started is the seq the answering read started at, which orders the
// values written to the heads cache (loadHeads). read gets it too: it runs
// inside the flight, and checks the cache again and stores its result there
// before the flight ends, so a caller that arrives between the end of a
// flight and the store does not start another read.
func (c *remoteCache) shared(ctx context.Context, key string, fresh bool, read func(ctx context.Context, started uint64) (any, error)) (v any, started uint64, err error) {
	asked := c.seq.Add(1)
	if fresh {
		c.nextGeneration(key, c.generation(key))
		if c.onFresh != nil {
			c.onFresh(key)
		}
	}
	for attempt := 0; ; attempt++ {
		gen := c.generation(key)
		// Fresh and shared reads never share a flight: a shared read checks
		// the cache first and may answer with heads an older read stored,
		// which a fresh caller must not take (#1667 QA).
		flightKey := key + "\x00" + strconv.FormatUint(gen, 10)
		if fresh {
			flightKey += "\x00fresh"
		}
		ch := c.flight.DoChan(flightKey, func() (any, error) {
			if c.inFlight != nil {
				c.inFlight(flightKey)
			}
			started := c.seq.Add(1)
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), historyTimeout)
			defer cancel()
			v, err := read(rctx, started)
			return flightResult{val: v, started: started}, err
		})
		if c.onWait != nil {
			c.onWait(key)
		}
		var r singleflight.Result
		select {
		case r = <-ch:
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
		res, _ := r.Val.(flightResult)
		if attempt == 0 && res.started < asked && (fresh || r.Err != nil) {
			c.nextGeneration(key, gen)
			continue
		}
		return res.val, res.started, r.Err
	}
}

func (c *remoteCache) missed(kind string) {
	if c.onMiss != nil {
		c.onMiss(kind)
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
	// started is the seq the ls-remote that read heads started at: a read
	// that started earlier but finished later does not replace it.
	started uint64
}

// remoteHeads returns the branch heads of url, from the cache when they are
// younger than remoteHeadsTTL.
func (c *remoteCache) remoteHeads(ctx context.Context, rh scm.RemoteHeadReader, url, token string, now time.Time) (map[string]string, error) {
	if h, ok := c.cachedHeads(url, now); ok {
		return h, nil
	}
	c.missed("heads")
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
// (unless fresh: shared), and stores them for the others. A shared (not
// fresh) read first checks the cache again: another flight may have stored
// heads since the caller missed.
func (c *remoteCache) loadHeads(ctx context.Context, rh scm.RemoteHeadReader, url, token string, now time.Time, fresh bool) (map[string]string, error) {
	v, _, err := c.shared(ctx, "heads\x00"+url+"\x00"+tokenKey(token), fresh, func(ctx context.Context, started uint64) (any, error) {
		if !fresh {
			if h, ok := c.cachedHeads(url, now); ok {
				return h, nil
			}
		}
		heads, err := rh.RemoteHeads(ctx, url, token)
		if err != nil {
			return nil, err
		}
		c.storeHeads(url, heads, now, started)
		return heads, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(map[string]string), nil
}

// cachedHeads returns the cached heads of url when they are younger than
// remoteHeadsTTL at now.
func (c *remoteCache) cachedHeads(url string, now time.Time) (map[string]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.heads[url]
	if ok && now.Sub(e.at) < remoteHeadsTTL && !now.Before(e.at) {
		return e.heads, true
	}
	return nil, false
}

// storeHeads caches heads read by the ls-remote that started at seq
// started. Reads overlap (a fresh read next to an older shared one): the
// cache keeps the one that started last, so an older read finishing last
// does not put back a head older than one already cached.
func (c *remoteCache) storeHeads(url string, heads map[string]string, now time.Time, started uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.heads == nil {
		c.heads = map[string]headsEntry{}
	}
	if e, ok := c.heads[url]; !ok || e.started <= started {
		c.heads[url] = headsEntry{at: now, heads: heads, started: started}
	}
}

// branchHistory returns the last maxCommits commits of branch at head.
func (c *remoteCache) branchHistory(ctx context.Context, rh scm.RemoteHeadReader, url, branch, head, token string, maxCommits int) ([]scm.CommitPaths, error) {
	key := url + "\x00" + branch + "\x00" + head + "\x00" + strconv.Itoa(maxCommits)
	if h, ok := c.cachedHistory(key); ok {
		return h, nil
	}
	c.missed("history")
	// Inside the flight: check again (another flight may have stored it
	// since), and store before the flight ends.
	v, _, err := c.shared(ctx, "history\x00"+key+"\x00"+tokenKey(token), false, func(ctx context.Context, _ uint64) (any, error) {
		if h, ok := c.cachedHistory(key); ok {
			return h, nil
		}
		h, err := rh.BranchHistory(ctx, url, branch, token, maxCommits)
		if err != nil {
			return nil, err
		}
		if len(h) > 0 && h[0].SHA == head {
			// Otherwise the branch moved again since the ls-remote: do not
			// file this history under head.
			c.storeHistory(key, h)
		}
		return h, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]scm.CommitPaths), nil
}

func (c *remoteCache) cachedHistory(key string) ([]scm.CommitPaths, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.history[key]
	return h, ok
}

func (c *remoteCache) storeHistory(key string, h []scm.CommitPaths) {
	c.mu.Lock()
	defer c.mu.Unlock()
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
}

// branchGraph returns the commit graph of branch at head, read at depth
// maxCommits (scm.BranchGraphReader): from the cache, or one shared read.
// A graph read after the branch moved past head is returned but not cached
// under head. Graphs are kept per token, so a Pipeline never gets an answer
// read with another Pipeline's credentials.
func (c *remoteCache) branchGraph(ctx context.Context, gr scm.BranchGraphReader, url, branch, head, token string, maxCommits int) (branchGraph, error) {
	key := url + "\x00" + branch + "\x00" + head + "\x00" + strconv.Itoa(maxCommits) + "\x00" + tokenKey(token)
	if g, ok := c.cachedGraph(key); ok {
		return g, nil
	}
	c.missed("graph")
	v, _, err := c.shared(ctx, "graph\x00"+key, false, func(ctx context.Context, _ uint64) (any, error) {
		if g, ok := c.cachedGraph(key); ok {
			return g, nil
		}
		h, graph, err := gr.BranchGraph(ctx, url, branch, token, maxCommits)
		if err != nil {
			return branchGraph{}, err
		}
		g := branchGraph{head: h, graph: graph}
		if g.head == head {
			c.storeGraph(key, g)
		}
		return g, nil
	})
	if err != nil {
		return branchGraph{}, err
	}
	return v.(branchGraph), nil
}

func (c *remoteCache) cachedGraph(key string) (branchGraph, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	g, ok := c.graphs[key]
	return g, ok
}

func (c *remoteCache) storeGraph(key string, g branchGraph) {
	c.mu.Lock()
	defer c.mu.Unlock()
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
}
