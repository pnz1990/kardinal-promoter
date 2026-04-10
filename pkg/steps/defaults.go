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

package steps

// DefaultSequence returns the default step sequence for a given approval mode.
//
// For "auto" approval mode (no PR review required):
//
//	git-clone → kustomize-set-image → git-commit → git-push → health-check
//
// For "pr-review" approval mode:
//
//	git-clone → kustomize-set-image → git-commit → git-push → open-pr → wait-for-merge → health-check
func DefaultSequence(approvalMode string) []string {
	base := []string{
		"git-clone",
		"kustomize-set-image",
		"git-commit",
		"git-push",
	}
	if approvalMode == "pr-review" {
		base = append(base, "open-pr", "wait-for-merge")
	}
	base = append(base, "health-check")
	return base
}
