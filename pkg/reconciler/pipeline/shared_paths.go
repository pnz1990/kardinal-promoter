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
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// conditionPathConflict is True on a Pipeline when two of its environments,
// or one of its environments and one of another Pipeline on the same
// repository and branch, write overlapping paths (the same directory, or one
// inside the other). Pipelines that share a branch are safe
// only on separate paths: git-push replays each promotion's files onto the
// other writers' commits, and a promotion that changed the same files as
// another is redone from a fresh clone, so the later writer's version of a
// shared path wins. The condition is absent when the paths are separate.
const conditionPathConflict = "PathConflict"

const reasonOverlappingPath = "OverlappingPath"

// repoKey identifies the repository and branch a Pipeline writes:
// normalizeRepoURL of the URL, and the branch (main when empty).
func repoKey(p *kardinalv1alpha1.Pipeline) string {
	b := p.Spec.Git.Branch
	if b == "" {
		b = "main"
	}
	return normalizeRepoURL(p.Spec.Git.URL) + "#" + b
}

// normalizeRepoURL reduces the ways to spell one repository's URL to
// host/path, lowercased: https://host/org/repo, http://, ssh://git@host/org/repo,
// ssh://git@host:22/org/repo and the scp form git@host:org/repo all give
// host/org/repo; userinfo, the port, a trailing ".git" and "/" are dropped.
func normalizeRepoURL(raw string) string {
	u := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
		host, rest, _ := strings.Cut(u, "/")
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		if h, _, ok := strings.Cut(host, ":"); ok {
			host = h
		}
		u = host + "/" + rest
	} else if host, rest, ok := strings.Cut(u, ":"); ok && !strings.Contains(host, "/") {
		// scp-like: [user@]host:path
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		u = host + "/" + strings.TrimPrefix(rest, "/")
	}
	for {
		t := strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
		if t == u {
			return u
		}
		u = t
	}
}

// cleanPath is p relative to the repository root, cleaned.
func cleanPath(p string) string {
	return path.Clean(strings.TrimPrefix(strings.TrimSpace(p), "./"))
}

// envPath is the cleaned path of environment e.
func envPath(e kardinalv1alpha1.EnvironmentSpec) string {
	if e.Path == "" {
		return "environments/" + e.Name
	}
	return cleanPath(e.Path)
}

// overlaps reports whether path a is b or contains it, or the reverse.
func overlaps(a, b string) bool {
	if a == b || a == "." || b == "." {
		return true
	}
	return strings.HasPrefix(b, a+"/") || strings.HasPrefix(a, b+"/")
}

// written is one path an environment writes.
type written struct{ env, path string }

// writtenPaths are the paths p's environments write in git: each
// environment's directory, and a Helm valuesFile outside it (helm resolves
// valuesFile from the environment path, so "../shared/values.yaml" writes
// next to it). update.strategy argocd environments write no git. A fleet
// environment writes its targets' paths, each a directory of its own.
func writtenPaths(p *kardinalv1alpha1.Pipeline) []written {
	var out []written
	targets := graph.FleetTargetSpecs(p)
	var envs []kardinalv1alpha1.EnvironmentSpec
	for _, e := range p.Spec.Environments {
		if e.Fleet != nil {
			envs = append(envs, targets[e.Name]...)
			continue
		}
		envs = append(envs, e)
	}
	for _, e := range envs {
		if e.Update.Strategy == "argocd" {
			continue
		}
		dir := envPath(e)
		out = append(out, written{e.Name, dir})
		if h := e.Update.Helm; e.Update.Strategy == "helm" && h != nil && h.ValuesFile != "" {
			if f := cleanPath(path.Join(dir, h.ValuesFile)); !overlaps(dir, f) {
				out = append(out, written{e.Name, f})
			}
		}
	}
	return out
}

// pathConflict returns the PathConflict condition for p given every
// Pipeline the controller sees, or nil when p shares no path: two of p's
// own environments, or one of p's and one of another Pipeline on the same
// repository and branch, write overlapping paths. Pipelines of p's namespace
// are named; those of other namespaces only counted, since p's status must
// not reveal what other namespaces run.
func pathConflict(p *kardinalv1alpha1.Pipeline, all []kardinalv1alpha1.Pipeline) *metav1.Condition {
	key := repoKey(p)
	mine := writtenPaths(p)
	var found []string
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			found = append(found, s)
		}
	}
	for i := range mine {
		for j := i + 1; j < len(mine); j++ {
			if mine[i].env != mine[j].env && overlaps(mine[i].path, mine[j].path) {
				add(fmt.Sprintf("environments %s (%s) and %s (%s) of this Pipeline",
					mine[i].env, mine[i].path, mine[j].env, mine[j].path))
			}
		}
	}
	elsewhere := map[string]map[string]bool{} // my env -> other-namespace Pipelines
	for i := range all {
		o := &all[i]
		if o.Namespace == p.Namespace && o.Name == p.Name || repoKey(o) != key {
			continue
		}
		for _, m := range mine {
			for _, w := range writtenPaths(o) {
				if !overlaps(m.path, w.path) {
					continue
				}
				if o.Namespace == p.Namespace {
					add(fmt.Sprintf("environment %s (%s) and Pipeline %s environment %s (%s)",
						m.env, m.path, o.Name, w.env, w.path))
					continue
				}
				k := m.env + " (" + m.path + ")"
				if elsewhere[k] == nil {
					elsewhere[k] = map[string]bool{}
				}
				elsewhere[k][o.Namespace+"/"+o.Name] = true
			}
		}
	}
	for k, others := range elsewhere {
		add(fmt.Sprintf("environment %s and %d other Pipeline(s) in other namespaces", k, len(others)))
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
		Message: "environments write overlapping paths of the same repository and branch, so their " +
			"promotions overwrite each other's files and their PRs can conflict: " + strings.Join(found, "; ") +
			"; give each environment its own path",
	}
}

// pipelinePeers enqueues, for a Pipeline that was created, deleted, or
// whose spec changed (a generation change; status writes are ignored), every
// other Pipeline on the repository and branch it writes, and on an update
// also on the one it wrote before, so moving a Pipeline to another
// repository clears the condition it caused.
func (r *Reconciler) pipelinePeers() handler.EventHandler {
	enqueue := func(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request], objs ...client.Object) {
		for _, o := range objs {
			for _, req := range r.pipelinesSharingRepo(ctx, o) {
				q.Add(req)
			}
		}
	}
	return handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, q, e.Object)
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if e.ObjectOld.GetGeneration() == e.ObjectNew.GetGeneration() {
				return
			}
			enqueue(ctx, q, e.ObjectOld, e.ObjectNew)
		},
		DeleteFunc: func(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, q, e.Object)
		},
	}
}

// pipelinesSharingRepo returns every Pipeline other than obj that writes the
// repository and branch obj writes.
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
