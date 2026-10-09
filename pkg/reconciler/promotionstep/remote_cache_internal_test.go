// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
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
