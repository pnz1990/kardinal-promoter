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

// RenderStepName is the step of a layout: branch environment that waits for
// its RenderRun.
const RenderStepName = "render"

// OutputRenderRequested is "true" once the render step ran: the reconciler
// then sets status.renderRequestedAt, which lets the Graph create the
// environment's RenderRun.
const OutputRenderRequested = "renderRequested"

// OutputRenderPullRequest is "true" when the render step's list opens a PR:
// the RenderRun then pushes to the promotion branch.
const OutputRenderPullRequest = "renderPullRequest"

// RenderJobSequence is what the render Job of a layout: branch environment
// runs: clone the rendered branch and the DRY source, set the Bundle's
// images in the DRY checkout (never committed), render it into the rendered
// branch checkout, commit and push. A config or mixed Bundle's configRef
// commit is the DRY commit itself, so there is no config-merge.
func RenderJobSequence(bundleType, updateStrategy string) []string {
	seq := []string{"git-clone"}
	if bundleType != "config" {
		switch updateStrategy {
		case "helm":
			seq = append(seq, "helm-set-image")
		case "yaml":
			seq = append(seq, "yaml-update")
		default:
			seq = append(seq, "kustomize-set-image")
		}
	}
	return append(seq, "render-manifests", "git-commit", "git-push")
}

// DefaultSequenceForBundle returns the default step sequence based on approval mode,
// bundle type, update strategy, and layout.
//
// bundleType: "image" | "config" | "mixed" | "chart" | "" (defaults to image behaviour)
// updateStrategy: "kustomize" | "helm" | "argocd" | "yaml" | "" (defaults to kustomize)
// layout: "directory" | "branch" | "" (defaults to directory)
//
// Routing rules:
//   - config bundle → git-clone, config-merge, git-commit, git-push, [open-pr, wait-for-merge,] health-check
//   - mixed bundle → git-clone, config-merge, then the image update steps below, git-commit, git-push, ...:
//     the config commit is merged first, so the Bundle's images win over the image pins it carries
//   - image + argocd → argocd-set-image, health-check (no git operations)
//   - image + helm  → git-clone, helm-set-image, git-commit, git-push, [open-pr, wait-for-merge,] health-check
//   - chart (helm)  → the same: helm-set-image writes the chart version (graph.Build
//     refuses a chart Bundle in an environment whose strategy is not helm, or whose layout is branch)
//   - image + yaml  → git-clone, yaml-update, git-commit, git-push, [open-pr, wait-for-merge,] health-check
//   - layout:branch → render, [open-pr, wait-for-merge,] health-check: render waits for the
//     environment's RenderRun, a Job that runs RenderJobSequence (never the controller)
//   - image + kustomize (default) → git-clone, kustomize-set-image, git-commit, git-push, [open-pr, wait-for-merge,] health-check
func DefaultSequenceForBundle(approvalMode, bundleType, updateStrategy, layout string) []string {
	// ArgoCD-native path: no git operations, no PR — direct Kubernetes API patch.
	// health-check runs after the patch to verify the ArgoCD Application synced.
	// There is nothing to review, so argocd-set-image fails the promotion when
	// approvalMode is pr-review instead of skipping the review (C05-steps-11).
	if updateStrategy == "argocd" {
		return []string{"argocd-set-image", "health-check"}
	}

	if layout == "branch" {
		// Rendered manifests: the render (clone, image update, render, commit,
		// push) runs in the environment's RenderRun Job; the step waits for
		// its result, then opens the PR or checks health as usual.
		seq := []string{RenderStepName}
		if approvalMode == "pr-review" {
			seq = append(seq, OpenPRStepName, "wait-for-merge")
		}
		return append(seq, "health-check")
	}

	var updateSteps []string
	{
		if bundleType == "config" || bundleType == "mixed" {
			updateSteps = []string{"config-merge"}
		}
		switch {
		case bundleType == "config":
			// No image to update.
		case updateStrategy == "helm" || bundleType == "chart":
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
