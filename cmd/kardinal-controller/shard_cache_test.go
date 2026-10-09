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
// the cluster; without sharding the Lease cache is untouched. Either way
// the Job cache keeps its run-Job label filter.
func TestShardCacheOpts(t *testing.T) {
	unsharded := shardCacheOpts(buildCacheOpts(""), "")
	require.Len(t, unsharded.ByObject, 1, "only the run-Job filter")
	opts := shardCacheOpts(buildCacheOpts(""), "b")
	require.Len(t, opts.ByObject, 2, "the run-Job filter and the shard Leases")
	for obj, by := range opts.ByObject {
		if _, ok := obj.(*batchv1.Job); ok {
			assert.True(t, by.Label.Matches(labels.Set{"kardinal.io/run-job": "x"}), "the run-Job filter is kept")
			assert.False(t, by.Label.Matches(labels.Set{}))
			continue
		}
		_, ok := obj.(*coordinationv1.Lease)
		require.True(t, ok)
		assert.True(t, by.Field.Matches(fields.Set{"metadata.name": "kardinal-shard"}))
		assert.False(t, by.Field.Matches(fields.Set{"metadata.name": "kardinal-promoter-leader-b"}))
		assert.True(t, by.Label.Matches(labels.Set{"app.kubernetes.io/managed-by": "kardinal-promoter"}))
		assert.False(t, by.Label.Matches(labels.Set{}))
	}
}
