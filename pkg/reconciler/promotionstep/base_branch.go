// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"

// defaultBaseBranch matches the +kubebuilder:default of PipelineGit.Branch.
const defaultBaseBranch = "main"

// baseBranch is the branch every step of the Pipeline works against: git-clone
// checks it out, git-push pushes to it when the step opens no PR, and open-pr
// targets it (B99). The API server fills spec.git.branch with main when it is
// absent, but not when it is an explicit "", so the empty value means main here
// too and the clone, the push and the PR base never disagree.
func baseBranch(pipeline *v1alpha1.Pipeline) string {
	if pipeline.Spec.Git.Branch == "" {
		return defaultBaseBranch
	}
	return pipeline.Spec.Git.Branch
}
