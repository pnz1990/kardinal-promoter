// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"strconv"
	"sync"
	"time"

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
// branch rebuild by up to remoteHeadsTTL, and an empty cache costs one more
// read. The decisions it feeds are recorded in the step's status.outputs.
type remoteCache struct {
	mu      sync.Mutex
	heads   map[string]headsEntry
	history map[string][]scm.CommitPaths
	order   []string // history keys, oldest first
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
	heads, err := rh.RemoteHeads(ctx, url, token)
	if err != nil {
		return nil, err
	}
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
	h, err := rh.BranchHistory(ctx, url, branch, token, maxCommits)
	if err != nil {
		return nil, err
	}
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
