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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// envSubdir returns the environment's directory relative to the checkout
// root: environments[].path, or environments/<name> when it is unset.
//
// The path must stay inside the checkout: absolute paths and paths that climb
// out with ".." are rejected (C05-steps-13). Symlinks are handled by opening
// the checkout with openCheckout and doing all file IO through the os.Root.
// Every error is permanent (parentsteps.Permanent): it comes from the
// Pipeline spec, so retrying cannot fix it.
func envSubdir(state *parentsteps.StepState) (string, error) {
	p := state.Environment.Path
	if p == "" {
		if state.Environment.Name == "" {
			return "", parentsteps.Permanent(fmt.Errorf("environment has no name and no path"))
		}
		p = filepath.Join("environments", state.Environment.Name)
	}
	rel, err := confinedRel(p)
	if err != nil {
		return "", fmt.Errorf("environment path: %w", err)
	}
	return rel, nil
}

// confinedRel cleans p and returns it when it is a relative path that stays
// inside its root. A rejected path is a permanent error.
func confinedRel(p string) (string, error) {
	if p == "" {
		return "", parentsteps.Permanent(fmt.Errorf("path is empty"))
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return "", parentsteps.Permanent(fmt.Errorf("path %q must be relative to the repository root", p))
	}
	c := filepath.Clean(filepath.FromSlash(p))
	if c == ".." || strings.HasPrefix(c, ".."+string(filepath.Separator)) {
		return "", parentsteps.Permanent(fmt.Errorf("path %q must stay inside the repository", p))
	}
	return c, nil
}

// rootEscapeMessage is the message of the error an os.Root returns for a path
// that leaves the root, for example through a symlink. The os package does
// not export that error value.
const rootEscapeMessage = "path escapes from parent"

// permanentIfEscape marks err permanent when an os.Root refused a path that
// leaves the checkout: the repository content makes the step escape, so
// retrying cannot fix it. Any other error is returned unchanged.
func permanentIfEscape(err error) error {
	if isRootEscape(err) {
		return parentsteps.Permanent(err)
	}
	return err
}

// isRootEscape reports whether err's chain holds an *fs.PathError for a path
// an os.Root refused. The refusal can be nested, as in
// "mkdirat a: statat a: path escapes from parent".
func isRootEscape(err error) bool {
	for err != nil {
		if pathErr, ok := err.(*fs.PathError); ok && pathErr.Err != nil && pathErr.Err.Error() == rootEscapeMessage {
			return true
		}
		switch u := err.(type) {
		case interface{ Unwrap() []error }:
			for _, e := range u.Unwrap() {
				if isRootEscape(e) {
					return true
				}
			}
			return false
		case interface{ Unwrap() error }:
			err = u.Unwrap()
		default:
			return false
		}
	}
	return false
}

// openCheckout opens the git working directory as an os.Root. Every read and
// write through it is confined to the checkout, including through symlinks,
// so a repository cannot make a step touch another promotion's files.
func openCheckout(state *parentsteps.StepState) (*os.Root, error) {
	if state.WorkDir == "" {
		return nil, fmt.Errorf("WorkDir not set")
	}
	dir := state.WorkDir
	if layoutBranch(state) {
		// The update steps edit the DRY source, which is rendered and never
		// committed; WorkDir holds the rendered branch.
		dir = parentsteps.DrySourceDir(state.WorkDir)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open work dir: %w", err)
	}
	return root, nil
}

// confinedRealPath resolves symlinks in root/rel and returns the real path,
// failing when it is outside the real root.
func confinedRealPath(root, rel string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve work dir: %w", err)
	}
	real, err := filepath.EvalSymlinks(filepath.Join(realRoot, rel))
	if err != nil {
		return "", fmt.Errorf("resolve environment path %s: %w", filepath.ToSlash(rel), err)
	}
	r, err := filepath.Rel(realRoot, real)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", parentsteps.Permanent(fmt.Errorf("environment path %s resolves outside the repository", filepath.ToSlash(rel)))
	}
	return real, nil
}
