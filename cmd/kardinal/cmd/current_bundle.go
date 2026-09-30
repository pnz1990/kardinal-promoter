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
)

// currentBundleByEnv returns, per environment, the Bundle that explain and
// status describe there: the newest Bundle (by creation time) that is not
// Superseded and has a PromotionStep or a gate instance in that environment.
// Ties go to the greater name. A step or gate instance whose Bundle is not in
// bundles (being deleted) is ignored, and so are gate templates, which have
// no bundle label.
//
// The rule reads only Bundle.status.phase and the objects that exist, so it
// holds for every path a Bundle takes: a Bundle that skips an environment, one
// waiting at a gate before its step exists, and one that has finished. The
// Graph stamps every gate instance of a Bundle when the Bundle starts, so an
// environment the newest Bundle has not reached yet shows that Bundle's gates
// (E2E-R02, E2E-R11).
func currentBundleByEnv(bundles []v1alpha1.Bundle, steps []v1alpha1.PromotionStep,
	gates []v1alpha1.PolicyGate) map[string]string {
	byName := make(map[string]*v1alpha1.Bundle, len(bundles))
	for i := range bundles {
		if bundles[i].Status.Phase != "Superseded" {
			byName[bundles[i].Name] = &bundles[i]
		}
	}
	current := make(map[string]*v1alpha1.Bundle)
	offer := func(env, bundle string) {
		b, ok := byName[bundle]
		if env == "" || !ok {
			return
		}
		cur, ok := current[env]
		if !ok || cur.CreationTimestamp.Before(&b.CreationTimestamp) ||
			(cur.CreationTimestamp.Equal(&b.CreationTimestamp) && b.Name > cur.Name) {
			current[env] = b
		}
	}
	for i := range steps {
		offer(steps[i].Spec.Environment, steps[i].Spec.BundleName)
	}
	for i := range gates {
		offer(gates[i].Labels["kardinal.io/environment"], gates[i].Labels["kardinal.io/bundle"])
	}

	out := make(map[string]string, len(current))
	for env, b := range current {
		out[env] = b.Name
	}
	return out
}
