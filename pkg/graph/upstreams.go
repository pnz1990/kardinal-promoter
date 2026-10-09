// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"fmt"
	"slices"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// DirectUpstreams returns the environments that envName directly depends on in
// the promotion DAG that Build generates for bundle. It uses the same ordering
// (dependsOn, waves, list order) and the same intent filter (targetEnvironment,
// skipEnvironments) as Build: a skipped environment is bridged to its own
// upstreams, exactly as the Graph does. A root environment returns nil.
//
// It returns an error when the Pipeline ordering is invalid or when envName is
// not part of the bundle's promotion.
func DirectUpstreams(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle,
	envName string) ([]string, error) {
	if pipeline == nil || bundle == nil {
		return nil, fmt.Errorf("direct upstreams: pipeline and bundle are required")
	}
	ordered, deps, err := resolveOrdering(pipeline)
	if err != nil {
		return nil, fmt.Errorf("direct upstreams: %w", err)
	}
	filtered, err := filterByIntent(ordered, deps, bundle)
	if err != nil {
		return nil, fmt.Errorf("direct upstreams: %w", err)
	}
	filteredSet := make(map[string]bool, len(filtered))
	for _, e := range filtered {
		filteredSet[e] = true
	}
	if !filteredSet[envName] {
		return nil, fmt.Errorf("direct upstreams: environment %q is not promoted by bundle %s", envName, bundle.Name)
	}
	return filteredDeps(envName, deps, filteredSet), nil
}

// UpstreamsVerified reports whether the Graph Build generates for bundle has
// released envName: every direct upstream environment (DirectUpstreams) has
// the bundle's PromotionStep Verified. That is the upstream half of the gate
// on envName's PromotionStep (verifiedCond). A root environment is released.
//
// steps may hold PromotionSteps of any bundle; only those with
// spec.bundleName == bundle.Name count. It returns false when envName is not
// part of the bundle's promotion or the Pipeline ordering is invalid.
func UpstreamsVerified(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle,
	envName string, steps []kardinalv1alpha1.PromotionStep) bool {
	return upstreamsVerified(pipeline, bundle, envName, len(steps), func(i int) *kardinalv1alpha1.PromotionStep { return &steps[i] })
}

// upstreamsVerified is UpstreamsVerified over n steps, the i-th at(i).
func upstreamsVerified(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle,
	envName string, n int, at func(int) *kardinalv1alpha1.PromotionStep) bool {
	ups, err := DirectUpstreams(pipeline, bundle, envName)
	if err != nil {
		return false
	}
	for _, up := range ups {
		verified := 0
		for i := range n {
			s := at(i)
			if s.Spec.BundleName != bundle.Name || s.Spec.Environment != up {
				continue
			}
			if s.Status.State != "Verified" {
				return false
			}
			verified++
		}
		if verified == 0 {
			return false
		}
	}
	return true
}

// GateHolds reports whether gate, a PolicyGate instance of bundle, is holding
// the bundle's promotion back in the gate's environment. It is the one rule
// for the UI API's blockerCount, kardinal status's blocking gates and the
// Block state of GateState. The gate
// must be not ready and the bundle still in flight (not Failed, Superseded or
// Rejected),
// and either:
//   - the bundle has no PromotionStep in the environment and every upstream
//     environment is Verified for it (UpstreamsVerified): the Graph creates
//     the step only once the gate is ready; or
//   - a step of the bundle in the environment lists the gate in
//     spec.requiredGates while that step is still Pending ("" or "Pending"):
//     the PromotionStep reconciler (checkRequiredGates) keeps such a step
//     Pending, before any git operation, until the gate is ready. spec.when
//     makes no difference (#1323).
//
// A gate of an environment the bundle has not reached, or whose steps have
// all left Pending, holds nothing. steps may hold PromotionSteps of any
// bundle; only the bundle's own count.
func GateHolds(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle,
	gate *kardinalv1alpha1.PolicyGate, steps []kardinalv1alpha1.PromotionStep) bool {
	return gateHolds(pipeline, bundle, gate, len(steps), func(i int) *kardinalv1alpha1.PromotionStep { return &steps[i] })
}

// GateHoldsSteps is GateHolds over step pointers, for callers that index many
// steps and must not copy them (the UI pipeline list).
func GateHoldsSteps(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle,
	gate *kardinalv1alpha1.PolicyGate, steps []*kardinalv1alpha1.PromotionStep) bool {
	return gateHolds(pipeline, bundle, gate, len(steps), func(i int) *kardinalv1alpha1.PromotionStep { return steps[i] })
}

func gateHolds(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle,
	gate *kardinalv1alpha1.PolicyGate, n int, at func(int) *kardinalv1alpha1.PromotionStep) bool {
	env := gate.Labels["kardinal.io/environment"]
	if gate.Status.Ready || env == "" || gate.Labels["kardinal.io/bundle"] != bundle.Name {
		return false
	}
	if phase := bundle.Status.Phase; phase == "Failed" || phase == "Superseded" || phase == "Rejected" {
		return false
	}
	stepped := false
	for i := range n {
		s := at(i)
		if s.Spec.BundleName != bundle.Name || s.Spec.Environment != env {
			continue
		}
		stepped = true
		pending := s.Status.State == "" || s.Status.State == "Pending"
		if pending && slices.Contains(s.Spec.RequiredGates, gate.Name) {
			return true
		}
	}
	return !stepped && upstreamsVerified(pipeline, bundle, env, n, at)
}

// The states GateState gives a PolicyGate instance.
const (
	GateStatePass       = "Pass"
	GateStateBlock      = "Block"
	GateStateSuperseded = "Superseded"
	GateStateRejected   = "Rejected"
	GateStatePending    = "Pending"
	GateStateWaiting    = "Waiting"
)

// GateState is the one state the UI API (bundle graph and gate list) and
// kardinal explain show for gate, a PolicyGate instance of bundle:
//   - Pass: ready.
//   - Block: holds the bundle back (GateHolds), evaluated yet or not. Only
//     these count as blocked.
//   - Superseded: its bundle was superseded. That is final; the gate is not
//     evaluated again.
//   - Rejected: its bundle was rejected (kardinal reject). Final, like
//     Superseded.
//   - Pending: not evaluated yet.
//   - Waiting: evaluated not ready, but the bundle is not held here: it has not
//     reached the environment, or it failed (a Failed bundle can retry).
//
// pipeline or bundle may be nil, when it is gone; nothing is held then. A gate
// template (no kardinal.io/bundle label) holds nothing either.
func GateState(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle,
	gate *kardinalv1alpha1.PolicyGate, steps []kardinalv1alpha1.PromotionStep) string {
	switch {
	case gate.Status.Ready:
		return GateStatePass
	case pipeline != nil && bundle != nil && GateHolds(pipeline, bundle, gate, steps):
		return GateStateBlock
	case bundle != nil && bundle.Status.Phase == "Superseded":
		return GateStateSuperseded
	case bundle != nil && bundle.Status.Phase == "Rejected":
		return GateStateRejected
	case gate.Status.LastEvaluatedAt == nil:
		return GateStatePending
	default:
		return GateStateWaiting
	}
}
