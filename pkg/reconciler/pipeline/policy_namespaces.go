// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// conditionPolicyNamespacesShared is False on a Pipeline whose
// spec.policyNamespaces names a namespace that has not agreed to have its
// PolicyGates read by another namespace's Pipelines (#2286): the translator
// copies their templates into the Pipeline's namespace, where its team reads
// them. A namespace agrees with the label kardinal.io/policy-shareable=true,
// or by being listed in --policy-shareable-namespaces; the org policy
// namespaces, which every Pipeline reads, and the Pipeline's own need
// nothing. The gates are still read: the condition, and a Warning Event when
// it appears or changes, say that a later release refuses them. The
// condition is absent when every namespace listed agreed. It does not change
// Ready.
const (
	conditionPolicyNamespacesShared = "PolicyNamespacesShared"
	reasonPolicyNamespaceNotShared  = "NotShared"
	eventPolicyNamespaceNotShared   = "PolicyNamespaceNotShared"
)

// policyNamespaceRecheck is how soon a Pipeline that names a namespace not
// shared is checked again. Namespaces are not watched (the controller may
// only get them), so a label added later is seen within it.
const policyNamespaceRecheck = 5 * time.Minute

// notSharedPolicyNamespaces returns the namespaces of p's
// spec.policyNamespaces that have not agreed to be read, sorted. One that
// does not exist or cannot be read says so; it has not agreed either.
func (r *Reconciler) notSharedPolicyNamespaces(ctx context.Context, p *kardinalv1alpha1.Pipeline) []string {
	reader := r.Reader
	if reader == nil {
		reader = r.Client
	}
	seen := map[string]bool{}
	var out []string
	for _, ns := range p.Spec.PolicyNamespaces {
		if ns == "" || ns == p.Namespace || seen[ns] || graph.IsPolicyNamespace(ns, r.PolicyNamespaces) ||
			slices.Contains(r.PolicyShareableNamespaces, ns) {
			continue
		}
		seen[ns] = true
		var n corev1.Namespace
		err := reader.Get(ctx, client.ObjectKey{Name: ns}, &n)
		switch {
		case err == nil && n.Labels[kardinalv1alpha1.LabelPolicyShareable] == "true":
		case err == nil:
			out = append(out, ns)
		case apierrors.IsNotFound(err):
			out = append(out, ns+" (not found)")
		default:
			zerolog.Ctx(ctx).Debug().Err(err).Str("policyNamespace", ns).Msg("read a policy namespace")
			out = append(out, ns+" (cannot be read)")
		}
	}
	sort.Strings(out)
	return out
}

// policyNamespacesShared returns p's PolicyNamespacesShared condition, or nil
// when every namespace of spec.policyNamespaces agreed to be read.
func (r *Reconciler) policyNamespacesShared(ctx context.Context, p *kardinalv1alpha1.Pipeline) *metav1.Condition {
	ns := r.notSharedPolicyNamespaces(ctx, p)
	if len(ns) == 0 {
		return nil
	}
	return &metav1.Condition{Type: conditionPolicyNamespacesShared, Status: metav1.ConditionFalse,
		Reason: reasonPolicyNamespaceNotShared, ObservedGeneration: p.Generation,
		Message: fmt.Sprintf("spec.policyNamespaces names namespaces that have not agreed to share their PolicyGates: %s. "+
			"Label each %s=true, or list it in the controller's --policy-shareable-namespaces "+
			"(chart controller.policyShareableNamespaces). Their gates are still read; a later release refuses them (#2286)",
			strings.Join(ns, ", "), kardinalv1alpha1.LabelPolicyShareable)}
}

// policyNamespacesNews reports whether desired is a PolicyNamespacesShared
// condition conditions do not have yet, or one with another message: the
// Warning Event is emitted once for each.
func policyNamespacesNews(conditions []metav1.Condition, desired *metav1.Condition) bool {
	if desired == nil {
		return false
	}
	c := meta.FindStatusCondition(conditions, conditionPolicyNamespacesShared)
	return c == nil || c.Message != desired.Message
}
