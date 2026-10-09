// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"encoding/json"
	"fmt"
)

// MaxGraphBytes is the largest estimated Graph object kardinal creates.
//
// A Graph is one etcd object, and etcd refuses a write over 1.5 MiB
// (1,572,864 bytes): "etcdserver: request is too large". The object holds the
// spec kardinal writes and the status kro writes, whose
// status.managedResources grows by one entry per object the Graph applies.
// The API server drops metadata.managedFields when a write would not fit, so
// those do not count. The guard leaves room for the status and for kro's
// write-ahead, which briefly holds the previous and next inventory. See
// docs/design/16-graph-capability-ledger.md gap G10.
const MaxGraphBytes = 1_200_000

// managedResourceBytes is the measured size of one status.managedResources
// entry on a kardinal Graph (kro v0.10.0-rc.0, kind, 1,050 entries: 242
// bytes each), rounded up. It assumes short names: an entry holds the node
// ID, kind, namespace, name and UID, so 40-50 character node IDs and object
// names make an entry about 450 bytes.
const managedResourceBytes = 260

// EstimateSize returns the estimated size in bytes of g once kro has applied
// every template node: the JSON of the object kardinal writes plus one
// status.managedResources entry per template node.
func EstimateSize(g *Graph) (int, error) {
	if g == nil {
		return 0, nil
	}
	data, err := json.Marshal(g)
	if err != nil {
		return 0, fmt.Errorf("marshal graph: %w", err)
	}
	// TODO(#1480 follow-up): a forEach node creates one object per item, so
	// count the items once the builder emits collections.
	// TestEstimateSize_NoCollections fails until then.
	templates := 0
	for _, n := range g.Spec.Nodes {
		if n.Template != nil {
			templates++
		}
	}
	return len(data) + templates*managedResourceBytes, nil
}

// CheckSize refuses a Graph whose estimated size exceeds MaxGraphBytes. The
// error wraps ErrInvalid: the size depends only on the Pipeline, the Bundle
// and the gates, so a retry fails the same way.
func CheckSize(g *Graph) error {
	size, err := EstimateSize(g)
	if err != nil {
		return fmt.Errorf("graph size: %w", err)
	}
	if size <= MaxGraphBytes {
		return nil
	}
	return asInvalid(fmt.Errorf(
		"graph size: the Graph for this Bundle would be about %d bytes with %d nodes, over kardinal's limit of %d bytes "+
			"(etcd stores at most 1.5 MiB per object); reduce the environments, PolicyGates or health checks per environment, "+
			"or split the Pipeline; a new Bundle or a Pipeline edit retries", size, len(g.Spec.Nodes), MaxGraphBytes))
}
