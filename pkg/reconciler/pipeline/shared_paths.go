// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// conditionPathConflict is True on a Pipeline when another Pipeline writes
// the same repository and branch at an overlapping environment path (the same
// directory, or one inside the other). Pipelines that share a branch are safe
// only on separate paths: git-push replays each promotion's files onto the
// other writers' commits, and a promotion that changed the same files as
// another is redone from a fresh clone, so the later writer's version of a
// shared path wins. The condition is absent when the paths are separate.
const conditionPathConflict = "PathConflict"

const reasonOverlappingPath = "OverlappingPath"

// repoKey identifies the repository and branch a Pipeline writes:
// the URL without a trailing ".git" or "/", lowercased, and the branch
// (main when empty).
func repoKey(p *kardinalv1alpha1.Pipeline) string {
	u := strings.ToLower(strings.TrimSpace(p.Spec.Git.URL))
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	b := p.Spec.Git.Branch
	if b == "" {
		b = "main"
	}
	return u + "#" + b
}

// envPaths returns the cleaned environment paths of p, by environment name.
func envPaths(p *kardinalv1alpha1.Pipeline) map[string]string {
	out := make(map[string]string, len(p.Spec.Environments))
	for _, e := range p.Spec.Environments {
		ep := e.Path
		if ep == "" {
			ep = "environments/" + e.Name
		}
		out[e.Name] = path.Clean(strings.TrimPrefix(ep, "./"))
	}
	return out
}

// overlaps reports whether directory a is b or contains it, or the reverse.
func overlaps(a, b string) bool {
	if a == b || a == "." || b == "." {
		return true
	}
	return strings.HasPrefix(b, a+"/") || strings.HasPrefix(a, b+"/")
}

// pathConflict returns the PathConflict condition for p given every
// Pipeline the controller sees, or nil when p shares no path. Only the
// environments that push to the base branch or open PRs against it matter,
// which is all of them except update.strategy argocd.
func pathConflict(p *kardinalv1alpha1.Pipeline, all []kardinalv1alpha1.Pipeline) *metav1.Condition {
	key := repoKey(p)
	mine := writtenPaths(p)
	var found []string
	for i := range all {
		o := &all[i]
		if o.Namespace == p.Namespace && o.Name == p.Name || repoKey(o) != key {
			continue
		}
		for env, mp := range mine {
			for oenv, op := range writtenPaths(o) {
				if overlaps(mp, op) {
					found = append(found, fmt.Sprintf("environment %s (%s) and %s/%s environment %s (%s)",
						env, mp, o.Namespace, o.Name, oenv, op))
				}
			}
		}
	}
	if len(found) == 0 {
		return nil
	}
	sort.Strings(found)
	if len(found) > 5 {
		found = append(found[:5], fmt.Sprintf("and %d more", len(found)-5))
	}
	return &metav1.Condition{
		Type:               conditionPathConflict,
		Status:             metav1.ConditionTrue,
		Reason:             reasonOverlappingPath,
		ObservedGeneration: p.Generation,
		Message: "another Pipeline writes the same repository and branch at an overlapping path, so their " +
			"promotions overwrite each other's files and their PRs can conflict: " + strings.Join(found, "; ") +
			"; give each Pipeline its own environment paths",
	}
}

// writtenPaths are the paths of p's environments that write git.
func writtenPaths(p *kardinalv1alpha1.Pipeline) map[string]string {
	paths := envPaths(p)
	for _, e := range p.Spec.Environments {
		if e.Update.Strategy == "argocd" {
			delete(paths, e.Name)
		}
	}
	return paths
}

// pipelinesSharingRepo maps a Pipeline event to every Pipeline that writes
// the same repository and branch, so a new or edited Pipeline updates the
// PathConflict condition of the others.
func (r *Reconciler) pipelinesSharingRepo(ctx context.Context, obj client.Object) []ctrl.Request {
	p, ok := obj.(*kardinalv1alpha1.Pipeline)
	if !ok {
		return nil
	}
	var list kardinalv1alpha1.PipelineList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	key := repoKey(p)
	var reqs []ctrl.Request
	for i := range list.Items {
		o := &list.Items[i]
		if repoKey(o) == key && (o.Namespace != p.Namespace || o.Name != p.Name) {
			reqs = append(reqs, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(o)})
		}
	}
	return reqs
}
