// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"context"
	"net/http"
)

// Archiver is a Server that can archive a repo. An archived repo is
// read-only: clones work and every push is rejected, so a promotion step's
// push fails with a retryable error until the repo is unarchived.
type Archiver interface {
	// SetArchived archives or unarchives the repo.
	SetArchived(ctx context.Context, r Repo, archived bool) error
}

// SetArchived edits the repo's archived flag.
func (f *forgejo) SetArchived(ctx context.Context, r Repo, archived bool) error {
	return f.do(ctx, http.MethodPatch, f.repoPath(r), map[string]bool{"archived": archived}, nil)
}
