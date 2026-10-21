// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package translator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// PolicyNamespacesError refuses a new version of a Pipeline whose
// spec.policyNamespaces names a namespace that has not agreed to share its
// PolicyGates (#2286): the version waits, it does not fail, and is
// translated once the namespace agrees or the entry is removed. Unchecked:
// the Pipeline reconciler has not checked the Pipeline's current generation
// yet (status.policyNamespaces), so nothing is read before it has.
// GraphGone: the version in flight whose Graph was deleted is not built
// again, since the gates of NotShared it started with were only there.
type PolicyNamespacesError struct {
	NotShared []string
	Unchecked bool
	GraphGone bool
}

func (e *PolicyNamespacesError) Error() string {
	if e.Unchecked {
		return "spec.policyNamespaces is not checked yet for this generation of the Pipeline: waiting for the Pipeline reconciler"
	}
	if e.GraphGone {
		ns := strings.Join(e.NotShared, ", ")
		return fmt.Sprintf("the version's Graph is gone, and the gates of %s it started with were only there: it is built again "+
			"once %s agrees to share its PolicyGates (label it %s=true, or list it in the controller's --policy-shareable-namespaces) "+
			"or is removed from spec.policyNamespaces (#2286)", ns, ns, kardinalv1alpha1.LabelPolicyShareable)
	}
	return fmt.Sprintf("spec.policyNamespaces names %s, which has not agreed to share its PolicyGates: label it %s=true, "+
		"list it in the controller's --policy-shareable-namespaces, or remove it from spec.policyNamespaces (#2286)",
		strings.Join(e.NotShared, ", "), kardinalv1alpha1.LabelPolicyShareable)
}

// IsPolicyNamespacesError reports whether err is a PolicyNamespacesError.
func IsPolicyNamespacesError(err error) bool {
	var e *PolicyNamespacesError
	return errors.As(err, &e)
}

// PolicyNamespacesAgreed refuses translating a new version (a Bundle with no
// Graph yet) of pipeline while a namespace of spec.policyNamespaces that
// must agree has not, as the Pipeline reconciler's status.policyNamespaces
// says for the Pipeline's generation: the translator never reads the
// Namespaces itself, and never reads one it was not told it may. The
// Pipeline's own namespace and the org policy namespaces need nothing.
// While the generation is not checked yet (Unchecked), no version is
// translated, not even one in flight: which namespaces' gates it may read,
// and which it keeps from its Graph, is not known yet.
func PolicyNamespacesAgreed(pipeline *kardinalv1alpha1.Pipeline, orgNS []string) error {
	var needs []string
	for _, ns := range pipeline.Spec.PolicyNamespaces {
		if mustAgree(pipeline, ns, orgNS) && !slices.Contains(needs, ns) {
			needs = append(needs, ns)
		}
	}
	if len(needs) == 0 {
		return nil
	}
	st := pipeline.Status.PolicyNamespaces
	if st == nil || st.ObservedGeneration != pipeline.Generation {
		return &PolicyNamespacesError{Unchecked: true}
	}
	var refused []string
	for _, ns := range needs {
		if slices.Contains(st.NotShared, ns) {
			refused = append(refused, ns)
		}
	}
	if len(refused) > 0 {
		slices.Sort(refused)
		return &PolicyNamespacesError{NotShared: refused}
	}
	return nil
}

// RefusedPolicyNamespaces is the namespaces of pipeline's
// spec.policyNamespaces the controller refuses for a new version, as
// PolicyNamespacesAgreed decides from status.policyNamespaces (nil while
// the generation is not checked yet): what kardinal policy simulate leaves
// out and reports.
func RefusedPolicyNamespaces(pipeline *kardinalv1alpha1.Pipeline, orgNS []string) []string {
	var e *PolicyNamespacesError
	if errors.As(PolicyNamespacesAgreed(pipeline, orgNS), &e) && !e.Unchecked {
		return e.NotShared
	}
	return nil
}

// UnreadPolicyNamespaces is the namespaces of pipeline's
// spec.policyNamespaces whose gates are not read: those refused, and, while
// the generation is not checked yet, every one that must agree. A version
// in flight keeps theirs as its Graph has them.
func UnreadPolicyNamespaces(pipeline *kardinalv1alpha1.Pipeline, orgNS []string) []string {
	var e *PolicyNamespacesError
	if !errors.As(PolicyNamespacesAgreed(pipeline, orgNS), &e) {
		return nil
	}
	if !e.Unchecked {
		return e.NotShared
	}
	var needs []string
	for _, ns := range pipeline.Spec.PolicyNamespaces {
		if mustAgree(pipeline, ns, orgNS) && !slices.Contains(needs, ns) {
			needs = append(needs, ns)
		}
	}
	return needs
}

// mustAgree reports whether ns, named in pipeline's spec.policyNamespaces,
// must agree to share its gates: it is neither the Pipeline's own namespace
// nor an org policy namespace.
func mustAgree(pipeline *kardinalv1alpha1.Pipeline, ns string, orgNS []string) bool {
	return ns != "" && ns != pipeline.Namespace && !graph.IsPolicyNamespace(ns, orgNS)
}

// frozenGates are, for bundle when it is a version in flight (it has a
// Graph) of a Pipeline whose spec.policyNamespaces names a namespace refused,
// the templates of those namespaces rebuilt from bundle's own Graph, and their
// digests (graph.FrozenTemplates): the version keeps the gates it started
// with. A Graph that cannot be read refuses the translation: rebuilt without
// those gates, the version would skip them.
func (t *Translator) frozenGates(ctx context.Context, pipeline *kardinalv1alpha1.Pipeline,
	bundle *kardinalv1alpha1.Bundle) ([]kardinalv1alpha1.PolicyGate, map[string]string, error) {
	refused := RefusedPolicyNamespaces(pipeline, t.policyNS)
	if bundle.Status.GraphRef == "" || len(refused) == 0 {
		return nil, nil, nil
	}
	if t.graphClient == nil {
		return nil, nil, fmt.Errorf("keep the gates of %s for version %s: no Graph client", strings.Join(refused, ", "), bundle.Name)
	}
	g, err := t.graphClient.Get(ctx, bundle.Namespace, bundle.Status.GraphRef)
	if apierrors.IsNotFound(err) {
		return nil, nil, &PolicyNamespacesError{NotShared: refused, GraphGone: true}
	}
	if err != nil {
		return nil, nil, fmt.Errorf("keep the gates of %s it started with for version %s: read its Graph: %w",
			strings.Join(refused, ", "), bundle.Name, err)
	}
	frozen, digests, err := graph.FrozenTemplates(g, refused)
	if err != nil {
		return nil, nil, fmt.Errorf("keep the gates of %s for version %s: %w", strings.Join(refused, ", "), bundle.Name, err)
	}
	return frozen, digests, nil
}
