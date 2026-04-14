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

package source

import (
	"context"
	"fmt"
)

// GitWatcher watches a Git repository for new commits.
// Phase 1: stub implementation that always returns "not changed".
// Real implementation will use go-git.
type GitWatcher struct {
	// RepoURL is the HTTPS Git repository URL.
	RepoURL string
	// Branch is the branch to watch.
	Branch string
	// PathGlob is the optional file glob to filter commits.
	PathGlob string
}

// Watch polls the Git repository for the latest commit on the watched branch.
// Phase 1 stub: returns not-changed for all requests to avoid
// requiring Git network access in unit tests.
// Real implementation: clone/fetch → walk commits → filter by PathGlob → return latest SHA.
func (w *GitWatcher) Watch(_ context.Context, lastDigest string) (*WatchResult, error) {
	if w.RepoURL == "" {
		return nil, fmt.Errorf("GitWatcher: repoURL must not be empty")
	}
	branch := w.Branch
	if branch == "" {
		branch = "main"
	}
	// Phase 1 stub: no real polling yet.
	return &WatchResult{
		Digest:  lastDigest,
		Tag:     "",
		Changed: false,
	}, nil
}
