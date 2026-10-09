// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"strings"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// ApprovalsNodeID is the ID of the selector ref node that reads the Bundle's
// Approval objects (kardinal approve). It is added only to Graphs with an
// approval gate. It starts with an uppercase letter, which CELSafeSlug never
// produces, so no environment can take it.
const ApprovalsNodeID = "Approvals"

// needsApprovals reports whether a gate instance made from gate reads the
// Approvals: it has spec.approval, or its expression uses approvals.*.
func needsApprovals(gate kardinalv1alpha1.PolicyGate) bool {
	return gate.Spec.Approval != nil || strings.Contains(gate.Spec.Expression, "approvals.")
}

// anyNeedsApprovals reports whether any gate of the Graph reads the Approvals.
func anyNeedsApprovals(gatesByEnv map[string][]kardinalv1alpha1.PolicyGate) bool {
	for _, gates := range gatesByEnv {
		for _, g := range gates {
			if needsApprovals(g) {
				return true
			}
		}
	}
	return false
}

// approvalsRefNode is a selector ref on the Approvals labelled with the
// Bundle, in the Bundle's namespace. kro lists them on every walk and watches
// them, so a new or deleted Approval re-renders the gate instances that copy
// them (gateCollections.approvalCollectionNode). An empty list is a valid, ready collection
// (executor/simple.go applyRefCollection). The Graph ServiceAccount needs
// list and watch on approvals (chart graph-rbac.yaml).
func approvalsRefNode(bundle *kardinalv1alpha1.Bundle) GraphNode {
	return GraphNode{
		ID: ApprovalsNodeID,
		Ref: map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1",
			"kind":       "Approval",
			"metadata": map[string]interface{}{
				"namespace": bundle.Namespace,
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"kardinal.io/bundle": bundle.Name,
					},
				},
			},
		},
	}
}
