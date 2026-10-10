// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline

import (
	"context"
	"fmt"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// reasonRenderedBranchConflict: an environment renders to a branch of a
// repository that an environment of another, older Pipeline renders to.
const reasonRenderedBranchConflict = "RenderedBranchConflict"

// renderedBranches are p's layout: branch environments by rendered branch,
// with each fleet's targets in its place (each renders to its own branch).
func renderedBranches(p *kardinalv1alpha1.Pipeline) map[string]string {
	out := map[string]string{}
	for _, e := range graph.ExpandedEnvironments(p) {
		if kardinalv1alpha1.RendersToBranch(p.Spec, e) {
			out[e.RenderedBranch()] = e.Name
		}
	}
	return out
}

// older reports whether a was created before b (by name when they were
// created in the same second): the older Pipeline keeps a contested
// rendered branch.
func older(a, b *kardinalv1alpha1.Pipeline) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	return a.Namespace+"/"+a.Name < b.Namespace+"/"+b.Name
}

// renderedBranchConflict returns why p may not render, or "": an
// environment of p renders to the same branch of the same repository as an
// environment of an older Pipeline (in any namespace the controller sees).
// The render marker refuses it at render time anyway; this says so on the
// Pipeline before a Bundle fails on it.
func (r *Reconciler) renderedBranchConflict(ctx context.Context, p *kardinalv1alpha1.Pipeline) (string, error) {
	mine := renderedBranches(p)
	if len(mine) == 0 {
		return "", nil
	}
	var list kardinalv1alpha1.PipelineList
	if err := r.List(ctx, &list); err != nil {
		return "", fmt.Errorf("list pipelines: %w", err)
	}
	var found []string
	for i := range list.Items {
		q := &list.Items[i]
		if q.UID == p.UID || !older(q, p) || !scm.SameRepoURL(q.Spec.Git.URL, p.Spec.Git.URL) {
			continue
		}
		for branch, theirs := range renderedBranches(q) {
			if env, ok := mine[branch]; ok {
				// Another namespace's Pipeline is not named: its name is
				// not this tenant's to read.
				who := fmt.Sprintf("environment %q of Pipeline %s/%s", theirs, q.Namespace, q.Name)
				if q.Namespace != p.Namespace {
					who = "a Pipeline in another namespace"
				}
				found = append(found, fmt.Sprintf("environment %q renders to %s, which %s renders to", env, branch, who))
			}
		}
	}
	if len(found) == 0 {
		return "", nil
	}
	return strings.Join(found, "; ") + "; set render.branch to a branch of its own", nil
}

// renderingPipelinesSharingRepo enqueues the other Pipelines of obj's
// repository that render to a branch, so a conflict clears when the older Pipeline
// changes or goes.
func (r *Reconciler) renderingPipelinesSharingRepo(ctx context.Context, obj client.Object) []ctrl.Request {
	p, ok := obj.(*kardinalv1alpha1.Pipeline)
	if !ok {
		return nil
	}
	var list kardinalv1alpha1.PipelineList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	var out []ctrl.Request
	for i := range list.Items {
		q := &list.Items[i]
		if q.UID != p.UID && len(renderedBranches(q)) > 0 && scm.SameRepoURL(q.Spec.Git.URL, p.Spec.Git.URL) {
			out = append(out, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(q)})
		}
	}
	return out
}
