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

// OCIWatcher watches an OCI registry for new image tags.
// Phase 1: stub implementation that always returns "not changed".
// Real implementation will use google/go-containerregistry.
type OCIWatcher struct {
	// Registry is the OCI registry URL (e.g. "ghcr.io/myorg/myapp").
	Registry string
	// TagFilter is an optional regex that tags must match.
	TagFilter string
}

// Watch polls the OCI registry for the latest tag.
// Phase 1 stub: returns not-changed for all requests to avoid
// requiring OCI registry access in unit tests.
// Real implementation: list tags → filter by TagFilter → return newest by pushed_at.
func (w *OCIWatcher) Watch(_ context.Context, lastDigest string) (*WatchResult, error) {
	if w.Registry == "" {
		return nil, fmt.Errorf("OCIWatcher: registry must not be empty")
	}
	// Phase 1 stub: no real polling yet.
	// The reconciler handles nil/empty result gracefully (sets phase=Watching, no Bundle created).
	return &WatchResult{
		Digest:  lastDigest,
		Tag:     "",
		Changed: false,
	}, nil
}
