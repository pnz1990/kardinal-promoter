// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"fmt"
	"path"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// LabelFleet is the label of a fleet target's PromotionStep, PRStatus and
// gate instances: the name of the fleet environment.
const LabelFleet = "kardinal.io/fleet"

// fleetMember is one target environment of a fleet.
type fleetMember struct {
	// fleet is the fleet environment's name.
	fleet string
	// index is the target's position in the fleet, which paces it.
	index int
	// maxConcurrent is the fleet's spec.fleet.maxConcurrent.
	maxConcurrent int
	// maxUnavailable is the fleet's spec.fleet.maxUnavailable, 0 when unset.
	maxUnavailable int
	// spec is the target as an environment: the fleet environment with the
	// target's name, path and health.
	spec kardinalv1alpha1.EnvironmentSpec
}

// FleetTargetName is the environment name of target in fleet environment env.
func FleetTargetName(env, target string) string { return env + "-" + target }

// fleetTargets returns the targets of fleet environment env: spec.targets,
// or the members status.fleets resolved for its selector.
func fleetTargets(p *kardinalv1alpha1.Pipeline, env kardinalv1alpha1.EnvironmentSpec) ([]kardinalv1alpha1.FleetTarget, error) {
	sel := env.Fleet.Selector
	if sel == nil {
		return env.Fleet.Targets, nil
	}
	if sel.Kind == kardinalv1alpha1.FleetSelectorTarget {
		// Static targets picked by their labels, in their order.
		ls, err := metav1.LabelSelectorAsSelector(sel.LabelSelector())
		if err != nil {
			return nil, asInvalid(fmt.Errorf("build: fleet environment %q: selector: %w", env.Name, err))
		}
		var out []kardinalv1alpha1.FleetTarget
		for _, t := range env.Fleet.Targets {
			if ls.Matches(labels.Set(t.Labels)) {
				out = append(out, t)
			}
		}
		return out, nil
	}
	for _, f := range p.Status.Fleets {
		if f.Environment != env.Name {
			continue
		}
		// With a message the targets are the last good ones (the selector
		// could not be read now), or the message names objects that are
		// not targets: the targets still count.
		if len(f.Targets) == 0 && f.Message != "" {
			return nil, fmt.Errorf("build: fleet environment %q: its selector could not be resolved: %s", env.Name, f.Message)
		}
		return f.Targets, nil
	}
	return nil, fmt.Errorf("build: fleet environment %q: its selector has not been resolved yet (status.fleets)", env.Name)
}

// fleetMembers returns every fleet target of p by environment name, and the
// target names of each fleet environment in order. It rejects a fleet
// without targets and a target whose environment name is not a DNS label or
// is taken by another environment.
func fleetMembers(p *kardinalv1alpha1.Pipeline) (map[string]fleetMember, map[string][]string, error) {
	members := map[string]fleetMember{}
	byFleet := map[string][]string{}
	names := map[string]bool{}
	for _, e := range p.Spec.Environments {
		names[e.Name] = true
	}
	for _, e := range p.Spec.Environments {
		if e.Fleet == nil {
			continue
		}
		targets, err := fleetTargets(p, e)
		if err != nil {
			return nil, nil, err
		}
		if len(targets) == 0 {
			return nil, nil, fmt.Errorf("build: fleet environment %q has no targets", e.Name)
		}
		base := e.Path
		if base == "" {
			base = "environments/" + e.Name
		}
		for i, t := range targets {
			name := FleetTargetName(e.Name, t.Name)
			if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
				return nil, nil, fmt.Errorf("build: fleet environment %q target %q: environment name %q is not a DNS label of at most 63 characters",
					e.Name, t.Name, name)
			}
			if names[name] {
				return nil, nil, fmt.Errorf("build: fleet environment %q target %q: environment name %q is already used", e.Name, t.Name, name)
			}
			names[name] = true
			spec := *e.DeepCopy()
			spec.Name = name
			spec.Fleet = nil
			spec.Path = t.Path
			if spec.Path == "" {
				spec.Path = path.Join(base, t.Name)
			}
			if t.Health != nil {
				spec.Health = *t.Health.DeepCopy()
			}
			maxUnavailable := 0
			if e.Fleet.MaxUnavailable != nil {
				maxUnavailable = *e.Fleet.MaxUnavailable
			}
			members[name] = fleetMember{fleet: e.Name, index: i, maxConcurrent: e.Fleet.MaxConcurrent,
				maxUnavailable: maxUnavailable, spec: spec}
			byFleet[e.Name] = append(byFleet[e.Name], name)
		}
	}
	return members, byFleet, nil
}

// expandFleets replaces each fleet environment in the ordering by its
// targets: a target depends on what its fleet depends on, and an
// environment that depends on a fleet depends on every target. deps then
// keeps an entry for each fleet name listing its targets, so a Bundle's
// targetEnvironment and skipEnvironments may name a fleet (filterByIntent);
// ordered does not contain fleet names.
func expandFleets(ordered []string, deps map[string][]string, byFleet map[string][]string) ([]string, map[string][]string) {
	if len(byFleet) == 0 {
		return ordered, deps
	}
	mapDeps := func(in []string) []string {
		var out []string
		for _, d := range in {
			if targets, ok := byFleet[d]; ok {
				out = append(out, targets...)
			} else {
				out = append(out, d)
			}
		}
		return out
	}
	expanded := make(map[string][]string, len(deps))
	var order []string
	for _, env := range ordered {
		targets, isFleet := byFleet[env]
		if !isFleet {
			expanded[env] = mapDeps(deps[env])
			order = append(order, env)
			continue
		}
		for _, t := range targets {
			expanded[t] = mapDeps(deps[env])
			order = append(order, t)
		}
	}
	for fleet, targets := range byFleet {
		expanded[fleet] = append([]string(nil), targets...)
	}
	return order, expanded
}

// isFleetName reports whether name is a fleet environment in deps (an entry
// expandFleets added) rather than an environment of the ordering.
func isFleetName(ordered []string, deps map[string][]string, name string) bool {
	if _, ok := deps[name]; !ok {
		return false
	}
	for _, e := range ordered {
		if e == name {
			return false
		}
	}
	return true
}

// EnvironmentSpecFor returns the environment named name of p: one of
// spec.environments, or a fleet target (the fleet environment with the
// target's name, path and health). ok is false when p has no such
// environment.
func EnvironmentSpecFor(p *kardinalv1alpha1.Pipeline, name string) (kardinalv1alpha1.EnvironmentSpec, bool) {
	for _, e := range p.Spec.Environments {
		if e.Name == name && e.Fleet == nil {
			return e, true
		}
	}
	members, _, err := fleetMembers(p)
	if err != nil {
		return kardinalv1alpha1.EnvironmentSpec{}, false
	}
	m, ok := members[name]
	return m.spec, ok
}

// ExpandedEnvironments returns p's environments in spec order with each
// fleet environment replaced by its targets (EnvironmentSpecFor's specs).
// A fleet whose targets cannot be resolved is left out.
func ExpandedEnvironments(p *kardinalv1alpha1.Pipeline) []kardinalv1alpha1.EnvironmentSpec {
	members, byFleet, err := fleetMembers(p)
	var out []kardinalv1alpha1.EnvironmentSpec
	for _, e := range p.Spec.Environments {
		if e.Fleet == nil {
			out = append(out, e)
			continue
		}
		if err != nil {
			continue
		}
		for _, name := range byFleet[e.Name] {
			out = append(out, members[name].spec)
		}
	}
	return out
}

// HasEnvironment reports whether name is an environment of p: one of
// spec.environments (a fleet environment included) or a fleet target.
func HasEnvironment(p *kardinalv1alpha1.Pipeline, name string) bool {
	for _, e := range p.Spec.Environments {
		if e.Name == name {
			return true
		}
	}
	_, ok := EnvironmentSpecFor(p, name)
	return ok
}

// FleetOf returns the fleet environment that target belongs to, or "" when
// target is not a fleet target of p.
func FleetOf(p *kardinalv1alpha1.Pipeline, target string) string {
	members, _, err := fleetMembers(p)
	if err != nil {
		return ""
	}
	return members[target].fleet
}

// FleetTargetEnvironments returns the target environment names of each
// fleet environment of p, in the order they are promoted (the UI's fleet
// roll-up). An error says a fleet's targets cannot be resolved.
func FleetTargetEnvironments(p *kardinalv1alpha1.Pipeline) (map[string][]string, error) {
	if !hasFleets(p) {
		return nil, nil
	}
	_, byFleet, err := fleetMembers(p)
	return byFleet, err
}

// FleetTargetSpecs returns each fleet environment's targets as
// environments (the fleet environment with the target's name, path and
// health), in the order they are promoted. A fleet whose targets cannot be
// resolved has none.
func FleetTargetSpecs(p *kardinalv1alpha1.Pipeline) map[string][]kardinalv1alpha1.EnvironmentSpec {
	if !hasFleets(p) {
		return nil
	}
	members, byFleet, err := fleetMembers(p)
	if err != nil {
		return nil
	}
	out := make(map[string][]kardinalv1alpha1.EnvironmentSpec, len(byFleet))
	for fleet, names := range byFleet {
		for _, n := range names {
			out[fleet] = append(out[fleet], members[n].spec)
		}
	}
	return out
}

// ValidateFleets returns what keeps p's fleets from being built: a fleet
// without targets, a selector that is not resolved (status.fleets) or could
// not be, and a target whose environment name is not a DNS label or is
// taken. Every Bundle of p fails with the same error when its Graph is built.
func ValidateFleets(p *kardinalv1alpha1.Pipeline) error {
	if !hasFleets(p) {
		return nil
	}
	_, _, err := fleetMembers(p)
	return err
}

// HasFleets reports whether any environment of p is a fleet.
func HasFleets(p *kardinalv1alpha1.Pipeline) bool { return hasFleets(p) }

// hasFleets reports whether any environment of p is a fleet.
func hasFleets(p *kardinalv1alpha1.Pipeline) bool {
	for i := range p.Spec.Environments {
		if p.Spec.Environments[i].Fleet != nil {
			return true
		}
	}
	return false
}

// EnvironmentNames lists p's environment names as a user may name them: every
// spec.environments entry, each fleet environment followed by its targets'
// environment names. A fleet whose targets cannot be resolved lists none.
func EnvironmentNames(p *kardinalv1alpha1.Pipeline) []string {
	var byFleet map[string][]string
	if hasFleets(p) {
		_, byFleet, _ = fleetMembers(p)
	}
	names := make([]string, 0, len(p.Spec.Environments))
	for _, e := range p.Spec.Environments {
		names = append(names, e.Name)
		names = append(names, byFleet[e.Name]...)
	}
	return names
}
