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

import (
	"net/url"
	"path/filepath"
)

// DefaultWorkDirRoot is the parent directory of all promotion checkouts.
const DefaultWorkDirRoot = "/tmp/kardinal"

// WorkDirFor returns the git working directory of one PromotionStep.
//
// The path is keyed by namespace, pipeline, bundle and environment, so two
// environments of one Bundle, or two same-named Pipelines in different
// namespaces, never share a checkout. Each segment is path-escaped, so no
// value can add a path separator or climb out of root.
func WorkDirFor(root, namespace, pipeline, bundle, environment string) string {
	return filepath.Join(root,
		workDirSegment(namespace),
		workDirSegment(pipeline),
		workDirSegment(bundle),
		workDirSegment(environment))
}

// ConfigSourceDir returns the directory where git-clone checks out a config
// Bundle's configRef commit. It is a sibling of workDir, never inside it, so
// the source checkout is never committed into the GitOps repository. The "#"
// is always escaped by workDirSegment, so the name cannot collide with another
// PromotionStep's work dir.
func ConfigSourceDir(workDir string) string {
	return filepath.Clean(workDir) + "#config-source"
}

// DrySourceDir is where git-clone checks out the DRY source (spec.git.branch)
// for layout: branch, next to workDir, which holds the rendered branch.
// Image update steps edit the DRY copy; it is never committed.
func DrySourceDir(workDir string) string {
	return filepath.Clean(workDir) + "#dry-source"
}

// workDirSegment makes s safe to use as one path segment. url.PathEscape is
// injective, so distinct names stay distinct.
func workDirSegment(s string) string {
	switch s {
	case "":
		return "_"
	case ".":
		return "%2E"
	case "..":
		return "%2E%2E"
	}
	return url.PathEscape(s)
}
