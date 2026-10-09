// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
)

// TestShardCacheOpts (#1505 QA): a sharded controller caches only the shard
// token Leases (name kardinal-shard, kardinal's label), not every Lease in
// the cluster; without sharding the Lease cache is untouched.
//
// The hook Job selector (only labelled Jobs are cached) is kept with
// sharding on: the shard entries are added, not substituted.
func TestShardCacheOpts(t *testing.T) {
	unsharded := shardCacheOpts(buildCacheOpts(""), "").ByObject
	require.Len(t, unsharded, 1, "only the hook Job selector")
	opts := shardCacheOpts(buildCacheOpts(""), "b")
	require.Len(t, opts.ByObject, 2)
	var leases int
	for obj, by := range opts.ByObject {
		if _, ok := obj.(*batchv1.Job); ok {
			assert.True(t, by.Label.Matches(labels.Set{"kardinal.io/hookrun": "x"}), "hook Jobs cached")
			assert.False(t, by.Label.Matches(labels.Set{}), "other Jobs not cached")
			continue
		}
		_, ok := obj.(*coordinationv1.Lease)
		require.True(t, ok)
		leases++
		assert.True(t, by.Field.Matches(fields.Set{"metadata.name": "kardinal-shard"}))
		assert.False(t, by.Field.Matches(fields.Set{"metadata.name": "kardinal-promoter-leader-b"}))
		assert.True(t, by.Label.Matches(labels.Set{"app.kubernetes.io/managed-by": "kardinal-promoter"}))
		assert.False(t, by.Label.Matches(labels.Set{}))
	}
	assert.Equal(t, 1, leases)
}
