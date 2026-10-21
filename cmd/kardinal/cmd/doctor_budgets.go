// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// controllerFlag is the value of --name on the controller Deployment's
// container, and whether it is set.
func controllerFlag(dep *appsv1.Deployment, name string) (string, bool) {
	for _, c := range dep.Spec.Template.Spec.Containers {
		argv := append(append([]string{}, c.Command...), c.Args...)
		for i, a := range argv {
			n, v, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
			if !strings.HasPrefix(a, "-") || n != name {
				continue
			}
			if !hasValue {
				if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
					return argv[i+1], true
				}
				return "true", true
			}
			return v, true
		}
	}
	return "", false
}

// checkAgentBudgets is the agent budgets check (docs/design/24-agent-budgets.md
// sections 15.2 and 17.4): with budgets off, the AgentIdentities whose budgets
// this release keeps but does not enforce; with them on, the ones whose
// ledger is missing (no writer evaluates it: a shard naming a ledger whose
// release does not run its reconciler) or behind their spec, whose versions
// wait. shown is false when no AgentIdentity has budgets.
func checkAgentBudgets(ctx context.Context, c sigs_client.Client, controllerNS string) (doctorResult, bool) {
	r := doctorResult{label: "Agent budgets"}
	var ids v1alpha1.AgentIdentityList
	if err := c.List(ctx, &ids); err != nil {
		return r, false // no registry, or not readable: nothing to say
	}
	var budgeted []v1alpha1.AgentIdentity
	for _, id := range ids.Items {
		if len(id.Spec.Budgets) > 0 {
			budgeted = append(budgeted, id)
		}
	}
	if len(budgeted) == 0 {
		return r, false
	}
	var deps appsv1.DeploymentList
	if err := c.List(ctx, &deps, sigs_client.InNamespace(controllerNS),
		sigs_client.MatchingLabels{"app.kubernetes.io/name": "kardinal-promoter"}); err != nil || len(deps.Items) == 0 {
		r.icon, r.warned = doctorWarn, true
		r.detail = fmt.Sprintf("%d agent(s) have budgets, and no kardinal-promoter Deployment in %s says whether they are enforced",
			len(budgeted), controllerNS)
		r.hint = "Installed elsewhere? Use --controller-namespace."
		return r, true
	}
	dep := &deps.Items[0]
	if on, _ := controllerFlag(dep, "agent-budgets"); on != "true" {
		r.icon, r.warned = doctorWarn, true
		r.detail = fmt.Sprintf("budgets are not enforced in this release (actors.budgets.enabled is false); kept, not enforced: %s",
			identityNames(budgeted))
		r.hint = "Turn them on: helm upgrade ... --set actors.budgets.enabled=true"
		return r, true
	}
	ledger, _ := controllerFlag(dep, "agent-budget-ledger")
	if ledger == "" {
		ledger, _ = controllerFlag(dep, "agent-index")
	}
	var missing, behind []v1alpha1.AgentIdentity
	for _, id := range budgeted {
		u, ok := id.Status.Usage[ledger]
		switch {
		case !ok:
			missing = append(missing, id)
		case u.ObservedGeneration != id.Generation:
			behind = append(behind, id)
		}
	}
	switch {
	case len(missing) > 0:
		r.icon, r.failed = doctorFail, true
		r.detail = fmt.Sprintf("ledger %s has no entry for %s: no controller evaluates it, so their versions wait", ledger, identityNames(missing))
		r.hint = fmt.Sprintf("Ledger %s is written by release %s's controller that owns cluster-scoped kinds: run it with "+
			"actors.budgets.enabled, or set actors.budgets.ledger to the default shard's release on every shard", ledger, ledger)
	case len(behind) > 0:
		r.icon, r.warned = doctorWarn, true
		r.detail = fmt.Sprintf("ledger %s is behind the spec of %s: their versions wait until it is evaluated", ledger, identityNames(behind))
		r.hint = "It catches up within seconds while ledger " + ledger + "'s writer runs; if not, check its logs"
	default:
		r.icon = doctorPass
		r.detail = fmt.Sprintf("%d agent(s) with budgets, counted in ledger %s", len(budgeted), ledger)
	}
	return r, true
}

func identityNames(ids []v1alpha1.AgentIdentity) string {
	out := make([]string, len(ids))
	for i := range ids {
		out[i] = ids[i].Name
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
