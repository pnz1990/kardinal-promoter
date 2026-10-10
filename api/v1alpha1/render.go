// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

// RendersToBranch reports whether environment e of a Pipeline with spec p is
// promoted with layout: branch (set on the environment or on spec.git).
func RendersToBranch(p PipelineSpec, e EnvironmentSpec) bool {
	return e.Layout == "branch" || p.Git.Layout == "branch"
}

// RenderedBranch is the branch layout: branch commits e's rendered manifests
// to: render.branch, or env/<name>.
func (e EnvironmentSpec) RenderedBranch() string {
	if e.Render != nil && e.Render.Branch != "" {
		return e.Render.Branch
	}
	return "env/" + e.Name
}
