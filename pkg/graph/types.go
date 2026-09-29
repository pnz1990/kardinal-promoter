// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GraphGVK is the GroupVersionKind for the kro Graph resource
// (kro v0.10.0+, feature gate GraphKind). Graph is a kro CRD — we interact
// with it via the dynamic client, not via generated types.
// See: https://kro.run/next/docs/concepts/graph/overview/
var GraphGVK = schema.GroupVersionKind{
	Group:   "kro.run",
	Version: "v1alpha1",
	Kind:    "Graph",
}

// GraphGVR is the GroupVersionResource for the kro Graph resource.
// Used with the dynamic client for CRUD operations.
var GraphGVR = schema.GroupVersionResource{
	Group:    "kro.run",
	Version:  "v1alpha1",
	Resource: "graphs",
}

// GraphSpec is a minimal Go representation of the kro Graph spec.
// Used for constructing and reading Graph objects via the dynamic client.
// Fields map to kro's api/v1alpha1 GraphSpec.
type GraphSpec struct {
	// Nodes is the list of resource nodes in the Graph. kro derives the
	// execution order from the CEL references between nodes.
	Nodes []GraphNode `json:"nodes,omitempty"`

	// ServiceAccountName is the ServiceAccount (in the Graph's namespace) that
	// kro impersonates when it reads and applies the Graph's resources.
	// Empty means the namespace's "default" ServiceAccount.
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
}

// GraphNode represents one node in the kro Graph.
//
// Exactly one of Template or Ref is set:
//
//	Template = kro creates and owns the object (server-side apply).
//	Ref      = kro reads an existing object (metadata.name) or a collection
//	           (metadata.selector) into scope without owning it.
//
// ReadyWhen feeds the Graph's Ready condition only. On a standalone Graph it
// does NOT hold back dependent nodes, so kardinal gates dependents with
// resolvability expressions instead (see resolvableWhen in builder.go and
// docs/design/16-graph-capability-ledger.md, gap G1).
type GraphNode struct {
	// ID is the unique node identifier within the Graph. Must match
	// ^[A-Za-z][A-Za-z0-9]*$ because other nodes reference it in CEL.
	ID string `json:"id"`

	// Template is the resource body for a node kro creates and owns.
	// Stored as a map to allow arbitrary Kubernetes resource shapes.
	Template map[string]interface{} `json:"template,omitempty"`

	// Ref identifies an existing object or collection:
	//
	//	{apiVersion, kind, metadata: {name | selector, namespace}}
	Ref map[string]interface{} `json:"ref,omitempty"`

	// ReadyWhen holds CEL expressions over the node itself (or "each" for a
	// forEach collection). They feed the Graph's Ready condition and the UI.
	ReadyWhen []string `json:"readyWhen,omitempty"`

	// IncludeWhen holds CEL expressions that conditionally include this node.
	// When any expression is false the node AND every node that depends on it
	// are excluded (and pruned if previously applied).
	IncludeWhen []string `json:"includeWhen,omitempty"`

	// ForEach expands the node into a collection. Each entry holds exactly one
	// iterator name mapped to a CEL expression that yields a list, for example
	// {"region": "${[\"us-east-1\",\"eu-west-1\"]}"}.
	ForEach []map[string]string `json:"forEach,omitempty"`
}

// GraphStatus is a minimal representation of the kro Graph status.
type GraphStatus struct {
	// Conditions holds Graph-level status conditions. kro emits:
	//   "Accepted"           — spec compiled (reason "Compiled") or rejected ("InvalidGraph").
	//   "ResourcesConverged" — every included node applied and ready.
	//   "Ready"              — Accepted and ResourcesConverged are both True.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// AppliedServiceAccount is the identity kro impersonated on the last apply.
	AppliedServiceAccount string `json:"appliedServiceAccount,omitempty"`
}

// Graph is the in-memory representation of a kro Graph resource.
// Not a registered Kubernetes type — used only for marshal/unmarshal
// when calling the dynamic client.
type Graph struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              GraphSpec   `json:"spec,omitempty"`
	Status            GraphStatus `json:"status,omitempty"`
}
