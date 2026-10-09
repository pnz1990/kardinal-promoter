// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// condGatesCreated reports whether every PolicyGate instance of the Bundle's
// Graph exists.
const condGatesCreated = "GatesCreated"

// maxMissingNamed is how many missing gate instances the condition names.
const maxMissingNamed = 5

// checkGatesCreated sets the Bundle's GatesCreated condition from the gate
// instances the Graph g declares (its PolicyGateData node) and the ones that
// exist.
//
// kro creates the instances from collection nodes and publishes a collection
// only when every item applied (ledger gap G11), so one instance that cannot
// be created (a ResourceQuota, an admission policy that denies it) holds
// every gated environment of the Bundle. The Graph's Ready condition may name
// another node, so this condition names the missing instances, with the
// Graph's ResourcesConverged message, to say why everything waits.
//
// Missing instances are reported only when kro says it cannot apply the
// Graph (ResourcesConverged False with a *Failed reason) or names one of them;
// otherwise they are still being created and the condition is left as it is.
// Reads CRD objects only; changes b in memory, the caller patches.
func (r *Reconciler) checkGatesCreated(ctx context.Context, b *kardinalv1alpha1.Bundle, g *graph.Graph) error {
	want := graphGateInstances(g)
	if len(want) == 0 {
		meta.RemoveStatusCondition(&b.Status.Conditions, condGatesCreated)
		return nil
	}
	var list kardinalv1alpha1.PolicyGateList
	if err := r.List(ctx, &list, client.InNamespace(b.Namespace),
		client.MatchingLabels{"kardinal.io/bundle": b.Name}); err != nil {
		return fmt.Errorf("list gate instances: %w", err)
	}
	have := make(map[string]bool, len(list.Items))
	for _, gate := range list.Items {
		have[gate.Name] = true
	}
	var missing []string
	for _, name := range want {
		if !have[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		setBundleCondition(b, condGatesCreated, metav1.ConditionTrue, "Created",
			fmt.Sprintf("all %d PolicyGate instances exist", len(want)))
		return nil
	}
	conv := meta.FindStatusCondition(g.Status.Conditions, "ResourcesConverged")
	if conv == nil || conv.Status != metav1.ConditionFalse {
		return nil
	}
	named := false
	for _, name := range missing {
		if strings.Contains(conv.Message, name) {
			named = true
			break
		}
	}
	if !named && !strings.HasSuffix(conv.Reason, "Failed") {
		return nil
	}
	sort.Strings(missing)
	shown := missing
	if len(shown) > maxMissingNamed {
		shown = shown[:maxMissingNamed]
	}
	more := ""
	if n := len(missing) - len(shown); n > 0 {
		more = fmt.Sprintf(" and %d more", n)
	}
	setBundleCondition(b, condGatesCreated, metav1.ConditionFalse, "ApplyFailed",
		fmt.Sprintf("%d of %d PolicyGate instances are not created (%s%s), so every gated environment waits; kro: %s: %s",
			len(missing), len(want), strings.Join(shown, ", "), more, conv.Reason, truncate(conv.Message, 600)))
	return nil
}

// graphGateInstances lists the gate instance names in g's PolicyGateData
// node, in Graph order.
func graphGateInstances(g *graph.Graph) []string {
	if g == nil {
		return nil
	}
	var names []string
	for _, n := range g.Spec.Nodes {
		if n.ID != graph.NodePolicyGateData {
			continue
		}
		var fields []string
		for f := range n.Def {
			if strings.HasPrefix(f, "gates") || strings.HasPrefix(f, "skipGates") {
				fields = append(fields, f)
			}
		}
		sort.Strings(fields)
		for _, f := range fields {
			items, _ := n.Def[f].([]interface{})
			for _, item := range items {
				if m, ok := item.(map[string]interface{}); ok {
					if name, ok := m["name"].(string); ok {
						names = append(names, name)
					}
				}
			}
		}
	}
	return names
}

// truncate shortens s to at most n bytes, on a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}
