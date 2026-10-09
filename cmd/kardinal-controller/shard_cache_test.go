// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	coordinationv1 "k8s.io/api/coordination/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
)

// TestShardCacheOpts (#1505 QA): a sharded controller caches only the shard
// token Leases (name kardinal-shard, kardinal's label), not every Lease in
// the cluster; without sharding the Lease cache is untouched.
func TestShardCacheOpts(t *testing.T) {
	assert.Nil(t, shardCacheOpts(buildCacheOpts(""), "").ByObject)
	opts := shardCacheOpts(buildCacheOpts(""), "b")
	require.Len(t, opts.ByObject, 1)
	for obj, by := range opts.ByObject {
		_, ok := obj.(*coordinationv1.Lease)
		require.True(t, ok)
		assert.True(t, by.Field.Matches(fields.Set{"metadata.name": "kardinal-shard"}))
		assert.False(t, by.Field.Matches(fields.Set{"metadata.name": "kardinal-promoter-leader-b"}))
		assert.True(t, by.Label.Matches(labels.Set{"app.kubernetes.io/managed-by": "kardinal-promoter"}))
		assert.False(t, by.Label.Matches(labels.Set{}))
	}
}
