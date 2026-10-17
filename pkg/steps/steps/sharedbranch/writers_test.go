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
	"strings"
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
//
// Under the race detector it runs raceWriters (30) writers: sixty took up to
// two minutes on CI there and hit the 120s test timeout even in a package of
// its own (#1638). Thirty still contend for every push, but do not always
// need a second reconcile, so the ErrContended retry is asserted only in the
// sixty-writer run without -race (about 8s), which CI runs as its own step
// ("go test (shared-branch writers, no race)").
func TestSharedBranch_SixtyWritersThroughTheEngine(t *testing.T) {
	const maxStepRetries = 5
	writers := 60
	if raceWriters > 0 {
		writers = raceWriters
	}
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

	head := branchHead(t, remote)
	for i := range writers {
		path := fmt.Sprintf("environments/p%02d/kustomization.yaml", i)
		content, err := fileAt(head, path)
		if err != nil && !errors.Is(err, errMissing) {
			// A failed object read is not a lost write (#2143): read again.
			t.Logf("reading %s at %s failed (%v); reading the branch again", path, head.Hash, err)
			head = branchHead(t, remote)
			content, err = fileAt(head, path)
		}
		if errors.Is(err, errMissing) {
			dumpLostWrite(t.Logf, head, path)
		}
		require.NoError(t, err, "writer %d's change is on the branch", i)
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
	if raceWriters == 0 {
		assert.Positive(t, retried, "sixty writers lose some reconciles (ErrContended) and retry them")
	}
}

// errMissing is fileAt's error when the tree has no such file: the path's
// entry or one of its directories is not there. Any other error is a failed
// object read (go-git's Tree.File reports both as "file not found").
var errMissing = errors.New("not in the tree")

// branchHead opens remote again and returns the commit main points at.
func branchHead(t *testing.T, remote string) *object.Commit {
	t.Helper()
	repo, err := gogit.PlainOpen(remote)
	require.NoError(t, err)
	ref, err := repo.Reference(plumbing.NewBranchReferenceName("main"), true)
	require.NoError(t, err)
	head, err := repo.CommitObject(ref.Hash())
	require.NoError(t, err)
	return head
}

// fileAt returns the content of path at commit c, errMissing when the tree
// has no such file.
func fileAt(c *object.Commit, path string) (string, error) {
	tree, err := c.Tree()
	if err != nil {
		return "", fmt.Errorf("read the tree of %s: %w", c.Hash, err)
	}
	if _, err := tree.FindEntry(path); errors.Is(err, object.ErrEntryNotFound) || errors.Is(err, object.ErrDirectoryNotFound) {
		return "", fmt.Errorf("%s at %s: %w", path, c.Hash, errMissing)
	} else if err != nil {
		return "", fmt.Errorf("look up %s at %s: %w", path, c.Hash, err)
	}
	f, err := tree.File(path)
	if err != nil {
		return "", fmt.Errorf("read %s at %s: %w", path, c.Hash, err)
	}
	return f.Contents()
}

// dumpLostWrite logs main's history, newest first, with whether each commit
// has path, and for the commit that dropped it (it lacks path, its parent
// has it) every change against that parent: which writer's commit lost the
// write, and whether it was rebased (its committer time is later than its
// author time), so a rare failure on CI says what happened (#2143).
func dumpLostWrite(logf func(format string, args ...any), head *object.Commit, path string) {
	var chain []*object.Commit
	if err := object.NewCommitPreorderIter(head, nil, nil).ForEach(func(c *object.Commit) error {
		chain = append(chain, c)
		return nil
	}); err != nil {
		logf("lost write: walking main's history stopped: %v", err)
	}
	has := func(c *object.Commit) string {
		_, err := fileAt(c, path)
		switch {
		case err == nil:
			return "has"
		case errors.Is(err, errMissing):
			return "missing"
		}
		return "unreadable (" + err.Error() + ")"
	}
	logf("lost write: %s is not at main %s; main's history, newest first:", path, head.Hash)
	for k, c := range chain {
		logf("  %3d %s parents=%d %s author=%s committer=%s %q", k, c.Hash.String()[:10], c.NumParents(), has(c),
			c.Author.When.Format("15:04:05.000000"), c.Committer.When.Format("15:04:05.000000"), firstLine(c.Message))
	}
	for k := 0; k+1 < len(chain); k++ {
		c, parent := chain[k], chain[k+1]
		if has(c) != "missing" || has(parent) != "has" {
			continue
		}
		logf("lost write: %s dropped %s (its parent %s has it); its changes against the parent:", c.Hash, path, parent.Hash)
		ct, err1 := c.Tree()
		pt, err2 := parent.Tree()
		if err1 != nil || err2 != nil {
			logf("  cannot read the trees: %v, %v", err1, err2)
			continue
		}
		changes, err := object.DiffTree(pt, ct)
		if err != nil {
			logf("  cannot diff the trees: %v", err)
			continue
		}
		for _, ch := range changes {
			logf("  %q -> %q", ch.From.Name, ch.To.Name)
		}
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// TestLostWriteDiagnostics (#2143): fileAt tells a file that is not in the
// tree from one whose objects cannot be read, and dumpLostWrite names the
// commit that dropped a file and its changes.
func TestLostWriteDiagnostics(t *testing.T) {
	ctx := context.Background()
	c := scm.NewGoGitClient()
	dir := t.TempDir()
	_, err := gogit.PlainInitWithOptions(dir, &gogit.PlainInitOptions{
		InitOptions: gogit.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")}})
	require.NoError(t, err)
	write := func(p, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, p), []byte(content), 0o600))
	}
	write("environments/p01/kustomization.yaml", "newTag: b01\n")
	write("environments/p02/kustomization.yaml", "newTag: b02\n")
	require.NoError(t, c.CommitAll(ctx, dir, "both", "t", "t@example.com"))
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "environments/p02")))
	require.NoError(t, c.CommitAll(ctx, dir, "drops p02", "t", "t@example.com"))
	write("README.md", "x\n")
	require.NoError(t, c.CommitAll(ctx, dir, "later", "t", "t@example.com"))

	head := branchHead(t, dir)
	content, err := fileAt(head, "environments/p01/kustomization.yaml")
	require.NoError(t, err)
	assert.Equal(t, "newTag: b01\n", content)
	_, err = fileAt(head, "environments/p02/kustomization.yaml")
	assert.ErrorIs(t, err, errMissing)
	_, err = fileAt(head, "nowhere/at/all.yaml")
	assert.ErrorIs(t, err, errMissing, "a missing directory is missing too")

	var lines []string
	dumpLostWrite(func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
		head, "environments/p02/kustomization.yaml")
	out := strings.Join(lines, "\n")
	dropper, err := head.Parent(0)
	require.NoError(t, err)
	assert.Contains(t, out, "lost write: "+dropper.Hash.String()+" dropped environments/p02/kustomization.yaml")
	assert.Contains(t, out, `"environments/p02/kustomization.yaml" -> ""`, "the dropping change is listed")
	assert.Contains(t, out, `"drops p02"`)

	// An object that cannot be read is not a missing file.
	repo, err := gogit.PlainOpen(dir)
	require.NoError(t, err)
	tree, err := head.Tree()
	require.NoError(t, err)
	envs, err := tree.FindEntry("environments")
	require.NoError(t, err)
	envTree, err := repo.TreeObject(envs.Hash)
	require.NoError(t, err)
	p01, err := envTree.FindEntry("p01")
	require.NoError(t, err)
	h := p01.Hash.String()
	require.NoError(t, os.Remove(filepath.Join(dir, ".git", "objects", h[:2], h[2:])))
	_, err = fileAt(branchHead(t, dir), "environments/p01/kustomization.yaml")
	require.Error(t, err)
	assert.NotErrorIs(t, err, errMissing, "a failed read is not a lost write")
}
