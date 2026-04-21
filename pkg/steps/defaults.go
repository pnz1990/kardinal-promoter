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
// It uses the kustomize strategy, which is the default update strategy.
//
// For "auto" approval mode (no PR review required):
//
//	git-clone → kustomize-set-image → git-commit → git-push → health-check
//
// For "pr-review" approval mode:
//
//	git-clone → kustomize-set-image → git-commit → git-push → open-pr → wait-for-merge → health-check
//
// Use DefaultSequenceForBundle for type-aware routing.
func DefaultSequence(approvalMode string) []string {
	return DefaultSequenceForBundle(approvalMode, "", "", "")
}

// DefaultSequenceForBundle returns the default step sequence based on approval mode,
// bundle type, update strategy, and layout. Callers should prefer this over DefaultSequence.
//
// bundleType: "image" | "config" | "mixed" | "" (defaults to image behaviour)
// updateStrategy: "kustomize" | "helm" | "argocd" | "" (defaults to kustomize)
// layout: "directory" | "branch" | "" (defaults to directory)
//
// Routing rules:
//   - config bundle → git-clone, config-merge, git-commit, git-push, [open-pr, wait-for-merge,] health-check
//   - image + argocd → argocd-set-image, health-check (no git operations)
//   - image + helm  → git-clone, helm-set-image, git-commit, git-push, [open-pr, wait-for-merge,] health-check
//   - layout:branch → git-clone, kustomize-set-image, kustomize-build, git-commit, git-push, [open-pr, wait-for-merge,] health-check
//   - image + kustomize (default) → git-clone, kustomize-set-image, git-commit, git-push, [open-pr, wait-for-merge,] health-check
func DefaultSequenceForBundle(approvalMode, bundleType, updateStrategy, layout string) []string {
	// ArgoCD-native path: no git operations, no PR — direct Kubernetes API patch.
	// health-check runs after the patch to verify the ArgoCD Application synced.
	if updateStrategy == "argocd" {
		return []string{"argocd-set-image", "health-check"}
	}

	var updateSteps []string
	switch {
	case bundleType == "config":
		updateSteps = []string{"config-merge"}
	case updateStrategy == "helm":
		updateSteps = []string{"helm-set-image"}
	case layout == "branch":
		// Rendered manifests: run kustomize-set-image then kustomize-build.
		// kustomize-build renders the overlay to a file; git-commit picks it up.
		updateSteps = []string{"kustomize-set-image", "kustomize-build"}
	default:
		updateSteps = []string{"kustomize-set-image"}
	}

	base := append([]string{"git-clone"}, updateSteps...)
	base = append(base, "git-commit", "git-push")
	if approvalMode == "pr-review" {
		base = append(base, "open-pr", "wait-for-merge")
	}
	base = append(base, "health-check")
	return base
}
