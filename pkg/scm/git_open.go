// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// openRepo opens the repository at dir (a working tree, or bare) with its packfiles kept
// open while the returned close runs. gogit.PlainOpen's storage opens,
// stats and closes the packfile for every object it reads: a rebase that
// diffs three trees (base, local and remote head) read thousands of objects
// and spent most of its time in open/fstat (#1646; with many writers on one
// branch each push retry fetches a pack more, so the cost grew with every
// rebase). close must be called when the repository is no longer used.
func openRepo(dir string) (*gogit.Repository, func() error, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("open repo at %s: %w", dir, err)
	}
	// Laid out as gogit.PlainOpen does: a working tree with a .git
	// directory, or a bare repository (dir is the git directory, no
	// working tree).
	root := osfs.New(abs)
	var dot, wt billy.Filesystem = root, nil
	if fi, err := root.Stat(gogit.GitDirName); err == nil && fi.IsDir() {
		if dot, err = root.Chroot(gogit.GitDirName); err != nil {
			return nil, nil, fmt.Errorf("open repo at %s: %w", dir, err)
		}
		wt = root
	} else if _, err := root.Stat("HEAD"); err != nil {
		return nil, nil, fmt.Errorf("open repo at %s: %w", dir, gogit.ErrRepositoryNotExists)
	}
	st := filesystem.NewStorageWithOptions(dot, cache.NewObjectLRUDefault(), filesystem.Options{KeepDescriptors: true})
	repo, err := gogit.Open(st, wt)
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("open repo at %s: %w", dir, err), st.Close())
	}
	return repo, st.Close, nil
}
