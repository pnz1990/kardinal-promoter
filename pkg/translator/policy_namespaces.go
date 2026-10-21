// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package translator

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// PolicyNamespacesError refuses a new version of a Pipeline whose
// spec.policyNamespaces names a namespace that has not agreed to share its
// PolicyGates (#2286): the version waits, it does not fail, and is
// translated once the namespace agrees or the entry is removed. Unchecked:
// the Pipeline reconciler has not checked the Pipeline's current generation
// yet (status.policyNamespaces), so nothing is read before it has.
type PolicyNamespacesError struct {
	NotShared []string
	Unchecked bool
}

func (e *PolicyNamespacesError) Error() string {
	if e.Unchecked {
		return "spec.policyNamespaces is not checked yet for this generation of the Pipeline: waiting for the Pipeline reconciler"
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

// policyNamespacesAgreed refuses translating a new version (a Bundle with no
// Graph yet) of pipeline while a namespace of spec.policyNamespaces that
// must agree has not, as the Pipeline reconciler's status.policyNamespaces
// says for the Pipeline's generation: the translator never reads the
// Namespaces itself, and never reads one it was not told it may. The
// Pipeline's own namespace and the org policy namespaces need nothing.
func policyNamespacesAgreed(pipeline *kardinalv1alpha1.Pipeline, orgNS []string) error {
	var needs []string
	for _, ns := range pipeline.Spec.PolicyNamespaces {
		if ns != "" && ns != pipeline.Namespace && !graph.IsPolicyNamespace(ns, orgNS) && !slices.Contains(needs, ns) {
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
// policyNamespacesAgreed decides from status.policyNamespaces (nil while
// the generation is not checked yet): what kardinal policy simulate leaves
// out and reports.
func RefusedPolicyNamespaces(pipeline *kardinalv1alpha1.Pipeline, orgNS []string) []string {
	var e *PolicyNamespacesError
	if errors.As(policyNamespacesAgreed(pipeline, orgNS), &e) && !e.Unchecked {
		return e.NotShared
	}
	return nil
}
