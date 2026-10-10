// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// condRunsCreated reports a HookRun, AnalysisRun or RenderRun of the
// Bundle's compact Graph that kro cannot create.
const condRunsCreated = "RunsCreated"

// checkRunsCreated sets the Bundle's RunsCreated condition from kro's
// ResourcesConverged condition on the compact Graph g.
//
// The compact shape creates HookRuns, AnalysisRuns and RenderRuns from collections
// (ledger G11). A run kro cannot apply (an admission policy, a quota, an
// invalid Job) holds only its own environment, because the steps read the
// runs back through selector refs, not through the collection, but its step
// only says it waits for the hook or the analysis. The PromotionStep
// reconciler does not read the Graph, so the error is reported here: the
// condition is False, naming the run, its environment and kro's message,
// while kro's message names a run of the Graph's HookRunData, RenderRunData or
// AnalysisRunData. It is removed once kro no longer names one; a run not
// named is not reported, since a run the admission has not reached yet does
// not exist either. Reads only g; changes b in memory, the caller patches.
func checkRunsCreated(b *kardinalv1alpha1.Bundle, g *graph.Graph) {
	runs := graphRuns(g)
	conv := meta.FindStatusCondition(g.Status.Conditions, "ResourcesConverged")
	var named []string
	if conv != nil && conv.Status == metav1.ConditionFalse {
		for name, env := range runs {
			if strings.Contains(conv.Message, name) {
				named = append(named, fmt.Sprintf("%s (%s)", name, env))
			}
		}
	}
	if len(named) == 0 {
		meta.RemoveStatusCondition(&b.Status.Conditions, condRunsCreated)
		return
	}
	sort.Strings(named)
	setBundleCondition(b, condRunsCreated, metav1.ConditionFalse, "ApplyFailed",
		fmt.Sprintf("kro cannot create %s, so that environment waits for it; kro: %s: %s",
			strings.Join(named, ", "), conv.Reason, truncate(conv.Message, 600)))
}

// graphRuns maps the HookRun and AnalysisRun names of g's compact data
// nodes to their environments.
func graphRuns(g *graph.Graph) map[string]string {
	out := map[string]string{}
	if g == nil {
		return out
	}
	for _, n := range g.Spec.Nodes {
		if n.ID != graph.NodeHookRunData && n.ID != graph.NodeAnalysisRunData && n.ID != graph.NodeRenderRunData {
			continue
		}
		for _, field := range n.Def {
			items, _ := field.([]interface{})
			for _, item := range items {
				m, _ := item.(map[string]interface{})
				name, _ := m["name"].(string)
				env, _ := m["environment"].(string)
				if name != "" {
					out[name] = env
				}
			}
		}
	}
	return out
}
