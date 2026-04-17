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
//
// API group updated to experimental.kro.run per krocodile commit 48224264
// (2026-04-10): Graph CRD moved from kro.run to experimental.kro.run to
// eliminate hard CRD conflicts with upstream kro.
var GraphGVK = schema.GroupVersionKind{
	Group:   "experimental.kro.run",
	Version: "v1alpha1",
	Kind:    "Graph",
}

// GraphGVR is the GroupVersionResource for the kro Graph resource.
// Used with the dynamic client for CRUD operations.
var GraphGVR = schema.GroupVersionResource{
	Group:    "experimental.kro.run",
	Version:  "v1alpha1",
	Resource: "graphs",
}

// GraphSpec is a minimal Go representation of the kro Graph spec.
// Used for constructing and reading Graph objects via the dynamic client.
// Fields map to the krocodile/experimental Graph CRD schema.
// See: https://github.com/ellistarn/kro/tree/krocodile/experimental
type GraphSpec struct {
	// Nodes is the ordered list of resource nodes in the Graph.
	// (Renamed from Resources in krocodile April 2026.)
	Nodes []GraphNode `json:"nodes,omitempty"`
}

// DeepCopyInto copies all fields of GraphSpec into out.
func (in *GraphSpec) DeepCopyInto(out *GraphSpec) {
	if in.Nodes != nil {
		out.Nodes = make([]GraphNode, len(in.Nodes))
		for i := range in.Nodes {
			in.Nodes[i].DeepCopyInto(&out.Nodes[i])
		}
	}
}

// DeepCopy returns a deep copy of GraphSpec.
func (in *GraphSpec) DeepCopy() *GraphSpec {
	if in == nil {
		return nil
	}
	out := new(GraphSpec)
	in.DeepCopyInto(out)
	return out
}

// GraphNode represents one resource node in the kro Graph.
//
// ReadyWhen vs PropagateWhen:
//
//	ReadyWhen   = health signal only. Feeds the Graph's aggregated Ready condition
//	              and the UI. Does NOT block downstream nodes.
//	PropagateWhen = data-flow gate. When unsatisfied, downstream nodes do not receive
//	                updated data and are not re-evaluated. This is the field that
//	                gates PolicyGate blocking. See design-v2.1.md §3.5.
//
// For PromotionStep nodes: use PropagateWhen to block downstream when not Verified.
//
//	propagateWhen: ["${dev.status.state == \"Verified\"}"]
//
// For PolicyGate nodes: use PropagateWhen to block downstream when gate not ready.
//
//	propagateWhen: ["${noWeekendDeploys.status.ready == true}"]
//
// ReadyWhen on PolicyGate nodes is only the UI health signal (shows pass/fail colour).
// The actual blocking is done by PropagateWhen on the upstream PolicyGate node.
type GraphNode struct {
	// ID is the unique node identifier within the Graph.
	ID string `json:"id"`

	// Template is the raw resource template for this node.
	// Stored as a map to allow arbitrary Kubernetes resource shapes.
	// Template is the node body for an Own node (Graph creates the resource).
	// Serialized as "template:" key — krocodile ≥ 05db829 (explicit-keyword schema).
	// Previous name for this concept: "template" was also used for Watch/WatchKind,
	// but those now use Ref/Watch fields.
	Template map[string]interface{} `json:"template,omitempty"`

	// Ref is the identity for a Ref node (dereference a single named object into scope).
	// Serialized as "ref:" key — krocodile ≥ 05db829 (explicit-keyword schema).
	// Replaces the old "template: {apiVersion, kind, metadata.name}" identity-only form.
	Ref map[string]interface{} `json:"ref,omitempty"`

	// Watch is the selector for a Watch node (observe a collection by selector).
	// Serialized as "watch:" key — krocodile ≥ 05db829 (explicit-keyword schema).
	// Replaces the old "template: {apiVersion, kind, selector}" WatchKind form.
	Watch map[string]interface{} `json:"watch,omitempty"`

	// ReadyWhen holds CEL expressions that are a health signal only.
	// They feed the Graph's aggregated Ready condition and the UI.
	// They do NOT block downstream node execution.
	ReadyWhen []string `json:"readyWhen,omitempty"`

	// PropagateWhen holds CEL expressions that gate data flow to dependents.
	// When any expression is unsatisfied, downstream nodes do not receive
	// updated data and are not re-evaluated. This is the correct mechanism
	// for PolicyGate blocking. See design-v2.1.md §3.5.
	PropagateWhen []string `json:"propagateWhen,omitempty"`

	// IncludeWhen holds CEL expressions that conditionally include this node.
	// When any expression is false, the node is excluded from the DAG.
	IncludeWhen []string `json:"includeWhen,omitempty"`

	// ForEach is a CEL expression that stamps out one node per collection item.
	ForEach string `json:"forEach,omitempty"`
}

// DeepCopyInto copies all fields of GraphNode into out.
func (in *GraphNode) DeepCopyInto(out *GraphNode) {
	out.ID = in.ID
	out.ForEach = in.ForEach
	if in.Template != nil {
		out.Template = make(map[string]interface{}, len(in.Template))
		for k, v := range in.Template {
			out.Template[k] = v
		}
	}
	if in.Ref != nil {
		out.Ref = make(map[string]interface{}, len(in.Ref))
		for k, v := range in.Ref {
			out.Ref[k] = v
		}
	}
	if in.Watch != nil {
		out.Watch = make(map[string]interface{}, len(in.Watch))
		for k, v := range in.Watch {
			out.Watch[k] = v
		}
	}
	if in.ReadyWhen != nil {
		in, out := &in.ReadyWhen, &out.ReadyWhen
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	if in.PropagateWhen != nil {
		in, out := &in.PropagateWhen, &out.PropagateWhen
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	if in.IncludeWhen != nil {
		in, out := &in.IncludeWhen, &out.IncludeWhen
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
}

// DeepCopy returns a deep copy of GraphNode.
func (in *GraphNode) DeepCopy() *GraphNode {
	if in == nil {
		return nil
	}
	out := new(GraphNode)
	in.DeepCopyInto(out)
	return out
}

// GraphStatus is a minimal representation of the kro Graph status.
type GraphStatus struct {
	// Phase is the overall Graph execution phase.
	Phase string `json:"phase,omitempty"`

	// Conditions holds Graph-level status conditions.
	// krocodile e082fe9+ emits two condition types:
	//   "Compiled" — graph spec parsed and CEL programs compiled (was "Accepted" ≤9c18aa34).
	//   "Ready"    — all nodes have converged.
	// Do not check for "Accepted"; use "Compiled" for spec validation status.
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
