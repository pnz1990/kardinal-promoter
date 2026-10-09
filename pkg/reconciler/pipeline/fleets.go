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

// resolveFleets returns status.fleets for p: for each fleet environment with
// a selector, the Argo CD Applications it selects as targets, sorted by name.
// An Application becomes a target named after it, with its
// spec.source.path (or its first spec.sources path) and argocd health on
// it. A selector that cannot be read, or an Application name that cannot be
// a target, is reported in the entry's message, and the Graph refuses the
// fleet. It reads objects only and writes nothing.
func (r *Reconciler) resolveFleets(ctx context.Context, p *kardinalv1alpha1.Pipeline) []kardinalv1alpha1.FleetStatus {
	var out []kardinalv1alpha1.FleetStatus
	for _, env := range p.Spec.Environments {
		if env.Fleet == nil || env.Fleet.Selector == nil {
			continue
		}
		out = append(out, r.resolveFleet(ctx, env.Name, env.Fleet.Selector))
	}
	return out
}

func (r *Reconciler) resolveFleet(ctx context.Context, env string, sel *kardinalv1alpha1.FleetSelector) kardinalv1alpha1.FleetStatus {
	st := kardinalv1alpha1.FleetStatus{Environment: env}
	ns := sel.Namespace
	if ns == "" {
		ns = defaultArgoNamespace
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(applicationListGVK)
	reader := r.Reader
	if reader == nil {
		reader = r.Client
	}
	if err := reader.List(ctx, list, client.InNamespace(ns), client.MatchingLabels(sel.MatchLabels)); err != nil {
		if meta.IsNoMatchError(err) {
			st.Message = "argoproj.io/v1alpha1 Applications are not served: install Argo CD or list the targets in spec.fleet.targets"
		} else {
			st.Message = fmt.Sprintf("list Argo CD Applications in %s: %v", ns, err)
		}
		return st
	}
	for _, app := range list.Items {
		name := app.GetName()
		if errs := validation.IsDNS1123Label(name); len(errs) > 0 || len(name) > 62 {
			st.Message = fmt.Sprintf("Application %s/%s cannot be a target: its name is not a DNS label of at most 62 characters", ns, name)
			st.Targets = nil
			return st
		}
		p, _, _ := unstructured.NestedString(app.Object, "spec", "source", "path")
		if p == "" {
			if sources, _, _ := unstructured.NestedSlice(app.Object, "spec", "sources"); len(sources) > 0 {
				if s, ok := sources[0].(map[string]interface{}); ok {
					p, _ = s["path"].(string)
				}
			}
		}
		if p == "" {
			st.Message = fmt.Sprintf("Application %s/%s cannot be a target: it has no spec.source.path", ns, name)
			st.Targets = nil
			return st
		}
		st.Targets = append(st.Targets, kardinalv1alpha1.FleetTarget{
			Name: name, Path: p,
			Health: &kardinalv1alpha1.HealthConfig{Type: "argocd",
				ArgoCD: &kardinalv1alpha1.HealthTargetRef{Name: name, Namespace: ns}},
		})
	}
	sort.Slice(st.Targets, func(i, j int) bool { return st.Targets[i].Name < st.Targets[j].Name })
	return st
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
		if env.Fleet != nil && env.Fleet.Selector != nil {
			return true
		}
	}
	return false
}
