// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GraphGVK is the GroupVersionKind for the kro Graph resource.
// Graph is a kro CRD — we interact with it via the dynamic client,
// not via generated types. This stub provides the GVK reference.
var GraphGVK = schema.GroupVersionKind{
	Group:   "kro.run",
	Version: "v1alpha1",
	Kind:    "Graph",
}

// GraphSpec is a minimal Go representation of the kro Graph spec.
// Used for constructing and reading Graph objects via the dynamic client.
// Fields map to the krocodile/experimental Graph CRD schema.
// See: https://github.com/ellistarn/kro/tree/krocodile/experimental
type GraphSpec struct {
	// Resources is the ordered list of resource nodes in the Graph.
	Resources []GraphNode `json:"resources,omitempty"`
}

// GraphNode represents one resource node in the kro Graph.
type GraphNode struct {
	// ID is the unique node identifier within the Graph.
	ID string `json:"id"`

	// Template is the raw resource template for this node.
	// Stored as a map to allow arbitrary Kubernetes resource shapes.
	Template map[string]interface{} `json:"template,omitempty"`

	// ReadyWhen holds CEL expressions that must all evaluate to true
	// before the Graph advances past this node.
	ReadyWhen []string `json:"readyWhen,omitempty"`
}

// GraphStatus is a minimal representation of the kro Graph status.
type GraphStatus struct {
	// Phase is the overall Graph execution phase.
	Phase string `json:"phase,omitempty"`

	// Conditions holds Graph-level status conditions.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
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
