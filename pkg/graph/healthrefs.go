// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
)

// healthRefKinds are the kinds the health ref nodes of releases before #1283
// read: Deployments, Argo CD Applications, Argo Rollouts, Flux
// Kustomizations and Flagger Canaries.
var healthRefKinds = map[string]bool{"Deployment": true, "Application": true, "Rollout": true,
	"Kustomization": true, "Canary": true}

// The labels releases before #1283 put on a Graph to say which namespaces
// its health refs read.
const (
	oldReaderLabels = "kardinal.io/reader-labels"
	oldReadsPrefix  = "reads.kardinal.io/"
)

// IsHealthRef reports whether n is a health ref node a release before #1283
// put in a Graph: a ref node with the ID health<Environment>[n] that reads
// one of the health kinds. No node references one, and nothing gates on it.
func IsHealthRef(n GraphNode) bool {
	if n.Ref == nil || len(n.ID) <= len("health") || !strings.HasPrefix(n.ID, "health") {
		return false
	}
	if c := n.ID[len("health")]; (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
		return false
	}
	kind, _ := n.Ref["kind"].(string)
	return healthRefKinds[kind]
}

// HasHealthRefs reports whether g has a health ref node (IsHealthRef).
func HasHealthRefs(g *Graph) bool {
	for _, n := range g.Spec.Nodes {
		if IsHealthRef(n) {
			return true
		}
	}
	return false
}

// RemoveHealthRefs removes the health ref nodes (IsHealthRef), and the reader
// labels, from Graph namespace/name, and reports whether it changed it. The
// reader ClusterRole they read through is gone (#1283): kro treats a
// forbidden ref read as an error, so a Graph built by an earlier release
// would not reconcile. Removing a ref node deletes nothing (kro owns no
// object of it). Update conflicts with kro's own writes are retried with a
// fresh read; a Graph that is gone is no change.
func (c *GraphClient) RemoveHealthRefs(ctx context.Context, namespace, name string) (bool, error) {
	res := c.dynamic.Resource(GraphGVR).Namespace(namespace)
	changed := false
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		u, err := res.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		nodes, _, err := unstructured.NestedSlice(u.Object, "spec", "nodes")
		if err != nil {
			return err
		}
		kept := make([]interface{}, 0, len(nodes))
		for _, raw := range nodes {
			if m, ok := raw.(map[string]interface{}); ok && IsHealthRef(nodeOf(m)) {
				continue
			}
			kept = append(kept, raw)
		}
		labels := u.GetLabels()
		relabel := hasOldReaderLabels(labels)
		for k := range labels {
			if k == oldReaderLabels || strings.HasPrefix(k, oldReadsPrefix) {
				delete(labels, k)
			}
		}
		if len(kept) == len(nodes) && !relabel {
			return nil
		}
		if err := unstructured.SetNestedSlice(u.Object, kept, "spec", "nodes"); err != nil {
			return err
		}
		u.SetLabels(labels)
		if _, err := res.Update(ctx, u, metav1.UpdateOptions{}); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("graph.RemoveHealthRefs %s/%s: %w", namespace, name, err)
	}
	return changed, nil
}

// nodeOf is the ID and ref of an unstructured Graph node.
func nodeOf(m map[string]interface{}) GraphNode {
	id, _ := m["id"].(string)
	ref, _ := m["ref"].(map[string]interface{})
	return GraphNode{ID: id, Ref: ref}
}

// healthRefsPage is how many Graphs RemoveAllHealthRefs reads at a time: a
// Graph can be large, so the pass never holds many.
const healthRefsPage = 50

// RemoveAllHealthRefs removes the health ref nodes and reader labels from
// every kardinal Graph (the kardinal.io/bundle label) in namespace, or in
// every namespace when it is "", reading them a page at a time. It returns
// how many Graphs it changed and how many it could not (RemoveHealthRefs
// failed): a Graph that fails is skipped, so one refused write does not
// leave the Graphs after it untouched. err joins those failures, or is the
// List error that stopped the pass.
func (c *GraphClient) RemoveAllHealthRefs(ctx context.Context, namespace string) (changed, failed int, err error) {
	res := c.dynamic.Resource(GraphGVR).Namespace(namespace)
	opts := metav1.ListOptions{LabelSelector: "kardinal.io/bundle", Limit: healthRefsPage}
	var errs []error
	for {
		list, lerr := res.List(ctx, opts)
		if lerr != nil {
			errs = append(errs, fmt.Errorf("graph.RemoveAllHealthRefs: list %q: %w", namespace, lerr))
			return changed, failed, errors.Join(errs...)
		}
		for i := range list.Items {
			u := &list.Items[i]
			g, gerr := FromUnstructured(u)
			if gerr != nil || (!HasHealthRefs(g) && !hasOldReaderLabels(u.GetLabels())) {
				continue
			}
			ok, rerr := c.RemoveHealthRefs(ctx, u.GetNamespace(), u.GetName())
			switch {
			case rerr != nil:
				failed++
				errs = append(errs, rerr)
			case ok:
				changed++
			}
		}
		if list.GetContinue() == "" {
			return changed, failed, errors.Join(errs...)
		}
		opts.Continue = list.GetContinue()
	}
}

// hasOldReaderLabels reports whether labels hold a reader label.
func hasOldReaderLabels(labels map[string]string) bool {
	for k := range labels {
		if k == oldReaderLabels || strings.HasPrefix(k, oldReadsPrefix) {
			return true
		}
	}
	return false
}
