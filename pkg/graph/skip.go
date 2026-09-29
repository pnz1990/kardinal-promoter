// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"fmt"
	"sort"
	"strings"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// DefaultPolicyNamespace is the org policy namespace used when the controller
// is not configured with one.
const DefaultPolicyNamespace = "platform-policies"

const (
	// LabelGateType marks a PolicyGate as a skip-permission gate
	// ("skip-permission"). Other values, or no label, mean an ordinary gate.
	LabelGateType = "kardinal.io/type"
	// GateTypeSkipPermission is the LabelGateType value of a skip-permission gate.
	GateTypeSkipPermission = "skip-permission"
	// AnnotationSkippedEnvironments lists, comma-separated, the skipped
	// environments a skip-permission gate instance stands for.
	AnnotationSkippedEnvironments = "kardinal.io/skipped-environments"
)

// isSkipPermissionGate reports whether g is labelled as a skip-permission gate.
func isSkipPermissionGate(g kardinalv1alpha1.PolicyGate) bool {
	return g.Labels[LabelGateType] == GateTypeSkipPermission
}

// policyNamespaceSet returns the org policy namespaces as a set, defaulting to
// DefaultPolicyNamespace.
func policyNamespaceSet(policyNamespaces []string) map[string]bool {
	if len(policyNamespaces) == 0 {
		policyNamespaces = []string{DefaultPolicyNamespace}
	}
	set := make(map[string]bool, len(policyNamespaces))
	for _, ns := range policyNamespaces {
		set[ns] = true
	}
	return set
}

// isOrgGate reports whether g is an ordinary (not skip-permission) gate owned
// by the platform: it lives in an org policy namespace, or it is labelled
// kardinal.io/scope=org.
func isOrgGate(g kardinalv1alpha1.PolicyGate, orgNS map[string]bool) bool {
	if isSkipPermissionGate(g) {
		return false
	}
	return orgNS[g.Namespace] || g.Labels["kardinal.io/scope"] == "org"
}

// isSkipPermissionFor reports whether g may grant a skip of envName: it is a
// skip-permission gate with spec.skipPermission set, it applies to envName,
// and it lives in an org policy namespace. A gate in a team namespace can
// never grant a skip, whatever its labels say.
func isSkipPermissionFor(g kardinalv1alpha1.PolicyGate, envName string, orgNS map[string]bool) bool {
	return isSkipPermissionGate(g) && g.Spec.SkipPermission && orgNS[g.Namespace] && appliesToEnv(g, envName)
}

// ValidateSkipPermissions checks the Bundle's intent.skipEnvironments against
// the org gates. Skipping an environment that an org gate applies to needs at
// least one skip-permission gate for that environment in an org policy
// namespace (policyNamespaces, DefaultPolicyNamespace when empty). Only the
// presence of the permission gate is checked here; its expression is
// evaluated by the PolicyGate reconciler on the instance Build adds in front
// of the next environment (see skipPermissionGates), so a permission whose
// expression is false holds the promotion at that environment.
//
// The returned error contains "skip denied". Build calls this function; the
// Bundle reconciler records the error as phase Failed with condition
// Failed/TranslationError.
func ValidateSkipPermissions(bundle *kardinalv1alpha1.Bundle,
	allGates []kardinalv1alpha1.PolicyGate, policyNamespaces []string) error {
	if bundle == nil || bundle.Spec.Intent == nil {
		return nil
	}
	orgNS := policyNamespaceSet(policyNamespaces)
	for _, skip := range bundle.Spec.Intent.SkipEnvironments {
		var orgGate string
		for _, g := range allGates {
			if isOrgGate(g, orgNS) && appliesToEnv(g, skip) {
				orgGate = g.Namespace + "/" + g.Name
				break
			}
		}
		if orgGate == "" {
			continue // no org gate: the skip needs no permission
		}
		permitted := false
		for _, g := range allGates {
			if isSkipPermissionFor(g, skip, orgNS) {
				permitted = true
				break
			}
		}
		if !permitted {
			return fmt.Errorf("build: skip denied for environment %q: org gate %s applies to it and "+
				"no skip-permission gate (label %s=%s, spec.skipPermission: true) in the org policy "+
				"namespaces %v allows skipping it", skip, orgGate, LabelGateType, GateTypeSkipPermission,
				sortedKeys(orgNS))
		}
	}
	return nil
}

// skipPermissionGate is one skip-permission gate instance to emit: the
// permission gate, and the skipped org-gated environments it stands for in
// front of one kept environment.
type skipPermissionGate struct {
	gate    kardinalv1alpha1.PolicyGate
	skipped []string
}

// skipPermissionGates returns, per kept environment, the skip-permission gates
// whose expressions must hold before that environment is promoted. For each
// kept environment K, every skipped environment S that K's dependency walk
// crosses (that is, K now follows S's upstreams directly) and that an org gate
// applies to contributes the permission gates for S. The instances are
// ordinary PolicyGate nodes, so the PolicyGate reconciler evaluates the
// permission expression against the Bundle and K waits until it is true.
//
// Skipping an environment nothing depends on (a leaf) holds nothing, because
// no environment is promoted through it.
func skipPermissionGates(filteredEnvs []string, deps map[string][]string,
	bundle *kardinalv1alpha1.Bundle, allGates []kardinalv1alpha1.PolicyGate,
	policyNamespaces []string) map[string][]skipPermissionGate {
	if bundle.Spec.Intent == nil || len(bundle.Spec.Intent.SkipEnvironments) == 0 {
		return nil
	}
	orgNS := policyNamespaceSet(policyNamespaces)
	skipped := make(map[string]bool, len(bundle.Spec.Intent.SkipEnvironments))
	for _, s := range bundle.Spec.Intent.SkipEnvironments {
		skipped[s] = true
	}
	orgGated := func(env string) bool {
		for _, g := range allGates {
			if isOrgGate(g, orgNS) && appliesToEnv(g, env) {
				return true
			}
		}
		return false
	}

	result := make(map[string][]skipPermissionGate)
	for _, kept := range filteredEnvs {
		// Skipped environments crossed on the way to kept's upstreams.
		var crossed []string
		seen := make(map[string]bool)
		var walk func(e string)
		walk = func(e string) {
			for _, dep := range deps[e] {
				if seen[dep] || !skipped[dep] {
					continue
				}
				seen[dep] = true
				if orgGated(dep) {
					crossed = append(crossed, dep)
				}
				walk(dep)
			}
		}
		walk(kept)
		if len(crossed) == 0 {
			continue
		}
		sort.Strings(crossed)

		// One instance per permission gate, listing the skipped envs it covers.
		byGate := make(map[string]*skipPermissionGate)
		var order []string
		for _, s := range crossed {
			for _, g := range allGates {
				if !isSkipPermissionFor(g, s, orgNS) {
					continue
				}
				key := g.Namespace + "/" + g.Name
				pg, ok := byGate[key]
				if !ok {
					pg = &skipPermissionGate{gate: g}
					byGate[key] = pg
					order = append(order, key)
				}
				pg.skipped = append(pg.skipped, s)
			}
		}
		sort.Strings(order)
		for _, key := range order {
			result[kept] = append(result[kept], *byGate[key])
		}
	}
	return result
}

// appliesToEnv returns true if the gate's kardinal.io/applies-to label
// contains the given environment name.
func appliesToEnv(gate kardinalv1alpha1.PolicyGate, envName string) bool {
	for _, e := range strings.Split(gate.Labels["kardinal.io/applies-to"], ",") {
		if e = strings.TrimSpace(e); e != "" && e == envName {
			return true
		}
	}
	return false
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
