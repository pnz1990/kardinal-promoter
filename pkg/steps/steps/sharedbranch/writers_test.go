// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package sharedbranch_test holds the sixty-writer shared-branch test on its
// own: under -race it takes most of a minute or more, and in pkg/steps/steps
// it pushed that package to CI's 120s test timeout.
package sharedbranch_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
	// The built-in steps (git-clone, git-commit, git-push) register here.
	_ "github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
)

// writeEnvFile is a test step that writes the environment's file, as an
// update step would.
type writeEnvFile struct{}

func (writeEnvFile) Name() string { return "test-write-env-file" }

func (writeEnvFile) Execute(_ context.Context, s *parentsteps.StepState) (parentsteps.StepResult, error) {
	p := filepath.Join(s.WorkDir, s.Environment.Path, "kustomization.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed}, err
	}
	if err := os.WriteFile(p, []byte("newTag: "+s.BundleName+"\n"), 0o600); err != nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed}, err
	}
	return parentsteps.StepResult{Status: parentsteps.StepSuccess}, nil
}

func init() { parentsteps.Register(writeEnvFile{}) }

// TestSharedBranch_SixtyWritersThroughTheEngine (#1504 QA): sixty
// environments of different Pipelines promote to one branch at once through
// the real step engine (git-clone, an update, git-commit, git-push) and the
// real git client, at the production bounds: 6 rebases per push, 3 sequence
// restarts per reconcile, and, for a reconcile that still loses
// (ErrContended), a retry as the reconciler does, at most maxStepRetries (5)
// times. Every change lands, nothing is lost, history is linear, and no
// reconcile waits.
func TestSharedBranch_SixtyWritersThroughTheEngine(t *testing.T) {
	const writers, maxStepRetries = 60, 5
	ctx := context.Background()
	c := scm.NewGoGitClient()

	seed := t.TempDir()
	_, err := gogit.PlainInitWithOptions(seed, &gogit.PlainInitOptions{
		InitOptions: gogit.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o600))
	require.NoError(t, c.CommitAll(ctx, seed, "seed", "t", "t@example.com"))
	remote := t.TempDir()
	_, err = gogit.PlainClone(remote, true, &gogit.CloneOptions{URL: seed})
	require.NoError(t, err)

	seq := []string{"git-clone", "test-write-env-file", "git-commit", "git-push"}
	var wg sync.WaitGroup
	errs := make([]error, writers)
	attempts := make([]int, writers)
	longest := make([]time.Duration, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempts[i] = 1; attempts[i] <= maxStepRetries+1; attempts[i]++ {
				state := &parentsteps.StepState{
					PipelineName: fmt.Sprintf("p%02d", i), BundleName: fmt.Sprintf("b%02d", i),
					Environment: v1alpha1.EnvironmentSpec{Name: "test", Path: fmt.Sprintf("environments/p%02d", i), Approval: "auto"},
					WorkDir:     filepath.Join(t.TempDir(), "w"),
					Outputs:     map[string]string{},
					Git:         parentsteps.GitConfig{URL: "file://" + remote, Branch: "main", AuthorName: "k", AuthorEmail: "k@example.com"},
					GitClient:   c,
					Sequence:    seq,
				}
				start := time.Now()
				_, _, err := parentsteps.NewEngine(seq).ExecuteFrom(ctx, state, 0)
				longest[i] = max(longest[i], time.Since(start))
				if err == nil {
					errs[i] = nil
					return
				}
				errs[i] = err
				if !errors.Is(err, parentsteps.ErrContended) {
					return
				}
			}
		}()
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "writer %d after %d reconciles", i, attempts[i])
	}

	repo, err := gogit.PlainOpen(remote)
	require.NoError(t, err)
	ref, err := repo.Reference(plumbing.NewBranchReferenceName("main"), true)
	require.NoError(t, err)
	head, err := repo.CommitObject(ref.Hash())
	require.NoError(t, err)
	tree, err := head.Tree()
	require.NoError(t, err)
	for i := range writers {
		f, err := tree.File(fmt.Sprintf("environments/p%02d/kustomization.yaml", i))
		require.NoError(t, err, "writer %d's change is on the branch", i)
		content, err := f.Contents()
		require.NoError(t, err)
		assert.Equal(t, fmt.Sprintf("newTag: b%02d\n", i), content)
	}
	n := 0
	require.NoError(t, object.NewCommitPreorderIter(head, nil, nil).ForEach(func(*object.Commit) error { n++; return nil }))
	assert.Equal(t, writers+1, n, "one commit per writer, linear")
	retried := 0
	for _, a := range attempts {
		if a > 1 {
			retried++
		}
	}
	t.Logf("%d of %d writers needed more than one reconcile", retried, writers)
}
