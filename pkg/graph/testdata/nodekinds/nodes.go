// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package graph is TestNodeKinds_Fixture's package: one node of each form
// nodeKinds must find, and of each it must report.
package graph

// GraphNode is pkg/graph's, cut to its ID.
type GraphNode struct {
	ID string
}

const (
	// NodeUsed is a node constant a GraphNode uses: a kind.
	NodeUsed = "Used"
	// NodeUnused is a node constant no GraphNode uses: the floor reports it.
	NodeUnused = "Unused"
)

// nodes builds a node of NodeUsed, and one whose type is elided.
func nodes() []GraphNode {
	return []GraphNode{{ID: NodeUsed}, {ID: "Elided"}}
}

// idFor is a node ID with an error.
func idFor() (string, error) { return "TwoValue", nil }

// twoValue sets a node's ID to one of a call's two results: nodeKinds
// cannot follow it, and reports it.
func twoValue() (GraphNode, error) {
	var n GraphNode
	var err error
	n.ID, err = idFor()
	return n, err
}

// declared builds a node whose ID is declared as one of a call's two
// results: reported.
func declared() GraphNode {
	var id, _ = idFor()
	return GraphNode{ID: id}
}

// ranged builds a node of each ID of a range: reported.
func ranged() []GraphNode {
	var out []GraphNode
	for _, id := range []string{"A", "B"} {
		out = append(out, GraphNode{ID: id})
	}
	return out
}

// suffixed changes a node's ID by +=: reported.
func suffixed() GraphNode {
	n := GraphNode{ID: NodeUsed}
	n.ID += "Suffix"
	return n
}
