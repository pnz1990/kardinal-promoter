// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"fmt"
	"sort"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// currentBundleByEnv returns, per environment, the Bundle that explain and
// status describe there: the newest Bundle that is not Superseded or Rejected
// (lifecycle.Halted) and is in that environment, where a Rejected Bundle whose
// change is live there (lifecycle.RejectedLiveStep: its step is
// HealthChecking or Verified) counts as not Halted, so the view shows the
// rejected change that is deployed. A Bundle is in an environment when it has a PromotionStep
// there, or a gate instance there and it has not failed. Newest is
// lifecycle.CompareCreation, the order supersession uses. An environment
// where every Bundle is Halted falls back to the newest Halted Bundle
// with a PromotionStep there, as the UI falls back to the newest Superseded
// Bundle. A step or gate instance whose Bundle is not in bundles (being
// deleted) is ignored, and so are gate templates, which have no bundle label.
//
// The Graph creates every gate instance of a Bundle when the Bundle starts
// (gate nodes have no dependencies), so a gate instance shows only that a
// Bundle may still come. A Bundle waiting at a gate before its step exists
// is current there (E2E-R02, E2E-R11). A Failed or Superseded Bundle never
// will come, so its gate instances alone do not make it current: an
// environment it never reached keeps the Bundle deployed there.
func currentBundleByEnv(bundles []v1alpha1.Bundle, steps []v1alpha1.PromotionStep,
	gates []v1alpha1.PolicyGate) map[string]string {
	byName := make(map[string]*v1alpha1.Bundle, len(bundles))
	for i := range bundles {
		byName[bundles[i].Name] = &bundles[i]
	}
	current := make(map[string]*v1alpha1.Bundle)
	superseded := make(map[string]*v1alpha1.Bundle)
	offer := func(env, bundle string, gateOnly, live bool) {
		b, ok := byName[bundle]
		if env == "" || !ok {
			return
		}
		phase := b.Status.Phase
		if gateOnly && (phase == "Failed" || lifecycle.Halted(b)) {
			return
		}
		best := current
		if lifecycle.Halted(b) && !live {
			best = superseded
		}
		if cur, ok := best[env]; !ok || lifecycle.CompareCreation(b, cur) > 0 {
			best[env] = b
		}
	}
	for i := range steps {
		s := &steps[i]
		live := false
		if b, ok := byName[s.Spec.BundleName]; ok {
			live = lifecycle.RejectedLiveStep(b, s)
		}
		offer(s.Spec.Environment, s.Spec.BundleName, false, live)
	}
	for i := range gates {
		offer(gates[i].Labels["kardinal.io/environment"], gates[i].Labels["kardinal.io/bundle"], true, false)
	}

	out := make(map[string]string, len(current)+len(superseded))
	for env, b := range superseded {
		out[env] = b.Name
	}
	for env, b := range current {
		out[env] = b.Name
	}
	return out
}

// rejectedLiveHints returns one line per environment where the current
// Bundle (current: env → Bundle, from currentBundleByEnv) is Rejected and its
// change is live there, telling the operator to roll back. Environments are
// sorted; envFilter, when set, keeps one.
func rejectedLiveHints(pipeline string, current map[string]string, bundles []v1alpha1.Bundle,
	steps []v1alpha1.PromotionStep, envFilter string) []string {
	byName := make(map[string]*v1alpha1.Bundle, len(bundles))
	for i := range bundles {
		byName[bundles[i].Name] = &bundles[i]
	}
	var hints []string
	seen := map[string]bool{}
	envs := make([]string, 0, len(current))
	for env := range current {
		envs = append(envs, env)
	}
	sort.Strings(envs)
	for _, env := range envs {
		if envFilter != "" && env != envFilter {
			continue
		}
		b := byName[current[env]]
		if b == nil || !lifecycle.Rejected(b) {
			continue
		}
		for i := range steps {
			s := &steps[i]
			if s.Spec.Environment == env && lifecycle.RejectedLiveStep(b, s) && !seen[env] {
				seen[env] = true
				hints = append(hints, fmt.Sprintf("WARNING: bundle %s is Rejected in %s: %s (kardinal rollback %s --env %s)",
					b.Name, env, lifecycle.RejectedLiveHint, pipeline, env))
			}
		}
	}
	return hints
}
