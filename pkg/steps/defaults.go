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

// DefaultSequenceForBundle returns the default step sequence based on approval mode,
// bundle type, update strategy, and layout.
//
// bundleType: "image" | "config" | "mixed" | "" (defaults to image behaviour)
// updateStrategy: "kustomize" | "helm" | "argocd" | "" (defaults to kustomize)
// layout: "directory" | "branch" | "" (defaults to directory)
//
// Routing rules:
//   - config bundle → git-clone, config-merge, git-commit, git-push, [open-pr, wait-for-merge,] health-check
//   - mixed bundle → git-clone, config-merge, then the image update steps below, git-commit, git-push, ...:
//     the config commit is merged first, so the Bundle's images win over the image pins it carries
//   - image + argocd → argocd-set-image, health-check (no git operations)
//   - image + helm  → git-clone, helm-set-image, git-commit, git-push, [open-pr, wait-for-merge,] health-check
//   - layout:branch → git-clone, [kustomize-set-image | helm-set-image | yaml-update,] render-manifests,
//     git-commit, git-push, [open-pr, wait-for-merge,] health-check (no config-merge: a configRef
//     commit is the DRY commit that is rendered)
//   - image + kustomize (default) → git-clone, kustomize-set-image, git-commit, git-push, [open-pr, wait-for-merge,] health-check
func DefaultSequenceForBundle(approvalMode, bundleType, updateStrategy, layout string) []string {
	// ArgoCD-native path: no git operations, no PR — direct Kubernetes API patch.
	// health-check runs after the patch to verify the ArgoCD Application synced.
	// There is nothing to review, so argocd-set-image fails the promotion when
	// approvalMode is pr-review instead of skipping the review (C05-steps-11).
	if updateStrategy == "argocd" {
		return []string{"argocd-set-image", "health-check"}
	}

	var updateSteps []string
	if layout == "branch" {
		// Rendered manifests: the image update edits the DRY checkout (never
		// committed), render-manifests renders it into the rendered branch
		// checkout. A config or mixed Bundle's configRef commit is the DRY
		// commit itself, so there is no config-merge.
		if bundleType != "config" {
			switch updateStrategy {
			case "helm":
				updateSteps = append(updateSteps, "helm-set-image")
			case "yaml":
				updateSteps = append(updateSteps, "yaml-update")
			default:
				updateSteps = append(updateSteps, "kustomize-set-image")
			}
		}
		updateSteps = append(updateSteps, "render-manifests")
	} else {
		if bundleType == "config" || bundleType == "mixed" {
			updateSteps = []string{"config-merge"}
		}
		switch {
		case bundleType == "config":
			// No image to update.
		case updateStrategy == "helm":
			updateSteps = append(updateSteps, "helm-set-image")
		case updateStrategy == "yaml":
			updateSteps = append(updateSteps, "yaml-update")
		default:
			updateSteps = append(updateSteps, "kustomize-set-image")
		}
	}

	base := append([]string{"git-clone"}, updateSteps...)
	base = append(base, "git-commit", "git-push")
	if approvalMode == "pr-review" {
		base = append(base, OpenPRStepName, "wait-for-merge")
	}
	base = append(base, "health-check")
	return base
}
