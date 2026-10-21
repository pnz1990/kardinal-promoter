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
// nothing. Those that have not are refused (v0.14): the condition, a Warning
// Event when its list changes, Ready=False (PolicyNamespaceNotShared), and
// status.policyNamespaces, from which the Bundle reconciler translates no new
// version. The condition is absent when every namespace listed agreed.
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

// ReasonPolicyNamespaceNotShared is the reason of a Pipeline's Ready=False
// while its spec.policyNamespaces names a namespace that has not agreed to
// share its PolicyGates, and of a Bundle's wait for it (#2286).
const ReasonPolicyNamespaceNotShared = "PolicyNamespaceNotShared"

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

// NotSharedPolicyNamespaces reports whether p's PolicyNamespacesShared
// condition is False, and the namespaces of its spec.policyNamespaces it
// names as not agreeing to share their PolicyGates (#2286): what kardinal
// doctor lists before an upgrade to a controller that refuses them. It reads
// the condition this package writes, so the message's format stays here.
func NotSharedPolicyNamespaces(p *kardinalv1alpha1.Pipeline) (bool, []string) {
	if st := p.Status.PolicyNamespaces; st != nil {
		return len(st.NotShared) > 0, st.NotShared
	}
	// A controller that did not write status.policyNamespaces (v0.13).
	c := meta.FindStatusCondition(p.Status.Conditions, conditionPolicyNamespacesShared)
	if c == nil || c.Status != metav1.ConditionFalse {
		return false, nil
	}
	return true, lastNotShared(p.Status.Conditions)
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
// when every namespace of spec.policyNamespaces agreed), the
// status.policyNamespaces to write, and how soon to read the namespaces
// again (0: none needs it).
//
// A namespace that could not be read keeps the result of its last read
// (named if the last result named it), while the last result is for p's
// generation: an API blip neither refuses a namespace that agreed nor lets
// one through that had not. After a spec change there is no last result to
// keep, so the status is left as it was, for an older generation, and the
// Bundle reconciler translates no new version until a read succeeds (in
// policyNamespaceRetry).
func (r *Reconciler) policyNamespacesShared(ctx context.Context, p *kardinalv1alpha1.Pipeline) (
	*metav1.Condition, *kardinalv1alpha1.PipelinePolicyNamespaces, time.Duration) {
	chk := r.checkPolicyNamespaces(ctx, p)
	recheck := time.Duration(0)
	if chk.read {
		recheck = policyNamespaceRecheck
	}
	ns := chk.notShared
	reason := reasonPolicyNamespaceNotShared
	current := true
	if len(chk.unreadable) > 0 {
		recheck, reason = policyNamespaceRetry, reasonPolicyNamespaceUnreadable
		var last []string
		last, current = lastNotSharedOf(p)
		for _, u := range chk.unreadable {
			if slices.Contains(last, u) {
				ns = append(ns, u)
			}
		}
	}
	sort.Strings(ns)
	status := p.Status.PolicyNamespaces
	if current {
		status = &kardinalv1alpha1.PipelinePolicyNamespaces{ObservedGeneration: p.Generation, NotShared: ns}
	}
	if len(ns) == 0 {
		return nil, status, recheck
	}
	return &metav1.Condition{Type: conditionPolicyNamespacesShared, Status: metav1.ConditionFalse,
		Reason: reason, ObservedGeneration: p.Generation,
		Message: notSharedPrefix + strings.Join(ns, ", ") + notSharedSuffix + fmt.Sprintf("%s=true, or list it in the controller's "+
			"--policy-shareable-namespaces (chart controller.policyShareableNamespaces), or remove it from spec.policyNamespaces. "+
			"They are refused: the Pipeline's new versions wait until each agrees or is removed, and a version already promoting "+
			"keeps the gates it started with (#2286)", kardinalv1alpha1.LabelPolicyShareable)}, status, recheck
}

// lastNotSharedOf is the namespaces p's last check named, and whether that
// check is for p's current generation: status.policyNamespaces, or, from a
// controller that did not write it (v0.13), the condition's list.
func lastNotSharedOf(p *kardinalv1alpha1.Pipeline) ([]string, bool) {
	if st := p.Status.PolicyNamespaces; st != nil {
		return st.NotShared, st.ObservedGeneration == p.Generation
	}
	c := meta.FindStatusCondition(p.Status.Conditions, conditionPolicyNamespacesShared)
	return lastNotShared(p.Status.Conditions), c != nil && c.ObservedGeneration == p.Generation
}

// notSharedReady is the Ready condition of a Pipeline whose
// spec.policyNamespaces names namespaces (ns) that have not agreed: it is
// refused, its new versions wait (#2286).
func notSharedReady(p *kardinalv1alpha1.Pipeline, ns []string) metav1.Condition {
	return metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: ReasonPolicyNamespaceNotShared,
		ObservedGeneration: p.Generation,
		Message: fmt.Sprintf("spec.policyNamespaces names %s, which has not agreed to share its PolicyGates: new versions wait "+
			"until it is labelled %s=true, listed in the controller's --policy-shareable-namespaces, or removed (#2286)",
			strings.Join(ns, ", "), kardinalv1alpha1.LabelPolicyShareable)}
}

// policyNamespacesEqual reports whether a and b are the same check.
func policyNamespacesEqual(a, b *kardinalv1alpha1.PipelinePolicyNamespaces) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ObservedGeneration == b.ObservedGeneration && slices.Equal(a.NotShared, b.NotShared)
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
