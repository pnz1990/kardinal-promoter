// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// nodeIDPattern is kro's node ID grammar
// (kubernetes-sigs/kro pkg/graphengine/compiler/validation.go).
var nodeIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)

// reservedNodeIDs mirrors kro's reserved node IDs: Kubernetes manifest
// fields, kro's own vocabulary, and the CEL reserved words
// (kubernetes-sigs/kro pkg/graphengine/compiler/validation.go). kro rejects a
// Graph that uses one; Build rejects it first, with the environment or gate
// that produced it, instead of leaving a Graph that never compiles.
var reservedNodeIDs = map[string]bool{
	"apiVersion": true, "kind": true, "metadata": true, "namespace": true, "spec": true, "status": true,
	"graph": true, "graphengine": true, "kro": true,
	"each": true, "item": true, "items": true, "object": true, "self": true, "this": true, "context": true,
	// CEL reserved words.
	"true": true, "false": true, "null": true, "in": true,
	"as": true, "break": true, "const": true, "continue": true, "else": true,
	"for": true, "function": true, "if": true, "import": true, "let": true,
	"loop": true, "package": true, "return": true,
	"var": true, "void": true, "while": true,
}

// ErrInvalid is in the chain of every error Build and ValidateNodeIDs
// return. Build reads nothing but its input, so such an error is permanent:
// a retry fails the same way until the Pipeline, the Bundle or a PolicyGate
// changes. Callers use errors.Is(err, ErrInvalid) to tell it from API errors
// that a retry can fix.
var ErrInvalid = errors.New("pipeline, bundle or policy gates cannot be built into a graph")

// invalidError keeps the message of err and adds ErrInvalid to its chain.
type invalidError struct{ err error }

func (e *invalidError) Error() string   { return e.err.Error() }
func (e *invalidError) Unwrap() []error { return []error{e.err, ErrInvalid} }

// asInvalid marks err as permanent; nil stays nil.
func asInvalid(err error) error {
	if err == nil || errors.Is(err, ErrInvalid) {
		return err
	}
	return &invalidError{err: err}
}

// maxPipelineNameHint is the longest Pipeline name that leaves room for the
// rollback Bundle name "<pipeline>-rollback-xxxxx" in a 63-character label.
const maxPipelineNameHint = validation.LabelValueMaxLength - len("-rollback-xxxxx")

// ValidateNodeIDs checks the node IDs of a Graph the way kro's compiler does,
// before the Graph is written: each ID matches ^[A-Za-z][A-Za-z0-9]*$, is not
// reserved, is unique, and does not equal a forEach iterator name (an
// iterator shadows a node of the same name inside the forEach node's
// expressions). Environment and gate names that differ only in case or
// punctuation ("prod-eu", "prod_eu", "prodEu") map to the same ID; the error
// names both sources so the user knows what to rename. Errors wrap
// ErrInvalid.
func ValidateNodeIDs(nodes []GraphNode) error {
	return asInvalid(validateNodeIDs(nodes))
}

func validateNodeIDs(nodes []GraphNode) error {
	iterators := map[string]string{}
	for _, n := range nodes {
		for _, dim := range n.ForEach {
			for name := range dim {
				iterators[name] = n.ID
			}
		}
	}
	seen := make(map[string]GraphNode, len(nodes))
	for _, n := range nodes {
		switch {
		case !nodeIDPattern.MatchString(n.ID):
			return fmt.Errorf("build: %s gets node id %q, which does not match %s",
				describeNode(n), n.ID, nodeIDPattern.String())
		case reservedNodeIDs[n.ID]:
			return fmt.Errorf("build: %s gets node id %q, which kro reserves; rename it", describeNode(n), n.ID)
		}
		if prev, dup := seen[n.ID]; dup {
			return fmt.Errorf("build: %s and %s get the same node id %q; names that differ only in "+
				"case or punctuation map to the same id, so rename one", describeNode(prev), describeNode(n), n.ID)
		}
		seen[n.ID] = n
		if owner, ok := iterators[n.ID]; ok {
			return fmt.Errorf("build: %s gets node id %q, which is the forEach iterator name of node %q; rename it",
				describeNode(n), n.ID, owner)
		}
	}
	return nil
}

// describeNode names the object a node stands for, for error messages.
func describeNode(n GraphNode) string {
	obj := n.Template
	if obj == nil {
		obj = n.Ref
	}
	kind, _ := obj["kind"].(string)
	meta, _ := obj["metadata"].(map[string]interface{})
	labels, _ := meta["labels"].(map[string]interface{})
	env, _ := labels["kardinal.io/environment"].(string)
	switch {
	case kind == "PolicyGate":
		gate, _ := labels["kardinal.io/gate-template"].(string)
		return fmt.Sprintf("PolicyGate %q for environment %q", gate, env)
	case env != "":
		return fmt.Sprintf("%s for environment %q", kind, env)
	case kind != "":
		name, _ := meta["name"].(string)
		return fmt.Sprintf("%s %q", kind, name)
	}
	return fmt.Sprintf("node %q", n.ID)
}

// validateInput rejects Pipelines, Bundles and gates whose names the Graph
// cannot carry: every name ends up in a label value, and environment and
// region names end up in object names.
func validateInput(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle,
	gates []kardinalv1alpha1.PolicyGate) error {
	if errs := validation.IsValidLabelValue(pipeline.Name); len(errs) > 0 {
		return fmt.Errorf("build: pipeline name %q cannot be used as a label value (%s); "+
			"use at most %d characters so rollback Bundle names fit too",
			pipeline.Name, strings.Join(errs, "; "), maxPipelineNameHint)
	}
	if errs := validation.IsValidLabelValue(bundle.Name); len(errs) > 0 {
		return fmt.Errorf("build: bundle name %q cannot be used as a label value: %s",
			bundle.Name, strings.Join(errs, "; "))
	}
	for _, g := range gates {
		if errs := validation.IsValidLabelValue(g.Name); len(errs) > 0 {
			return fmt.Errorf("build: PolicyGate %s/%s: name cannot be used as a label value: %s",
				g.Namespace, g.Name, strings.Join(errs, "; "))
		}
	}
	return validateEnvironments(pipeline.Spec.Environments)
}

// validateEnvironments checks the environment names, regions, shards and steps.
func validateEnvironments(envs []kardinalv1alpha1.EnvironmentSpec) error {
	names := make(map[string]bool, len(envs))
	for _, e := range envs {
		if e.Name == "" {
			return fmt.Errorf("build: an environment has no name")
		}
		if errs := validation.IsValidLabelValue(e.Name); len(errs) > 0 {
			return fmt.Errorf("build: environment name %q cannot be used as a label value: %s",
				e.Name, strings.Join(errs, "; "))
		}
		if names[e.Name] {
			return fmt.Errorf("build: environment %q is declared twice", e.Name)
		}
		names[e.Name] = true
		if e.Shard != "" {
			if errs := validation.IsValidLabelValue(e.Shard); len(errs) > 0 {
				return fmt.Errorf("build: environment %q: shard %q cannot be used as a label value: %s",
					e.Name, e.Shard, strings.Join(errs, "; "))
			}
		}
		regions := make(map[string]bool, len(e.Regions))
		for _, r := range e.Regions {
			if errs := validation.IsDNS1123Label(r); len(errs) > 0 {
				return fmt.Errorf("build: environment %q: region %q must be a DNS-1123 label "+
					"(it becomes part of the PromotionStep name): %s", e.Name, r, strings.Join(errs, "; "))
			}
			if regions[r] {
				return fmt.Errorf("build: environment %q: region %q is listed twice", e.Name, r)
			}
			regions[r] = true
		}
		// The PromotionStep reconciler always runs the default step sequence
		// (steps.DefaultSequenceForBundle); PromotionStepSpec has no field to
		// carry a custom one. Refuse the Pipeline instead of silently ignoring
		// the steps the author asked for.
		if len(e.Steps) > 0 {
			return fmt.Errorf("build: environment %q declares %d steps; spec.environments[].steps is "+
				"not implemented yet (the controller always runs the default step sequence), so "+
				"remove it; see docs/custom-steps.md", e.Name, len(e.Steps))
		}
		if e.PromotionTemplate != nil {
			return fmt.Errorf("build: environment %q references PromotionTemplate %q; "+
				"spec.environments[].promotionTemplate is not implemented yet (the controller always "+
				"runs the default step sequence), so remove it; see docs/custom-steps.md",
				e.Name, e.PromotionTemplate.Name)
		}
	}
	return nil
}

// validateSkipNames rejects intent.skipEnvironments entries that name no
// environment, so a typo does not silently promote through the environment
// the author meant to skip or pass a skip-permission check it would fail.
func validateSkipNames(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle) error {
	if bundle.Spec.Intent == nil {
		return nil
	}
	known := make(map[string]bool, len(pipeline.Spec.Environments))
	for _, e := range pipeline.Spec.Environments {
		known[e.Name] = true
	}
	var unknown []string
	for _, s := range bundle.Spec.Intent.SkipEnvironments {
		if !known[s] {
			unknown = append(unknown, s)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("build: intent.skipEnvironments names unknown environments %v", unknown)
	}
	return nil
}
