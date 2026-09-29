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

package scm_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestGoGitClient_PushCreatesBranch clones a local repo, commits and pushes a
// promotion branch, then checks the branch exists on the remote at the new
// commit. It guards against a push that reports success but sends nothing.
func TestGoGitClient_PushCreatesBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-git filesystem tests in short mode (may hang on macOS)")
	}
	ctx := context.Background()
	c := scm.NewGoGitClient()

	// Seed a remote with one commit on "main".
	seedDir := t.TempDir()
	_, err := gogit.PlainInitWithOptions(seedDir, &gogit.PlainInitOptions{
		InitOptions: gogit.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(seedDir, "README"), []byte("seed\n"), 0o600))
	require.NoError(t, c.CommitAll(ctx, seedDir, "seed", "t", "t@example.com"))

	remoteDir := t.TempDir()
	_, err = gogit.PlainClone(remoteDir, true, &gogit.CloneOptions{URL: seedDir})
	require.NoError(t, err)

	workDir := filepath.Join(t.TempDir(), "work")
	require.NoError(t, c.Clone(ctx, "file://"+remoteDir, "main", workDir))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "README"), []byte("promoted\n"), 0o600))
	require.NoError(t, c.CommitAll(ctx, workDir, "promote", "t", "t@example.com"))

	work, err := gogit.PlainOpen(workDir)
	require.NoError(t, err)
	workHead, err := work.Head()
	require.NoError(t, err)

	require.NoError(t, c.Push(ctx, workDir, "origin", "kardinal/app-v1/test", ""))

	remote, err := gogit.PlainOpen(remoteDir)
	require.NoError(t, err)
	ref, err := remote.Reference(plumbing.NewBranchReferenceName("kardinal/app-v1/test"), true)
	require.NoError(t, err, "pushed branch must exist on the remote")
	require.Equal(t, workHead.Hash(), ref.Hash())
}
