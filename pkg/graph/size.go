// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"encoding/json"
	"fmt"
	"regexp"
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

// MaxGraphObjects is the most objects kardinal lets one Graph create. kro's
// status.managedResources holds at most 5000 entries (kro.run_graphs CRD
// maxItems), and its write-ahead briefly holds the previous and next
// inventory, so the limit leaves room below that.
const MaxGraphObjects = 4500

// managedResourceBytes is the measured size of one status.managedResources
// entry on a kardinal Graph (kro v0.10.0-rc.0, kind, 1,050 entries: 242
// bytes each), rounded up. It assumes short names: an entry holds the node
// ID, kind, namespace, name and UID, so 40-50 character node IDs and object
// names make an entry about 450 bytes.
const managedResourceBytes = 260

// EstimateSize returns the estimated size in bytes of g once kro has applied
// every template node: the JSON of the object kardinal writes plus one
// status.managedResources entry per object the template nodes create (one
// per item for a collection over a def node's list).
func EstimateSize(g *Graph) (int, error) {
	if g == nil {
		return 0, nil
	}
	data, err := json.Marshal(g)
	if err != nil {
		return 0, fmt.Errorf("marshal graph: %w", err)
	}
	return len(data) + ObjectCount(g)*managedResourceBytes, nil
}

// admissionData maps the compact shape's admission defs to the data
// nodes they filter: a collection over an admission creates at most every
// item of the data.
var admissionData = map[string]string{
	NodePromotionMetrics:  NodeMetricCheckData,
	NodePromotionHooks:    NodeHookRunData,
	NodePromotionAnalyses: NodeAnalysisRunData,
}

// reDefList matches a forEach expression that reads a def node's list field.
var reDefList = regexp.MustCompile(`^\$\{([A-Za-z][A-Za-z0-9]*)\.([A-Za-z][A-Za-z0-9]*)\}$`)

// ObjectCount is the number of objects g's template nodes create: one per
// scalar template node, and one per item of a collection whose forEach reads
// a def node's list (the builder's collections), or one per environment for
// the compact shape's PromotionSteps. A collection over anything else counts
// as one.
func ObjectCount(g *Graph) int {
	if g == nil {
		return 0
	}
	defs := map[string]map[string]interface{}{}
	for _, n := range g.Spec.Nodes {
		if n.Def != nil {
			defs[n.ID] = n.Def
		}
	}
	count := 0
	for _, n := range g.Spec.Nodes {
		if n.Template == nil {
			continue
		}
		items := 1
		if n.ID == NodePromotionSteps {
			// The compact shape's step collection reads a computed list; at
			// most one step per environment of the DAG.
			if list, ok := defs[NodePromotionDAG]["steps"].([]interface{}); ok {
				count += len(list)
				continue
			}
		}
		if len(n.ForEach) == 1 {
			for _, expr := range n.ForEach[0] {
				if m := reDefList.FindStringSubmatch(expr); m != nil {
					src := m[1]
					if data, ok := admissionData[src]; ok {
						// A computed admission list: at most every item in
						// the data.
						src = data
					}
					if list, ok := defs[src][m[2]].([]interface{}); ok {
						items = len(list)
					}
				}
			}
		}
		count += items
	}
	return count
}

// CheckSize refuses a Graph whose estimated size exceeds MaxGraphBytes. The
// error wraps ErrInvalid: the size depends only on the Pipeline, the Bundle
// and the gates, so a retry fails the same way.
func CheckSize(g *Graph) error {
	size, err := EstimateSize(g)
	if err != nil {
		return fmt.Errorf("graph size: %w", err)
	}
	if n := ObjectCount(g); n > MaxGraphObjects {
		return asInvalid(fmt.Errorf(
			"graph size: the Graph for this Bundle would create %d objects, over kardinal's limit of %d "+
				"(kro tracks at most 5000 per Graph in status.managedResources); reduce the environments or "+
				"PolicyGates per environment, or split the Pipeline; a new Bundle or a Pipeline edit retries",
			n, MaxGraphObjects))
	}
	if size <= MaxGraphBytes {
		return nil
	}
	return asInvalid(fmt.Errorf(
		"graph size: the Graph for this Bundle would be about %d bytes with %d nodes and %d objects, over kardinal's limit of %d bytes "+
			"(etcd stores at most 1.5 MiB per object); reduce the environments, PolicyGates or health checks per environment, "+
			"or split the Pipeline; a new Bundle or a Pipeline edit retries", size, len(g.Spec.Nodes), ObjectCount(g), MaxGraphBytes))
}
