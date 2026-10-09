// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// fleetResync is how often a Pipeline with a selector fleet re-reads the
// Applications its selectors match. The controller does not watch Argo CD
// Applications (the CRD may not be installed), so membership changes are seen
// within this interval.
const fleetResync = time.Minute

// defaultArgoNamespace is a fleet selector's default namespace.
const defaultArgoNamespace = "argocd"

// applicationListGVK is Argo CD's Application list kind.
var applicationListGVK = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationList"}

// clusterProfileListGVK is the cluster inventory's ClusterProfile list kind
// (SIG Multicluster, KEP-4322).
var clusterProfileListGVK = schema.GroupVersionKind{Group: "multicluster.x-k8s.io", Version: "v1alpha1", Kind: "ClusterProfileList"}

// resolveFleets returns status.fleets for p: for each fleet environment with
// an Application or ClusterProfile selector, the objects it selects as
// targets, sorted by name. A selector of kind Target picks from
// spec.fleet.targets and needs no status. An object that cannot be a
// target, or a selector that cannot be read, is reported in the entry's
// message, and the Graph refuses the fleet. It reads objects only and writes
// nothing.
func (r *Reconciler) resolveFleets(ctx context.Context, p *kardinalv1alpha1.Pipeline) []kardinalv1alpha1.FleetStatus {
	var out []kardinalv1alpha1.FleetStatus
	for _, env := range p.Spec.Environments {
		if env.Fleet == nil || !resolvedSelector(env.Fleet.Selector) {
			continue
		}
		out = append(out, r.resolveFleet(ctx, p.Namespace, env.Name, env.Fleet.Selector))
	}
	return out
}

// resolvedSelector reports whether sel selects cluster objects the
// controller resolves into status.fleets.
func resolvedSelector(sel *kardinalv1alpha1.FleetSelector) bool {
	return sel != nil && sel.Kind != kardinalv1alpha1.FleetSelectorTarget
}

func (r *Reconciler) resolveFleet(ctx context.Context, pipelineNS, env string, sel *kardinalv1alpha1.FleetSelector) kardinalv1alpha1.FleetStatus {
	st := kardinalv1alpha1.FleetStatus{Environment: env}
	kind := sel.Kind
	if kind == "" {
		kind = kardinalv1alpha1.FleetSelectorApplication
	}
	ns, gvk := sel.Namespace, applicationListGVK
	if kind == kardinalv1alpha1.FleetSelectorClusterProfile {
		gvk = clusterProfileListGVK
		if ns == "" {
			ns = pipelineNS
		}
	} else if ns == "" {
		ns = defaultArgoNamespace
	}
	ls, err := metav1.LabelSelectorAsSelector(sel.LabelSelector())
	if err != nil {
		st.Message = fmt.Sprintf("selector: %v", err)
		return st
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk)
	reader := r.Reader
	if reader == nil {
		reader = r.Client
	}
	if err := reader.List(ctx, list, client.InNamespace(ns), client.MatchingLabelsSelector{Selector: ls}); err != nil {
		switch {
		case meta.IsNoMatchError(err) && kind == kardinalv1alpha1.FleetSelectorClusterProfile:
			st.Message = "multicluster.x-k8s.io/v1alpha1 ClusterProfiles are not served: install a cluster inventory or list the targets in spec.fleet.targets"
		case meta.IsNoMatchError(err):
			st.Message = "argoproj.io/v1alpha1 Applications are not served: install Argo CD or list the targets in spec.fleet.targets"
		default:
			st.Message = fmt.Sprintf("list %ss in %s: %v", kind, ns, err)
		}
		return st
	}
	for _, obj := range list.Items {
		name := obj.GetName()
		if errs := validation.IsDNS1123Label(name); len(errs) > 0 || len(name) > 62 {
			st.Message = fmt.Sprintf("%s %s/%s cannot be a target: its name is not a DNS label of at most 62 characters", kind, ns, name)
			st.Targets = nil
			return st
		}
		t := kardinalv1alpha1.FleetTarget{Name: name}
		if kind == kardinalv1alpha1.FleetSelectorApplication {
			path := applicationPath(&obj)
			if path == "" {
				st.Message = fmt.Sprintf("Application %s/%s cannot be a target: it has no spec.source.path", ns, name)
				st.Targets = nil
				return st
			}
			t.Path = path
			t.Health = &kardinalv1alpha1.HealthConfig{Type: "argocd",
				ArgoCD: &kardinalv1alpha1.HealthTargetRef{Name: name, Namespace: ns}}
		}
		// A ClusterProfile target keeps the fleet's path (followed by the
		// cluster's name) and the fleet environment's health check.
		st.Targets = append(st.Targets, t)
	}
	sort.Slice(st.Targets, func(i, j int) bool { return st.Targets[i].Name < st.Targets[j].Name })
	return st
}

// applicationPath is an Argo CD Application's spec.source.path, or the path
// of its first spec.sources entry.
func applicationPath(app *unstructured.Unstructured) string {
	p, _, _ := unstructured.NestedString(app.Object, "spec", "source", "path")
	if p == "" {
		if sources, _, _ := unstructured.NestedSlice(app.Object, "spec", "sources"); len(sources) > 0 {
			if s, ok := sources[0].(map[string]interface{}); ok {
				p, _ = s["path"].(string)
			}
		}
	}
	return p
}

// fleetsEqual reports whether two status.fleets lists are the same.
func fleetsEqual(a, b []kardinalv1alpha1.FleetStatus) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// hasSelectorFleet reports whether p has a fleet environment with a selector.
func hasSelectorFleet(p *kardinalv1alpha1.Pipeline) bool {
	for _, env := range p.Spec.Environments {
		if env.Fleet != nil && resolvedSelector(env.Fleet.Selector) {
			return true
		}
	}
	return false
}
