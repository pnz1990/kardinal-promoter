// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// fleetResync is how often a Pipeline with a selector fleet re-reads the
// Applications its selectors match. The controller does not watch Argo CD
// Applications (the CRD may not be installed), so membership changes are seen
// within this interval.
const fleetResync = time.Minute

// maxFleetTargets is the most targets a selector may resolve to, as many as
// spec.fleet.targets may list.
const maxFleetTargets = 500

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
// spec.fleet.targets and needs no status. A selected object that cannot be a
// target is skipped and named in the entry's message; the others are the
// targets. A selector that cannot be read (an API error, the CRD not served,
// a namespace the controller does not read) keeps the last targets it
// resolved to, with the reason in the message, so a Bundle in flight goes on
// with them; a fleet that never resolved has none, and the Graph refuses it.
// It reads objects only and writes nothing.
func (r *Reconciler) resolveFleets(ctx context.Context, p *kardinalv1alpha1.Pipeline) []kardinalv1alpha1.FleetStatus {
	var out []kardinalv1alpha1.FleetStatus
	for _, env := range p.Spec.Environments {
		if env.Fleet == nil || !resolvedSelector(env.Fleet.Selector) {
			continue
		}
		st, ok := r.resolveFleet(ctx, p, env.Name, env.Fleet.Selector)
		if !ok {
			// Keep the last good membership.
			for _, prev := range p.Status.Fleets {
				if prev.Environment == env.Name {
					st.Targets = prev.Targets
				}
			}
		}
		out = append(out, st)
	}
	return out
}

// resolvedSelector reports whether sel selects cluster objects the
// controller resolves into status.fleets.
func resolvedSelector(sel *kardinalv1alpha1.FleetSelector) bool {
	return sel != nil && sel.Kind != kardinalv1alpha1.FleetSelectorTarget
}

// maxSkippedNamed is how many skipped objects a fleet message names.
const maxSkippedNamed = 5

// resolveFleet resolves one selector. ok is false when the selector could
// not be read at all: st then has only the message, and the caller keeps
// the last targets.
func (r *Reconciler) resolveFleet(ctx context.Context, p *kardinalv1alpha1.Pipeline, env string,
	sel *kardinalv1alpha1.FleetSelector) (st kardinalv1alpha1.FleetStatus, ok bool) {
	pipelineNS := p.Namespace
	st = kardinalv1alpha1.FleetStatus{Environment: env}
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
		if ns != pipelineNS && !slices.Contains(r.FleetClusterProfileNamespaces, ns) {
			st.Message = fmt.Sprintf("selector.namespace %q: ClusterProfiles are read only from the Pipeline's namespace "+
				"and the controller's fleets.clusterProfileNamespaces", ns)
			return st, false
		}
	} else {
		allowed := r.FleetApplicationNamespaces
		if len(allowed) == 0 {
			allowed = []string{defaultArgoNamespace}
		}
		if ns == "" {
			ns = allowed[0]
		}
		if !slices.Contains(allowed, ns) {
			st.Message = fmt.Sprintf("selector.namespace %q: Applications are read only from the controller's "+
				"fleets.applicationNamespaces (%s)", ns, strings.Join(allowed, ", "))
			return st, false
		}
	}
	ls, err := metav1.LabelSelectorAsSelector(sel.LabelSelector())
	if err != nil {
		st.Message = fmt.Sprintf("selector: %v", err)
		return st, false
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk)
	reader := r.Reader
	if reader == nil {
		reader = r.Client
	}
	// One more than the cap: past it the fleet is refused without saying
	// how many objects the namespace holds.
	if err := reader.List(ctx, list, client.InNamespace(ns), client.MatchingLabelsSelector{Selector: ls},
		client.Limit(maxFleetTargets+1)); err != nil {
		switch {
		case meta.IsNoMatchError(err) && kind == kardinalv1alpha1.FleetSelectorClusterProfile:
			st.Message = "multicluster.x-k8s.io/v1alpha1 ClusterProfiles are not served: install a cluster inventory or list the targets in spec.fleet.targets"
		case meta.IsNoMatchError(err):
			st.Message = "argoproj.io/v1alpha1 Applications are not served: install Argo CD or list the targets in spec.fleet.targets"
		default:
			st.Message = fmt.Sprintf("list %ss in %s: %v", kind, ns, err)
		}
		return st, false
	}
	if len(list.Items) > maxFleetTargets || list.GetContinue() != "" {
		st.Message = fmt.Sprintf("the selector matches more than %d %ss; narrow it", maxFleetTargets, kind)
		return st, false
	}
	var skipped []string
	for _, obj := range list.Items {
		name := obj.GetName()
		if errs := validation.IsDNS1123Label(name); len(errs) > 0 || len(name) > 62 {
			skipped = append(skipped, fmt.Sprintf("%s %s (its name is not a DNS label of at most 62 characters)", kind, name))
			continue
		}
		t := kardinalv1alpha1.FleetTarget{Name: name}
		if kind == kardinalv1alpha1.FleetSelectorApplication {
			repoURL, path := applicationSource(&obj)
			if path == "" {
				skipped = append(skipped, fmt.Sprintf("Application %s (no spec.source.path)", name))
				continue
			}
			// kardinal writes the target's path in the Pipeline's
			// repository: an Application that deploys from another one
			// would never see the change. Its repository is not named.
			if !sameRepository(repoURL, p.Spec.Git.URL) {
				skipped = append(skipped, fmt.Sprintf("Application %s (it deploys from another repository than spec.git.url)", name))
				continue
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
	if len(skipped) > 0 {
		sort.Strings(skipped)
		named := skipped
		if len(named) > maxSkippedNamed {
			named = append(named[:maxSkippedNamed:maxSkippedNamed], fmt.Sprintf("and %d more", len(skipped)-maxSkippedNamed))
		}
		st.Message = "not targets: " + strings.Join(named, "; ")
	}
	return st, true
}

// applicationSource is an Argo CD Application's spec.source repoURL and
// path, or those of its first spec.sources entry with a path.
func applicationSource(app *unstructured.Unstructured) (repoURL, path string) {
	path, _, _ = unstructured.NestedString(app.Object, "spec", "source", "path")
	if path != "" {
		repoURL, _, _ = unstructured.NestedString(app.Object, "spec", "source", "repoURL")
		return repoURL, path
	}
	sources, _, _ := unstructured.NestedSlice(app.Object, "spec", "sources")
	for _, src := range sources {
		if s, ok := src.(map[string]interface{}); ok {
			if p, _ := s["path"].(string); p != "" {
				r, _ := s["repoURL"].(string)
				return r, p
			}
		}
	}
	return "", ""
}

// sameRepository reports whether two git remote URLs name the same
// repository on the same host (scm.RepoIdentity), whatever their scheme,
// user or ".git" suffix.
func sameRepository(a, b string) bool {
	ha, ra, errA := scm.RepoIdentity(a)
	hb, rb, errB := scm.RepoIdentity(b)
	return errA == nil && errB == nil && strings.EqualFold(ha, hb) && strings.EqualFold(ra, rb)
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
