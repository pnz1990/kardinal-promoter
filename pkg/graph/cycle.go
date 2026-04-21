// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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

package graph

import (
	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// DetectCycle checks whether a Pipeline's dependsOn relationships contain a
// circular dependency. It returns a non-nil error with a human-readable cycle
// path (e.g. "prod → uat → prod (cycle!)") when a cycle is found.
//
// This function is a pure, side-effect-free predicate — it performs no I/O.
// It is called from both the ValidatingAdmissionWebhook (fast-fail at apply
// time) and from resolveOrdering in the bundle translator (fail at Graph
// build time if the webhook was not in place).
//
// Design ref: docs/design/15-production-readiness.md §Lens 4
func DetectCycle(pipeline *kardinalv1alpha1.Pipeline) error {
	_, _, err := resolveOrdering(pipeline)
	return err
}
