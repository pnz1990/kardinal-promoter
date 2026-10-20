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
// its list changes, say that a later release refuses them. The condition is
// absent when every namespace listed agreed. It does not change Ready.
//
// Reason NotShared: every namespace was read. Reason
// PolicyNamespaceUnreadable: a namespace could not be read (an API error
// other than NotFound); it keeps the result of its last read, so an API blip
// neither raises nor clears the warning. The message names the same
// namespaces either way and does not tell a namespace that does not exist
// from one that is not labelled: the controller log does.
const (
	conditionPolicyNamespacesShared = "PolicyNamespacesShared"
	reasonPolicyNamespaceNotShared  = "NotShared"
	reasonPolicyNamespaceUnreadable = "PolicyNamespaceUnreadable"
	eventPolicyNamespaceNotShared   = "PolicyNamespaceNotShared"
)

// policyNamespaceRecheck is how soon a Pipeline that names a namespace
// needing consent is read again, agreed or not: Namespaces are not watched
// (the controller may only get them), so a label added or removed later is
// seen within it. policyNamespaceRetry is the same after a failed read.
const (
	policyNamespaceRecheck = 5 * time.Minute
	policyNamespaceRetry   = time.Minute
)

// notSharedPrefix and notSharedSuffix frame the namespaces in the
// condition's message; lastNotShared reads them back.
const (
	notSharedPrefix = "spec.policyNamespaces names namespaces that have not agreed to share their PolicyGates: "
	notSharedSuffix = ". Label each "
)

// policyNamespaceCheck is what reading p's spec.policyNamespaces found.
type policyNamespaceCheck struct {
	// read: an entry needed consent, so it is read again later.
	read bool
	// notShared were read and are not labelled (or do not exist).
	notShared []string
	// unreadable could not be read.
	unreadable []string
}

// checkPolicyNamespaces reads the namespaces of p's spec.policyNamespaces
// that need consent, with Reader (the API server: there is no Namespace
// cache), every time it is called.
func (r *Reconciler) checkPolicyNamespaces(ctx context.Context, p *kardinalv1alpha1.Pipeline) policyNamespaceCheck {
	reader := r.Reader
	if reader == nil {
		reader = r.Client
	}
	log := zerolog.Ctx(ctx)
	seen := map[string]bool{}
	var chk policyNamespaceCheck
	for _, ns := range p.Spec.PolicyNamespaces {
		if ns == "" || ns == p.Namespace || seen[ns] || graph.IsPolicyNamespace(ns, r.PolicyNamespaces) ||
			slices.Contains(r.PolicyShareableNamespaces, ns) {
			continue
		}
		seen[ns], chk.read = true, true
		var n corev1.Namespace
		err := reader.Get(ctx, client.ObjectKey{Name: ns}, &n)
		switch {
		case err == nil && n.Labels[kardinalv1alpha1.LabelPolicyShareable] == "true":
		case err == nil:
			chk.notShared = append(chk.notShared, ns)
		case apierrors.IsNotFound(err):
			// Named as one not labelled: the condition must not tell a
			// Pipeline author which namespaces exist.
			log.Info().Str("policyNamespace", ns).Msg("spec.policyNamespaces names a namespace that does not exist")
			chk.notShared = append(chk.notShared, ns)
		default:
			log.Warn().Err(err).Str("policyNamespace", ns).Msg("read a policy namespace; its last result is kept")
			chk.unreadable = append(chk.unreadable, ns)
		}
	}
	return chk
}

// lastNotShared are the namespaces conditions' PolicyNamespacesShared
// condition named.
func lastNotShared(conditions []metav1.Condition) []string {
	c := meta.FindStatusCondition(conditions, conditionPolicyNamespacesShared)
	if c == nil {
		return nil
	}
	list, ok := strings.CutPrefix(c.Message, notSharedPrefix)
	if !ok {
		return nil
	}
	list, _, _ = strings.Cut(list, notSharedSuffix)
	return strings.Split(list, ", ")
}

// policyNamespacesShared returns p's PolicyNamespacesShared condition (nil
// when every namespace of spec.policyNamespaces agreed) and how soon to read
// the namespaces again (0: none needs it). A namespace that could not be
// read counts as its last read found it: named if the stored condition named
// it.
func (r *Reconciler) policyNamespacesShared(ctx context.Context, p *kardinalv1alpha1.Pipeline) (*metav1.Condition, time.Duration) {
	chk := r.checkPolicyNamespaces(ctx, p)
	recheck := time.Duration(0)
	if chk.read {
		recheck = policyNamespaceRecheck
	}
	ns := chk.notShared
	reason := reasonPolicyNamespaceNotShared
	if len(chk.unreadable) > 0 {
		recheck, reason = policyNamespaceRetry, reasonPolicyNamespaceUnreadable
		last := lastNotShared(p.Status.Conditions)
		for _, u := range chk.unreadable {
			if slices.Contains(last, u) {
				ns = append(ns, u)
			}
		}
	}
	if len(ns) == 0 {
		return nil, recheck
	}
	sort.Strings(ns)
	return &metav1.Condition{Type: conditionPolicyNamespacesShared, Status: metav1.ConditionFalse,
		Reason: reason, ObservedGeneration: p.Generation,
		Message: notSharedPrefix + strings.Join(ns, ", ") + notSharedSuffix + fmt.Sprintf("%s=true, or list it in the controller's "+
			"--policy-shareable-namespaces (chart controller.policyShareableNamespaces). Their gates are still read; "+
			"a later release refuses them (#2286)", kardinalv1alpha1.LabelPolicyShareable)}, recheck
}

// policyNamespacesNews reports whether desired names other namespaces than
// the PolicyNamespacesShared condition of conditions (or there is none): the
// Warning Event is emitted once for each list. Only a read changes the list,
// so a failed read never emits it.
func policyNamespacesNews(conditions []metav1.Condition, desired *metav1.Condition) bool {
	if desired == nil {
		return false
	}
	c := meta.FindStatusCondition(conditions, conditionPolicyNamespacesShared)
	return c == nil || c.Message != desired.Message
}
