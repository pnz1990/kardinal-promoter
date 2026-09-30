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
	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// currentBundleByEnv returns, per environment, the Bundle that explain and
// status describe there: the newest Bundle that is not Superseded and is in
// that environment. A Bundle is in an environment when it has a PromotionStep
// there, or a gate instance there and it has not failed. Newest is
// lifecycle.CompareCreation, the order supersession uses. An environment
// where every Bundle is Superseded falls back to the newest Superseded Bundle
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
	offer := func(env, bundle string, gateOnly bool) {
		b, ok := byName[bundle]
		if env == "" || !ok {
			return
		}
		phase := b.Status.Phase
		if gateOnly && (phase == "Failed" || phase == "Superseded") {
			return
		}
		best := current
		if phase == "Superseded" {
			best = superseded
		}
		if cur, ok := best[env]; !ok || lifecycle.CompareCreation(b, cur) > 0 {
			best[env] = b
		}
	}
	for i := range steps {
		offer(steps[i].Spec.Environment, steps[i].Spec.BundleName, false)
	}
	for i := range gates {
		offer(gates[i].Labels["kardinal.io/environment"], gates[i].Labels["kardinal.io/bundle"], true)
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
